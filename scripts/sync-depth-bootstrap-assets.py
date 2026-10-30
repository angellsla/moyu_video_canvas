#!/usr/bin/env python3
"""Synchronize the small depth-capture worker embedded in the desktop build.

The desktop bootstrap must carry the worker source without carrying a Python
environment or model data.  The source tree is authoritative.  ``--check``
only reads files and compares SHA-256 digests; it never installs dependencies
or executes worker code.
"""

from __future__ import annotations

import argparse
import hashlib
from pathlib import Path
import shutil
import sys


ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "tools" / "depth-capture" / "depth_capture"
DESTINATION = (
    ROOT
    / "backend"
    / "internal"
    / "depthruntime"
    / "bootstrap_assets"
    / "worker"
    / "depth_capture"
)


def _python_files(directory: Path) -> dict[str, Path]:
    if not directory.is_dir():
        return {}
    return {
        path.name: path
        for path in directory.glob("*.py")
        if path.is_file() and not path.is_symlink()
    }


def _digest(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _differences(source_files: dict[str, Path], destination_files: dict[str, Path]) -> list[str]:
    differences: list[str] = []
    for name in sorted(source_files):
        source = source_files[name]
        destination = destination_files.get(name)
        if destination is None:
            differences.append(f"missing embedded worker file: {name}")
            continue
        if destination.is_symlink():
            differences.append(f"embedded worker file is a symlink: {name}")
            continue
        if _digest(source) != _digest(destination):
            differences.append(f"embedded worker hash differs: {name}")
    for name in sorted(set(destination_files) - set(source_files)):
        differences.append(f"unexpected embedded worker file: {name}")
    return differences


def check() -> int:
    source_files = _python_files(SOURCE)
    destination_files = _python_files(DESTINATION)
    if not source_files:
        print(f"No worker Python sources found under {SOURCE}", file=sys.stderr)
        return 1
    if not DESTINATION.is_dir():
        print(f"Embedded worker directory is missing: {DESTINATION}", file=sys.stderr)
        return 1
    differences = _differences(source_files, destination_files)
    if differences:
        print("Depth bootstrap worker is out of sync:", file=sys.stderr)
        for difference in differences:
            print(f"  {difference}", file=sys.stderr)
        print("Run this script without --check to resynchronize it.", file=sys.stderr)
        return 1
    print(f"Depth bootstrap worker is in sync ({len(source_files)} files): {DESTINATION}")
    return 0


def sync() -> int:
    source_files = _python_files(SOURCE)
    if not source_files:
        print(f"No worker Python sources found under {SOURCE}", file=sys.stderr)
        return 1
    DESTINATION.mkdir(parents=True, exist_ok=True)
    destination_files = _python_files(DESTINATION)
    for name, source in sorted(source_files.items()):
        destination = DESTINATION / name
        if destination.is_symlink():
            print(f"Refusing to overwrite symlink: {destination}", file=sys.stderr)
            return 1
        shutil.copyfile(source, destination)
    for name in sorted(set(destination_files) - set(source_files)):
        stale = DESTINATION / name
        if stale.is_symlink():
            stale.unlink()
        elif stale.is_file():
            stale.unlink()
    return check()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--check",
        action="store_true",
        help="compare source and embedded worker hashes without writing or executing code",
    )
    args = parser.parse_args()
    return check() if args.check else sync()


if __name__ == "__main__":
    raise SystemExit(main())
