"""Standard-library client for private external MLflow tracking through RayTrain.

Every write requires an explicit command. Creation uses a caller-supplied
idempotency key; the client never retries requests automatically. Identifiers
in URLs are RayTrain platform IDs, not upstream MLflow experiment/run IDs.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import unicodedata
import urllib.parse
from typing import Any

if __package__:
    from .client import (MAX_BATCH_BYTES, RUN_PATTERN, PlatformAPIError,
                         RayTrainMLflowClient, _encode_batch, _read_payload)
else:
    from client import (MAX_BATCH_BYTES, RUN_PATTERN, PlatformAPIError,
                        RayTrainMLflowClient, _encode_batch, _read_payload)

# Preserve the example's public error name while sharing safe metadata handling.
RayTrainAPIError = PlatformAPIError


def require_platform_id(value: str, label: str) -> str:
    if not isinstance(value, str) or not RUN_PATTERN.fullmatch(value):
        raise ValueError(f"{label} must be a 32 character lowercase hexadecimal platform ID")
    return value


def read_json_file(path: str) -> dict:
    """Read one bounded, strict local batch file without changing the file."""
    return _read_payload(path)


def _name(value: str) -> str:
    if (not isinstance(value, str) or not value or value != value.strip()
            or len(value.encode("utf-8")) > 128
            or any(unicodedata.category(char) == "Cc" for char in value)):
        raise ValueError("name must contain 1 to 128 UTF-8 bytes without surrounding whitespace or controls")
    return value


def _page_query(limit: int, cursor: str) -> str:
    if type(limit) is not int or not 1 <= limit <= 100:
        raise ValueError("limit must be an integer from 1 to 100")
    if (not isinstance(cursor, str) or len(cursor) > 2048
            or any(ord(char) < 32 or ord(char) == 127 for char in cursor)):
        raise ValueError("cursor must be at most 2048 characters without controls")
    return urllib.parse.urlencode({"limit": limit, **({"cursor": cursor} if cursor else {})})


class RayTrainExternalTracking(RayTrainMLflowClient):
    """Reuse HTTPS-only, bounded, redacted, no-redirect/no-retry transport."""

    def _external_request(self, method, path, body=None, idempotency_key=None):
        if method not in {"GET", "POST"} or not isinstance(path, str) or not path.startswith("/api/v1/mlflow/"):
            raise ValueError("external requests must use the configured origin's tracking API")
        try:
            encoded = None if body is None else json.dumps(body, ensure_ascii=False, allow_nan=False).encode("utf-8")
        except (TypeError, ValueError, UnicodeError):
            raise ValueError("request body must contain valid finite JSON values") from None
        if encoded is not None and len(encoded) > MAX_BATCH_BYTES:
            raise ValueError("request body exceeds 256 KiB")
        return self._request(method, path, encoded, idempotency_key=idempotency_key)

    def request(self, method: str, path: str, body: Any = None, idempotency_key: str | None = None) -> dict:
        data, _ = self._external_request(method, path, body, idempotency_key)
        return data

    @staticmethod
    def _unexpected(request_id):
        return PlatformAPIError(200, "INVALID_RESPONSE", "Response did not match the requested platform resource. Verify state before retrying writes.", request_id)

    def _create(self, path, name, idempotency_key, experiment_id=None):
        if idempotency_key is None:
            raise ValueError("creation requires an explicit idempotency key")
        data, request_id = self._external_request("POST", path, {"name": _name(name)}, idempotency_key)
        identifier = data.get("id")
        if (not isinstance(identifier, str) or not RUN_PATTERN.fullmatch(identifier)
                or data.get("name") != name
                or (experiment_id is not None and data.get("experimentId") != experiment_id)):
            raise self._unexpected(request_id)
        return data

    def capabilities(self) -> dict:
        return self.request("GET", "/api/v1/mlflow/capabilities")

    def create_experiment(self, name: str, idempotency_key: str) -> dict:
        return self._create("/api/v1/mlflow/experiments", name, idempotency_key)

    def list_experiments(self, limit: int = 50, cursor: str = "") -> dict:
        """Read one owner-scoped page; pass its nextCursor explicitly to continue."""
        return self.request("GET", "/api/v1/mlflow/experiments?" + _page_query(limit, cursor))

    def create_run(self, experiment_id: str, name: str, idempotency_key: str) -> dict:
        experiment_id = require_platform_id(experiment_id, "experiment ID")
        return self._create(f"/api/v1/mlflow/experiments/{experiment_id}/runs", name, idempotency_key, experiment_id)

    def list_runs(self, experiment_id: str, limit: int = 50, cursor: str = "") -> dict:
        experiment_id = require_platform_id(experiment_id, "experiment ID")
        return self.request("GET", f"/api/v1/mlflow/experiments/{experiment_id}/runs?" + _page_query(limit, cursor))

    def get_run(self, run_id: str) -> dict:
        run_id = require_platform_id(run_id, "run ID")
        data, request_id = self._external_request("GET", f"/api/v1/mlflow/runs/{run_id}")
        if not isinstance(data.get("run"), dict) or data["run"].get("id") != run_id:
            raise self._unexpected(request_id)
        return data

    def log_batch(self, run_id: str, batch: dict) -> dict:
        run_id = require_platform_id(run_id, "run ID")
        data, request_id = self._request("POST", f"/api/v1/mlflow/runs/{run_id}/log-batch", _encode_batch(batch))
        if data.get("runId") != run_id or data.get("logged") is not True:
            raise self._unexpected(request_id)
        return data

    def finish_run(self, run_id: str, status: str) -> dict:
        run_id = require_platform_id(run_id, "run ID")
        if not isinstance(status, str) or status.upper() not in {"FINISHED", "FAILED", "KILLED"}:
            raise ValueError("status must be FINISHED, FAILED or KILLED")
        status = status.upper()
        data, request_id = self._external_request("POST", f"/api/v1/mlflow/runs/{run_id}/finish", {"status": status})
        if data.get("id") != run_id or data.get("state") != status:
            raise self._unexpected(request_id)
        return data


def print_sdk_example() -> None:
    print("""# MLflow SDK 3.14 limited adapter illustration.
