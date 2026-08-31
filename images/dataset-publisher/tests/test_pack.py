from __future__ import annotations

import pickle
import importlib.util
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import numpy as np


PROJECT_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(PROJECT_ROOT))

from raytrain_publisher.pack import (  # noqa: E402
    DEFAULT_ROW_GROUP_BYTES,
    DEFAULT_SHARD_BYTES,
    MAX_ROW_GROUP_BYTES,
    MAX_SHARD_BYTES,
    MIN_ROW_GROUP_BYTES,
    MIN_SHARD_BYTES,
    PackConfig,
    build_argument_parser,
    build_cbgs_sample_plan,
    build_manifest,
    build_partition_summary,
    dump_trusted_index_manifest,
    dump_trusted_index,
    estimate_row_bytes,
    iter_prepared_shard_plans,
    load_trusted_index,
    load_trusted_index_document,
    load_trusted_index_manifest,
    plan_row_groups,
    plan_shards,
    prepare_rows,
    publish_dataset,
    resolve_input_path,
)
from raytrain_publisher import pack as pack_module  # noqa: E402
from raytrain_publisher.schema import (  # noqa: E402
    ROW_FIELD_NAMES,
    compute_source_digest,
    dump_trusted_info,
    load_trusted_info,
    validate_sample_row,
)


