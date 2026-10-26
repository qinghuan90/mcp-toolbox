#!/usr/bin/env python3
"""Publish only a real, built desktop package and its checksum manifest."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import zipfile
import struct

root = Path(__file__).resolve().parent.parent
target_os, arch = sys.argv[1:3]
version = (root / "desktop/VERSION").read_text().strip()
project_root = root.parent
license_paths = [project_root / name for name in ("LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md", "third-party-licenses.json")]
license_paths += sorted((project_root / "LICENSES").rglob("*"))
license_paths = [path for path in license_paths if path.is_file()]
if not all((project_root / name).is_file() for name in ("LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md", "third-party-licenses.json")):
    raise SystemExit("Project and third-party license files are required for packaging")
if not re.fullmatch(r"\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?", version):
    raise SystemExit("Invalid desktop/VERSION")
releases = root / "dist/desktop"
releases.mkdir(parents=True, exist_ok=True)
stem = f"Toolbox-{version}-{target_os}-{arch}"
if target_os == "darwin":
    source = root / "desktop/build/bin/Toolbox.app"
    if not (source / "Contents/MacOS/Toolbox").is_file():
        raise SystemExit("Built application is missing")
    filename = stem + ".zip"
    temporary = releases / (".building-" + filename)
    subprocess.run(["ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", str(source), str(temporary)], check=True)
    signature = "adhoc"
elif target_os == "linux":
    source = root / "desktop/build/bin/Toolbox"
    if not source.is_file():
        raise SystemExit("Built executable is missing")
    filename = stem + ".tar.gz"
    temporary = releases / (".building-" + filename)
    with tarfile.open(temporary, "w:gz") as archive:
        archive.add(source, arcname="Toolbox")
        archive.add(root / "desktop/README.md", arcname="README.md")
        for path in license_paths:
            archive.add(path, arcname=path.relative_to(project_root).as_posix())
    signature = "unsigned"
elif target_os == "windows":
    source = root / "desktop/build/bin/Toolbox.exe"
    if not source.is_file():
        raise SystemExit("Built Windows executable is missing")
    # Validate PE architecture rather than publishing an accidentally renamed binary.
    with source.open("rb") as binary:
        if binary.read(2) != b"MZ":
            raise SystemExit("Windows executable lacks MZ header")
        binary.seek(0x3c)
        pe_offset = struct.unpack("<I", binary.read(4))[0]
        binary.seek(pe_offset)
        if binary.read(4) != b"PE\0\0":
            raise SystemExit("Windows executable lacks PE header")
        machine = struct.unpack("<H", binary.read(2))[0]
        if machine != {"amd64": 0x8664}.get(arch):
            raise SystemExit("Windows executable architecture mismatch")
    filename = stem + ".zip"
    temporary = releases / (".building-" + filename)
    with zipfile.ZipFile(temporary, "w", compression=zipfile.ZIP_DEFLATED) as archive:
        archive.write(source, arcname="Toolbox.exe")
        archive.write(root / "desktop/README-windows.md", arcname="README-windows.md")
    signature = "unsigned"
else:
    raise SystemExit("Unsupported desktop package target")
if target_os in ("darwin", "windows"):
    with zipfile.ZipFile(temporary, "a", compression=zipfile.ZIP_DEFLATED) as archive:
        for path in license_paths:
            archive.write(path, arcname=path.relative_to(project_root).as_posix())
        archive.writestr("SOURCE_CODE.txt", f"Toolbox {version}\nSource: https://github.com/qinghuan90/mcp-toolbox/tree/{version}\nLicense: AGPL-3.0-only\n")
destination = releases / filename
os.replace(temporary, destination)
digest = hashlib.sha256()
with destination.open("rb") as package:
    for block in iter(lambda: package.read(1024 * 1024), b""):
        digest.update(block)
entry = {"version": version, "os": target_os, "arch": arch, "filename": filename,
         "size": destination.stat().st_size, "sha256": digest.hexdigest(), "signature": signature}
if target_os == "windows":
    entry.update({"minimum_os": "Windows 11", "minimum_build": 22000, "runtime": "Microsoft Edge WebView2 Evergreen"})
manifest_file = releases / "manifest.json"
manifest = json.loads(manifest_file.read_text()) if manifest_file.exists() else {"releases": []}
manifest["releases"] = [item for item in manifest["releases"] if (item["os"], item["arch"]) != (target_os, arch)] + [entry]
manifest["releases"].sort(key=lambda item: (item["os"], item["arch"], item["version"]))
temporary_manifest = releases / ".manifest.json.tmp"
temporary_manifest.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
os.replace(temporary_manifest, manifest_file)
print(f"Desktop package: {destination}")
print(f"SHA256: {entry['sha256']}")
