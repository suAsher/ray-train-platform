#!/usr/bin/env python3
"""Safely extract a platform-owned Ray SDK package into /workspace.

The archive is produced by a local Ray SDK and is stored under the caller's
personal workspace root. This helper rejects links and path traversal before
writing any file, so the init container cannot escape its emptyDir workspace.
"""

import argparse
import os
import stat
import zipfile


MAX_FILES = 10_000
MAX_UNCOMPRESSED_BYTES = 2 * 1024 * 1024 * 1024


def safe_destination(root, name):
    if not name or name.startswith(("/", "\\")):
        raise ValueError("archive member path is invalid")
    target = os.path.realpath(os.path.join(root, name))
    if target != root and not target.startswith(root + os.sep):
        raise ValueError("archive member escapes destination")
    return target


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--archive", required=True)
    parser.add_argument("--destination", required=True)
    args = parser.parse_args()

    destination = os.path.realpath(args.destination)
    os.makedirs(destination, exist_ok=True)
    with zipfile.ZipFile(args.archive) as archive:
        members = archive.infolist()
        if len(members) > MAX_FILES or sum(member.file_size for member in members) > MAX_UNCOMPRESSED_BYTES:
            raise ValueError("archive exceeds platform extraction limits")
        for member in members:
            mode = member.external_attr >> 16
            if stat.S_ISLNK(mode):
                raise ValueError("archive symlinks are not allowed")
            target = safe_destination(destination, member.filename)
            if member.is_dir():
                os.makedirs(target, exist_ok=True)
                continue
            os.makedirs(os.path.dirname(target), exist_ok=True)
            with archive.open(member, "r") as source, open(target, "xb") as output:
                while True:
                    chunk = source.read(1024 * 1024)
                    if not chunk:
                        break
                    output.write(chunk)


if __name__ == "__main__":
    main()
