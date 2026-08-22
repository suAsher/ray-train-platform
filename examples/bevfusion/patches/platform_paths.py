"""Resolve dataset paths that were recorded as absolute paths in the index.

The BEVFusion `*_infos_*.pkl` files store fully qualified paths, for example

    /mnt/storage/public/bevfusion/fz-3dod-v1/raw/.../LIDAR_TOP/1755401392.199735.bin

That path is only correct on the machine where the index was generated. The
moment the dataset is published under a different root — a different mount
point, a different data space, a different cluster — every sample fails to
open, and the failure surfaces minutes into training as a FileNotFoundError
rather than at submission time.

This module makes the recorded prefix irrelevant. It keeps the *tail* of each
recorded path and re-roots it at the directory the dataset is actually mounted
at, which the platform supplies as ``PLATFORM_DATASET_PATH``.

The number of leading components to drop is discovered from a real sample by
trying progressively shorter suffixes until one exists on disk. Successful
rewrites are cached; a later cache miss is rediscovered so one index can span
multiple published namespaces. Resolved paths are always confined to the
selected mount root.
"""

from __future__ import annotations

import os
from typing import Any, Dict, Iterable, List, Optional, Tuple

#: Environment variable the training platform sets to the mounted input root.
DATASET_ROOT_ENV = "PLATFORM_DATASET_PATH"


def safe_path_parts(recorded_path: str) -> Optional[List[str]]:
    """Split an index path without allowing traversal components."""
    parts = [part for part in recorded_path.split("/") if part]
    if any(part in (".", "..") for part in parts):
        return None
    return parts


def mounted_dataset_root() -> Optional[str]:
    """Return the directory the dataset is mounted at, if the platform set one."""
    root = os.environ.get(DATASET_ROOT_ENV, "").strip()
    return root or None


def existing_path_within_root(path: str, mounted_root: str) -> bool:
    """Return true only for an existing path contained by the selected root."""
    try:
        root = os.path.realpath(mounted_root)
        candidate = os.path.realpath(path)
        return os.path.commonpath((root, candidate)) == root and os.path.exists(candidate)
    except (OSError, ValueError):
        return False


def discover_drop_count(recorded_path: str, mounted_root: str) -> Optional[int]:
    """Find how many leading components of ``recorded_path`` to discard.

    Tries the longest suffix first so a short, ambiguous tail such as
    ``LIDAR_TOP/x.bin`` can never win over the full dataset-relative path.
    Returns ``None`` when no suffix resolves, which the caller treats as
    "leave the path alone" rather than guessing.
    """
    parts = safe_path_parts(recorded_path)
    if parts is None:
        return None
    for drop in range(len(parts)):
        candidate = os.path.join(mounted_root, *parts[drop:])
        if existing_path_within_root(candidate, mounted_root):
            return drop
    return None


def discover_path_rewrite(
    recorded_path: str, mounted_root: str
) -> Optional[Tuple[Tuple[str, ...], int]]:
    """Discover a safe cached rewrite for a recorded dataset path.

    The common case is ``mounted_root + recorded suffix``. Some legacy
    indexes also renamed one namespace while publishing, for example
    ``/temp_data/fz/<scene>`` became ``<mounted_root>/cnfzhjyg/<scene>``.
    In that case this function tries each immediate mounted namespace before
    the recorded suffix. A successful rewrite is cached by the resolver and
    reused until a later sample no longer exists under that mapping.
    """
    direct_drop = discover_drop_count(recorded_path, mounted_root)
    if direct_drop is not None:
        return (), direct_drop

    parts = safe_path_parts(recorded_path)
    if parts is None:
        return None
    try:
        namespaces = tuple(
            sorted(
                entry.name
                for entry in os.scandir(mounted_root)
                if entry.is_dir(follow_symlinks=False)
            )
        )
    except OSError:
        return None
    for drop in range(len(parts)):
        for namespace in namespaces:
            candidate = os.path.join(mounted_root, namespace, *parts[drop:])
            if existing_path_within_root(candidate, mounted_root):
                return (namespace,), drop
    return None


class DatasetPathResolver:
    """Re-roots recorded absolute paths onto the real mount point.

    A rewrite is discovered lazily from the first path that resolves and then
    reused. A later cache miss is rediscovered, preserving the fast common case
    without assuming every record came from one namespace.
    """

    def __init__(self, mounted_root: Optional[str] = None) -> None:
        configured_root = mounted_root if mounted_root is not None else mounted_dataset_root()
        # Keep the configured mount spelling (for example /mnt/storage/public)
        # in returned paths; containment checks resolve symlinks separately.
        self._root = configured_root
        self._prefix: Tuple[str, ...] = ()
        self._drop: Optional[int] = None
        self._resolved = False
        self._disabled = self._root is None

    @property
    def active(self) -> bool:
        return not self._disabled

    def resolve(self, path: Any) -> Any:
        """Return ``path`` re-rooted at the mount point.

        Non-string values, already-valid paths and paths that cannot be
        resolved are returned unchanged: this must never turn a working
        configuration into a broken one.
        """
        if self._disabled or not isinstance(path, str) or not path:
            return path
        parts = safe_path_parts(path)
        if parts is None:
            return path
        if existing_path_within_root(path, self._root):
            # The recorded path already points inside the selected data root.
            return path
        if not self._resolved:
            rewrite = discover_path_rewrite(path, self._root)
            if rewrite is None:
                return path
            self._prefix, self._drop = rewrite
            self._resolved = True
        if self._drop is None or self._drop >= len(parts):
            return path
        candidate = os.path.join(self._root, *self._prefix, *parts[self._drop:])
        if existing_path_within_root(candidate, self._root):
            return candidate

        # A single index can contain samples published under different
        # namespaces. Re-discover only when the cached rewrite misses.
        rewrite = discover_path_rewrite(path, self._root)
        if rewrite is None:
            return path
        self._prefix, self._drop = rewrite
        candidate = os.path.join(self._root, *self._prefix, *parts[self._drop:])
        return candidate if existing_path_within_root(candidate, self._root) else path

    def resolve_sweeps(self, sweeps: Iterable[Dict[str, Any]]) -> List[Dict[str, Any]]:
        """Re-root the ``data_path`` of each sweep, leaving other keys intact."""
        resolved: List[Dict[str, Any]] = []
        for sweep in sweeps or []:
            if isinstance(sweep, dict) and "data_path" in sweep:
                sweep = dict(sweep)
                sweep["data_path"] = self.resolve(sweep["data_path"])
            resolved.append(sweep)
        return resolved
