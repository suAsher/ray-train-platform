#!/usr/bin/env python3
"""Safely extract a platform-owned Ray SDK package into /workspace.

The archive is produced by a local Ray SDK and is stored under the caller's
personal workspace root. This helper rejects links and path traversal before
writing any file, so the init container cannot escape its emptyDir workspace.
"""

import argparse
import os
import re
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


def require_entrypoint(destination, script, module):
    if script:
        if not script.endswith('.py') or not os.path.isfile(safe_destination(destination, script)):
            raise ValueError('required Python entrypoint script is missing')
    if module:
        if not re.fullmatch(r'[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*', module):
            raise ValueError('required Python module is invalid')
        relative = module.replace('.', '/')
        candidates = [relative + '.py', relative + '/__main__.py']
        if not any(os.path.isfile(safe_destination(destination, candidate)) for candidate in candidates):
            raise ValueError('required Python entrypoint module is missing')


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--archive", required=True)
    parser.add_argument("--destination", required=True)
    parser.add_argument('--max-uncompressed-bytes', type=int, default=MAX_UNCOMPRESSED_BYTES)
    target = parser.add_mutually_exclusive_group()
    target.add_argument('--required-script')
    target.add_argument('--required-module')
    args = parser.parse_args(argv)
    if not 1 <= args.max_uncompressed_bytes <= MAX_UNCOMPRESSED_BYTES:
        raise ValueError('invalid archive expansion limit')

    destination = os.path.realpath(args.destination)
    os.makedirs(destination, exist_ok=True)
    with zipfile.ZipFile(args.archive) as archive:
        members = archive.infolist()
        if len(members) > MAX_FILES or sum(member.file_size for member in members) > args.max_uncompressed_bytes:
            raise ValueError("archive exceeds platform extraction limits")
        # Check every path/link before writing any content into the workspace.
        for member in members:
            if stat.S_ISLNK(member.external_attr >> 16):
                raise ValueError("archive symlinks are not allowed")
            safe_destination(destination, member.filename)
        total = 0
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
                member_total = 0
                while True:
                    chunk = source.read(1024 * 1024)
                    if not chunk:
                        break
                    total += len(chunk)
                    member_total += len(chunk)
                    if total > args.max_uncompressed_bytes or member_total > member.file_size:
                        raise ValueError('archive exceeds its declared extraction size')
                    output.write(chunk)
                if member_total != member.file_size:
                    raise ValueError('archive size does not match its declared extraction size')
    require_entrypoint(destination, args.required_script, args.required_module)


if __name__ == "__main__":
    main()