# Set RAYTRAIN_API and MLFLOW_TRACKING_TOKEN in the environment.
# PLATFORM_RUN_ID must be the REST-precreated RayTrain platform run id.

import os
import time
import mlflow

client = mlflow.tracking.MlflowClient(
    tracking_uri=os.environ["RAYTRAIN_API"].rstrip("/") + "/api/v1/mlflow-tracking"
)
run_id = os.environ["RAYTRAIN_PLATFORM_RUN_ID"]
client.get_run(run_id)
client.log_metric(run_id, "external/quality_score", 0.91, step=1)
client.log_param(run_id, "external.evaluator_version", "v1")
client.set_tag(run_id, "external.source", "quality-service")
client.set_terminated(run_id, status="FINISHED", end_time=int(time.time() * 1000))

# Do not call create_experiment, create_run, start_run, log_artifact, log_model,
# registry, trace or serving APIs on this adapter. artifact_uri is raytrain-disabled.
""")

def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default=os.environ.get("RAYTRAIN_API", ""), help="RayTrain HTTPS API origin; may also use RAYTRAIN_API")
    parser.add_argument("--token-env", default="RAYTRAIN_PAT", help="environment variable containing the PAT")
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("capabilities")
    list_experiments = sub.add_parser("list-experiments")
    list_experiments.add_argument("--limit", type=int, default=50)
    list_experiments.add_argument("--cursor", default="")
    create_experiment = sub.add_parser("create-experiment")
    create_experiment.add_argument("--name", required=True)
    create_experiment.add_argument("--idempotency-key", required=True)
    list_runs = sub.add_parser("list-runs")
    list_runs.add_argument("--experiment-id", required=True)
    list_runs.add_argument("--limit", type=int, default=50)
    list_runs.add_argument("--cursor", default="")
    create_run = sub.add_parser("create-run")
    create_run.add_argument("--experiment-id", required=True)
    create_run.add_argument("--name", required=True)
    create_run.add_argument("--idempotency-key", required=True)
    get_run = sub.add_parser("get-run")
    get_run.add_argument("--run-id", required=True)
    log_batch = sub.add_parser("log-batch")
    log_batch.add_argument("--run-id", required=True)
    log_batch.add_argument("--payload", required=True)
    finish = sub.add_parser("finish")
    finish.add_argument("--run-id", required=True)
    finish.add_argument("--status", required=True, choices=["FINISHED", "FAILED", "KILLED"])
    sub.add_parser("sdk-example")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.command == "sdk-example":
        print_sdk_example()
        return 0
    credential = os.environ.get(args.token_env, "")
    try:
        integration = RayTrainExternalTracking(args.base_url, credential)
        result = _dispatch(args, integration)
        print(json.dumps(result, ensure_ascii=False, allow_nan=False, indent=2, sort_keys=True).replace(credential, "[REDACTED]"))
        return 0
    except PlatformAPIError as error:
        message, exit_code = str(error), 1
    except ValueError as error:
        message, exit_code = f"input error: {error}", 2
    except OSError:
        message, exit_code = "Could not read the local payload file.", 2
    print(message.replace(credential, "[REDACTED]") if credential else message, file=sys.stderr)
    return exit_code


def _dispatch(args, client):
    if args.command == "capabilities":
        result = client.capabilities()
    elif args.command == "list-experiments":
        result = client.list_experiments(args.limit, args.cursor)
    elif args.command == "create-experiment":
        result = client.create_experiment(args.name, args.idempotency_key)
    elif args.command == "list-runs":
        result = client.list_runs(args.experiment_id, args.limit, args.cursor)
    elif args.command == "create-run":
        result = client.create_run(args.experiment_id, args.name, args.idempotency_key)
    elif args.command == "get-run":
        result = client.get_run(args.run_id)
    elif args.command == "log-batch":
        result = client.log_batch(args.run_id, read_json_file(args.payload))
    elif args.command == "finish":
        result = client.finish_run(args.run_id, args.status)
    else:
        raise AssertionError(args.command)
    return result


if __name__ == "__main__":
    raise SystemExit(main())
