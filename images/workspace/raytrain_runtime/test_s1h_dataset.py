from __future__ import annotations

import hashlib
import importlib
import inspect
import json
import os
import pathlib
import pickle
import sys
import tempfile
import unittest
from collections.abc import Iterator, Mapping
from typing import Any

import numpy as np


RUNTIME_PARENT = pathlib.Path(__file__).resolve().parent.parent
if str(RUNTIME_PARENT) not in sys.path:
    sys.path.insert(0, str(RUNTIME_PARENT))


def _runtime_module():
    try:
        return importlib.import_module("raytrain_runtime.s1h_dataset")
    except ModuleNotFoundError as error:
        raise AssertionError("the S1H Ray Data bridge is not implemented") from error


def _trusted_info(info: dict[str, Any]) -> bytes:
    return pickle.dumps(
        {
            "__raytrain_publisher_format__": "trusted-info-v1",
            "payload": info,
        },
        protocol=pickle.HIGHEST_PROTOCOL,
    )


def _source_digest(row: Mapping[str, Any]) -> str:
    metadata = json.dumps(
        {
            "token": row["token"],
            "scene": row["scene"],
            "split": row["split"],
            "class_ids": tuple(row["class_ids"]),
            "timestamp": row["timestamp"],
        },
        ensure_ascii=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    digest = hashlib.sha256(b"raytrain-source-v1\x00")
    for component in (metadata, bytes(row["points"]), bytes(row["info"])):
        digest.update(len(component).to_bytes(8, byteorder="big", signed=False))
        digest.update(component)
    return digest.hexdigest()


def _row(
    token: str = "token-001",
    *,
    class_ids: tuple[int, ...] = (2, 1),
    points: np.ndarray | None = None,
    info: dict[str, Any] | None = None,
) -> dict[str, Any]:
    point_values = points
    if point_values is None:
        point_values = np.array(
            [
                [1.25, -2.5, 3.75, 0.5, 7.0],
                [-0.0, 8.5, -9.25, 10.0, 0.125],
            ],
            dtype=np.float32,
        )
    trusted = info if info is not None else {
        "lidar_feature_count": 5,
        "boxes": [
            [1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 0.25, 0.5, -0.5],
            [7.0, 8.0, 9.0, 2.0, 3.0, 4.0, -0.25, 0.0, 0.0],
        ],
        "labels": [2, 1],
    }
    row = {
        "token": token,
        "scene": "scene-a",
        "split": "train",
        "class_ids": list(class_ids),
        "timestamp": 1_725_000_000_000_000,
        "points": np.ascontiguousarray(point_values).tobytes(order="C"),
        "info": _trusted_info(trusted),
        "source_digest": "0" * 64,
    }
    row["source_digest"] = _source_digest(row)
    return row


def _write_marker(path: str) -> int:
    pathlib.Path(path).write_text("unsafe pickle executed", encoding="utf-8")
    return 0


class _PickleExploit:
    def __init__(self, marker: str) -> None:
        self.marker = marker

    def __reduce__(self):
        return _write_marker, (self.marker,)


class S1HRowDecodeTest(unittest.TestCase):
    def test_row_roundtrip_recovers_pipeline_dict_and_float32_points(self):
        bridge = _runtime_module()
        original = _row()
        original_copy = dict(original)

        sample = bridge.decode_s1h_row(original)

        self.assertEqual(sample["token"], "token-001")
        self.assertEqual(sample["sample_idx"], "token-001")
        self.assertEqual(sample["scene"], "scene-a")
        self.assertEqual(sample["split"], "train")
        self.assertEqual(sample["class_ids"], (2, 1))
        self.assertEqual(sample["timestamp"], 1_725_000_000_000_000)
        self.assertEqual(sample["source_digest"], original["source_digest"])
        self.assertEqual(sample["sweeps"], [])
        self.assertEqual(
            sample["ann_info"]["gt_bboxes_3d"].dtype,
            np.dtype(np.float32),
        )
        self.assertEqual(sample["ann_info"]["gt_bboxes_3d"].shape, (2, 9))
        self.assertEqual(
            sample["ann_info"]["gt_labels_3d"].dtype,
            np.dtype(np.int64),
        )
        self.assertEqual(sample["ann_info"]["gt_labels_3d"].tolist(), [2, 1])
        self.assertEqual(sample["points"].dtype, np.dtype(np.float32))
        self.assertEqual(sample["points"].shape, (2, 5))
        self.assertEqual(sample["points"].tobytes(order="C"), original["points"])
        self.assertTrue(sample["points"].flags.writeable)
        self.assertEqual(original, original_copy)
        self.assertNotIn("cams", sample)
        self.assertNotIn("image_paths", sample)

    def test_point_shape_uses_compatible_five_column_default(self):
        bridge = _runtime_module()
        row = _row(
            class_ids=(1,),
            info={
                "boxes": [[1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 0.0]],
                "labels": [1],
            }
        )

        sample = bridge.decode_s1h_row(row)

        self.assertEqual(sample["points"].shape, (2, 5))

    def test_rejects_point_shape_and_digest_corruption_with_clear_errors(self):
        bridge = _runtime_module()
        invalid_shape = _row()
        invalid_shape["points"] = invalid_shape["points"][:-4]
        invalid_shape["source_digest"] = _source_digest(invalid_shape)
        with self.assertRaisesRegex(bridge.S1HRowError, "point.*shape"):
            bridge.decode_s1h_row(invalid_shape)

        invalid_digest = _row()
        invalid_digest["points"] = (
            bytes([invalid_digest["points"][0] ^ 1])
            + invalid_digest["points"][1:]
        )
        with self.assertRaisesRegex(bridge.S1HRowError, "digest"):
            bridge.decode_s1h_row(invalid_digest)

    def test_corrupt_row_error_does_not_echo_uri_or_secret_values(self):
        bridge = _runtime_module()
        private_uri = "tos://access-key:secret-token@internal-bucket/private"
        row = _row(token=private_uri)

        with self.assertRaises(bridge.S1HRowError) as caught:
            bridge.decode_s1h_row(row)

        message = str(caught.exception)
        self.assertIn("token", message)
        self.assertNotIn("tos://", message)
        self.assertNotIn("access-key", message)
        self.assertNotIn("secret-token", message)
        self.assertNotIn("internal-bucket", message)

    def test_restricted_info_decoder_rejects_globals_without_execution(self):
        bridge = _runtime_module()
        with tempfile.TemporaryDirectory() as temporary:
            marker = pathlib.Path(temporary) / "executed"
            row = _row()
            row["info"] = pickle.dumps(
                {
                    "__raytrain_publisher_format__": "trusted-info-v1",
                    "payload": {"exploit": _PickleExploit(str(marker))},
                },
                protocol=pickle.HIGHEST_PROTOCOL,
            )
            row["source_digest"] = _source_digest(row)

            with self.assertRaisesRegex(bridge.S1HRowError, "trusted info"):
                bridge.decode_s1h_row(row)

            self.assertFalse(marker.exists())

    def test_info_uses_an_explicit_lidar_only_allowlist(self):
        bridge = _runtime_module()
        forbidden_values = (
            {
                "cams": {
                    "CAM_FRONT": {
                        "data_path": "tos://internal/private/front.jpg",
                    }
                }
            },
            {"camera_front": {"data_path": "/private/front.jpg"}},
            {"provenance": {"source_uri": "tos://internal/private/index"}},
            {"pose": {"translation": [1.0, 2.0, 3.0]}},
        )
        for forbidden_info in forbidden_values:
            with self.subTest(forbidden_field=next(iter(forbidden_info))):
                row = _row(
                    info={
                        "lidar_feature_count": 5,
                        "boxes": [],
                        "labels": [],
                        **forbidden_info,
                    }
                )
                with self.assertRaisesRegex(
                    bridge.S1HRowError, "allowlist"
                ) as caught:
                    bridge.decode_s1h_row(row)

                self.assertNotIn("tos://", str(caught.exception))
                self.assertNotIn("front.jpg", str(caught.exception))

    def test_allowlisted_box_and_label_shapes_must_match(self):
        bridge = _runtime_module()
        mismatched = _row(
            info={
                "lidar_feature_count": 5,
                "boxes": [[1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 0.0]],
                "labels": [],
            }
        )
        invalid_box_width = _row(
            info={
                "lidar_feature_count": 5,
                "boxes": [[1.0, 2.0, 3.0]],
                "labels": [1],
            }
        )

        with self.assertRaisesRegex(bridge.S1HRowError, "boxes.*labels"):
            bridge.decode_s1h_row(mismatched)
        with self.assertRaisesRegex(bridge.S1HRowError, "box.*7 or 9"):
            bridge.decode_s1h_row(invalid_box_width)

    def test_annotation_labels_must_match_top_level_cbgs_classes(self):
        bridge = _runtime_module()
        row = _row(
            class_ids=(0, 2),
            info={
                "boxes": [[1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 0.0]],
                "labels": [2],
            },
        )

        with self.assertRaisesRegex(bridge.S1HRowError, "labels.*class_ids"):
            bridge.decode_s1h_row(row)

        duplicate_classes = _row(
            class_ids=(2, 2),
            info={
                "boxes": [[1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 0.0]],
                "labels": [2],
            },
        )
        with self.assertRaisesRegex(bridge.S1HRowError, "class_ids.*duplicate"):
            bridge.decode_s1h_row(duplicate_classes)


class _MetadataOnlyRow(Mapping[str, Any]):
    def __init__(self, token: str, class_ids: tuple[int, ...]) -> None:
        self.values = {
            "token": token,
            "class_ids": class_ids,
            "source_digest": hashlib.sha256(token.encode("utf-8")).hexdigest(),
        }
        self.reads: list[str] = []

    def __getitem__(self, key: str) -> Any:
        self.reads.append(key)
        if key in {"points", "info"}:
            raise AssertionError("sample refs must not read payload columns")
        return self.values[key]

    def __iter__(self) -> Iterator[str]:
        return iter(self.values)

    def __len__(self) -> int:
        return len(self.values)


class SampleReferenceTest(unittest.TestCase):
    def test_sample_ref_iterator_is_lazy_and_never_retains_payload(self):
        bridge = _runtime_module()
        rows = [
            _MetadataOnlyRow("token-a", (0, 1)),
            _MetadataOnlyRow("token-b", (1,)),
        ]

        refs = bridge.iter_sample_refs(rows)
        self.assertEqual(rows[0].reads, [])
        first = next(refs)

        self.assertEqual(first.ordinal, 0)
        self.assertEqual(first.token, "token-a")
        self.assertEqual(first.class_ids, (0, 1))
        self.assertFalse(hasattr(first, "points"))
        self.assertFalse(hasattr(first, "info"))
        self.assertEqual(rows[1].reads, [])
        self.assertNotIn("points", rows[0].reads)
        self.assertNotIn("info", rows[0].reads)


def _refs(bridge):
    class_ids = (
        (0,),
        (0, 1),
        (1,),
        (1, 2),
        (2,),
        (0, 2),
        (0,),
        (1,),
        (2,),
    )
    return tuple(
        bridge.SampleRef(
            ordinal=index,
            token=f"token-{index}",
            class_ids=categories,
            source_digest=hashlib.sha256(f"token-{index}".encode("utf-8")).hexdigest(),
        )
        for index, categories in enumerate(class_ids)
    )


def _legacy_cbgs_indices(
    refs: tuple[Any, ...], *, class_count: int, seed: int
) -> tuple[list[int], tuple[int, ...]]:
    class_sample_indices = {class_id: [] for class_id in range(class_count)}
    for index, ref in enumerate(refs):
        for class_id in ref.class_ids:
            class_sample_indices[class_id].append(index)
    duplicated_samples = sum(len(indices) for indices in class_sample_indices.values())
    class_distribution = {
        class_id: len(indices) / duplicated_samples
        for class_id, indices in class_sample_indices.items()
    }
    fraction = 1.0 / class_count
    ratios = [fraction / value for value in class_distribution.values()]
    rng = np.random.RandomState(seed)
    selected: list[int] = []
    counts: list[int] = []
    for class_indices, ratio in zip(class_sample_indices.values(), ratios):
        count = int(len(class_indices) * ratio)
        counts.append(count)
        selected.extend(rng.choice(class_indices, count).tolist())
    return selected, tuple(counts)


class CBGSEquivalenceTest(unittest.TestCase):
    def test_fixed_seed_matches_legacy_single_initialization_plan(self):
        bridge = _runtime_module()
        refs = _refs(bridge)
        expected_indices, expected_counts = _legacy_cbgs_indices(
            refs, class_count=3, seed=91
        )

        plan = bridge.build_cbgs_plan(refs, class_count=3, seed=91)

        self.assertNotIn(
            "epoch",
            inspect.signature(bridge.build_cbgs_plan).parameters,
        )
        self.assertFalse(hasattr(plan, "epoch"))
        self.assertEqual(plan.class_counts, expected_counts)
        self.assertEqual(len(plan), len(expected_indices))
        self.assertEqual(
            [sample.token for sample in plan[:8]],
            [refs[index].token for index in expected_indices[:8]],
        )
        self.assertEqual(
            [sample.token for sample in plan],
            [refs[index].token for index in expected_indices],
        )

    def test_seed_is_deterministic_without_mutating_numpy_global_rng(self):
        bridge = _runtime_module()
        refs = _refs(bridge)
        np.random.seed(12345)
        expected_global_next = np.random.RandomState(12345).randint(0, 1_000_000)

        first = bridge.build_cbgs_plan(refs, class_count=3, seed=17)
        repeated = bridge.build_cbgs_plan(refs, class_count=3, seed=17)
        different_seed = bridge.build_cbgs_plan(refs, class_count=3, seed=18)

        self.assertEqual(first, repeated)
        self.assertNotEqual(
            [sample.token for sample in first],
            [sample.token for sample in different_seed],
        )
        self.assertEqual(np.random.randint(0, 1_000_000), expected_global_next)

    def test_cbgs_rejects_missing_classes_instead_of_silently_changing_counts(self):
        bridge = _runtime_module()
        refs = tuple(ref for ref in _refs(bridge) if 2 not in ref.class_ids)

        with self.assertRaisesRegex(ValueError, "class 2.*no samples"):
            bridge.build_cbgs_plan(refs, class_count=3, seed=1)

        with self.assertRaisesRegex(ValueError, "class_count.*32768"):
            bridge.build_cbgs_plan(refs, class_count=32_769, seed=1)


if __name__ == "__main__":
    unittest.main(verbosity=2)
