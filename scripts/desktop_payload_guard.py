#!/usr/bin/env python3
"""Reject optional depth-runtime dependencies and user configuration in desktop payloads."""

import argparse
import os
from pathlib import Path
import re
import sys
import zipfile


FORBIDDEN_PARTS = {
    ".python",
    ".venv",
    "venv",
    "site-packages",
    "torch",
    "torchvision",
    "torchaudio",
    "triton",
    "numpy",
    "opencv",
    "opencv_python",
    "opencv-python",
    "cv2",
    "depth-runtime",
    "python-runtime",
    "python_runtime",
    "python-env",
    "python_env",
}
FORBIDDEN_FILES = {
    "depth-runtime.json",
    "video_depth_anything_vits.pth",
}
MODEL_SUFFIXES = (".pth", ".pt", ".ckpt", ".safetensors")
SOURCE_LIMITS = {
    "depth_capture": 2 * 1024 * 1024,
    "video_depth_anything": 8 * 1024 * 1024,
    "vda": 8 * 1024 * 1024,
}


def _is_link_or_junction(path: Path) -> bool:
    is_junction = getattr(path, "is_junction", None)
    return path.is_symlink() or bool(is_junction and is_junction())


def _is_python_runtime_name(name: str) -> bool:
    folded = name.casefold()
    return bool(
        re.fullmatch(r"python(?:\d+(?:\.\d+)?)?(?:\.exe|\.dll|\.dylib)?", folded)
        or re.fullmatch(r"pythonw(?:\d+(?:\.\d+)?)?(?:\.exe|\.dll|\.dylib)?", folded)
        or re.fullmatch(r"libpython\d+(?:\.\d+)*\.(?:so(?:\.\d+)*|dylib|dll)", folded)
        or folded.endswith(".pyd")
        or folded.endswith(".whl")
        or folded == "pyvenv.cfg"
    )


def _reason(relative: Path) -> str | None:
    parts = [part.casefold() for part in relative.parts]
    name = parts[-1] if parts else ""
    if name in FORBIDDEN_FILES:
        return "depth-runtime user configuration or checkpoint"
    if any(part in FORBIDDEN_PARTS for part in parts):
        return "Python/Torch depth runtime dependency"
    if any(_is_python_runtime_name(part) for part in parts):
        return "Python runtime or extension package"
    if name.endswith(MODEL_SUFFIXES):
        return "model checkpoint"
    if _is_python_runtime_name(name):
        return "Python runtime or extension package"
    if name.startswith(("libtorch", "torch_cpu", "torch_cuda", "torchvision")):
        return "Torch runtime library"
    if name.startswith("beeftv-depth-runtime-v") or name.startswith("depth-runtime-v"):
        return "standalone depth-runtime archive"
    return None


def _source_directory_bytes(source: Path) -> int:
    total = 0
    for current, directories, files in os.walk(source, followlinks=False):
        current_path = Path(current)
        directories[:] = [name for name in directories if not _is_link_or_junction(current_path / name)]
        for name in files:
            path = current_path / name
            if _is_link_or_junction(path):
                continue
            try:
                total += path.stat().st_size
            except OSError:
                continue
    return total


def validate_desktop_payload(payload: Path) -> None:
    root = payload.resolve(strict=True)
    if not root.is_dir():
        raise ValueError(f"Desktop payload must be a directory: {payload}")

    failures: list[str] = []
    source_roots: list[tuple[str, Path]] = []
    for current, directories, files in os.walk(root, followlinks=False):
        current_path = Path(current)
        for name in directories + files:
            item = current_path / name
            relative = item.relative_to(root)
            reason = _reason(relative)
            if reason:
                failures.append(f"{relative}: {reason}")
                continue

            if _is_link_or_junction(item):
                try:
                    target = item.resolve(strict=True)
                    target.relative_to(root)
                except (OSError, ValueError):
                    failures.append(f"{relative}: symlink escapes the desktop payload")
                continue

            if name.casefold() in SOURCE_LIMITS and item.is_dir():
                source_roots.append((name.casefold(), item))

            if item.is_file() and item.suffix.casefold() in {".zip", ".beeftv-plugin"}:
                try:
                    with zipfile.ZipFile(item) as archive:
                        for entry in archive.infolist():
                            member = Path(entry.filename.replace("\\", "/"))
                            member_reason = _reason(member)
                            if member_reason:
                                failures.append(f"{relative}!{entry.filename}: {member_reason}")
                except (OSError, zipfile.BadZipFile):
                    pass

        directories[:] = [name for name in directories if not _is_link_or_junction(current_path / name)]

    for name, source in source_roots:
        limit = SOURCE_LIMITS[name]
        size = _source_directory_bytes(source)
        if size > limit:
            failures.append(f"{source.relative_to(root)}: source snapshot is {size} bytes (limit {limit})")

    if failures:
        details = "\n  ".join(failures[:20])
        if len(failures) > 20:
            details += f"\n  ... and {len(failures) - 20} more"
        raise ValueError(
            "Desktop payload contains depth-runtime dependencies, model data, user configuration, "
            "or an out-of-tree symlink:\n  " + details
        )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True, help="Final desktop payload directory")
    args = parser.parse_args()
    try:
        validate_desktop_payload(args.input)
    except (OSError, ValueError) as error:
        print(error, file=sys.stderr)
        return 1
    print(f"Desktop payload boundary verified: {args.input}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
