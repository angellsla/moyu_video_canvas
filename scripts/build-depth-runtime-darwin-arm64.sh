#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOL_DIR="$ROOT_DIR/tools/depth-capture"
OUTPUT_DIR="${1:-$ROOT_DIR/build/depth-runtime}"
STAGE_DIR="$(mktemp -d /tmp/beeftv-depth-runtime.XXXXXX)"
RUNTIME_STAGE="$STAGE_DIR/runtime"
VDA_COMMIT="4f5ae23172ba60fd7bc11ef671cca678842c7072"
MODEL_SHA256="13379300b739e659f076a59d52e9801bd8d38c541a7e71f73bbca4dcfb013609"
MODEL_SIZE="116440756"
MODEL_NAME="video_depth_anything_vits.pth"
ARCHIVE_NAME="beeftv-depth-runtime-v1-darwin-arm64.zip"
RUNTIME_BASE_URL="${CANVAS_DEPTH_RUNTIME_BASE_URL:-}"
MODEL_BASE_URL="${CANVAS_DEPTH_MODEL_BASE_URL:-https://hf-mirror.com/depth-anything/Video-Depth-Anything-Small/resolve/main}"
MODEL_FALLBACK_BASE_URL="${CANVAS_DEPTH_MODEL_FALLBACK_BASE_URL:-https://huggingface.co/depth-anything/Video-Depth-Anything-Small/resolve/main}"

if [[ -z "$RUNTIME_BASE_URL" ]]; then
  echo "CANVAS_DEPTH_RUNTIME_BASE_URL is required to publish the runtime manifest" >&2
  exit 1
