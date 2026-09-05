"""Bounded inspection of site metadata in a verified reference manifest."""

from __future__ import annotations

import json
import re
from collections import Counter

_SITE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$")
MAX_SITES = 256


def normalize_sites(values) -> tuple[str, ...]:
    if not isinstance(values, (list, tuple)) or len(values) > MAX_SITES:
        raise ValueError("dataset sites must be a list of at most 256 site codes")
    if any(not isinstance(value, str) or not _SITE.fullmatch(value) for value in values):
        raise ValueError("dataset site code is invalid")
    return tuple(sorted(set(values)))


def parse_sites_json(value: str) -> tuple[str, ...]:
    if not isinstance(value, str) or len(value) > 40_000:
        raise ValueError("dataset sites JSON is invalid")
    try:
        return normalize_sites(json.loads(value))
    except (TypeError, json.JSONDecodeError):
        raise ValueError("dataset sites JSON must be an array of site codes") from None


def selected_training_count(path: str, sites: tuple[str, ...], *, parquet_module=None) -> int:
    """Read only site/split columns; never collect sample payloads on the driver.

    Callers verify the manifest digest before invoking this function. Fail closed
    if any training row lacks site metadata: accepting a partial inventory would
    silently omit unidentified samples from the selected training scope.
    """
    sites = normalize_sites(sites)
    if not sites:
        raise ValueError("site selection must not be empty")
    if parquet_module is None:
        import pyarrow.parquet as parquet_module
    requested = frozenset(sites)
    counts = Counter({site: 0 for site in sites})
    with parquet_module.ParquetFile(path) as manifest:
        if not {"site_id", "split"}.issubset(manifest.schema_arrow.names):
            raise ValueError("dataset version lacks site metadata; republish before selecting sites")
        for batch in manifest.iter_batches(batch_size=8192, columns=["site_id", "split"]):
            columns = batch.to_pydict()
            for site, split in zip(columns["site_id"], columns["split"]):
                if split != "train":
                    continue
                if not isinstance(site, str) or not _SITE.fullmatch(site):
                    raise ValueError("dataset version has incomplete site metadata")
                if site in requested:
                    counts[site] += 1
    if any(count == 0 for count in counts.values()):
        raise ValueError("selected site is unknown or has no training samples in this version")
    return sum(counts.values())
