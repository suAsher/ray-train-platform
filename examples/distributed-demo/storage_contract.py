"""Small, dependency-free helpers for storage acceptance jobs."""

from __future__ import annotations

from pathlib import Path, PurePosixPath


def expected_file(dataset_root: str, relative_path: str) -> Path:
    """Resolve one explicit dataset file without recursively listing TOS."""
    relative = PurePosixPath(relative_path)
    if relative.is_absolute() or not relative.parts or any(part in {"", ".", ".."} for part in relative.parts):
        raise ValueError("expected dataset file must be a safe relative path")
    return Path(dataset_root).joinpath(*relative.parts)