fi
RUNTIME_BASE_URL="${RUNTIME_BASE_URL%/}"
MODEL_BASE_URL="${MODEL_BASE_URL%/}"
MODEL_FALLBACK_BASE_URL="${MODEL_FALLBACK_BASE_URL%/}"
for url in "$RUNTIME_BASE_URL" "$MODEL_BASE_URL" "$MODEL_FALLBACK_BASE_URL"; do
  [[ "$url" == https://* ]] || { echo "Depth component URLs must use HTTPS: $url" >&2; exit 1; }
done

trap 'rm -rf "$STAGE_DIR"' EXIT

if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
  echo "This builder requires an Apple Silicon Mac" >&2
  exit 1
fi
if ! command -v uv >/dev/null 2>&1; then
  echo "uv is required to build the optional depth runtime" >&2
  exit 1
fi

mkdir -p "$OUTPUT_DIR" "$RUNTIME_STAGE/.venv/lib/python3.11" "$RUNTIME_STAGE/.venv/bin" "$RUNTIME_STAGE/worker"
(
  cd "$TOOL_DIR"
  uv sync --python 3.11 --frozen
)

PYTHON_BIN="$(cd "$TOOL_DIR" && uv run python -c 'import sys; print(sys.executable)')"
PYTHON_REAL="$(python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$PYTHON_BIN")"
PYTHON_ROOT="$(cd "$(dirname "$PYTHON_REAL")/.." && pwd)"
rsync -aL --exclude='__pycache__' "$PYTHON_ROOT/" "$RUNTIME_STAGE/.python/"
rsync -aL --exclude='__pycache__' --exclude='*.pyc' "$TOOL_DIR/.venv/lib/python3.11/site-packages/" "$RUNTIME_STAGE/.venv/lib/python3.11/site-packages/"
rsync -a --exclude='__pycache__' --exclude='*.pyc' "$TOOL_DIR/depth_capture/" "$RUNTIME_STAGE/worker/depth_capture/"

printf '%s\n' \
  '#!/bin/sh' \
  'RUNTIME_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"' \
  'export PYTHONPATH="$RUNTIME_DIR/.venv/lib/python3.11/site-packages${PYTHONPATH:+:$PYTHONPATH}"' \
  'exec "$RUNTIME_DIR/.python/bin/python3.11" "$@"' \
  > "$RUNTIME_STAGE/.venv/bin/python"
chmod 0750 "$RUNTIME_STAGE/.venv/bin/python"

if [[ -n "${BEEFTV_VDA_SOURCE_DIR:-}" ]]; then
  if [[ "$(git -C "$BEEFTV_VDA_SOURCE_DIR" rev-parse HEAD)" != "$VDA_COMMIT" ]]; then
    echo "BEEFTV_VDA_SOURCE_DIR is not at the required commit $VDA_COMMIT" >&2
    exit 1
  fi
  rsync -a "$BEEFTV_VDA_SOURCE_DIR/" "$STAGE_DIR/vda-repo/"
else
  git -c http.version=HTTP/1.1 clone --filter=blob:none --no-checkout https://github.com/DepthAnything/Video-Depth-Anything.git "$STAGE_DIR/vda-repo"
  git -C "$STAGE_DIR/vda-repo" checkout "$VDA_COMMIT"
fi
rsync -a --exclude='.git' --exclude='checkpoints' --exclude='outputs' "$STAGE_DIR/vda-repo/" "$RUNTIME_STAGE/vda/"

cp "$STAGE_DIR/vda-repo/LICENSE" "$RUNTIME_STAGE/LICENSE-Video-Depth-Anything" 2>/dev/null || true
printf '%s\n' "$VDA_COMMIT" > "$RUNTIME_STAGE/VDA_COMMIT"

"$RUNTIME_STAGE/.venv/bin/python" -c 'import torch, cv2, numpy; print(torch.__version__)'
(
  cd "$RUNTIME_STAGE"
  ditto -c -k --norsrc . "$OUTPUT_DIR/$ARCHIVE_NAME"
)

RUNTIME_SIZE="$(stat -f '%z' "$OUTPUT_DIR/$ARCHIVE_NAME")"
RUNTIME_SHA256="$(shasum -a 256 "$OUTPUT_DIR/$ARCHIVE_NAME" | awk '{print $1}')"
RUNTIME_FILES="$(python3 -c 'import sys,zipfile; print(len(zipfile.ZipFile(sys.argv[1]).infolist()))' "$OUTPUT_DIR/$ARCHIVE_NAME")"
RUNTIME_EXPANDED_SIZE="$(python3 -c 'import sys,zipfile; print(sum(item.file_size for item in zipfile.ZipFile(sys.argv[1]).infolist()))' "$OUTPUT_DIR/$ARCHIVE_NAME")"
python3 - "$OUTPUT_DIR/depth-runtime-manifest.json" \
  "$RUNTIME_BASE_URL/$ARCHIVE_NAME" "$RUNTIME_SIZE" "$RUNTIME_SHA256" "$RUNTIME_FILES" "$RUNTIME_EXPANDED_SIZE" \
  "$MODEL_BASE_URL/$MODEL_NAME" "$MODEL_FALLBACK_BASE_URL/$MODEL_NAME" "$MODEL_SIZE" "$MODEL_SHA256" <<'PY'
import json
import sys

output, runtime_url, runtime_size, runtime_sha, runtime_files, expanded_size, model_url, model_fallback_url, model_size, model_sha = sys.argv[1:]
manifest = {
    "version": 1,
    "runtime": {
        "urls": [runtime_url],
        "size": int(runtime_size),
        "sha256": runtime_sha,
        "files": int(runtime_files),
        "expandedSize": int(expanded_size),
    },
    "model": {
        "urls": list(dict.fromkeys((model_url, model_fallback_url))),
        "size": int(model_size),
        "sha256": model_sha,
    },
}
with open(output, "w", encoding="utf-8") as target:
    json.dump(manifest, target, ensure_ascii=False, indent=2)
    target.write("\n")
PY

echo "Runtime: $OUTPUT_DIR/$ARCHIVE_NAME"
echo "Manifest: $OUTPUT_DIR/depth-runtime-manifest.json"
