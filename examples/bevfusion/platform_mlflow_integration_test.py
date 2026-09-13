"""CPU-only integration with real MMCV/MLflow; uses a temporary local store.

Run in an isolated builder process with RAYTRAIN_REAL_MLFLOW_TEST=1 and
mlflow-skinny==2.17.2, mmcv==1.4.0, CPU PyTorch and torchvision installed.
No production credentials, tracking server, training data or GPU are needed.
"""

import contextlib
import importlib.util
import io
import logging
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock


@unittest.skipUnless(os.environ.get("RAYTRAIN_REAL_MLFLOW_TEST") == "1",
                     "requires isolated builder MLflow/MMCV dependencies")
class RealMLflowLoggingTest(unittest.TestCase):
    def test_late_pytorch_import_keeps_one_log_and_persists_metrics(self):
        # Imports happen in a fresh process; do not stub any dependency here.
        from mmcv import Config
        from mmcv.runner.hooks.logger.mlflow import MlflowLoggerHook
        from mmcv.utils import get_logger
        # Initialize the tracking client's own logging configuration first.
        # This test isolates the later mlflow.pytorch import performed by MMCV,
        # rather than MLflow's unrelated first-import dictConfig file closure.
        import mlflow

        self.assertNotIn("mlflow.pytorch", __import__("sys").modules)

        adapter_path = Path(os.environ.get(
            "BEVFUSION_MLFLOW_ADAPTER",
            str(Path(__file__).with_name("patches") / "platform_mlflow.py"),
        ))
        spec = importlib.util.spec_from_file_location("platform_mlflow", adapter_path)
        adapter = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(adapter)

        with tempfile.TemporaryDirectory() as directory:
            output = io.StringIO()
            log_file = Path(directory) / "training.log"
            environment = {
                "MLFLOW_TRACKING_URI": (Path(directory) / "mlruns").as_uri(),
                "MLFLOW_EXPERIMENT_NAME": "logging-regression",
                "MLFLOW_RUN_NAME": "isolated-cpu-test",
                "RAYTRAIN_JOB_ID": "job-logging-test",
                "RAYTRAIN_TENANT_ID": "test",
                "RAYTRAIN_SUBMITTER_USER_ID": "test",
                "RAYTRAIN_MLFLOW_PROVENANCE": "test-only",
                "RAYTRAIN_CLUSTER_ATTEMPT": "1",
            }
            config = Config(dict(
                optimizer=dict(type="AdamW", lr=0.001),
                runner=dict(max_epochs=1), seed=42,
                log_config=dict(interval=50, hooks=[dict(type="TextLoggerHook")]),
            ))
            with mock.patch.dict(os.environ, environment, clear=True), contextlib.redirect_stderr(output):
                logger = get_logger("mmdet3d", log_file=str(log_file))
                handlers = list(logger.handlers)
                client = adapter.start_platform_mlflow(config, 0, 8)
                run_id = client.active_run().info.run_id
                self.assertEqual(client.__version__, "2.17.2")
                root_before = list(logging.getLogger().handlers)
                hook_config = dict(config.log_config.hooks[-1])
                hook_config.pop("type")
                hook = MlflowLoggerHook(**hook_config)
                self.assertGreater(len(logging.getLogger().handlers), len(root_before),
                                   "fixture must reproduce MLflow's late root handler")
                logger.info("Epoch [1][50/100] loss: 0.25")
                logging.getLogger("unrelated-library").error("unrelated-root-message")
                runner = SimpleNamespace(
                    mode="train", epoch=0, iter=49,
                    log_buffer=SimpleNamespace(output={"loss": 0.25, "time": 1.0}),
                    current_lr=lambda: [0.001], current_momentum=lambda: [0.9],
                )
                hook.log(runner)
                adapter.finish_platform_mlflow(client, "FINISHED")
                recorded = client.tracking.MlflowClient().get_run(run_id)
                self.assertEqual(recorded.data.metrics["train/loss"], 0.25)
                self.assertEqual(recorded.data.metrics["learning_rate"], 0.001)
                self.assertEqual(recorded.data.params["world_size"], "8")
                self.assertEqual(recorded.info.status, "FINISHED")
                self.assertEqual(logger.handlers, handlers)
                self.assertEqual(log_file.read_text().count("Epoch [1][50/100]"), 1)
                self.assertIn("unrelated-root-message", output.getvalue())
                self.assertEqual(output.getvalue().count("Epoch [1][50/100]"), 1,
                                 output.getvalue())
                for handler in handlers:
                    handler.close()


if __name__ == "__main__":
    unittest.main()