class TrustedIndexAndInputTests(unittest.TestCase):
    @staticmethod
    def _sample(
        token: str,
        scene: str,
        timestamp: int,
        lidar_path: str,
        *,
        split: str = "train",
    ) -> dict:
        return {
            "token": token,
            "scene": scene,
            "split": split,
            "class_ids": [0],
            "timestamp": timestamp,
            "lidar_path": lidar_path,
            "point_columns": 5,
            "info": {
                "lidar_feature_count": 5,
                "boxes": [[1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 0.25]],
                "labels": [0],
            },
        }

    @staticmethod
    def _write_points(path: Path, offset: float) -> bytes:
        path.parent.mkdir(parents=True, exist_ok=True)
        points = np.array(
            [[offset, 2.0, 3.0, 0.5, 7.0], [offset + 1.0, 5.0, 6.0, 0.75, 8.0]],
            dtype=np.float32,
        )
        points.tofile(path)
        return path.read_bytes()

    def test_trusted_index_round_trip_requires_explicit_publisher_envelope(self) -> None:
        samples = [self._sample("token-a", "scene-a", 20, "scene-a/a.bin")]

        encoded = dump_trusted_index(samples, class_count=1, cbgs_seed=17)

        self.assertEqual(load_trusted_index(encoded), samples)
        document = load_trusted_index_document(encoded)
        self.assertEqual(document.class_count, 1)
        self.assertEqual(document.cbgs_seed, 17)
        self.assertEqual(list(document.samples), samples)
        with self.assertRaisesRegex(pickle.UnpicklingError, "trusted publisher"):
            load_trusted_index(pickle.dumps(samples))

    def test_trusted_index_requires_explicit_cbgs_contract(self) -> None:
        samples = [self._sample("token-a", "scene-a", 20, "scene-a/a.bin")]

        with self.assertRaisesRegex(TypeError, "class_count"):
            dump_trusted_index(samples)
        with self.assertRaisesRegex(ValueError, "class_count"):
            dump_trusted_index(samples, class_count=0)
        with self.assertRaisesRegex(ValueError, "seed"):
            dump_trusted_index(samples, class_count=1, cbgs_seed=-1)

    def test_sharded_trusted_index_manifest_round_trip_is_bounded_and_explicit(self) -> None:
        parts = [
            {
                "key": "trusted-index-v2.parts/sha256-" + "a" * 64 + ".pkl",
                "sha256": "a" * 64,
                "sample_count": 2000,
            },
            {
                "key": "trusted-index-v2.parts/sha256-" + "b" * 64 + ".pkl",
                "sha256": "b" * 64,
                "sample_count": 153,
            },
        ]

        encoded = dump_trusted_index_manifest(
            parts,
            class_count=10,
            cbgs_seed=17,
            sample_count=2153,
        )
        manifest = load_trusted_index_manifest(encoded)

        self.assertEqual(manifest.class_count, 10)
        self.assertEqual(manifest.cbgs_seed, 17)
        self.assertEqual(manifest.sample_count, 2153)
        self.assertEqual([part.key for part in manifest.parts], [part["key"] for part in parts])
        self.assertLess(len(encoded), 64 * 1024)

    def test_sharded_trusted_index_manifest_rejects_count_and_digest_errors(self) -> None:
        part = {
            "key": "trusted-index-v2.parts/sha256-" + "a" * 64 + ".pkl",
            "sha256": "a" * 64,
            "sample_count": 2,
        }
        with self.assertRaisesRegex(ValueError, "sample count"):
            dump_trusted_index_manifest(
                [part], class_count=1, cbgs_seed=0, sample_count=3
            )
        with self.assertRaisesRegex(ValueError, "digest"):
            dump_trusted_index_manifest(
                [{**part, "sha256": "not-a-digest"}],
                class_count=1,
                cbgs_seed=0,
                sample_count=2,
            )

    def test_cbgs_plan_matches_legacy_reference_order_without_payload_copy(self) -> None:
        def sample(token: str, scene: str, timestamp: int, class_ids: list[int], *, split: str = "train") -> dict:
            value = self._sample(token, scene, timestamp, f"{scene}/{token}.bin", split=split)
            return {
                **value,
                "class_ids": class_ids,
                "info": {
                    **value["info"],
                    "boxes": value["info"]["boxes"] * len(class_ids),
                    "labels": class_ids,
                },
            }

        samples = [
            sample("a", "scene-a", 1, [0]),
            sample("b", "scene-b", 2, [0, 1]),
            sample("c", "scene-c", 3, [1]),
            sample("val", "scene-v", 4, [0], split="val"),
        ]

        planned = build_cbgs_sample_plan(samples, class_count=2, seed=23)

        class_indices = {0: [0, 1], 1: [1, 2]}
        random = np.random.RandomState(23)
        expected = []
        for indices in class_indices.values():
            expected.extend(samples[index]["token"] for index in random.choice(indices, 2))
        self.assertEqual([sample["token"] for sample in planned], expected)
        self.assertTrue(all(sample["split"] == "train" for sample in planned))
        self.assertIs(planned[0], next(sample for sample in samples if sample["token"] == expected[0]))

    def test_prepare_rows_is_lidar_only_sorted_and_byte_exact(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            input_root = Path(temp_dir) / "input"
            expected_a = self._write_points(input_root / "scene-a" / "a.bin", 1.0)
            expected_b = self._write_points(input_root / "scene-b" / "b.bin", 10.0)
            samples = [
                self._sample("token-b", "scene-b", 10, "scene-b/b.bin", split="val"),
                self._sample("token-a", "scene-a", 20, "scene-a/a.bin"),
            ]

            rows = prepare_rows(reversed(samples), input_root=input_root)

            self.assertEqual([row["token"] for row in rows], ["token-a", "token-b"])
            self.assertEqual(rows[0]["points"], expected_a)
            self.assertEqual(rows[1]["points"], expected_b)
            self.assertEqual(load_trusted_info(rows[0]["info"]), samples[1]["info"])
            self.assertTrue(all("camera" not in row and "images" not in row for row in rows))
            self.assertNotIn(str(input_root), repr(rows))
            self.assertRegex(rows[0]["source_digest"], r"^[0-9a-f]{64}$")

            repeated = prepare_rows(samples, input_root=input_root)
            self.assertEqual(rows, repeated)

    def test_paths_cannot_escape_input_root(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            workspace = Path(temp_dir)
            input_root = workspace / "input"
            input_root.mkdir()
            outside = workspace / "outside.bin"
            self._write_points(outside, 1.0)

            with self.assertRaisesRegex(ValueError, "input root"):
                resolve_input_path(input_root, "../outside.bin")
            with self.assertRaisesRegex(ValueError, "input root"):
                resolve_input_path(input_root, str(outside))
            with self.assertRaisesRegex(ValueError, "URI"):
                resolve_input_path(input_root, "tos://bucket/private.bin")

            link = input_root / "escape.bin"
            try:
                link.symlink_to(outside)
            except (NotImplementedError, OSError):
                pass
            else:
                with self.assertRaisesRegex(ValueError, "input root"):
                    resolve_input_path(input_root, "escape.bin")

    def test_prepare_rows_rejects_unknown_camera_duplicates_and_bad_dimensions(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            input_root = Path(temp_dir) / "input"
            self._write_points(input_root / "scene-a" / "a.bin", 1.0)
            sample = self._sample("token-a", "scene-a", 20, "scene-a/a.bin")

            with self.assertRaisesRegex(ValueError, "fields"):
                prepare_rows([{**sample, "cams": {"front": "front.jpg"}}], input_root=input_root)

            with self.assertRaisesRegex(ValueError, "duplicate token"):
                prepare_rows([sample, dict(sample)], input_root=input_root)

            with self.assertRaisesRegex(ValueError, "point_columns"):
                prepare_rows([{**sample, "point_columns": 3}], input_root=input_root)

            mismatched_labels = {
                **sample,
                "info": {**sample["info"], "labels": [1]},
            }
            with self.assertRaisesRegex(ValueError, "labels.*class_ids"):
                prepare_rows([mismatched_labels], input_root=input_root)

            with_pose = {
                **sample,
                "info": {**sample["info"], "pose": {"translation": [1.0, 2.0, 3.0]}},
            }
            with self.assertRaisesRegex(ValueError, "fields"):
                prepare_rows([with_pose], input_root=input_root)

    def test_prepare_rows_rejects_nested_camera_and_internal_location_metadata(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            input_root = Path(temp_dir) / "input"
            self._write_points(input_root / "scene-a" / "a.bin", 1.0)
            sample = self._sample("token-a", "scene-a", 20, "scene-a/a.bin")

            camera_info = {
                **sample,
                "info": {
                    **sample["info"],
                    "cams": {"CAM_FRONT": {"data_path": "/mnt/private/front.jpg"}},
                },
            }
            with self.assertRaisesRegex(ValueError, "lidar-only"):
                prepare_rows([camera_info], input_root=input_root)

            camel_case_camera = {
                **sample,
                "info": {
                    **sample["info"],
                    "cameraMetadata": {"imagePath": "private/front.jpg"},
                },
            }
            with self.assertRaisesRegex(ValueError, "lidar-only"):
                prepare_rows([camel_case_camera], input_root=input_root)

            internal_uri = {
                **sample,
                "info": {
                    **sample["info"],
                    "provenance": {"source_uri": "tos://private-bucket/raw/index.pkl"},
                },
            }
            with self.assertRaisesRegex(ValueError, "internal location"):
                prepare_rows([internal_uri], input_root=input_root)

    def test_point_read_does_not_trust_a_previously_resolved_path(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            workspace = Path(temp_dir)
            input_root = workspace / "input"
            expected = self._write_points(input_root / "scene-a" / "a.bin", 1.0)
            outside = workspace / "outside.bin"
            self._write_points(outside, 100.0)
            sample = self._sample("token-a", "scene-a", 20, "scene-a/a.bin")

            with mock.patch.object(
                pack_module,
                "resolve_input_path",
                return_value=outside,
            ):
                rows = prepare_rows([sample], input_root=input_root)

            self.assertEqual(
                rows[0]["points"],
                expected,
                "the actual read must be rooted independently of an earlier path check",
            )


class DeterministicShardPlanningTests(unittest.TestCase):
    @staticmethod
    def _row(token: str, scene: str, timestamp: int, payload_size: int) -> dict:
        points = np.arange(payload_size // 4, dtype=np.float32).reshape(-1, 1).tobytes()
        info = dump_trusted_info({"lidar_feature_count": 1, "labels": [1]})
        digest = compute_source_digest(
            token=token,
            scene=scene,
            split="train",
            class_ids=[1],
            timestamp=timestamp,
            points=points,
            info=info,
        )
        return validate_sample_row(
            {
                "token": token,
                "scene": scene,
                "split": "train",
                "class_ids": [1],
                "timestamp": timestamp,
                "points": points,
                "info": info,
                "source_digest": digest,
            }
        )

    def test_default_targets_are_in_contract_ranges_and_small_values_are_injectable(self) -> None:
        self.assertLessEqual(MIN_SHARD_BYTES, DEFAULT_SHARD_BYTES)
        self.assertLessEqual(DEFAULT_SHARD_BYTES, MAX_SHARD_BYTES)
        self.assertLessEqual(MIN_ROW_GROUP_BYTES, DEFAULT_ROW_GROUP_BYTES)
        self.assertLessEqual(DEFAULT_ROW_GROUP_BYTES, MAX_ROW_GROUP_BYTES)

        injected = PackConfig(target_shard_bytes=512, target_row_group_bytes=128)
        self.assertEqual(injected.target_shard_bytes, 512)
        self.assertEqual(injected.target_row_group_bytes, 128)

        with self.assertRaisesRegex(ValueError, "positive"):
            PackConfig(target_shard_bytes=0, target_row_group_bytes=1)
        with self.assertRaisesRegex(ValueError, "row group"):
            PackConfig(target_shard_bytes=64, target_row_group_bytes=65)

    def test_scenes_stay_grouped_with_deterministic_order_and_digest(self) -> None:
        rows = [
            self._row("b-2", "scene-b", 2, 64),
            self._row("a-2", "scene-a", 2, 64),
            self._row("c-1", "scene-c", 1, 64),
            self._row("a-1", "scene-a", 1, 64),
        ]
        scene_a_size = sum(estimate_row_bytes(row) for row in rows if row["scene"] == "scene-a")

        first = plan_shards(rows, target_shard_bytes=scene_a_size)
        repeated = plan_shards(reversed(rows), target_shard_bytes=scene_a_size)

        self.assertGreater(len(first), 1)
        self.assertEqual([plan.digest for plan in first], [plan.digest for plan in repeated])
        self.assertEqual(
            [row["token"] for plan in first for row in plan.rows],
            ["a-1", "a-2", "b-2", "c-1"],
        )
        scene_locations = {}
        for shard_index, plan in enumerate(first):
            for scene in plan.scenes:
                self.assertNotIn(scene, scene_locations)
                scene_locations[scene] = shard_index

        row_target = estimate_row_bytes(first[0].rows[0])
        row_groups = plan_row_groups(first[0].rows, target_row_group_bytes=row_target)
        self.assertEqual(len(row_groups), 2)
        self.assertEqual([group[0]["token"] for group in row_groups], ["a-1", "a-2"])

    def test_prepared_shard_iterator_is_lazy_and_bounds_point_payload_memory(self) -> None:
        samples = [
            TrustedIndexAndInputTests._sample(
                f"token-{index}",
                f"scene-{index}",
                index,
                f"scene-{index}/points.bin",
            )
            for index in range(6)
        ]
        prepared_tokens: list[str] = []

        def prepare(sample, *, input_root):
            prepared_tokens.append(sample["token"])
            return self._row(sample["token"], sample["scene"], sample["timestamp"], 64)

        target_bytes = estimate_row_bytes(
            prepare(samples[0], input_root=Path("/unused"))
        )
        prepared_tokens.clear()
        with tempfile.TemporaryDirectory() as temp_dir, mock.patch.object(
            pack_module, "_prepare_row", side_effect=prepare
        ), mock.patch.object(
            pack_module,
            "_estimate_index_sample_bytes",
            return_value=target_bytes,
        ):
            iterator = iter_prepared_shard_plans(
                samples,
                input_root=Path(temp_dir),
                target_shard_bytes=target_bytes,
            )
            first = next(iterator)

        self.assertEqual([row["token"] for row in first.rows], ["token-0"])
        self.assertLess(
            len(prepared_tokens),
            len(samples),
            "the publisher must not load every lidar payload before yielding its first shard",
        )

    def test_manifest_and_partition_summary_are_deterministic_and_path_free(self) -> None:
        rows = [
            self._row("b-1", "scene-b", 1, 64),
            self._row("a-1", "scene-a", 1, 64),
        ]
        config = PackConfig(target_shard_bytes=512, target_row_group_bytes=128)
        plans = plan_shards(rows, target_shard_bytes=config.target_shard_bytes)
        shard_records = tuple(
            {
                "path": f"sha256-{plan.digest}.parquet",
                "sha256": plan.digest,
                "logical_digest": plan.digest,
                "size_bytes": plan.estimated_bytes,
                "row_count": len(plan.rows),
                "row_group_count": len(
                    plan_row_groups(
                        plan.rows,
                        target_row_group_bytes=config.target_row_group_bytes,
                    )
                ),
                "scenes": list(plan.scenes),
            }
            for plan in plans
        )

        summary = build_partition_summary(plans, shard_records)
        manifest = build_manifest(
            dataset_id="s1h-yihan",
            version_id="20260830.1+sha256-test",
            config=config,
            plans=plans,
            shard_records=shard_records,
            partition_summary=summary,
        )
        repeated = build_manifest(
            dataset_id="s1h-yihan",
            version_id="20260830.1+sha256-test",
            config=config,
            plans=plans,
            shard_records=shard_records,
            partition_summary=summary,
        )

        self.assertEqual(manifest, repeated)
        self.assertRegex(manifest["manifest_digest"], r"^[0-9a-f]{64}$")
        self.assertEqual(
            [partition["scene"] for partition in summary["partitions"]],
            ["scene-a", "scene-b"],
        )
        serialized = json.dumps({"manifest": manifest, "summary": summary})
        self.assertNotIn("lidar_path", serialized)
        self.assertNotIn("input_root", serialized)
        self.assertNotIn("password", serialized.lower())


class ParquetPublicationTests(TrustedIndexAndInputTests):
    def test_publish_writes_parquet_manifest_and_partition_summary(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            workspace = Path(temp_dir)
            input_root = workspace / "input"
            samples = [
                self._sample("a-2", "scene-a", 2, "scene-a/2.bin"),
                self._sample("b-1", "scene-b", 1, "scene-b/1.bin", split="val"),
                self._sample("a-1", "scene-a", 1, "scene-a/1.bin"),
            ]
            for index, sample in enumerate(samples):
                self._write_points(input_root / sample["lidar_path"], float(index + 1))
            index_path = input_root / "index.pkl"
            index_path.write_bytes(dump_trusted_index(samples, class_count=1))
            output_dir = workspace / "output"
            config = PackConfig(target_shard_bytes=1024, target_row_group_bytes=256)

            if importlib.util.find_spec("pyarrow") is None:
                with self.assertRaisesRegex(RuntimeError, r"pyarrow==25\.0\.1"):
                    publish_dataset(
                        input_root=input_root,
                        index_path="index.pkl",
                        output_dir=output_dir,
                        dataset_id="s1h-yihan",
                        version_id="20260830.1+sha256-test",
                        config=config,
                    )
                self.assertFalse((output_dir / "manifest.json").exists())
                return

            import pyarrow.parquet as pq

            manifest = publish_dataset(
                input_root=input_root,
                index_path="index.pkl",
                output_dir=output_dir,
                dataset_id="s1h-yihan",
                version_id="20260830.1+sha256-test",
                config=config,
            )
            repeated = publish_dataset(
                input_root=input_root,
                index_path="index.pkl",
                output_dir=workspace / "output-repeated",
                dataset_id="s1h-yihan",
                version_id="20260830.1+sha256-test",
                config=config,
            )

            self.assertEqual(manifest["row_count"], 3)
            self.assertEqual(manifest["scene_count"], 2)
            self.assertEqual(
                manifest,
                repeated,
                "pinned PyArrow writes must be content-deterministic",
            )
            self.assertEqual(
                json.loads((output_dir / "manifest.json").read_text(encoding="utf-8")),
                manifest,
            )
            summary = json.loads(
                (output_dir / "partition-summary.json").read_text(encoding="utf-8")
            )
            self.assertEqual([item["scene"] for item in summary["partitions"]], ["scene-a", "scene-b"])

            tokens = []
            for shard in manifest["shards"]:
                parquet_path = output_dir / shard["path"]
                parquet_file = pq.ParquetFile(parquet_path)
                self.assertEqual(parquet_file.schema_arrow.names, list(ROW_FIELD_NAMES))
                self.assertEqual(
                    parquet_file.metadata.row_group(0).column(0).compression,
                    "ZSTD",
                )
                tokens.extend(parquet_file.read(columns=["token"])["token"].to_pylist())
            self.assertEqual(tokens, ["a-1", "a-2", "b-1"])

    def test_immutable_metadata_commit_never_uses_overwriting_replace(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            destination = Path(temp_dir) / "manifest.json"
            with mock.patch.object(
                os,
                "replace",
                side_effect=AssertionError("metadata commits must not overwrite"),
            ):
                pack_module._write_json_atomically(destination, {"version": 1})
                pack_module._write_json_atomically(destination, {"version": 1})

            self.assertEqual(
                json.loads(destination.read_text(encoding="utf-8")),
                {"version": 1},
            )
            with self.assertRaisesRegex(ValueError, "immutable publication metadata"):
                pack_module._write_json_atomically(destination, {"version": 2})


class ContainerContractTests(unittest.TestCase):
    def test_dependencies_and_non_root_entrypoint_are_pinned(self) -> None:
        requirements = (PROJECT_ROOT / "requirements.txt").read_text(encoding="utf-8").splitlines()
        dockerfile = (PROJECT_ROOT / "Dockerfile").read_text(encoding="utf-8")
        dockerignore = (PROJECT_ROOT / ".dockerignore").read_text(encoding="utf-8")

        self.assertEqual(
            requirements,
            ["numpy==2.3.5", "pyarrow==25.0.1", "tos==2.9.2"],
        )
        self.assertIn("ARG PYTHON_BASE_IMAGE=", dockerfile)
        self.assertIn("FROM ${PYTHON_BASE_IMAGE}", dockerfile)
        self.assertIn("COPY requirements.txt /app/requirements.txt", dockerfile)
        self.assertIn("python3 -m pip install --no-cache-dir", dockerfile)
        self.assertIn("COPY raytrain_publisher /app/raytrain_publisher", dockerfile)
        self.assertIn("USER 65532:65532", dockerfile)
        self.assertIn(
            'ENTRYPOINT ["python3", "-m", "raytrain_publisher.pack"]',
            dockerfile,
        )
        self.assertNotIn("ACCESS_KEY", dockerfile)
        self.assertNotIn("SECRET_KEY", dockerfile)
        self.assertIn("__pycache__/", dockerignore)
        self.assertIn("*.pyc", dockerignore)

    def test_module_entrypoint_accepts_only_publication_parameters(self) -> None:
        parser = build_argument_parser()

        arguments = parser.parse_args(
            [
                "--input-root",
                "/data/input",
                "--index",
                "index.pkl",
                "--output-dir",
                "/data/output",
                "--dataset-id",
                "s1h-yihan",
                "--version-id",
                "20260830.1",
                "--target-shard-bytes",
                str(MIN_SHARD_BYTES),
                "--target-row-group-bytes",
                str(MIN_ROW_GROUP_BYTES),
                "--compression",
                "none",
            ]
        )

        self.assertEqual(arguments.target_shard_bytes, MIN_SHARD_BYTES)
        self.assertEqual(arguments.target_row_group_bytes, MIN_ROW_GROUP_BYTES)
        self.assertEqual(arguments.compression, "none")
        self.assertFalse(any("credential" in action.dest for action in parser._actions))
        self.assertFalse(any("secret" in action.dest for action in parser._actions))

        with self.assertRaises(SystemExit):
            parser.parse_args(
                [
                    "--input-root",
                    "/data/input",
                    "--index",
                    "index.pkl",
                    "--output-dir",
                    "/data/output",
                    "--dataset-id",
                    "s1h-yihan",
                    "--version-id",
                    "20260830.1",
                    "--target-shard-bytes",
                    "1024",
                ]
            )

    def test_module_entrypoint_runs_after_all_helpers_are_defined(self) -> None:
        source = (PROJECT_ROOT / "raytrain_publisher" / "pack.py").read_text(
            encoding="utf-8"
        )

        self.assertGreater(
            source.rfind('if __name__ == "__main__":'),
            source.rfind("def _prepare_row"),
        )


if __name__ == "__main__":
    unittest.main()
