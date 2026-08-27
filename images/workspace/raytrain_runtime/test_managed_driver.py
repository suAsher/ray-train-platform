from __future__ import annotations

import dataclasses
from contextlib import contextmanager
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

from raytrain_runtime.entrypoint import PythonEntrypoint  # noqa: E402
from raytrain_runtime.reporting import finalize_checkpoint  # noqa: E402
import raytrain_runtime.managed_driver as managed_driver  # noqa: E402
from raytrain_runtime.managed_driver import (  # noqa: E402
    DriverConfig,
    _train_loop,
    build_trainer,
    main,
    parse_driver_config,
)


JOB_ID = "job-0123456789abcdef01234567"
PARENT_JOB_ID = "job-89abcdef0123456701234567"
BASE_ENV = {
    "PLATFORM_JOB_ID": JOB_ID,
    "PLATFORM_OUTPUT_PATH": "/mnt/data/output",
}


class CapturedConfig:
    def __init__(self, **kwargs):
        self.kwargs = kwargs


class CapturedTrainer(CapturedConfig):
    pass


FAKE_RAY = types.SimpleNamespace(
    TorchTrainer=CapturedTrainer,
    ScalingConfig=CapturedConfig,
    RunConfig=CapturedConfig,
    FailureConfig=CapturedConfig,
    CheckpointConfig=CapturedConfig,
)

MANAGED_ENVIRONMENT_KEYS = (
    "RAYTRAIN_CHECKPOINT_EVERY_EPOCHS",
    "RAYTRAIN_CHECKPOINT_KEEP_LATEST",
    "RAYTRAIN_CHECKPOINT_KEEP_BEST",
    "RAYTRAIN_CHECKPOINT_BEST_METRIC",
    "RAYTRAIN_CHECKPOINT_BEST_MODE",
    "RAYTRAIN_CHECKPOINT_OUTPUT_PATH",
    "RAYTRAIN_RESUME_CHECKPOINT_PATH",
    "PLATFORM_CHECKPOINT_PATH",
)


def loop_config(job_id: str = JOB_ID) -> dict[str, object]:
    storage_path = f"/mnt/data/output/.platform/ray-train/{job_id}"
    return {
        "entrypoint": dataclasses.asdict(
            PythonEntrypoint("path", "train.py", ("train.py",))
        ),
        "checkpoint_every_epochs": 7,
        "keep_latest": 11,
        "keep_best": 2,
        "best_metric": "val/nds",
        "best_mode": "min",
        "job_id": job_id,
        "parent_job_id": "",
        "storage_path": storage_path,
    }


def driver_argv(*extra: str, entrypoint: tuple[str, ...] = ("python", "train.py")) -> list[str]:
    return [
        "--nodes",
        "2",
        "--gpus-per-node",
        "8",
        "--cpus-per-node",
        "64",
        *extra,
        "--",
        *entrypoint,
    ]


