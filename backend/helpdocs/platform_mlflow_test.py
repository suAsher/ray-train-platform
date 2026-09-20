import os
import sys
import unittest
from types import SimpleNamespace
from unittest.mock import Mock, patch

from platform_mlflow import PlatformMLflow


class TrainingConnectionTest(unittest.TestCase):
    def setUp(self):
        self.env = {
            "MLFLOW_TRACKING_URI": "http://training-ingest/mlflow",
            "MLFLOW_EXPERIMENT_NAME": "raytrain-team",
            "MLFLOW_RUN_NAME": "job-123",
            "RAYTRAIN_JOB_ID": "job-123",
            "RAYTRAIN_TENANT_ID": "team",
            "RAYTRAIN_SUBMITTER_USER_ID": "user-123",
            "RAYTRAIN_MLFLOW_PROVENANCE": "test-only-provenance",
        }
        self.client = Mock()
        self.client.get_experiment_by_name.return_value = SimpleNamespace(experiment_id="7")
        self.client.create_run.return_value = SimpleNamespace(info=SimpleNamespace(run_id="run-123"))
        self.mlflow = Mock()
        self.mlflow.active_run.return_value = None
        self.mlflow.MlflowClient.return_value = self.client
        self.environ = patch.dict(os.environ, self.env, clear=True)
        self.modules = patch.dict(sys.modules, {"mlflow": self.mlflow})
        self.environ.start()
        self.modules.start()
        self.addCleanup(self.environ.stop)
        self.addCleanup(self.modules.stop)

    def test_legacy_ddp_without_cluster_attempt_links_and_ends_own_run(self):
        reporter = PlatformMLflow(global_rank=0)
        tags = self.client.create_run.call_args.kwargs["tags"]
        self.assertEqual(tags["platform.provenance"], self.env["RAYTRAIN_MLFLOW_PROVENANCE"])
        self.assertNotIn("platform.cluster_attempt", tags)
        reporter.params({"epochs": 2})
        reporter.metrics({"train/loss": 1.5}, step=0)
        self.client.log_metric.assert_called_once_with("run-123", "train/loss", 1.5, step=0)
        reporter.finish("FINISHED")
        reporter.finish("FINISHED")
        self.client.set_terminated.assert_called_once_with("run-123", status="FINISHED")

    def test_other_global_ranks_never_initialize(self):
        for rank in (1, 4, 8):
            reporter = PlatformMLflow(global_rank=rank)
            reporter.metrics({"loss": 1}, 1)
            reporter.finish("FAILED")
        self.mlflow.MlflowClient.assert_not_called()

    def test_missing_identity_disables_without_creating_unlinked_run(self):
        for key in ("RAYTRAIN_JOB_ID", "RAYTRAIN_MLFLOW_PROVENANCE"):
            with patch.dict(os.environ, {**self.env, key: ""}, clear=True):
                reporter = PlatformMLflow(global_rank=0)
                reporter.metrics({"loss": 1}, 1)
        self.client.create_run.assert_not_called()
        self.client.log_metric.assert_not_called()

    def test_failed_initialization_never_logs_implicitly(self):
        self.client.create_run.side_effect = RuntimeError("secret must not be printed")
        with patch("builtins.print") as output:
            reporter = PlatformMLflow(global_rank=0)
            reporter.metrics({"loss": 1}, 1)
            reporter.finish("FAILED")
        self.client.log_metric.assert_not_called()
        self.mlflow.log_metric.assert_not_called()
        self.assertNotIn("secret", str(output.call_args_list))

    def test_existing_linked_run_is_reused_but_not_ended(self):
        self.mlflow.active_run.return_value = SimpleNamespace(info=SimpleNamespace(run_id="existing"))
        self.client.get_run.return_value = SimpleNamespace(data=SimpleNamespace(tags={
            "platform.job_id": "job-123", "platform.tenant_id": "team",
            "platform.submitter_user_id": "user-123", "platform.provenance": "test-only-provenance",
        }))
        reporter = PlatformMLflow(global_rank=0)
        reporter.metrics({"loss": 1}, 1)
        reporter.finish("FAILED")
        self.client.create_run.assert_not_called()
        self.client.set_terminated.assert_not_called()
        self.client.log_metric.assert_called_once_with("existing", "loss", 1.0, step=1)

    def test_unrelated_active_run_is_not_modified(self):
        self.mlflow.active_run.return_value = SimpleNamespace(info=SimpleNamespace(run_id="other"))
        self.client.get_run.return_value = SimpleNamespace(data=SimpleNamespace(tags={}))
        reporter = PlatformMLflow(global_rank=0)
        reporter.metrics({"loss": 1}, 1)
        reporter.finish("FINISHED")
        self.client.log_metric.assert_not_called()
        self.client.set_terminated.assert_not_called()

    def test_write_failure_does_not_hide_training_failure(self):
        reporter = PlatformMLflow(global_rank=0)
        self.client.log_param.side_effect = RuntimeError("unavailable")
        self.client.log_metric.side_effect = RuntimeError("unavailable")
        reporter.params({"lr": 0.01})
        reporter.metrics({"loss": 1}, 1)
        reporter.finish("FAILED")
        self.client.set_terminated.assert_called_once_with("run-123", status="FAILED")

    def test_cluster_attempt_only_when_injected(self):
        with patch.dict(os.environ, {"RAYTRAIN_CLUSTER_ATTEMPT": "2"}):
            PlatformMLflow(global_rank=0)
        self.assertEqual(self.client.create_run.call_args.kwargs["tags"]["platform.cluster_attempt"], "2")


if __name__ == "__main__":
    unittest.main()
