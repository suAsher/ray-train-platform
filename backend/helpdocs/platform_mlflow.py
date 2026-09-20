"""Copy as platform_mlflow.py beside your training entry point; needs mlflow."""
import os
import time


class PlatformMLflow:
    def __init__(self, global_rank):
        self.client = None
        self.run_id = None
        self.owned = False
        self.warned = set()
        self.retry_at = 0
        if global_rank != 0:
            return
        try:
            # Bound auxiliary network waits; explicit user settings take priority.
            os.environ.setdefault("MLFLOW_HTTP_REQUEST_TIMEOUT", "10")
            os.environ.setdefault("MLFLOW_HTTP_REQUEST_MAX_RETRIES", "0")
            import mlflow

            def required(name):
                value = os.environ.get(name, "").strip()
                if not value:
                    raise ValueError("missing platform connection or identity")
                return value

            tags = {
                "platform.job_id": required("RAYTRAIN_JOB_ID"),
                "platform.tenant_id": required("RAYTRAIN_TENANT_ID"),
                "platform.submitter_user_id": required("RAYTRAIN_SUBMITTER_USER_ID"),
                "platform.provenance": required("RAYTRAIN_MLFLOW_PROVENANCE"),
            }
            attempt = os.environ.get("RAYTRAIN_CLUSTER_ATTEMPT", "").strip()
            if attempt:
                tags["platform.cluster_attempt"] = attempt
            client = mlflow.MlflowClient(tracking_uri=required("MLFLOW_TRACKING_URI"))
            active = mlflow.active_run()
            if active is not None:
                run = client.get_run(active.info.run_id)
                if any(run.data.tags.get(key) != value for key, value in tags.items()):
                    raise ValueError("active Run belongs to a different training context")
                run_id = active.info.run_id
            else:
                name = required("MLFLOW_EXPERIMENT_NAME")
                experiment = client.get_experiment_by_name(name)
                if experiment is None:
                    try:
                        experiment_id = client.create_experiment(name)
                    except Exception:
                        experiment = client.get_experiment_by_name(name)
                        if experiment is None:
                            raise
                        experiment_id = experiment.experiment_id
                else:
                    experiment_id = experiment.experiment_id
                run = client.create_run(
                    experiment_id, run_name=required("MLFLOW_RUN_NAME"), tags=tags
                )
                run_id = run.info.run_id
                self.owned = True
            self.client, self.run_id = client, run_id
            print("MLflow Run:", run_id, flush=True)
        except Exception as exc:
            self._warn("初始化", exc)

    def _warn(self, operation, exc):
        self.retry_at = time.monotonic() + 60
        if operation not in self.warned:
            print("MLflow " + operation + "失败（训练继续）：" + type(exc).__name__, flush=True)
            self.warned.add(operation)

    def params(self, values):
        if self.run_id is None or time.monotonic() < self.retry_at:
            return
        try:
            for key, value in values.items():
                self.client.log_param(self.run_id, key, value)
        except Exception as exc:
            self._warn("参数上报", exc)

    def metrics(self, values, step):
        if self.run_id is None or time.monotonic() < self.retry_at:
            return
        try:
            for key, value in values.items():
                self.client.log_metric(self.run_id, key, float(value), step=int(step))
        except Exception as exc:
            self._warn("指标上报", exc)

    def finish(self, status="FINISHED"):
        if self.run_id is None or not self.owned:
            return
        try:
            self.client.set_terminated(self.run_id, status=status)
        except Exception as exc:
            self._warn("结束 Run", exc)
        finally:
            self.owned = False
