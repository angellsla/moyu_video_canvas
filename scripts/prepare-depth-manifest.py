"""Verify built packages and prepare the unsigned payload for the signing job."""
import hashlib
import json
import os
from pathlib import Path
import sys
from urllib.parse import urlparse

DEFAULT_MODEL_BASE = "https://hf-mirror.com/depth-anything/Video-Depth-Anything-Small/resolve/main"
DEFAULT_MODEL_FALLBACK_BASE = "https://huggingface.co/depth-anything/Video-Depth-Anything-Small/resolve/main"


def base_url(value, label):
    value = (value or "").strip().rstrip("/")
    parsed = urlparse(value)
    if parsed.scheme != "https" or not parsed.netloc or parsed.query or parsed.fragment:
        raise ValueError(f"{label} must be an HTTPS base URL without query or fragment")
    return value


def verify_file(root, entry, runtime_base):
    name = entry["name"]
    if Path(name).name != name or "/" in name or "\\" in name:
        raise ValueError("unsafe artifact name")
    path = root / name
    if path.stat().st_size != entry["size"] or entry["size"] <= 0:
        raise ValueError("artifact size mismatch")
    with path.open("rb") as source:
        digest = hashlib.file_digest(source, "sha256").hexdigest()
    if digest != entry["sha256"]:
        raise ValueError("artifact hash mismatch")
    return {"urls": [runtime_base + "/" + name], "size": entry["size"], "sha256": digest}


def prepare(root, runtime_base_url=None, model_base_url=None, model_fallback_base_url=None):
    runtime_base = base_url(runtime_base_url or os.environ.get("CANVAS_DEPTH_RUNTIME_BASE_URL"), "CANVAS_DEPTH_RUNTIME_BASE_URL")
    model_base = base_url(model_base_url or os.environ.get("CANVAS_DEPTH_MODEL_BASE_URL") or DEFAULT_MODEL_BASE, "CANVAS_DEPTH_MODEL_BASE_URL")
    model_fallback_base = base_url(
        model_fallback_base_url or os.environ.get("CANVAS_DEPTH_MODEL_FALLBACK_BASE_URL") or DEFAULT_MODEL_FALLBACK_BASE,
        "CANVAS_DEPTH_MODEL_FALLBACK_BASE_URL",
    )
    runtimes = {}
    for variant in ("cpu", "cuda"):
        name = f"beeftv-depth-runtime-v1-windows-amd64-{variant}.zip"
        meta = json.loads((root / (name + ".json")).read_text(encoding="utf-8-sig"))
        if meta["name"] != name or not 0 < meta["files"] <= 50000 or not 0 < meta["expandedSize"] <= 12 << 30:
            raise ValueError("invalid runtime metadata")
        if meta.get("parts"):
            parts = []
            whole = hashlib.sha256()
            for index, part in enumerate(meta["parts"], 1):
                if part["name"] != f"{name}.part-{index:03d}" or part["size"] >= 2 << 30:
                    raise ValueError("invalid release part")
                parts.append(verify_file(root, part, runtime_base))
                with (root / part["name"]).open("rb") as source:
                    while chunk := source.read(1024 * 1024):
                        whole.update(chunk)
            if sum(p["size"] for p in parts) != meta["size"] or whole.hexdigest() != meta["sha256"]:
                raise ValueError("assembled runtime mismatch")
            artifact = {"urls": [], "size": meta["size"], "sha256": meta["sha256"], "parts": parts}
        else:
            artifact = verify_file(root, meta, runtime_base)
        artifact.update(files=meta["files"], expandedSize=meta["expandedSize"])
        runtimes[f"windows-amd64/{variant}"] = artifact
    model_name = "video_depth_anything_vits.pth"
    model_urls = list(dict.fromkeys((model_base + "/" + model_name, model_fallback_base + "/" + model_name)))
    return {"version": 2, "runtimes": runtimes, "model": {
        "urls": model_urls,
        "size": 116440756,
        "sha256": "13379300b739e659f076a59d52e9801bd8d38c541a7e71f73bbca4dcfb013609",
    }}


if __name__ == "__main__":
    Path(sys.argv[2]).write_text(json.dumps(prepare(Path(sys.argv[1])), separators=(",", ":")), encoding="utf-8")