class DriverConfigTest(unittest.TestCase):
    def test_user_help_argument_after_separator_runs_user_entrypoint(self):
        parsed = parse_driver_config(driver_argv(entrypoint=("python", "train.py", "--help")), environ=BASE_ENV)
        trainer = mock.Mock()

        with mock.patch("raytrain_runtime.managed_driver.parse_driver_config", return_value=parsed) as parse:
            with mock.patch("raytrain_runtime.managed_driver.build_trainer", return_value=trainer):
                result = main(driver_argv(entrypoint=("python", "train.py", "--help")))

        self.assertEqual(result, 0)
        parse.assert_called_once()
        trainer.fit.assert_called_once_with()

    def test_parse_preserves_all_non_default_policy_values(self):
        config = parse_driver_config(
            driver_argv(
                "--max-failures",
                "7",
                "--checkpoint-every-epochs",
                "4",
                "--checkpoint-keep-latest",
                "9",
                "--checkpoint-keep-best",
                "2",
                "--best-metric",
                "val/nds",
                "--best-mode",
                "max",
                "--parent-job-id",
                PARENT_JOB_ID,
            ),
            environ=BASE_ENV,
        )

        self.assertEqual(config.max_failures, 7)
        self.assertEqual(config.checkpoint_every_epochs, 4)
        self.assertEqual(config.keep_latest, 9)
        self.assertEqual(config.keep_best, 2)
        self.assertEqual(config.best_metric, "val/nds")
        self.assertEqual(config.best_mode, "max")
        self.assertEqual(config.parent_job_id, PARENT_JOB_ID)

    def test_parse_preserves_policy_boundary_values(self):
        config = parse_driver_config(
            driver_argv(
                "--max-failures",
                "10",
                "--checkpoint-every-epochs",
                "100000",
                "--checkpoint-keep-latest",
                "1000",
                "--checkpoint-keep-best",
                "1000",
            ),
            environ=BASE_ENV,
        )

        self.assertEqual(
            (
                config.max_failures,
                config.checkpoint_every_epochs,
                config.keep_latest,
                config.keep_best,
            ),
            (10, 100000, 1000, 1000),
        )

    def test_parse_accepts_legacy_shell_wrapped_python_command(self):
        config = parse_driver_config(
            driver_argv(entrypoint=("/bin/sh", "-lc", "python -m package.train --epochs 2")),
            environ=BASE_ENV,
        )

        self.assertEqual(config.entrypoint.kind, "module")
        self.assertEqual(config.entrypoint.argv, ("package.train", "--epochs", "2"))

    def test_invalid_entrypoint_is_rejected_before_ray_is_loaded(self):
        with mock.patch(
            "raytrain_runtime.managed_driver._load_ray_components",
            side_effect=AssertionError("Ray must not load"),
        ):
            with self.assertRaisesRegex(ValueError, "must not contain torchrun"):
                parse_driver_config(
                    driver_argv(entrypoint=("torchrun", "train.py")),
                    environ=BASE_ENV,
                )

    def test_storage_path_is_job_scoped_below_platform_output(self):
        config = parse_driver_config(driver_argv(), environ=BASE_ENV)

        self.assertEqual(
            config.storage_path,
            f"/mnt/data/output/.platform/ray-train/{JOB_ID}",
        )

    def test_storage_path_rejects_traversal_and_outside_output(self):
        invalid = (
            "/tmp/ray-train/job",
            "/mnt/data/output/.platform/ray-train/../outside",
            "/mnt/data/output/other/job",
        )
        for storage_path in invalid:
            with self.subTest(storage_path=storage_path):
                with self.assertRaisesRegex(ValueError, "storage path"):
                    parse_driver_config(
                        driver_argv("--storage-path", storage_path),
                        environ=BASE_ENV,
                    )

    def test_platform_output_must_be_stable_output_mount(self):
        with self.assertRaisesRegex(ValueError, "PLATFORM_OUTPUT_PATH"):
            parse_driver_config(
                driver_argv(),
                environ={**BASE_ENV, "PLATFORM_OUTPUT_PATH": "/mnt/storage/me/runs/job"},
            )

    def test_parent_job_id_uses_existing_platform_format(self):
        with self.assertRaisesRegex(ValueError, "parent job ID"):
            parse_driver_config(
                driver_argv("--parent-job-id", "job-not-valid"),
                environ=BASE_ENV,
            )

    def test_driver_config_is_immutable(self):
        config = parse_driver_config(driver_argv(), environ=BASE_ENV)

        with self.assertRaises(dataclasses.FrozenInstanceError):
            config.max_failures = 9  # type: ignore[misc]


