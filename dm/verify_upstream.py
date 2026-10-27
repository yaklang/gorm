#!/usr/bin/env python3
"""Verify that the driver is only a module-path relocation of a pinned ZIP."""

import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import zipfile


ROOT = Path(__file__).resolve().parent
OVERLAY = {"LICENSE", "README.md", "UPSTREAM.json", "driver_test.go", "verify_upstream.py"}


def sha256(value):
    return hashlib.sha256(value).hexdigest()


def verify(archive):
    provenance = json.loads((ROOT / "UPSTREAM.json").read_text())
    upstream = provenance["upstream"]
    raw = archive.read_bytes()
    if sha256(raw) != upstream["archive_sha256"]:
        raise ValueError("upstream archive checksum mismatch")
    old = upstream["module"].encode()
    new = provenance["mirror_module"].encode()
    prefix = upstream["module"] + "@" + upstream["version"] + "/"
    records = {item["upstream_path"]: item for item in provenance["files"]}
    if len(records) != len(provenance["files"]):
        raise ValueError("duplicate provenance paths")
    observed = set()
    with zipfile.ZipFile(archive) as source:
        for entry in source.infolist():
            if entry.is_dir():
                continue
            if not entry.filename.startswith(prefix):
                raise ValueError("unexpected upstream archive prefix")
            relative = entry.filename[len(prefix):]
            if (relative in observed or relative not in records
                    or ".." in PurePosixPath(relative).parts):
                raise ValueError("unexpected or duplicate upstream file")
            observed.add(relative)
            item = records[relative]
            original = source.read(entry)
            if sha256(original) != item["upstream_sha256"]:
                raise ValueError("upstream file checksum mismatch: " + relative)
            transformed = original
            if relative == "go.mod":
                transformed = transformed.replace(b"module " + old, b"module " + new)
            elif relative.endswith(".go"):
                transformed = transformed.replace(b'"' + old, b'"' + new)
            expected_path = "UPSTREAM_README.md" if relative == "README.md" else relative
            if item["mirror_path"] != expected_path:
                raise ValueError("unexpected mirror path: " + relative)
            actual = (ROOT / expected_path).read_bytes()
            if transformed != actual or sha256(actual) != item["mirror_sha256"]:
                raise ValueError("mirror differs from allowed relocation: " + relative)
    if observed != set(records):
        raise ValueError("incomplete upstream archive")
    expected = {item["mirror_path"] for item in records.values()} | OVERLAY
    actual = {str(path.relative_to(ROOT)) for path in ROOT.rglob("*") if path.is_file()}
    if actual != expected:
        raise ValueError("unexpected or missing mirror files: " + str(sorted(actual ^ expected)))
    print("Verified %d upstream files; only allowed module-path changes are present." % len(records))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, required=True)
    arguments = parser.parse_args()
    try:
        verify(arguments.archive)
    except (ValueError, OSError, KeyError, zipfile.BadZipFile) as error:
        parser.exit(1, "upstream verification failed: %s\n" % error)
