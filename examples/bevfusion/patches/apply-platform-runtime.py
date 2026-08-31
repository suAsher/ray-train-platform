#!/usr/bin/env python3
"""Apply the BEVFusion DDP and object-storage runtime compatibility patch."""

from pathlib import Path
import sys

from runtime_patch import patch_gitignore, patch_training_entrypoint
from ray_data_s1h_integration import (
    patch_s1h_train_api,
    patch_s1h_training_entrypoint,
)
from ray_train_managed import patch_managed_training_entrypoint


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: apply-platform-runtime.py <bevfusion-checkout>", file=sys.stderr)
        return 2

    checkout = Path(sys.argv[1]).resolve()
    target = checkout / "tools" / "westwell_train.py"
    train_api = checkout / "mmdet3d" / "apis" / "train.py"
    gitignore = checkout / ".gitignore"
    if not target.is_file():
        print("not a BEVFusion checkout, missing tools/westwell_train.py", file=sys.stderr)
        return 2
    if not train_api.is_file():
        print("not a BEVFusion checkout, missing mmdet3d/apis/train.py", file=sys.stderr)
        return 2

    original = target.read_text(encoding="utf-8")
    patched = patch_s1h_training_entrypoint(
        patch_managed_training_entrypoint(
            patch_training_entrypoint(original)
        )
    )
    original_train_api = train_api.read_text(encoding="utf-8")
    patched_train_api = patch_s1h_train_api(original_train_api)
    original_gitignore = (
        gitignore.read_text(encoding="utf-8") if gitignore.is_file() else None
    )
    patched_gitignore = (
        patch_gitignore(original_gitignore)
        if original_gitignore is not None
        else None
    )

    changed = False
    if patched != original:
        target.write_text(patched, encoding="utf-8")
        print("patched: tools/westwell_train.py")
        changed = True
    else:
        print("already patched: tools/westwell_train.py")

    if patched_train_api != original_train_api:
        train_api.write_text(patched_train_api, encoding="utf-8")
        print("patched: mmdet3d/apis/train.py")
        changed = True
    else:
        print("already patched: mmdet3d/apis/train.py")

    if original_gitignore is None:
        print("runtime archive has no .gitignore, skipped")
    elif patched_gitignore != original_gitignore:
        assert patched_gitignore is not None
        gitignore.write_text(patched_gitignore, encoding="utf-8")
        print("patched: .gitignore")
        changed = True
    else:
        print("already patched: .gitignore")

    if not changed:
        print("already patched checkout")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