class TrainerFactoryTest(unittest.TestCase):
    def test_factory_rejects_driver_config_with_outside_storage_path(self):
        config = DriverConfig(
            entrypoint=PythonEntrypoint("path", "train.py", ("train.py",)),
            nodes=1,
            gpus_per_node=1,
            cpus_per_node=1,
            max_failures=0,
            checkpoint_every_epochs=0,
            keep_latest=0,
            keep_best=0,
            best_metric="",
            best_mode="min",
            job_id=JOB_ID,
            parent_job_id="",
            storage_path="/tmp/outside",
        )

        with self.assertRaisesRegex(ValueError, "storage path"):
            build_trainer(config, ray_components=FAKE_RAY)

    def test_factory_creates_deterministic_two_node_eight_gpu_trainer(self):
        config = DriverConfig(
            entrypoint=PythonEntrypoint("path", "train.py", ("train.py", "--epochs", "2")),
            nodes=2,
            gpus_per_node=8,
            cpus_per_node=64,
            max_failures=2,
            checkpoint_every_epochs=3,
            keep_latest=4,
            keep_best=2,
            best_metric="val/nds",
            best_mode="max",
            job_id=JOB_ID,
            parent_job_id=PARENT_JOB_ID,
            storage_path=f"/mnt/data/output/.platform/ray-train/{JOB_ID}",
        )

        trainer = build_trainer(config, ray_components=FAKE_RAY)

        self.assertIsInstance(trainer, CapturedTrainer)
        scaling = trainer.kwargs["scaling_config"].kwargs
        self.assertEqual(scaling["num_workers"], 16)
        self.assertTrue(scaling["use_gpu"])
        self.assertEqual(scaling["resources_per_worker"], {"CPU": 8})
        self.assertEqual(scaling["placement_strategy"], "PACK")

        run = trainer.kwargs["run_config"].kwargs
        self.assertEqual(run["name"], JOB_ID)
        self.assertEqual(run["storage_path"], config.storage_path)
        self.assertEqual(run["failure_config"].kwargs["max_failures"], 2)
        checkpoint = run["checkpoint_config"].kwargs
        self.assertEqual(checkpoint["num_to_keep"], 6)
        self.assertNotIn("checkpoint_score_attribute", checkpoint)
        self.assertNotIn("checkpoint_score_order", checkpoint)

        loop = trainer.kwargs["train_loop_config"]
        self.assertEqual(loop["checkpoint_every_epochs"], 3)
        self.assertEqual(loop["keep_latest"], 4)
        self.assertEqual(loop["keep_best"], 2)
        self.assertEqual(loop["parent_job_id"], PARENT_JOB_ID)
        self.assertEqual(loop["storage_path"], config.storage_path)

    def test_cpu_per_worker_has_floor_of_one(self):
        config = DriverConfig(
            entrypoint=PythonEntrypoint("path", "train.py", ("train.py",)),
            nodes=1,
            gpus_per_node=8,
            cpus_per_node=4,
            max_failures=0,
            checkpoint_every_epochs=0,
            keep_latest=0,
            keep_best=0,
            best_metric="",
            best_mode="min",
            job_id=JOB_ID,
            parent_job_id="",
            storage_path=f"/mnt/data/output/.platform/ray-train/{JOB_ID}",
        )

        trainer = build_trainer(config, ray_components=FAKE_RAY)

        scaling = trainer.kwargs["scaling_config"].kwargs
        self.assertEqual(scaling["resources_per_worker"], {"CPU": 1})
        checkpoint = trainer.kwargs["run_config"].kwargs["checkpoint_config"].kwargs
        self.assertIsNone(checkpoint["num_to_keep"])
        self.assertNotIn("checkpoint_score_attribute", checkpoint)
        self.assertNotIn("checkpoint_score_order", checkpoint)

    def test_ray_copy_bound_includes_latest_and_best_without_score_ordering(self):
        config = DriverConfig(
            entrypoint=PythonEntrypoint("path", "train.py", ("train.py",)),
            nodes=1,
            gpus_per_node=1,
            cpus_per_node=8,
            max_failures=0,
            checkpoint_every_epochs=1,
            keep_latest=0,
            keep_best=2,
            best_metric="mAP",
            best_mode="min",
            job_id=JOB_ID,
            parent_job_id="",
            storage_path=f"/mnt/data/output/.platform/ray-train/{JOB_ID}",
        )

        trainer = build_trainer(config, ray_components=FAKE_RAY)
        checkpoint = trainer.kwargs["run_config"].kwargs["checkpoint_config"].kwargs

        self.assertEqual(checkpoint, {"num_to_keep": 2})


