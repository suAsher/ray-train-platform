from __future__ import annotations

import contextlib
import hashlib
import importlib.util
import io
import json
import pickle
import sys
import tempfile
import threading
import time
import unittest
from dataclasses import replace
from pathlib import Path
from unittest import mock

import numpy as np


PROJECT_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(PROJECT_ROOT))

from raytrain_publisher import cloud_publish  # noqa: E402
from raytrain_publisher.pack import (  # noqa: E402
    DEFAULT_SHARD_BYTES,
    MAX_SHARD_BYTES,
    MIN_SHARD_BYTES,
    PackConfig,
    dump_trusted_index,
    dump_trusted_index_manifest,
)
from raytrain_publisher.tos_storage import (  # noqa: E402
    MAX_INDEX_BYTES,
    TOSObjectInfo,
    TOSStorageError,
)


SHARD_CONFIG = PackConfig(target_shard_bytes=1, target_row_group_bytes=1)
HAS_PYARROW = importlib.util.find_spec("pyarrow") is not None


def _sample(
    token: str,
    scene: str,
    lidar_path: str,
    *,
    timestamp: int,
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


def _points(offset: float) -> bytes:
    return np.array(
        [
            [offset, 2.0, 3.0, 0.5, 7.0],
            [offset + 1.0, 5.0, 6.0, 0.75, 8.0],
        ],
        dtype=np.float32,
    ).tobytes()


def _samples() -> list[dict]:
    return [
        _sample("a-2", "scene-a", "scene-a/a-2.bin", timestamp=2),
        _sample("c-1", "scene-c", "scene-c/c-1.bin", timestamp=1, split="test"),
        _sample("a-1", "scene-a", "scene-a/a-1.bin", timestamp=1),
        _sample("b-1", "scene-b", "scene-b/b-1.bin", timestamp=1, split="val"),
    ]


def _source_objects() -> dict[str, bytes]:
    return {
        "scene-a/a-1.bin": _points(1.0),
        "scene-a/a-2.bin": _points(2.0),
        "scene-b/b-1.bin": _points(3.0),
        "scene-c/c-1.bin": _points(4.0),
    }


class _FakeTOSStorage:
    """Behavioral stand-in for the scoped storage boundary used by the entrypoint."""

    def __init__(
        self,
        *,
        index_payload: bytes,
        source_objects: dict[str, bytes],
        output_root: Path,
        fail_on_shard_attempt: int | None = None,
        index_objects: dict[str, bytes] | None = None,
    ) -> None:
        self.index_payload = index_payload
        self.index_objects = dict(index_objects or {})
        self.source_objects = dict(source_objects)
        self.output_root = output_root
        self.fail_on_shard_attempt = fail_on_shard_attempt
        self.objects: dict[str, tuple[bytes, str]] = {}
        self.events: list[tuple[str, str]] = []
        self.index_budgets: list[int] = []
        self.head_calls: list[str] = []
        self.download_calls: list[tuple[str, int]] = []
        self.stage_snapshots: list[tuple[str, tuple[str, ...]]] = []
        self.conflicts = 0
        self._shard_attempts = 0

    def get_index(self, key: str, *, maximum_bytes: int) -> bytes:
        self.events.append(("get_index", key))
        self.index_budgets.append(maximum_bytes)
        payload = self.index_objects.get(key, self.index_payload)
        if len(payload) > maximum_bytes:
            raise TOSStorageError("index read failed AK=index-secret")
        return payload

    def head_source(self, key: str) -> TOSObjectInfo:
        self.head_calls.append(key)
        try:
            payload = self.source_objects[key]
        except KeyError:
            raise TOSStorageError("source HEAD failed AK=head-secret") from None
        return TOSObjectInfo(
            size=len(payload),
            sha256=hashlib.sha256(payload).hexdigest(),
        )

    def download_file(
        self,
        key: str,
        destination: str | Path,
        *,
        maximum_bytes: int,
    ) -> TOSObjectInfo:
        try:
            payload = self.source_objects[key]
        except KeyError:
            raise TOSStorageError("source download failed SK=download-secret") from None
        if len(payload) > maximum_bytes:
            raise TOSStorageError("source download exceeded budget")
        path = Path(destination)
        path.write_bytes(payload)
        self.events.append(("download", key))
        self.download_calls.append((key, maximum_bytes))
        return TOSObjectInfo(
            size=len(payload),
            sha256=hashlib.sha256(payload).hexdigest(),
        )

    def put_immutable(
        self,
        key: str,
        content: bytes | io.BufferedReader,
        *,
        sha256: str,
        maximum_bytes: int,
        size: int | None = None,
        content_type: str = "application/octet-stream",
    ) -> None:
        del content_type
        if isinstance(content, bytes):
            payload = content
        else:
            position = content.tell()
            payload = content.read()
            content.seek(position)
        staged = tuple(
            sorted(
                path.name
                for path in self.output_root.rglob("*.bin")
                if path.is_file()
            )
        )
        self.stage_snapshots.append((key, staged))
        self.events.append(("put", key))
        if "/shards/" in key:
            self._shard_attempts += 1
            if self._shard_attempts == self.fail_on_shard_attempt:
                raise TOSStorageError(
                    "immutable put failed AK=secret-ak SK=secret-sk tos://private/path"
                )
        if key in self.objects:
            self.conflicts += 1
            raise TOSStorageError("immutable object already exists")
        if size != len(payload) or len(payload) > maximum_bytes:
            raise TOSStorageError("immutable upload size validation failed")
        if hashlib.sha256(payload).hexdigest() != sha256:
            raise TOSStorageError("immutable upload digest validation failed")
        self.objects[key] = (payload, sha256)

    def verify_immutable(
        self,
        key: str,
        *,
        expected_size: int,
        expected_sha256: str,
    ) -> TOSObjectInfo:
        self.events.append(("verify", key))
        stored = self.objects.get(key)
        if (
            stored is None
            or len(stored[0]) != expected_size
            or stored[1] != expected_sha256
        ):
            raise TOSStorageError(
                "immutable verification failed AK=verify-secret private/path"
            )
        return TOSObjectInfo(size=expected_size, sha256=expected_sha256)


def _request(output_dir: Path) -> object:
    return cloud_publish.CloudPublishRequest(
        run_id="publication-123",
        dataset_id="dataset-s1h",
        dataset_version_id="version-123",
        version="20260830.1",
        schema_version="s1h-lidar-parquet-v1",
        source_bucket="source-bucket",
        target_bucket="target-bucket",
        tos_endpoint="tos-cn-shanghai.ivolces.com",
        tos_region="cn-shanghai",
        source_root="private-team-root/labeled",
        source_index=".raytrain/trusted-index-v2.pkl",
        internal_prefix="ray-train/platform/datasets",
        output_dir=output_dir,
    )


def _storage(output_dir: Path) -> _FakeTOSStorage:
    return _FakeTOSStorage(
        index_payload=dump_trusted_index(_samples(), class_count=1),
        source_objects=_source_objects(),
        output_root=output_dir,
    )


class CloudPublisherCLIContractTests(unittest.TestCase):
    def test_cloud_loader_reassembles_verified_trusted_index_parts(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            samples = _samples()
            first_payload = dump_trusted_index(samples[:2], class_count=1, cbgs_seed=7)
            second_payload = dump_trusted_index(samples[2:], class_count=1, cbgs_seed=7)
            first_digest = hashlib.sha256(first_payload).hexdigest()
            second_digest = hashlib.sha256(second_payload).hexdigest()
            first_relative = f"trusted-index-v2.parts/sha256-{first_digest}.pkl"
            second_relative = f"trusted-index-v2.parts/sha256-{second_digest}.pkl"
            first_key = f".raytrain/{first_relative}"
            second_key = f".raytrain/{second_relative}"
            manifest = dump_trusted_index_manifest(
                [
                    {"key": first_relative, "sha256": first_digest, "sample_count": 2},
                    {"key": second_relative, "sha256": second_digest, "sample_count": 2},
                ],
                class_count=1,
                cbgs_seed=7,
                sample_count=4,
            )
            storage = _FakeTOSStorage(
                index_payload=manifest,
                index_objects={first_key: first_payload, second_key: second_payload},
                source_objects=_source_objects(),
                output_root=Path(temp_dir),
            )

            document = cloud_publish.load_cloud_trusted_index(
                storage=storage,
                source_root=_request(Path(temp_dir)).source_root,
                source_index=_request(Path(temp_dir)).source_index,
            )

            self.assertEqual([sample["token"] for sample in document.samples], [
                sample["token"] for sample in samples
            ])
            self.assertEqual(document.class_count, 1)
            self.assertEqual(document.cbgs_seed, 7)

    def test_cloud_loader_rejects_tampered_or_escaping_index_part(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            payload = dump_trusted_index(_samples()[:1], class_count=1)
            digest = hashlib.sha256(payload).hexdigest()
            for key, part_payload in (
                ("trusted-index-v2.parts/escape.pkl", payload),
                (f"trusted-index-v2.parts/sha256-{digest}.pkl", payload + b"tamper"),
            ):
                manifest = dump_trusted_index_manifest(
                    [{"key": key, "sha256": digest, "sample_count": 1}],
                    class_count=1,
                    cbgs_seed=0,
                    sample_count=1,
                )
                storage = _FakeTOSStorage(
                    index_payload=manifest,
                    index_objects={f".raytrain/{key}": part_payload},
                    source_objects=_source_objects(),
                    output_root=Path(temp_dir),
                )
                with self.assertRaises((ValueError, TOSStorageError)):
                    cloud_publish.load_cloud_trusted_index(
                        storage=storage,
                        source_root=_request(Path(temp_dir)).source_root,
                        source_index=_request(Path(temp_dir)).source_index,
                    )

    def test_parser_accepts_the_exact_kubernetes_adapter_arguments(self) -> None:
        parser = cloud_publish.build_argument_parser()
        values = {
            "run_id": "publication-123",
            "dataset_id": "dataset-s1h",
            "dataset_version_id": "version-123",
            "version": "20260830.1",
            "schema_version": "s1h-lidar-parquet-v1",
            "source_bucket": "source-bucket",
            "target_bucket": "target-bucket",
            "tos_endpoint": "tos-cn-shanghai.ivolces.com",
            "tos_region": "cn-shanghai",
            "source_root": "ray-train/team-a/labeled",
            "source_index": ".raytrain/trusted-index-v2.pkl",
            "internal_prefix": "ray-train/platform/datasets",
            "output_dir": "/work/publisher",
        }
        argv: list[str] = []
        for destination, value in values.items():
            argv.extend(("--" + destination.replace("_", "-"), value))

        arguments = parser.parse_args(argv)

        self.assertEqual(
            vars(arguments),
            {**values, "output_dir": Path(values["output_dir"])},
        )
        self.assertEqual(
            {action.dest for action in parser._actions if action.dest != "help"},
            set(values),
        )
        self.assertEqual(
            cloud_publish.DEFAULT_PACK_CONFIG.target_shard_bytes,
            DEFAULT_SHARD_BYTES,
        )
        self.assertGreaterEqual(DEFAULT_SHARD_BYTES, MIN_SHARD_BYTES)
        self.assertLessEqual(DEFAULT_SHARD_BYTES, MAX_SHARD_BYTES)


class CloudPublisherPipelineTests(unittest.TestCase):
    def test_remote_metadata_inspection_is_parallel_bounded_and_deduplicated(self) -> None:
        samples = tuple(
            _sample(f"sample-{index}", f"scene-{index}", f"scene-{index}/points.bin", timestamp=index)
            for index in range(12)
        )
        samples = samples + (dict(samples[0]),)

        class ConcurrentStorage:
            def __init__(self) -> None:
                self.lock = threading.Lock()
                self.active = 0
                self.peak = 0
                self.calls: list[str] = []

            def head_source(self, key: str) -> object:
                with self.lock:
                    self.active += 1
                    self.peak = max(self.peak, self.active)
                    self.calls.append(key)
                time.sleep(0.01)
                with self.lock:
                    self.active -= 1
                return type("ObjectInfo", (), {"size": 128, "sha256": None})()

        storage = ConcurrentStorage()
        inspected, source_objects = cloud_publish._inspect_remote_samples(
            samples,
            storage=storage,
            max_workers=4,
            batch_size=5,
        )

        self.assertEqual(len(inspected), len(samples))
        self.assertEqual(len(source_objects), 12)
        self.assertEqual(len(storage.calls), 12)
        self.assertGreater(storage.peak, 1)
        self.assertLessEqual(storage.peak, 4)
        self.assertEqual([item.sample["token"] for item in inspected], [item["token"] for item in samples])

    def test_reference_manifest_persists_publisher_cbgs_order_and_raw_eval_rows(self) -> None:
        samples = [
            _sample("train-a", "scene-a", "scene-a/a.bin", timestamp=1),
            _sample("train-b", "scene-b", "scene-b/b.bin", timestamp=2),
            _sample("train-c", "scene-c", "scene-c/c.bin", timestamp=3),
            _sample("val-a", "scene-v", "scene-v/v.bin", timestamp=4, split="val"),
        ]
        samples[0]["class_ids"] = [0]
        samples[0]["info"]["labels"] = [0]
        samples[1]["class_ids"] = [0, 1]
        samples[1]["info"]["labels"] = [0, 1]
        samples[1]["info"]["boxes"] = samples[1]["info"]["boxes"] * 2
        samples[2]["class_ids"] = [1]
        samples[2]["info"]["labels"] = [1]
        samples[3]["class_ids"] = [0]
        samples[3]["info"]["labels"] = [0]
        locators = {
            sample["token"]: {
                "token": sample["token"],
                "class_ids": sample["class_ids"],
                "source_digest": hashlib.sha256(sample["token"].encode()).hexdigest(),
                "split": sample["split"],
                "shard_path": "dataset-s1h/shards/sha256-" + "a" * 64 + ".parquet",
                "row_index": index,
            }
            for index, sample in enumerate(samples)
        }

        rows = cloud_publish.build_reference_manifest_rows(
            samples,
            locators,
            class_count=2,
            cbgs_seed=23,
        )

        class_indices = {0: [0, 1], 1: [1, 2]}
        random = np.random.RandomState(23)
        expected_train = []
        for indexes in class_indices.values():
            expected_train.extend(samples[index]["token"] for index in random.choice(indexes, 2))
        self.assertEqual(
            [row["token"] for row in rows],
            expected_train + ["val-a"],
        )
        self.assertEqual([row["ordinal"] for row in rows], list(range(len(rows))))
        self.assertEqual(len({row["shard_path"] for row in rows}), 1)

    @unittest.skipUnless(HAS_PYARROW, "the pinned PyArrow dependency is unavailable")
    def test_stages_one_bounded_shard_at_a_time_and_commits_real_manifest_last(
        self,
    ) -> None:
        import pyarrow.parquet as parquet

        with tempfile.TemporaryDirectory() as temp_dir:
            output_dir = Path(temp_dir) / "publisher-output"
            storage = _storage(output_dir)

            result = cloud_publish.publish_cloud_dataset(
                _request(output_dir),
                storage=storage,
                pack_config=SHARD_CONFIG,
            )

            put_keys = [key for operation, key in storage.events if operation == "put"]
            verify_keys = [
                key for operation, key in storage.events if operation == "verify"
            ]
            manifest_key = "dataset-s1h/manifests/version-123.parquet"
            shard_keys = put_keys[:-1]
            self.assertEqual(put_keys[-1], manifest_key)
            self.assertEqual(verify_keys, put_keys)
            self.assertEqual(len(shard_keys), 3)
            self.assertTrue(
                all(
                    key.startswith("dataset-s1h/shards/sha256-")
                    and key.endswith(".parquet")
                    for key in shard_keys
                )
            )

            self.assertEqual(
                [staged for _key, staged in storage.stage_snapshots],
                [
                    ("a-1.bin", "a-2.bin"),
                    ("b-1.bin",),
                    ("c-1.bin",),
                    (),
                ],
            )
            self.assertLess(
                max(len(staged) for _key, staged in storage.stage_snapshots),
                len(storage.source_objects),
            )
            self.assertEqual(
                sorted(storage.download_calls),
                sorted((key, len(payload)) for key, payload in storage.source_objects.items()),
            )
            self.assertEqual(list(output_dir.iterdir()), [])

            manifest_payload, manifest_digest = storage.objects[manifest_key]
            manifest_table = parquet.read_table(io.BytesIO(manifest_payload))
            self.assertEqual(
                manifest_table.schema.names,
                [
                    "ordinal",
                    "token",
                    "class_ids",
                    "source_digest",
                    "split",
                    "shard_path",
                    "row_index",
                ],
            )
            manifest_rows = manifest_table.to_pylist()
            self.assertEqual(
                [row["token"] for row in manifest_rows],
                ["a-2", "a-1", "b-1", "c-1"],
            )
            self.assertEqual(
                [row["ordinal"] for row in manifest_rows],
                list(range(len(manifest_rows))),
            )
            for row in manifest_rows:
                self.assertIn(row["shard_path"], storage.objects)
                shard_payload = storage.objects[row["shard_path"]][0]
                shard_rows = parquet.read_table(io.BytesIO(shard_payload)).to_pylist()
                self.assertEqual(shard_rows[row["row_index"]]["token"], row["token"])

            encoded = json.dumps(result, separators=(",", ":"), sort_keys=True)
            self.assertLessEqual(len(encoded.encode("utf-8")), 4096)
            self.assertEqual(set(result), {"progress", "receipt"})
            self.assertEqual(
                result["progress"],
                {
                    "total_partitions": 3,
                    "completed_partitions": 3,
                    "failed_partitions": 0,
                    "source_object_count": 4,
                    "processed_object_count": 4,
                    "failed_object_count": 0,
                },
            )
            self.assertEqual(
                set(result["receipt"]),
                {
                    "dataset_id",
                    "dataset_version_id",
                    "version",
                    "manifest_sha256",
                    "manifest_object_key",
                    "schema_version",
                    "train_samples",
                    "val_samples",
                    "test_samples",
                    "source_object_count",
                    "logical_bytes",
                    "packed_bytes",
                },
            )
            self.assertEqual(result["receipt"]["train_samples"], 2)
            self.assertEqual(result["receipt"]["val_samples"], 1)
            self.assertEqual(result["receipt"]["test_samples"], 1)
            self.assertEqual(result["receipt"]["source_object_count"], 4)
            self.assertEqual(
                result["receipt"]["logical_bytes"],
                sum(len(payload) for payload in storage.source_objects.values()),
            )
            self.assertEqual(
                result["receipt"]["packed_bytes"],
                sum(len(payload) for payload, _digest in storage.objects.values()),
            )
            self.assertEqual(result["receipt"]["manifest_sha256"], manifest_digest)
            self.assertEqual(
                result["receipt"]["manifest_object_key"],
                "ray-train/platform/datasets/"
                "dataset-s1h/manifests/version-123.parquet",
            )
            for sensitive in (
                "source-bucket",
                "target-bucket",
                "tos-cn-shanghai",
                "private-team-root",
                "trusted-index-v2.pkl",
                "AK=",
                "SK=",
            ):
                self.assertNotIn(sensitive, encoded)
                self.assertNotIn(sensitive.encode("utf-8"), manifest_payload)

    @unittest.skipUnless(HAS_PYARROW, "the pinned PyArrow dependency is unavailable")
    def test_same_digests_are_retryable_without_overwriting(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            output_dir = Path(temp_dir) / "publisher-output"
            storage = _storage(output_dir)
            request = _request(output_dir)

            first = cloud_publish.publish_cloud_dataset(
                request,
                storage=storage,
                pack_config=SHARD_CONFIG,
            )
            stored_before = dict(storage.objects)
            event_offset = len(storage.events)
            second = cloud_publish.publish_cloud_dataset(
                request,
                storage=storage,
                pack_config=SHARD_CONFIG,
            )

            self.assertEqual(second, first)
            self.assertEqual(storage.objects, stored_before)
            self.assertEqual(storage.conflicts, len(storage.objects))
            retry_events = storage.events[event_offset:]
            retry_puts = [key for operation, key in retry_events if operation == "put"]
            retry_verifies = [
                key for operation, key in retry_events if operation == "verify"
            ]
            self.assertEqual(retry_verifies, retry_puts)
            self.assertTrue(retry_puts[-1].endswith("/manifests/version-123.parquet"))

    @unittest.skipUnless(HAS_PYARROW, "the pinned PyArrow dependency is unavailable")
    def test_different_manifest_at_the_same_version_is_a_conflict(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            output_dir = Path(temp_dir) / "publisher-output"
            storage = _storage(output_dir)
            request = _request(output_dir)
            cloud_publish.publish_cloud_dataset(
                request,
                storage=storage,
                pack_config=SHARD_CONFIG,
            )
            manifest_key = "dataset-s1h/manifests/version-123.parquet"
            original_manifest = storage.objects[manifest_key]
            storage.source_objects = {
                **storage.source_objects,
                "scene-c/c-1.bin": _points(99.0),
            }

            with self.assertRaises(cloud_publish.CloudPublishError) as raised:
                cloud_publish.publish_cloud_dataset(
                    request,
                    storage=storage,
                    pack_config=SHARD_CONFIG,
                )

            self.assertEqual(str(raised.exception), "dataset cloud publication failed")
            self.assertEqual(storage.objects[manifest_key], original_manifest)

    @unittest.skipUnless(HAS_PYARROW, "the pinned PyArrow dependency is unavailable")
    def test_shard_failure_never_commits_manifest_and_cleans_scratch(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            output_dir = Path(temp_dir) / "publisher-output"
            storage = _FakeTOSStorage(
                index_payload=dump_trusted_index(_samples(), class_count=1),
                source_objects=_source_objects(),
                output_root=output_dir,
                fail_on_shard_attempt=2,
            )

            with self.assertRaises(cloud_publish.CloudPublishError) as raised:
                cloud_publish.publish_cloud_dataset(
                    _request(output_dir),
                    storage=storage,
                    pack_config=SHARD_CONFIG,
                )

            message = str(raised.exception)
            self.assertEqual(message, "dataset cloud publication failed")
            for sensitive in ("secret-ak", "secret-sk", "tos://", "private/path"):
                self.assertNotIn(sensitive, message)
            self.assertFalse(any("/manifests/" in key for key in storage.objects))
            self.assertFalse(any("/manifests/" in key for _operation, key in storage.events))
            self.assertEqual(list(output_dir.iterdir()), [])

    def test_rejects_escaping_source_path_before_any_source_or_target_io(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            output_dir = Path(temp_dir) / "publisher-output"
            unsafe = _sample(
                "escape",
                "scene-a",
                "../AK=secret-outside.bin",
                timestamp=1,
            )
            storage = _FakeTOSStorage(
                index_payload=dump_trusted_index([unsafe], class_count=1),
                source_objects={"../AK=secret-outside.bin": _points(1.0)},
                output_root=output_dir,
            )

            with self.assertRaises(cloud_publish.CloudPublishError) as raised:
                cloud_publish.publish_cloud_dataset(
                    _request(output_dir),
                    storage=storage,
                    pack_config=SHARD_CONFIG,
                )

            self.assertEqual(str(raised.exception), "dataset cloud publication failed")
            self.assertEqual(storage.head_calls, [])
            self.assertEqual(storage.download_calls, [])
            self.assertEqual(storage.objects, {})

    def test_rejects_non_trusted_pickle_without_staging_or_uploading(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            output_dir = Path(temp_dir) / "publisher-output"
            storage = _FakeTOSStorage(
                index_payload=pickle.dumps(_samples()),
                source_objects=_source_objects(),
                output_root=output_dir,
            )

            with self.assertRaises(cloud_publish.CloudPublishError):
                cloud_publish.publish_cloud_dataset(
                    _request(output_dir),
                    storage=storage,
                    pack_config=SHARD_CONFIG,
                )

            self.assertEqual(storage.head_calls, [])
            self.assertEqual(storage.download_calls, [])
            self.assertEqual(storage.objects, {})

    def test_rejects_escaping_source_index_before_any_storage_call(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            output_dir = Path(temp_dir) / "publisher-output"
            storage = _storage(output_dir)
            request = replace(
                _request(output_dir),
                source_index="../AK=secret-index.pkl",
            )

            with self.assertRaises(cloud_publish.CloudPublishError) as raised:
                cloud_publish.publish_cloud_dataset(
                    request,
                    storage=storage,
                    pack_config=SHARD_CONFIG,
                )

            self.assertEqual(str(raised.exception), "dataset cloud publication failed")
            self.assertEqual(storage.events, [])

    def test_rejects_encoded_generated_target_key_before_any_storage_call(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            output_dir = Path(temp_dir) / "publisher-output"
            storage = _storage(output_dir)
            request = replace(
                _request(output_dir),
                dataset_id="dataset%2fescape",
            )

            with self.assertRaises(cloud_publish.CloudPublishError) as raised:
                cloud_publish.publish_cloud_dataset(
                    request,
                    storage=storage,
                    pack_config=SHARD_CONFIG,
                )

            self.assertEqual(str(raised.exception), "dataset cloud publication failed")
            self.assertEqual(storage.events, [])

    def test_rewraps_even_an_injected_cloud_error_with_sensitive_context(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            output_dir = Path(temp_dir) / "publisher-output"
            storage = _storage(output_dir)
            storage.get_index = mock.Mock(
                side_effect=cloud_publish.CloudPublishError(
                    "AK=secret-ak SK=secret-sk tos://private/path"
                )
            )

            with self.assertRaises(cloud_publish.CloudPublishError) as raised:
                cloud_publish.publish_cloud_dataset(
                    _request(output_dir),
                    storage=storage,
                    pack_config=SHARD_CONFIG,
                )

            self.assertEqual(str(raised.exception), "dataset cloud publication failed")


class CloudPublisherEntrypointTests(unittest.TestCase):
    @unittest.skipUnless(HAS_PYARROW, "the pinned PyArrow dependency is unavailable")
    def test_main_uses_irsa_storage_and_writes_the_same_bounded_summary(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            workspace = Path(temp_dir)
            output_dir = workspace / "publisher-output"
            termination_log = workspace / "termination.log"
            storage = _storage(output_dir)
            argv = [
                "--run-id",
                "publication-123",
                "--dataset-id",
                "dataset-s1h",
                "--dataset-version-id",
                "version-123",
                "--version",
                "20260830.1",
                "--schema-version",
                "s1h-lidar-parquet-v1",
                "--source-bucket",
                "source-bucket",
                "--target-bucket",
                "target-bucket",
                "--tos-endpoint",
                "tos-cn-shanghai.ivolces.com",
                "--tos-region",
                "cn-shanghai",
                "--source-root",
                "private-team-root/labeled",
                "--source-index",
                ".raytrain/trusted-index-v2.pkl",
                "--internal-prefix",
                "ray-train/platform/datasets",
                "--output-dir",
                str(output_dir),
            ]
            stdout = io.StringIO()
            stderr = io.StringIO()

            with (
                mock.patch.object(
                    cloud_publish,
                    "TOSStorage",
                    return_value=storage,
                ) as storage_constructor,
                mock.patch.object(
                    cloud_publish,
                    "TERMINATION_LOG_PATH",
                    termination_log,
                ),
                mock.patch.object(
                    cloud_publish,
                    "DEFAULT_PACK_CONFIG",
                    SHARD_CONFIG,
                ),
                contextlib.redirect_stdout(stdout),
                contextlib.redirect_stderr(stderr),
            ):
                exit_code = cloud_publish.main(argv)

            self.assertEqual(exit_code, 0)
            self.assertEqual(stderr.getvalue(), "")
            stdout_payload = stdout.getvalue().strip()
            self.assertEqual(termination_log.read_text(encoding="utf-8"), stdout_payload)
            self.assertLessEqual(len(stdout_payload.encode("utf-8")), 4096)
            self.assertEqual(json.loads(stdout_payload)["receipt"]["dataset_id"], "dataset-s1h")
            self.assertEqual(cloud_publish.DEFAULT_TERMINATION_LOG_PATH, Path("/dev/termination-log"))

            constructor_options = storage_constructor.call_args.kwargs
            self.assertEqual(constructor_options["source_prefix"], "private-team-root/labeled")
            self.assertEqual(
                constructor_options["internal_dataset_prefix"],
                "ray-train/platform/datasets",
            )
            self.assertIsInstance(
                constructor_options["irsa_provider"],
                cloud_publish.VKEIRSAProvider,
            )
            self.assertEqual(storage.events[0], ("get_index", ".raytrain/trusted-index-v2.pkl"))
            self.assertEqual(storage.index_budgets, [MAX_INDEX_BYTES])

    def test_main_exposes_only_a_generic_error(self) -> None:
        stderr = io.StringIO()
        with (
            mock.patch.object(
                cloud_publish,
                "TOSStorage",
                side_effect=RuntimeError(
                    "AK=secret-ak SK=secret-sk tos://private/path endpoint.internal"
                ),
            ),
            contextlib.redirect_stderr(stderr),
        ):
            exit_code = cloud_publish.main(
                [
                    "--run-id",
                    "publication-123",
                    "--dataset-id",
                    "dataset-s1h",
                    "--dataset-version-id",
                    "version-123",
                    "--version",
                    "20260830.1",
                    "--schema-version",
                    "s1h-lidar-parquet-v1",
                    "--source-bucket",
                    "source-bucket",
                    "--target-bucket",
                    "target-bucket",
                    "--tos-endpoint",
                    "tos-cn-shanghai.ivolces.com",
                    "--tos-region",
                    "cn-shanghai",
                    "--source-root",
                    "private-team-root/labeled",
                    "--source-index",
                    ".raytrain/trusted-index-v2.pkl",
                    "--internal-prefix",
                    "ray-train/platform/datasets",
                    "--output-dir",
                    "/work/publisher",
                ]
            )

        self.assertEqual(exit_code, 2)
        self.assertEqual(stderr.getvalue(), "dataset cloud publication failed\n")


if __name__ == "__main__":
    unittest.main()
