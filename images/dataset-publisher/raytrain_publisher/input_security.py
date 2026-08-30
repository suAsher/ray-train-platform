"""No-follow input reads and lidar-only metadata policy for the publisher."""

from __future__ import annotations

import os
import re
import stat
from pathlib import Path


_CAMERA_INFO_KEYS = frozenset(
    {"cam", "camera", "cameras", "cams", "image", "images", "img", "imgs"}
)
_CAMERA_INFO_FRAGMENTS = ("camera", "image")
_INTERNAL_LOCATION_KEY_PARTS = frozenset(
    {"bucket", "endpoint", "file", "filename", "path", "root", "uri", "url"}
)
_INTERNAL_LOCATION_KEY_FRAGMENTS = (
    "bucket",
    "endpoint",
    "filename",
    "path",
    "root",
    "uri",
    "url",
)
_WINDOWS_ABSOLUTE_PATH = re.compile(r"^[a-zA-Z]:[\\/]")


def validate_lidar_only_info(value: object) -> None:
    """Reject camera metadata and every internal location representation."""

    if type(value) is dict:
        for key, item in value.items():
            if type(key) is not str:
                raise ValueError("lidar-only info keys must be strings")
            normalized = re.sub(r"[^a-z0-9]+", "_", key.lower()).strip("_")
            key_parts = frozenset(part for part in normalized.split("_") if part)
            compact_key = normalized.replace("_", "")
            if key_parts & _CAMERA_INFO_KEYS or any(
                fragment in compact_key for fragment in _CAMERA_INFO_FRAGMENTS
            ):
                raise ValueError(
                    "lidar-only info must not contain camera or image metadata"
                )
            if key_parts & _INTERNAL_LOCATION_KEY_PARTS or any(
                fragment in compact_key
                for fragment in _INTERNAL_LOCATION_KEY_FRAGMENTS
            ):
                raise ValueError(
                    "lidar-only info must not contain an internal location"
                )
            validate_lidar_only_info(item)
        return
    if type(value) is list:
        for item in value:
            validate_lidar_only_info(item)
        return
    if type(value) is str and (
        "://" in value
        or "/" in value
        or "\\" in value
        or _WINDOWS_ABSOLUTE_PATH.match(value)
    ):
        raise ValueError("lidar-only info must not contain an internal location")


def resolve_input_path(input_root: Path, candidate_path: object) -> Path:
    """Validate one path through an fd-rooted, no-follow directory walk."""

    descriptor, canonical_path = _open_input_file(
        input_root, candidate_path, field_name="lidar path"
    )
    os.close(descriptor)
    return canonical_path


def input_file_size(
    input_root: Path, candidate_path: object, *, field_name: str
) -> int:
    descriptor, _canonical_path = _open_input_file(
        input_root, candidate_path, field_name=field_name
    )
    try:
        return os.fstat(descriptor).st_size
    finally:
        os.close(descriptor)


def read_input_bytes(
    input_root: Path,
    candidate_path: object,
    *,
    maximum_bytes: int,
    field_name: str,
) -> bytes:
    descriptor, _canonical_path = _open_input_file(
        input_root, candidate_path, field_name=field_name
    )
    try:
        if os.fstat(descriptor).st_size > maximum_bytes:
            raise ValueError(f"{field_name} exceeds the size limit")
        with os.fdopen(descriptor, "rb", closefd=False) as source:
            payload = source.read(maximum_bytes + 1)
        if len(payload) > maximum_bytes:
            raise ValueError(f"{field_name} exceeds the size limit")
        return payload
    finally:
        os.close(descriptor)


def _normalized_input_parts(
    input_root: Path, candidate_path: object, *, field_name: str
) -> tuple[Path, tuple[str, ...]]:
    try:
        root = Path(input_root).resolve(strict=True)
    except OSError as error:
        raise ValueError("input root is not accessible") from error
    if not root.is_dir():
        raise ValueError("input root must be a directory")
    if (
        not isinstance(candidate_path, str)
        or not candidate_path
        or "\x00" in candidate_path
        or any(
            ord(character) < 32 or ord(character) == 127
            for character in candidate_path
        )
    ):
        raise ValueError(f"{field_name} must be a non-empty normalized string")
    if "://" in candidate_path:
        raise ValueError(f"{field_name} must not use a URI")
    if "\\" in candidate_path:
        raise ValueError(f"{field_name} must use POSIX separators")

    candidate = Path(candidate_path)
    if candidate.is_absolute():
        try:
            relative = candidate.relative_to(root)
        except ValueError as error:
            raise ValueError(
                f"{field_name} must remain inside the input root"
            ) from error
    else:
        relative = candidate
    parts = relative.parts
    if not parts or any(part in {"", ".", ".."} for part in parts):
        raise ValueError(f"{field_name} must remain inside the input root")
    return root, tuple(parts)


def _open_input_file(
    input_root: Path, candidate_path: object, *, field_name: str
) -> tuple[int, Path]:
    root, parts = _normalized_input_parts(
        input_root, candidate_path, field_name=field_name
    )
    close_on_exec = getattr(os, "O_CLOEXEC", 0)
    no_follow = getattr(os, "O_NOFOLLOW", 0)
    directory_only = getattr(os, "O_DIRECTORY", 0)
    directory_flags = os.O_RDONLY | close_on_exec | no_follow | directory_only
    file_flags = os.O_RDONLY | close_on_exec | no_follow

    try:
        directory_fd = os.open(root, directory_flags)
    except OSError as error:
        raise ValueError("input root is not accessible") from error
    try:
        for component in parts[:-1]:
            next_fd = os.open(component, directory_flags, dir_fd=directory_fd)
            os.close(directory_fd)
            directory_fd = next_fd
        file_fd = os.open(parts[-1], file_flags, dir_fd=directory_fd)
    except OSError as error:
        raise ValueError(
            f"{field_name} is not accessible inside the input root"
        ) from error
    finally:
        os.close(directory_fd)

    metadata = os.fstat(file_fd)
    if not stat.S_ISREG(metadata.st_mode):
        os.close(file_fd)
        raise ValueError(f"{field_name} must be a regular file")
    return file_fd, root.joinpath(*parts)
