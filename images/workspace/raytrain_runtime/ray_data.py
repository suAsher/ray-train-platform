"""Validated, opt-in Ray Data adapter for managed Ray Train jobs."""

from __future__ import annotations

import dataclasses
import posixpath
import unicodedata
import urllib.parse
from typing import Any


_STABLE_INPUT_ROOT = "/mnt/data/input"
_SUPPORTED_FORMATS = frozenset(("parquet", "images"))


@dataclasses.dataclass(frozen=True)
class DatasetConfig:
    """Immutable registered-dataset reference below the governed input mount."""

    format: str
    uri: str

    def __post_init__(self) -> None:
        if self.format not in _SUPPORTED_FORMATS:
            raise ValueError(f"unsupported Ray Data format: {self.format}")
        if not self.uri or any(
            unicodedata.category(character) == "Cc" for character in self.uri
        ):
            raise ValueError(
                "Ray Data URI must not be empty or contain control characters"
            )
        if "\\" in self.uri:
            raise ValueError("Ray Data URI must use POSIX separators")
        parsed = urllib.parse.urlsplit(self.uri)
        if parsed.scheme or parsed.netloc or parsed.username or parsed.password:
            raise ValueError("Ray Data URI must not contain a scheme or credentials")
        if parsed.query or parsed.fragment:
            raise ValueError("Ray Data URI must not contain a query or fragment")
        prefix = _STABLE_INPUT_ROOT + "/"
        if not self.uri.startswith(prefix):
            raise ValueError(f"Ray Data URI must stay below {_STABLE_INPUT_ROOT}")
        relative = self.uri[len(prefix) :]
        if any(segment in (".", "..") for segment in relative.split("/")):
            raise ValueError("Ray Data URI must not contain traversal segments")
        if not relative or posixpath.normpath(self.uri) != self.uri:
            raise ValueError("Ray Data URI must be a canonical mounted path")


def build_dataset(config: DatasetConfig) -> Any:
    """Build the registered Ray Dataset without loading Ray Data at import time."""

    from ray import data

    if config.format == "parquet":
        return data.read_parquet(config.uri)
    if config.format == "images":
        return data.read_images(config.uri, include_paths=True)
    raise ValueError(f"unsupported Ray Data format: {config.format}")


def worker_iterator(name: str = "train") -> Any:
    """Return prefetched Torch batches from a named Ray Train dataset shard."""

    from ray import train

    shard = train.get_dataset_shard(name)
    if shard is None:
        raise RuntimeError(f"Ray Data shard {name!r} is unavailable")
    return shard.iter_torch_batches(prefetch_batches=2)