class TrainLoopEnvironmentTest(unittest.TestCase):
    def test_non_default_policy_and_job_checkpoint_path_reach_user_entrypoint(self):
        observed = {}

        def capture(_entrypoint):
            observed.update({key: os.environ.get(key) for key in MANAGED_ENVIRONMENT_KEYS})

        with mock.patch.dict(
            os.environ,
            {"PLATFORM_CHECKPOINT_PATH": "/mnt/data/resume/checkpoint-7"},
            clear=True,
        ):
            with mock.patch("raytrain_runtime.managed_driver.execute", side_effect=capture):
                _train_loop(loop_config())

        self.assertEqual(
            observed,
            {
                "RAYTRAIN_CHECKPOINT_EVERY_EPOCHS": "7",
                "RAYTRAIN_CHECKPOINT_KEEP_LATEST": "11",
                "RAYTRAIN_CHECKPOINT_KEEP_BEST": "2",
                "RAYTRAIN_CHECKPOINT_BEST_METRIC": "val/nds",
                "RAYTRAIN_CHECKPOINT_BEST_MODE": "min",
                "RAYTRAIN_CHECKPOINT_OUTPUT_PATH": (
                    f"/mnt/data/output/.platform/ray-train/{JOB_ID}/checkpoints"
                ),
                "RAYTRAIN_RESUME_CHECKPOINT_PATH": None,
                "PLATFORM_CHECKPOINT_PATH": "/mnt/data/resume/checkpoint-7",
            },
        )

    def test_lazy_ray_checkpoint_is_handed_to_user_code_and_environment_is_restored(self):
        observed = {}
        with tempfile.TemporaryDirectory() as temporary:
            checkpoint = pathlib.Path(temporary) / "checkpoint"
            checkpoint.mkdir()
            (checkpoint / "training_state.pth").write_bytes(b"state")
            finalize_checkpoint(checkpoint, {"epoch": 1, "step": 10})

            class FakeCheckpoint:
                @contextmanager
                def as_directory(self):
                    yield str(checkpoint)

            original = {
                "PLATFORM_CHECKPOINT_PATH": "/mnt/data/resume/backend-input",
                "RAYTRAIN_RESUME_CHECKPOINT_PATH": "/before/ray-resume",
            }

            def capture(_entrypoint):
                observed.update(
                    {key: os.environ.get(key) for key in MANAGED_ENVIRONMENT_KEYS}
                )

            with mock.patch.dict(os.environ, original, clear=True):
                with mock.patch.object(
                    managed_driver,
                    "_load_resume_checkpoint",
                    return_value=FakeCheckpoint(),
                    create=True,
                ):
                    with mock.patch(
                        "raytrain_runtime.managed_driver.execute", side_effect=capture
                    ):
                        _train_loop(loop_config())
                self.assertEqual(dict(os.environ), original)

        self.assertEqual(
            observed["RAYTRAIN_RESUME_CHECKPOINT_PATH"], str(checkpoint.resolve())
        )
        self.assertEqual(
            observed["PLATFORM_CHECKPOINT_PATH"],
            "/mnt/data/resume/backend-input",
        )

    def test_invalid_ray_checkpoint_fails_before_user_code_runs(self):
        execute = mock.Mock()
        with tempfile.TemporaryDirectory() as temporary:
            checkpoint = pathlib.Path(temporary) / "incomplete"
            checkpoint.mkdir()
            (checkpoint / "partial.pth").write_bytes(b"partial")

            class FakeCheckpoint:
                @contextmanager
                def as_directory(self):
                    yield str(checkpoint)

            with mock.patch.object(
                managed_driver,
                "_load_resume_checkpoint",
                return_value=FakeCheckpoint(),
                create=True,
            ):
                with mock.patch(
                    "raytrain_runtime.managed_driver.execute", execute
                ):
                    with self.assertRaisesRegex(ValueError, "complete manifest"):
                        _train_loop(loop_config())

        execute.assert_not_called()

    def test_resume_checkpoint_loader_imports_ray_only_when_called(self):
        sentinel = object()
        fake_train = types.ModuleType("ray.train")
        fake_train.get_checkpoint = lambda: sentinel
        fake_ray = types.ModuleType("ray")
        fake_ray.train = fake_train

        with mock.patch.dict(
            sys.modules, {"ray": fake_ray, "ray.train": fake_train}
        ):
            loaded = managed_driver._load_resume_checkpoint()

        self.assertIs(loaded, sentinel)

    def test_checkpoint_paths_are_scoped_by_job_storage(self):
        observed = []

        def capture(_entrypoint):
            observed.append(os.environ["RAYTRAIN_CHECKPOINT_OUTPUT_PATH"])

        with mock.patch("raytrain_runtime.managed_driver.execute", side_effect=capture):
            _train_loop(loop_config("job-aaaaaaaaaaaaaaaaaaaaaaaa"))
            _train_loop(loop_config("job-bbbbbbbbbbbbbbbbbbbbbbbb"))

        self.assertEqual(
            observed,
            [
                "/mnt/data/output/.platform/ray-train/job-aaaaaaaaaaaaaaaaaaaaaaaa/checkpoints",
                "/mnt/data/output/.platform/ray-train/job-bbbbbbbbbbbbbbbbbbbbbbbb/checkpoints",
            ],
        )

    def test_restores_overwritten_and_new_environment_after_success(self):
        original = {
            "RAYTRAIN_CHECKPOINT_KEEP_LATEST": "original",
            "RAYTRAIN_CHECKPOINT_OUTPUT_PATH": "/before/output-checkpoints",
            "PLATFORM_CHECKPOINT_PATH": "/before/resume-checkpoint",
            "UNRELATED": "preserved",
        }
        with mock.patch.dict(os.environ, original, clear=True):
            with mock.patch("raytrain_runtime.managed_driver.execute"):
                _train_loop(loop_config())
            self.assertEqual(dict(os.environ), original)

    def test_restores_overwritten_and_new_environment_after_exception(self):
        original = {
            "RAYTRAIN_CHECKPOINT_BEST_MODE": "original",
            "UNRELATED": "preserved",
        }

        def mutate_environment_then_fail(_entrypoint):
            os.environ["RAYTRAIN_CHECKPOINT_BEST_MODE"] = "user-mutated"
            os.environ.pop("RAYTRAIN_CHECKPOINT_KEEP_LATEST")
            raise RuntimeError("user code failed")

        with mock.patch.dict(os.environ, original, clear=True):
            with mock.patch(
                "raytrain_runtime.managed_driver.execute",
                side_effect=mutate_environment_then_fail,
            ):
                with self.assertRaisesRegex(RuntimeError, "user code failed"):
                    _train_loop(loop_config())
            self.assertEqual(dict(os.environ), original)


if __name__ == "__main__":
    unittest.main()
