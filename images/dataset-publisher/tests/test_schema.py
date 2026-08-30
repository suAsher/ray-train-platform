from __future__ import annotations

import sys
import tempfile
import unittest
import pickle
import importlib.util
from pathlib import Path

import numpy as np


PROJECT_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(PROJECT_ROOT))

from raytrain_publisher.schema import (  # noqa: E402
    ROW_FIELD_NAMES,
    compute_source_digest,
    dump_trusted_info,
    get_arrow_schema,
    load_trusted_info,
    pack_float32_points,
    unpack_float32_points,
    validate_sample_row,
)


class Float32PointPayloadTests(unittest.TestCase):
    def test_pack_and_unpack_match_numpy_fromfile_bytes(self) -> None:
        expected = np.array(
            [
                [1.25, -2.5, 3.75, 0.5, 7.0],
                [-0.0, 8.5, -9.25, 10.0, 0.125],
            ],
            dtype=np.float32,
        )

        with tempfile.TemporaryDirectory() as temp_dir:
            point_path = Path(temp_dir) / "sample.bin"
            expected.tofile(point_path)
            from_file = np.fromfile(point_path, dtype=np.float32).reshape(-1, 5)

            payload = pack_float32_points(from_file)
            unpacked = unpack_float32_points(payload, columns=5)

            self.assertEqual(payload, point_path.read_bytes())
            self.assertEqual(unpacked.dtype, np.dtype(np.float32))
            self.assertEqual(unpacked.shape, expected.shape)
            self.assertEqual(unpacked.tobytes(order="C"), from_file.tobytes(order="C"))

    def test_point_payload_rejects_non_float32_and_invalid_shape(self) -> None:
        with self.assertRaisesRegex(ValueError, "float32"):
            pack_float32_points(np.array([[1.0, 2.0]], dtype=np.float64))

        with self.assertRaisesRegex(ValueError, "two-dimensional"):
            pack_float32_points(np.array([1.0, 2.0], dtype=np.float32))

        with self.assertRaisesRegex(ValueError, "divisible"):
            unpack_float32_points(b"not-float32", columns=5)

        with self.assertRaisesRegex(ValueError, "positive"):
            unpack_float32_points(b"", columns=0)


class SampleRowSchemaTests(unittest.TestCase):
    def _valid_row(self) -> dict:
        points = np.array([[1.0, 2.0, 3.0, 0.25, 7.0]], dtype=np.float32)
        point_payload = pack_float32_points(points)
        info_payload = dump_trusted_info(
            {
                "boxes": [[1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 0.0]],
                "labels": [2],
                "lidar_feature_count": 5,
            }
        )
        digest = compute_source_digest(
            token="token-001",
            scene="scene-a",
            split="train",
            class_ids=[2, 1],
            timestamp=1_725_000_000_000_000,
            points=point_payload,
            info=info_payload,
        )
        return {
            "token": "token-001",
            "scene": "scene-a",
            "split": "train",
            "class_ids": [2, 1],
            "timestamp": 1_725_000_000_000_000,
            "points": point_payload,
            "info": info_payload,
            "source_digest": digest,
        }

    def test_row_has_complete_lidar_only_schema(self) -> None:
        row = self._valid_row()

        validated = validate_sample_row(row)

        self.assertEqual(tuple(validated), ROW_FIELD_NAMES)
        self.assertEqual(
            ROW_FIELD_NAMES,
            (
                "token",
                "scene",
                "split",
                "class_ids",
                "timestamp",
                "points",
                "info",
                "source_digest",
            ),
        )
        self.assertNotIn("camera", validated)
        self.assertNotIn("images", validated)
        self.assertIsNot(validated, row)

    def test_source_digest_is_deterministic_and_sensitive_to_payload(self) -> None:
        row = self._valid_row()
        digest_one = compute_source_digest(
            token=row["token"],
            scene=row["scene"],
            split=row["split"],
            class_ids=row["class_ids"],
            timestamp=row["timestamp"],
            points=row["points"],
            info=row["info"],
        )
        digest_two = compute_source_digest(
            token=row["token"],
            scene=row["scene"],
            split=row["split"],
            class_ids=tuple(row["class_ids"]),
            timestamp=row["timestamp"],
            points=row["points"],
            info=row["info"],
        )
        changed = compute_source_digest(
            token=row["token"],
            scene=row["scene"],
            split=row["split"],
            class_ids=row["class_ids"],
            timestamp=row["timestamp"],
            points=row["points"] + b"\x00\x00\x00\x00",
            info=row["info"],
        )

        self.assertEqual(digest_one, digest_two)
        self.assertRegex(digest_one, r"^[0-9a-f]{64}$")
        self.assertNotEqual(digest_one, changed)

    def test_row_rejects_missing_unknown_camera_and_invalid_values(self) -> None:
        valid = self._valid_row()

        for missing_name in ROW_FIELD_NAMES:
            invalid = {key: value for key, value in valid.items() if key != missing_name}
            with self.subTest(missing=missing_name):
                with self.assertRaisesRegex(ValueError, "fields"):
                    validate_sample_row(invalid)

        with self.assertRaisesRegex(ValueError, "fields"):
            validate_sample_row({**valid, "camera_front": b"jpeg"})
        with self.assertRaisesRegex(ValueError, "scene"):
            validate_sample_row({**valid, "scene": "../escape"})
        with self.assertRaisesRegex(ValueError, "split"):
            validate_sample_row({**valid, "split": "training"})
        with self.assertRaisesRegex(ValueError, "class_ids"):
            validate_sample_row({**valid, "class_ids": [32768]})
        with self.assertRaisesRegex(ValueError, "timestamp"):
            validate_sample_row({**valid, "timestamp": True})
        with self.assertRaisesRegex(ValueError, "source_digest"):
            validate_sample_row({**valid, "source_digest": "secret-token"})

        malformed_info = {**valid, "info": pickle.dumps({"labels": [2]})}
        malformed_info["source_digest"] = compute_source_digest(
            token=malformed_info["token"],
            scene=malformed_info["scene"],
            split=malformed_info["split"],
            class_ids=malformed_info["class_ids"],
            timestamp=malformed_info["timestamp"],
            points=malformed_info["points"],
            info=malformed_info["info"],
        )
        with self.assertRaisesRegex(ValueError, "trusted publisher"):
            validate_sample_row(malformed_info)


