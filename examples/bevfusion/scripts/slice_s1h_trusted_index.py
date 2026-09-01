#!/usr/bin/env python3
"""Create a deterministic, bounded S1H acceptance index from a full index."""

from __future__ import annotations

import argparse
import hashlib
import heapq
import json
from pathlib import Path
from typing import Any, Iterable, Sequence

from build_s1h_trusted_index import write_multimodal_index_bundle


SELECTION_DOMAIN = b"raytrain-s1h-acceptance-slice-v1\0"
MULTIMODAL_SCHEMA_VERSION = "s1h-multimodal-webdataset-v2"
MULTIMODAL_INDEX_FORMAT = "trusted-index-v3"
MULTIMODAL_INDEX_MANIFEST_FORMAT = "trusted-index-sharded-v2"


def select_samples(
    source_index: Path,
    *,
    train_samples: int,
    val_samples: int,
) -> tuple[list[dict[str, Any]], dict[str, int]]:
    limits = {"train": train_samples, "val": val_samples}
    if any(isinstance(value, bool) or not isinstance(value, int) or value < 1 for value in limits.values()):
        raise ValueError("slice sample limits must be positive integers")
    heaps: dict[str, list[tuple[int, str, dict[str, Any]]]] = {
        split: [] for split in limits
    }
    seen_tokens: set[str] = set()
    source_counts = {"train": 0, "val": 0}
    for sample in _iter_verified_samples(Path(source_index), seen_tokens=seen_tokens):
        split = sample["split"]
        if split not in limits:
            continue
        source_counts[split] += 1
        rank = int.from_bytes(
            hashlib.sha256(SELECTION_DOMAIN + sample["token"].encode("utf-8")).digest(),
            byteorder="big",
        )
        entry = (-rank, sample["token"], sample)
        heap = heaps[split]
        if len(heap) < limits[split]:
            heapq.heappush(heap, entry)
        elif rank < -heap[0][0]:
            heapq.heapreplace(heap, entry)
    if any(source_counts[split] < limit for split, limit in limits.items()):
        raise ValueError("full trusted index does not contain the requested acceptance slice")
    selected = [entry[2] for split in ("train", "val") for entry in heaps[split]]
    selected.sort(
        key=lambda sample: (
            sample["split"].encode("utf-8"),
            sample["token"].encode("utf-8"),
        )
    )
    return selected, source_counts


def _iter_verified_samples(
    source_index: Path,
    *,
    seen_tokens: set[str],
) -> Iterable[dict[str, Any]]:
    root = _json_document(source_index.read_bytes(), "slice source manifest")
    required = {
        "format", "sample_schema_version", "class_count", "cbgs_seed",
        "sample_count", "parts",
    }
    if set(root) != required or root["format"] != MULTIMODAL_INDEX_MANIFEST_FORMAT:
        raise ValueError("slice source manifest is invalid")
    if root["sample_schema_version"] != MULTIMODAL_SCHEMA_VERSION:
        raise ValueError("slice source schema is unsupported")
    parts = root["parts"]
    if type(parts) is not list or not parts:
        raise ValueError("slice source manifest is invalid")
    declared_count = 0
    for part in parts:
        if type(part) is not dict or set(part) != {"key", "sha256", "sample_count"}:
            raise ValueError("slice source part is invalid")
        digest, key, count = part["sha256"], part["key"], part["sample_count"]
        if (
            not isinstance(digest, str)
            or len(digest) != 64
            or any(character not in "0123456789abcdef" for character in digest)
            or key != f"{source_index.stem}.parts/sha256-{digest}.json"
            or isinstance(count, bool)
            or not isinstance(count, int)
            or count < 1
        ):
            raise ValueError("slice source part is invalid")
        payload = (source_index.parent / key).read_bytes()
        if hashlib.sha256(payload).hexdigest() != digest:
            raise ValueError("slice source part digest verification failed")
        document = _json_document(payload, "slice source part")
        if (
            document.get("format") != MULTIMODAL_INDEX_FORMAT
            or document.get("sample_schema_version") != MULTIMODAL_SCHEMA_VERSION
            or document.get("class_count") != root["class_count"]
            or document.get("cbgs_seed") != root["cbgs_seed"]
            or type(document.get("samples")) is not list
            or len(document["samples"]) != count
        ):
            raise ValueError("slice source part contract is invalid")
        for value in document["samples"]:
            sample = _validated_source_sample(value)
            if sample["token"] in seen_tokens:
                raise ValueError("slice source contains a duplicate token")
            seen_tokens.add(sample["token"])
            yield sample
        declared_count += count
    if declared_count != root["sample_count"]:
        raise ValueError("slice source sample count is invalid")


def _validated_source_sample(value: object) -> dict[str, Any]:
    if type(value) is not dict or value.get("schema_version") != MULTIMODAL_SCHEMA_VERSION:
        raise ValueError("slice source sample is invalid")
    token, split = value.get("token"), value.get("split")
    if (
        not isinstance(token, str)
        or not token
        or token.strip() != token
        or split not in {"train", "val", "test"}
        or type(value.get("payloads")) is not dict
        or type(value.get("info")) is not dict
    ):
        raise ValueError("slice source sample is invalid")
    return dict(value)


def _json_document(payload: bytes, field_name: str) -> dict[str, Any]:
    try:
        value = json.loads(payload)
    except (UnicodeDecodeError, json.JSONDecodeError):
        raise ValueError(f"{field_name} is invalid") from None
    if type(value) is not dict:
        raise ValueError(f"{field_name} is invalid")
    return value


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source-index", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--train-samples", type=int, required=True)
    parser.add_argument("--val-samples", type=int, required=True)
    parser.add_argument("--samples-per-part", type=int, default=2000)
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    arguments = parse_args(argv)
    selected, source_counts = select_samples(
        arguments.source_index,
        train_samples=arguments.train_samples,
        val_samples=arguments.val_samples,
    )
    bundle = write_multimodal_index_bundle(
        selected,
        output=arguments.output,
        cbgs_seed=0,
        samples_per_part=arguments.samples_per_part,
    )
    summary = {
        **bundle,
        "samples": len(selected),
        "source_train_samples": source_counts["train"],
        "source_val_samples": source_counts["val"],
        "train_samples": sum(sample["split"] == "train" for sample in selected),
        "val_samples": sum(sample["split"] == "val" for sample in selected),
    }
    print(json.dumps(summary, separators=(",", ":"), sort_keys=True), flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
