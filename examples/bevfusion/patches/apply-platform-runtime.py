#!/usr/bin/env python3
"""Apply the BEVFusion DDP and object-storage runtime compatibility patch."""

from pathlib import Path
import sys

from runtime_patch import patch_gitignore, patch_training_entrypoint
from ray_train_managed import patch_managed_training_entrypoint


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: apply-platform-runtime.py <bevfusion-checkout>", file=sys.stderr)
        return 2

    checkout = Path(sys.argv[1]).resolve()
    target = checkout / "tools" / "westwell_train.py"
    gitignore = checkout / ".gitignore"
    if not target.is_file():
        print(f"not a BEVFusion checkout, missing: {target}", file=sys.stderr)
        return 2
    if not gitignore.is_file():
        print(f"not a BEVFusion checkout, missing: {gitignore}", file=sys.stderr)
        return 2

    original = target.read_text(encoding="utf-8")
    patched = patch_managed_training_entrypoint(
        patch_training_entrypoint(original)
    )
    original_gitignore = gitignore.read_text(encoding="utf-8")
    patched_gitignore = patch_gitignore(original_gitignore)

    changed = False
    if patched != original:
        target.write_text(patched, encoding="utf-8")
        print(f"patched: {target}")
        changed = True
    else:
        print(f"already patched: {target}")

    if patched_gitignore != original_gitignore:
        gitignore.write_text(patched_gitignore, encoding="utf-8")
        print(f"patched: {gitignore}")
        changed = True
    else:
        print(f"already patched: {gitignore}")

    if not changed:
        print(f"already patched checkout: {checkout}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