def _create_marker_file(path: str) -> int:
    Path(path).write_text("executed", encoding="utf-8")
    return 0


class _PickleExploit:
    def __init__(self, marker_path: str) -> None:
        self.marker_path = marker_path

    def __reduce__(self):
        return _create_marker_file, (self.marker_path,)


class TrustedInfoPickleTests(unittest.TestCase):
    def test_trusted_info_round_trip_is_explicit_and_deterministic(self) -> None:
        first = {
            "pose": {"translation": [1.0, 2.0, 3.0], "valid": True},
            "labels": [3, 1],
            "calibration": b"calibration-bytes",
            "optional": None,
        }
        same_content_different_order = {
            "optional": None,
            "calibration": b"calibration-bytes",
            "labels": [3, 1],
            "pose": {"valid": True, "translation": [1.0, 2.0, 3.0]},
        }

        encoded = dump_trusted_info(first)

        self.assertEqual(encoded, dump_trusted_info(same_content_different_order))
        self.assertEqual(load_trusted_info(encoded), first)

    def test_restricted_unpickler_rejects_globals_without_execution(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            marker_path = Path(temp_dir) / "pickle-executed"
            malicious = pickle.dumps(
                {
                    "__raytrain_publisher_format__": "trusted-info-v1",
                    "payload": _PickleExploit(str(marker_path)),
                },
                protocol=pickle.HIGHEST_PROTOCOL,
            )

            with self.assertRaisesRegex(pickle.UnpicklingError, "forbidden"):
                load_trusted_info(malicious)

            self.assertFalse(marker_path.exists())

    def test_loader_rejects_untrusted_or_ambiguous_structures(self) -> None:
        with self.assertRaisesRegex(pickle.UnpicklingError, "trusted publisher"):
            load_trusted_info(pickle.dumps({"labels": [1]}))

        with self.assertRaisesRegex(ValueError, "supported"):
            dump_trusted_info({"ambiguous_tuple": (1, 2)})

        with self.assertRaisesRegex(ValueError, "finite"):
            dump_trusted_info({"invalid": float("nan")})

        with self.assertRaisesRegex(ValueError, "sensitive"):
            dump_trusted_info({"aws_secret_access_key": "must-not-be-published"})
        with self.assertRaisesRegex(ValueError, "sensitive"):
            dump_trusted_info({"AK": "must-not-be-published"})


class ArrowSchemaTests(unittest.TestCase):
    def test_arrow_schema_is_lidar_only_and_strictly_typed(self) -> None:
        if importlib.util.find_spec("pyarrow") is None:
            with self.assertRaisesRegex(RuntimeError, r"pyarrow==25\.0\.1"):
                get_arrow_schema()
            return

        import pyarrow as pa

        schema = get_arrow_schema()

        self.assertEqual(tuple(schema.names), ROW_FIELD_NAMES)
        self.assertEqual(schema.field("token").type, pa.string())
        self.assertEqual(schema.field("scene").type, pa.string())
        self.assertEqual(schema.field("split").type, pa.string())
        self.assertEqual(schema.field("class_ids").type, pa.list_(pa.int16()))
        self.assertEqual(schema.field("timestamp").type, pa.int64())
        self.assertEqual(schema.field("points").type, pa.binary())
        self.assertEqual(schema.field("info").type, pa.binary())
        self.assertEqual(schema.field("source_digest").type, pa.string())
        self.assertTrue(all(not field.nullable for field in schema))
        self.assertEqual(schema.metadata[b"raytrain.schema_version"], b"parquet-v1")
        self.assertFalse(any("camera" in name or "image" in name for name in schema.names))


if __name__ == "__main__":
    unittest.main()
