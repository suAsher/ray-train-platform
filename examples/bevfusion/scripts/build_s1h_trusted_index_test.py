"""Contract tests for the S1H PKL to trusted-index adapter."""

from __future__ import annotations

import sys
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import numpy as np


SCRIPT_DIR = Path(__file__).resolve().parent
PUBLISHER_ROOT = Path(__file__).resolve().parents[3] / "images" / "dataset-publisher"
for path in (SCRIPT_DIR, PUBLISHER_ROOT):
    if str(path) not in sys.path:
        sys.path.insert(0, str(path))


class BuildS1HTrustedIndexTest(unittest.TestCase):
    def setUp(self) -> None:
        try:
            import build_s1h_trusted_index as adapter
        except ModuleNotFoundError as error:
            raise AssertionError("S1H trusted-index adapter is not implemented") from error
        self.adapter = adapter

    def test_converts_lidar_only_info_with_exact_s1h_class_order(self):
        with tempfile.TemporaryDirectory() as directory:
            source_root = Path(directory)
            lidar = source_root / "cnfzhjyg" / "package" / "samples" / "LIDAR_TOP" / "1.bin"
            lidar.parent.mkdir(parents=True)
            lidar.write_bytes(np.arange(8, dtype=np.float32).tobytes())
            info = self._info(
                lidar,
                gt_names=np.array(["Car", "igv.w_container", "unknown"]),
                gt_boxes=np.array(
                    [
                        [1, 2, 3, 4, 5, 6, 0.1],
                        [7, 8, 9, 10, 11, 12, 0.2],
                        [13, 14, 15, 16, 17, 18, 0.3],
                    ],
                    dtype=np.float32,
                ),
            )

            samples = self.adapter.convert_infos(
                [info], split="train", source_root=source_root
            )

        self.assertEqual(len(samples), 1)
        sample = samples[0]
        self.assertEqual(sample["token"], "sample-token")
        self.assertEqual(sample["scene"], "scene-token")
        self.assertEqual(sample["split"], "train")
        self.assertEqual(sample["class_ids"], [0, 2])
        self.assertEqual(
            sample["lidar_path"],
            "cnfzhjyg/package/samples/LIDAR_TOP/1.bin",
        )
        self.assertEqual(sample["point_columns"], 4)
        self.assertEqual(sample["info"]["labels"], [0, 2])
        self.assertEqual(len(sample["info"]["boxes"]), 2)

    def test_converts_multimodal_info_to_path_free_v2_payload_descriptors(self):
        with tempfile.TemporaryDirectory() as directory:
            source_root = Path(directory)
            lidar = source_root / "site" / "samples" / "LIDAR_TOP" / "1.bin"
            sweep = source_root / "site" / "sweeps" / "LIDAR_TOP" / "0.bin"
            camera = source_root / "site" / "samples" / "CAM_FRONT" / "1.jpg"
            for payload_path, payload in (
                (lidar, np.arange(8, dtype=np.float32).tobytes()),
                (sweep, np.arange(8, dtype=np.float32).tobytes()),
                (camera, b"jpeg-payload"),
            ):
                payload_path.parent.mkdir(parents=True, exist_ok=True)
                payload_path.write_bytes(payload)
            info = self._info(lidar)
            info.update(
                {
                    "sweeps": [
                        {
                            "lidar_path": str(sweep),
                            "timestamp": 123400,
                            "sensor2lidar_rotation": np.eye(3),
                            "sensor2lidar_translation": np.zeros(3),
                        }
                    ],
                    "cams": {
                        "CAM_FRONT": {
                            "data_path": str(camera),
                            "sensor2lidar_rotation": np.eye(3),
                            "sensor2lidar_translation": np.zeros(3),
                            "sensor2ego_rotation": np.array([1.0, 0.0, 0.0, 0.0]),
                            "sensor2ego_translation": np.zeros(3),
                            "camera_intrinsics": np.eye(3),
                        }
                    },
                    "lidar2ego_rotation": np.array([1.0, 0.0, 0.0, 0.0]),
                    "lidar2ego_translation": np.zeros(3),
                    "ego2global_rotation": np.array([1.0, 0.0, 0.0, 0.0]),
                    "ego2global_translation": np.zeros(3),
                    "num_lidar_pts": np.array([4]),
                    "gt_velocity": np.array([[0.0, 0.0]], dtype=np.float32),
                }
            )

            samples = self.adapter.convert_multimodal_infos(
                [info], split="train", source_root=source_root
            )
            from raytrain_publisher.multimodal import pack_multimodal_shard

            packed = pack_multimodal_shard(samples, input_root=source_root)

        self.assertEqual(len(samples), 1)
        sample = samples[0]
        self.assertEqual(sample["schema_version"], "s1h-multimodal-webdataset-v2")
        self.assertNotIn("lidar_path", sample)
        self.assertEqual(sample["payloads"]["lidar"], "site/samples/LIDAR_TOP/1.bin")
        self.assertEqual(sample["payloads"]["sweeps"], ["site/sweeps/LIDAR_TOP/0.bin"])
        self.assertEqual(sample["payloads"]["cameras"], {"CAM_FRONT": "site/samples/CAM_FRONT/1.jpg"})
        self.assertEqual(sample["info"]["sweeps"][0]["timestamp"], 123400)
        self.assertEqual(packed.members[0]["token"], "sample-token")
        self.assertEqual(sample["info"]["cams"]["CAM_FRONT"]["camera_intrinsics"], [[1.0, 0.0, 0.0], [0.0, 1.0, 0.0], [0.0, 0.0, 1.0]])
        self.assertEqual(sample["info"]["lidar2ego_rotation"], [[1.0, 0.0, 0.0], [0.0, 1.0, 0.0], [0.0, 0.0, 1.0]])
        serialized = __import__("json").dumps(sample, sort_keys=True)
        self.assertNotIn(str(source_root), serialized)
        self.assertNotIn("data_path", serialized)

    def test_rejects_multimodal_info_with_unsafe_or_incomplete_camera_payloads(self):
        with tempfile.TemporaryDirectory() as directory:
            source_root = Path(directory)
            lidar = source_root / "site" / "samples" / "LIDAR_TOP" / "1.bin"
            lidar.parent.mkdir(parents=True)
            lidar.write_bytes(np.arange(8, dtype=np.float32).tobytes())
            incomplete = self._info(lidar)
            incomplete["sweeps"] = []
            incomplete["cams"] = {"CAM_FRONT": {"data_path": "../escape.jpg"}}
            with self.assertRaisesRegex(ValueError, "camera"):
                self.adapter.convert_multimodal_infos(
                    [incomplete], split="train", source_root=source_root
                )

    def test_serializes_output_with_the_publisher_restricted_contract(self):
        from raytrain_publisher.pack import load_trusted_index_document

        with tempfile.TemporaryDirectory() as directory:
            source_root = Path(directory)
            lidar = source_root / "site" / "scene" / "samples" / "LIDAR_TOP" / "1.bin"
            lidar.parent.mkdir(parents=True)
            lidar.write_bytes(np.arange(8, dtype=np.float32).tobytes())
            samples = self.adapter.convert_infos(
                [self._info(lidar)], split="val", source_root=source_root
            )
            payload = self.adapter.dump_index(samples, cbgs_seed=17)

        document = load_trusted_index_document(payload)
        self.assertEqual(document.class_count, 9)
        self.assertEqual(document.cbgs_seed, 17)
        self.assertEqual(document.samples[0]["split"], "val")

    def test_parallel_conversion_preserves_input_order(self):
        with tempfile.TemporaryDirectory() as directory:
            source_root = Path(directory)
            lidar = source_root / "site" / "scene" / "samples" / "LIDAR_TOP" / "1.bin"
            lidar.parent.mkdir(parents=True)
            lidar.write_bytes(np.arange(8, dtype=np.float32).tobytes())
            first = self._info(lidar)
            first["token"] = "first"
            second = self._info(lidar)
            second["token"] = "second"

            samples = self.adapter.convert_infos(
                [first, second],
                split="train",
                source_root=source_root,
                workers=2,
            )

        self.assertEqual([sample["token"] for sample in samples], ["first", "second"])

    def test_writes_a_content_addressed_sharded_bundle(self):
        from raytrain_publisher.pack import load_trusted_index_document, load_trusted_index_manifest

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            lidar = root / "site" / "scene" / "samples" / "LIDAR_TOP" / "1.bin"
            lidar.parent.mkdir(parents=True)
            lidar.write_bytes(np.arange(8, dtype=np.float32).tobytes())
            samples = []
            for index in range(3):
                info = self._info(lidar)
                info["token"] = f"token-{index}"
                samples.extend(self.adapter.convert_infos([info], split="train", source_root=root))
            output = root / "trusted-index-v2.pkl"

            summary = self.adapter.write_index_bundle(
                samples,
                output=output,
                cbgs_seed=3,
                samples_per_part=2,
            )

            manifest = load_trusted_index_manifest(output.read_bytes())
            self.assertEqual(manifest.sample_count, 3)
            self.assertEqual(len(manifest.parts), 2)
            restored = []
            for part in manifest.parts:
                payload = (root / part.key).read_bytes()
                self.assertEqual(__import__("hashlib").sha256(payload).hexdigest(), part.sha256)
                restored.extend(load_trusted_index_document(payload).samples)
            self.assertEqual([item["token"] for item in restored], ["token-0", "token-1", "token-2"])
            self.assertEqual(summary["part_count"], 2)

    def test_writes_a_sharded_multimodal_v2_index_without_source_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            lidar = root / "site" / "samples" / "LIDAR_TOP" / "1.bin"
            camera = root / "site" / "samples" / "CAM_FRONT" / "1.jpg"
            for path, payload in ((lidar, np.arange(8, dtype=np.float32).tobytes()), (camera, b"camera")):
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(payload)
            info = self._info(lidar)
            info.update(
                {
                    "sweeps": [],
                    "cams": {"CAM_FRONT": {"data_path": str(camera), "sensor2lidar_rotation": np.eye(3), "sensor2lidar_translation": np.zeros(3), "sensor2ego_rotation": np.eye(3), "sensor2ego_translation": np.zeros(3), "camera_intrinsics": np.eye(3)}},
                    "lidar2ego_rotation": np.eye(3), "lidar2ego_translation": np.zeros(3),
                    "ego2global_rotation": np.eye(3), "ego2global_translation": np.zeros(3),
                }
            )
            samples = self.adapter.convert_multimodal_infos([info], split="train", source_root=root)
            output = root / "trusted-index-v3.json"

            summary = self.adapter.write_multimodal_index_bundle(
                samples, output=output, cbgs_seed=7, samples_per_part=1
            )
            manifest = __import__("json").loads(output.read_text(encoding="utf-8"))
            part_path = root / manifest["parts"][0]["key"]
            part = __import__("json").loads(part_path.read_text(encoding="utf-8"))

        self.assertEqual(manifest["format"], "trusted-index-sharded-v2")
        self.assertEqual(manifest["sample_schema_version"], "s1h-multimodal-webdataset-v2")
        self.assertEqual(part["format"], "trusted-index-v3")
        self.assertEqual(part["samples"][0]["token"], "sample-token")
        self.assertEqual(summary["part_count"], 1)
        self.assertNotIn(str(root), __import__("json").dumps(part, sort_keys=True))

    def test_cli_requires_explicit_multimodal_format_selection(self):
        arguments = self.adapter.parse_args(
            [
                "--source-root", "source", "--train-pkl", "train.pkl",
                "--output", "trusted-index-v3.json", "--format", "multimodal-v2",
            ]
        )
        self.assertEqual(arguments.format, "multimodal-v2")

    def test_bundle_adaptively_splits_a_part_that_exceeds_the_restricted_format(self):
        samples = [{"token": f"token-{index}"} for index in range(5)]
        original_dump = self.adapter.dump_index

        def bounded_dump(chunk, *, cbgs_seed):
            if len(chunk) > 2:
                raise ValueError("trusted info contains too many values")
            return f"{cbgs_seed}:{','.join(item['token'] for item in chunk)}".encode()

        with tempfile.TemporaryDirectory() as directory, mock.patch.object(
            self.adapter, "dump_index", side_effect=bounded_dump
        ):
            summary = self.adapter.write_index_bundle(
                samples,
                output=Path(directory) / "trusted-index-v2.pkl",
                cbgs_seed=9,
                samples_per_part=5,
            )

        self.assertEqual(summary["part_count"], 3)
        self.assertEqual(original_dump.__name__, "dump_index")

    def test_rejects_path_escape_duplicate_token_and_invalid_boxes(self):
        with tempfile.TemporaryDirectory() as directory:
            source_root = Path(directory)
            outside = source_root.parent / "outside.bin"
            outside.write_bytes(np.arange(8, dtype=np.float32).tobytes())
            with self.assertRaisesRegex(ValueError, "source root"):
                self.adapter.convert_infos(
                    [self._info(outside)], split="train", source_root=source_root
                )

            lidar = source_root / "site" / "scene" / "1.bin"
            lidar.parent.mkdir(parents=True)
            lidar.write_bytes(np.arange(8, dtype=np.float32).tobytes())
            duplicate = self._info(lidar)
            with self.assertRaisesRegex(ValueError, "duplicate token"):
                self.adapter.merge_splits(
                    train_infos=[duplicate],
                    val_infos=[dict(duplicate)],
                    source_root=source_root,
                )

            invalid = self._info(lidar)
            invalid["gt_boxes"] = np.array(
                [[1, 2, 3, 4, 5, 6, np.nan]], dtype=np.float32
            )
            with self.assertRaisesRegex(ValueError, "finite"):
                self.adapter.convert_infos(
                    [invalid], split="train", source_root=source_root
                )

    def test_requires_explicit_scene_token_and_supported_split(self):
        with tempfile.TemporaryDirectory() as directory:
            source_root = Path(directory)
            lidar = source_root / "site" / "scene" / "1.bin"
            lidar.parent.mkdir(parents=True)
            lidar.write_bytes(np.arange(8, dtype=np.float32).tobytes())
            missing_scene = self._info(lidar)
            del missing_scene["scene_token"]
            with self.assertRaisesRegex(ValueError, "scene_token"):
                self.adapter.convert_infos(
                    [missing_scene], split="train", source_root=source_root
                )
            with self.assertRaisesRegex(ValueError, "split"):
                self.adapter.convert_infos(
                    [self._info(lidar)], split="dev", source_root=source_root
                )

    @staticmethod
    def _info(
        lidar: Path,
        *,
        gt_names: np.ndarray | None = None,
        gt_boxes: np.ndarray | None = None,
    ) -> dict[str, object]:
        return {
            "token": "sample-token",
            "scene_token": "scene-token",
            "timestamp": 123456,
            "lidar_path": str(lidar),
            "gt_names": gt_names if gt_names is not None else np.array(["Car"]),
            "gt_boxes": gt_boxes
            if gt_boxes is not None
            else np.array([[1, 2, 3, 4, 5, 6, 0.1]], dtype=np.float32),
        }


if __name__ == "__main__":
    unittest.main(verbosity=2)
