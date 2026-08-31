"""Managed Ray Train entrypoint for a BEVFusion working-directory snapshot."""

from __future__ import annotations

import os
from pathlib import Path
import subprocess
import sys
from collections.abc import Mapping, Sequence


_PREPARE = "/usr/local/bin/raytrain-bevfusion-prepare"
_PATCHER = "/usr/local/bin/apply-raytrain-bevfusion-runtime"
_NOOP = "/usr/bin/true"
_IMMUTABLE_IMAGE_CHECKOUTS = (Path("/opt/bevfusion").resolve(strict=False),)


def _is_checkout(root: Path) -> bool:
    return (
        (root / "tools" / "westwell_train.py").is_file()
        and (root / "mmdet3d" / "apis" / "train.py").is_file()
    )


def find_checkout(
    *,
    cwd: Path | None = None,
    search_paths: Sequence[str] | None = None,
    environ: Mapping[str, str] | None = None,
) -> Path:
    """Locate user code while refusing the image's immutable source fallback."""

    values = os.environ if environ is None else environ
    image_source = values.get("RAYTRAIN_IMAGE_SOURCE_ROOT", "").strip()
    configured_image_checkout = (
        Path(image_source).resolve(strict=False).parent if image_source else None
    )
    immutable_checkouts = set(_IMMUTABLE_IMAGE_CHECKOUTS)
    if configured_image_checkout is not None:
        immutable_checkouts.add(configured_image_checkout)
    candidates = (Path.cwd() if cwd is None else Path(cwd),)
    candidates += tuple(Path(value) for value in (sys.path if search_paths is None else search_paths) if value)
    seen: set[Path] = set()
    for candidate in candidates:
        resolved = candidate.resolve(strict=False)
        if resolved in seen or resolved in immutable_checkouts:
            continue
        seen.add(resolved)
        if _is_checkout(resolved):
            return resolved
    raise RuntimeError("BEVFusion working-directory snapshot is unavailable")


def prepare_checkout(
    checkout: Path,
    *,
    environ: Mapping[str, str] | None = None,
) -> None:
    """Apply the image-pinned, lock-protected runtime adapter once per node."""

    root = Path(checkout).resolve(strict=True)
    if not _is_checkout(root):
        raise RuntimeError("BEVFusion working-directory snapshot is invalid")
    child_environment = dict(os.environ if environ is None else environ)
    child_environment["RAYTRAIN_SOURCE_ROOT"] = str(root)
    child_environment["RAYTRAIN_BEVFUSION_PATCHER"] = _PATCHER
    subprocess.run(
        [_PREPARE, _NOOP],
        cwd=root,
        env=child_environment,
        check=True,
    )


def prepare_current_worker() -> None:
    """Trusted Ray Train hook loaded by absolute image path by the driver."""

    checkout = find_checkout(environ=os.environ)
    prepare_checkout(checkout, environ=os.environ)
