from __future__ import annotations

import hashlib
import os
import pathlib
import sys
import tempfile
import types
import unittest
from unittest import mock


RUNTIME_PARENT = pathlib.Path(__file__).resolve().parent.parent
if str(RUNTIME_PARENT) not in sys.path:
    sys.path.insert(0, str(RUNTIME_PARENT))

from raytrain_runtime import managed_driver  # noqa: E402
from raytrain_runtime.managed_driver import build_trainer, parse_driver_config  # noqa: E402
from raytrain_runtime.ray_data import StreamingDatasetConfig  # noqa: E402
from raytrain_runtime.test_managed_driver import (  # noqa: E402
    BASE_ENV,
    CapturedConfig,
    FAKE_RAY,
    driver_argv,
)


def _streaming_args(root: pathlib.Path, digest: str) -> list[str]:
    manifest = root / "dataset-s1h" / "manifests" / "version-1.parquet"
    return driver_argv(
        "--data-mode",
        "streaming",
        "--dataset-id",
        "dataset-s1h",
        "--dataset-version-id",
        "version-1",
        "--dataset-manifest-path",
        str(manifest),
        "--dataset-manifest-sha256",
        digest,
        "--dataset-root",
        str(root),
        "--dataset-train-samples",
        "33",
        "--dataset-cache-policy",
        "bounded",
        "--dataset-prefetch-batches",
        "4",
        "--dataset-shuffle-seed",
        "19",
    )


class ManagedStreamingParseTest(unittest.TestCase):
    def test_parses_only_complete_pinned_streaming_provenance(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary).resolve()
            manifest = root / "dataset-s1h" / "manifests" / "version-1.parquet"
            manifest.parent.mkdir(parents=True)
            manifest.write_bytes(b"manifest")
            digest = hashlib.sha256(manifest.read_bytes()).hexdigest()

            config = parse_driver_config(
                _streaming_args(root, digest),
                environ={**BASE_ENV, "PLATFORM_RAY_VERSION": "2.58.0"},
            )

        self.assertEqual(config.data_mode, "streaming")
        self.assertIsInstance(config.dataset, StreamingDatasetConfig)
        self.assertEqual(config.dataset.dataset_id, "dataset-s1h")
        self.assertEqual(config.dataset.train_samples, 33)
        self.assertEqual(config.dataset.cache_policy, "bounded")
        self.assertEqual(config.dataset.prefetch_batches, 4)
        self.assertEqual(config.dataset.shuffle_seed, 19)
        self.assertEqual(config.ray_version, "2.58.0")

    def test_streaming_rejects_legacy_uri_or_incomplete_provenance(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary).resolve()
            manifest = root / "dataset-s1h" / "manifests" / "version-1.parquet"
            manifest.parent.mkdir(parents=True)
            manifest.write_bytes(b"manifest")
            digest = hashlib.sha256(manifest.read_bytes()).hexdigest()
            args = _streaming_args(root, digest)

            with self.assertRaisesRegex(ValueError, "dataset format and URI"):
                separator = args.index("--")
                parse_driver_config(
                    [
                        *args[:separator],
                        "--dataset-format",
                        "parquet",
                        *args[separator:],
                    ],
                    environ=BASE_ENV,
                )
            missing_digest = list(args)
            index = missing_digest.index("--dataset-manifest-sha256")
            del missing_digest[index : index + 2]
            with self.assertRaises(ValueError):
                parse_driver_config(missing_digest, environ=BASE_ENV)


class ManagedStreamingTrainerTest(unittest.TestCase):
    def test_torch_trainer_receives_prepared_ref_dataset_and_worker_provenance(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary).resolve()
            manifest = root / "dataset-s1h" / "manifests" / "version-1.parquet"
            manifest.parent.mkdir(parents=True)
            manifest.write_bytes(b"manifest")
            config = parse_driver_config(
                _streaming_args(root, hashlib.sha256(manifest.read_bytes()).hexdigest()),
                environ=BASE_ENV,
            )
            prepared = object()
            ray_api = types.SimpleNamespace(**vars(FAKE_RAY), DataConfig=CapturedConfig)
            with mock.patch.object(
                managed_driver,
                "_load_s1h_streaming_dataset",
                return_value=(prepared, 3, 15),
                create=True,
            ) as load:
                trainer = build_trainer(config, ray_components=ray_api)

        load.assert_called_once_with(config.dataset, world_size=16)
        self.assertEqual(trainer.kwargs["datasets"], {"train": prepared})
        self.assertEqual(
            trainer.kwargs["scaling_config"].kwargs["resources_per_worker"],
            {"CPU": 7},
        )
        loop = trainer.kwargs["train_loop_config"]
        self.assertEqual(loop["data_mode"], "streaming")
        self.assertEqual(loop["worker_sample_count"], 3)
        self.assertEqual(loop["dataset_padding_count"], 15)
        self.assertEqual(loop["dataset_id"], "dataset-s1h")
        self.assertEqual(loop["dataset_version_id"], "version-1")
        self.assertEqual(loop["dataset_cache_policy"], "bounded")

    def test_train_loop_exports_non_secret_provenance_and_restores_environment(self):
        observed = {}
        loop = {
            "entrypoint": {"kind": "path", "target": "train.py", "argv": ["train.py"]},
            "checkpoint_every_epochs": 1,
            "keep_latest": 1,
            "keep_best": 0,
            "best_metric": "",
            "best_mode": "max",
            "job_id": "job-0123456789abcdef01234567",
            "parent_job_id": "",
            "storage_path": "/mnt/data/output/.platform/ray-train/job-0123456789abcdef01234567",
            "data_mode": "streaming",
            "worker_sample_count": 3,
            "dataset_padding_count": 15,
            "dataset_id": "dataset-s1h",
            "dataset_version_id": "version-1",
            "dataset_manifest_sha256": "a" * 64,
            "dataset_root": "/mnt/data/.platform/datasets",
            "dataset_cache_policy": "bounded",
            "dataset_prefetch_batches": 4,
            "dataset_shuffle_seed": 19,
            "ray_version": "2.58.0",
        }

        def capture(_entrypoint):
            observed.update(
                {
                    key: value
                    for key, value in os.environ.items()
                    if key.startswith("PLATFORM_DATASET_")
                    or key.startswith("RAYTRAIN_DATASET_")
                    or key == "PLATFORM_DATA_MODE"
                    or key == "PLATFORM_RAY_VERSION"
                }
            )

        original = {"KEEP": "yes"}
        with mock.patch.dict(os.environ, original, clear=True):
            with mock.patch.object(managed_driver, "execute", side_effect=capture):
                managed_driver._train_loop(loop)
            self.assertEqual(dict(os.environ), original)

        self.assertEqual(observed["PLATFORM_DATA_MODE"], "streaming")
        self.assertEqual(observed["PLATFORM_DATASET_ID"], "dataset-s1h")
        self.assertEqual(observed["PLATFORM_DATASET_VERSION_ID"], "version-1")
        self.assertEqual(observed["PLATFORM_DATASET_CACHE_POLICY"], "bounded")
        self.assertEqual(observed["RAYTRAIN_DATASET_WORKER_SAMPLES"], "3")
        self.assertEqual(observed["RAYTRAIN_DATASET_PREFETCH_BATCHES"], "4")
        self.assertEqual(observed["PLATFORM_RAY_VERSION"], "2.58.0")
        self.assertNotIn("TOS", " ".join(observed))


if __name__ == "__main__":
    unittest.main(verbosity=2)
