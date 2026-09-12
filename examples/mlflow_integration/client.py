"""Standard-library client for the bounded RayTrain MLflow REST integration.

This is not an MLflow Tracking Server SDK adapter. All network operations
require an explicit method call or CLI subcommand. Writes are never retried.
"""

from __future__ import annotations

import argparse
import http.client
import json
import math
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any


MAX_BATCH_BYTES = 256 * 1024
MAX_RESPONSE_BYTES = 8 * 1024 * 1024
JOB_PATTERN = re.compile(r"[A-Za-z0-9_-]{1,128}\Z")
RUN_PATTERN = re.compile(r"[0-9a-f]{32}\Z")
IDEMPOTENCY_PATTERN = re.compile(r"[A-Za-z0-9._:-]{1,128}\Z")
WINDOW_NOTICE = (
    "Recent window only, at most 100 runs; this is not a full export. "
    "An absent run does not establish whether it exists or whether you have "
    "permission. Use read with an explicitly authorized job/run pair."
)


class PlatformAPIError(Exception):
    """Safe platform error metadata, with no raw response or transport detail."""

    def __init__(self, status: int, code: str, message: str, request_id: str = "", retry_after: int | None = None):
        self.status = status
        self.code = code
        self.message = message
        self.request_id = request_id
        self.retry_after = retry_after
        retry_hint = f", retry_after={retry_after}s" if retry_after is not None else ""
        super().__init__(
            f"{code} (HTTP {status or 'unavailable'}, request_id={request_id or 'unavailable'}{retry_hint}): {message}"
        )


class NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    """Never forward the Authorization header to a redirected URL."""

    def redirect_request(self, request, fp, code, message, headers, new_url):
        return None


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON field")
        result[key] = value
    return result


def _reject_constant(_value):
    raise ValueError("non-finite JSON value")


def _strict_json(raw):
    return json.loads(raw, object_pairs_hook=_unique_object, parse_constant=_reject_constant)


def _retry_after_seconds(headers) -> int | None:
    values = headers.get_all("Retry-After", [])
    if len(values) != 1:
        return None
    value = values[0].strip(" \t")
    if not re.fullmatch(r"[0-9]{1,10}", value):
        return None
    seconds = int(value)
    return seconds if seconds <= 2147483647 else None


def _encode_batch(payload: Any) -> bytes:
    if not isinstance(payload, dict) or not payload:
        raise ValueError("payload must be a nonempty JSON object")
    if set(payload) - {"metrics", "params", "tags"}:
        raise ValueError("payload supports only metrics, params and tags")
    if any(not isinstance(items, list) or len(items) > 100 for items in payload.values()):
        raise ValueError("each payload field must be an array of at most 100 items")
    if not any(payload.values()):
        raise ValueError("payload must contain at least one metric, parameter or tag")
    try:
        encoded = json.dumps(payload, ensure_ascii=False, allow_nan=False).encode("utf-8")
    except (TypeError, ValueError):
        raise ValueError("payload must contain valid finite JSON values") from None
    if len(encoded) > MAX_BATCH_BYTES:
        raise ValueError("payload exceeds 256 KiB")
    return encoded


class RayTrainMLflowClient:
    def __init__(self, base_url: str, token: str, timeout: float = 30):
        if not isinstance(base_url, str) or any(char.isspace() or ord(char) < 32 for char in base_url):
            raise ValueError("base URL must be an explicit HTTPS origin")
        parsed = urllib.parse.urlsplit(base_url)
        if (
            parsed.scheme != "https" or not parsed.hostname or parsed.username is not None
            or parsed.password is not None or parsed.path not in ("", "/")
            or parsed.query or parsed.fragment
        ):
            raise ValueError("base URL must be an HTTPS origin without credentials, path, query or fragment")
        if parsed.port is not None and not 1 <= parsed.port <= 65535:
            raise ValueError("base URL port is invalid")
        if not isinstance(token, str) or not token or token != token.strip() or any(ord(char) < 33 or ord(char) > 126 for char in token):
            raise ValueError("RAYTRAIN_PAT must be a nonempty header-safe value without whitespace")
        if isinstance(timeout, bool) or not isinstance(timeout, (float, int)) or not math.isfinite(timeout) or not 0 < timeout <= 120:
            raise ValueError("timeout must be between 0 and 120 seconds")
        self._base_url = base_url.rstrip("/")
        self._credential = token
        self._timeout = timeout
        self._opener = urllib.request.build_opener(NoRedirectHandler())

    def _safe_text(self, value: Any, fallback: str, limit: int = 1024) -> str:
        if not isinstance(value, str):
            return fallback
        redacted = value.replace(self._credential, "[REDACTED]")
        return "".join(char for char in redacted if ord(char) >= 32 and ord(char) != 127)[:limit]

    def _request(self, method: str, path: str, body: bytes | None = None, *, idempotency_key: str | None = None):
        headers = {"Authorization": "Bearer " + self._credential, "Accept": "application/json", "Content-Type": "application/json"}
        if idempotency_key is not None:
            if not isinstance(idempotency_key, str) or not IDEMPOTENCY_PATTERN.fullmatch(idempotency_key):
                raise ValueError("idempotency key must contain 1 to 128 ASCII letters, digits, dots, underscores, colons or hyphens")
            headers["Idempotency-Key"] = idempotency_key
        request = urllib.request.Request(
            self._base_url + path, data=body, method=method,
            headers=headers,
        )
        try:
            try:
                stream = self._opener.open(request, timeout=self._timeout)
            except urllib.error.HTTPError as error:
                stream = error
            with stream:
                status = stream.code
                retry_after = _retry_after_seconds(stream.headers)
                if 300 <= status < 400:
                    raise PlatformAPIError(status, "REDIRECT_BLOCKED", "Redirect refused; use the verified HTTPS API origin directly.", retry_after=retry_after)
                raw = stream.read(MAX_RESPONSE_BYTES + 1)
        except PlatformAPIError:
            raise
        except (urllib.error.URLError, OSError, http.client.HTTPException):
            raise PlatformAPIError(0, "NETWORK_ERROR", "Request outcome is unknown. Verify the run before retrying a write.") from None
        if len(raw) > MAX_RESPONSE_BYTES:
            raise PlatformAPIError(status, "INVALID_RESPONSE", "Response exceeded the size limit. Verify the run before retrying a write.", retry_after=retry_after)
        try:
            envelope = _strict_json(raw)
        except (ValueError, UnicodeError, RecursionError):
            raise PlatformAPIError(status, "INVALID_RESPONSE", "Response was not a valid platform envelope. Verify the run before retrying a write.", retry_after=retry_after) from None
        if not isinstance(envelope, dict):
            raise PlatformAPIError(status, "INVALID_RESPONSE", "Response was not a platform envelope.", retry_after=retry_after)
        request_id = self._safe_text(envelope.get("request_id"), "", 128)
        if not 200 <= status < 300 or envelope.get("success") is not True:
            error = envelope.get("error")
            error = error if isinstance(error, dict) else {}
            raise PlatformAPIError(
                status, self._safe_text(error.get("code"), "REQUEST_FAILED", 128),
                self._safe_text(error.get("message"), "Request failed; verify the run before retrying a write."), request_id, retry_after,
            )
        if not isinstance(envelope.get("data"), dict):
            raise PlatformAPIError(status, "INVALID_RESPONSE", "Response data was missing or invalid.", request_id, retry_after)
        return envelope["data"], request_id

    @staticmethod
    def _run_path(job_id: str, run_id: str) -> str:
        if not isinstance(job_id, str) or not JOB_PATTERN.fullmatch(job_id):
            raise ValueError("job ID must be an explicit platform job identifier")
        if not isinstance(run_id, str) or not RUN_PATTERN.fullmatch(run_id):
            raise ValueError("run ID must be exactly 32 lowercase hexadecimal characters")
        return f"/api/v1/jobs/{job_id}/mlflow/runs/{run_id}"

    def list_runs(self, limit: int = 100) -> dict:
        """Return the recent authorized window, never an exhaustive inventory."""
        if type(limit) is not int or not 1 <= limit <= 100:
            raise ValueError("limit must be an integer from 1 to 100")
        data, _ = self._request("GET", f"/api/v1/experiments?limit={limit}")
        return data

    def read_run(self, job_id: str, run_id: str) -> dict:
        data, request_id = self._request("GET", self._run_path(job_id, run_id))
        if not isinstance(data.get("run"), dict) or data["run"].get("id") != run_id:
            raise PlatformAPIError(200, "INVALID_RESPONSE", "Response did not match the requested run.", request_id)
        return data

    def log_batch(self, job_id: str, run_id: str, payload: dict) -> dict:
        """Write once to an existing RUNNING run. This operation is not atomic."""
        path = self._run_path(job_id, run_id) + "/log-batch"
        data, request_id = self._request("POST", path, _encode_batch(payload))
        if data.get("runId") != run_id or data.get("logged") is not True:
            raise PlatformAPIError(200, "INVALID_RESPONSE", "Write response was unexpected. Verify the run before retrying.", request_id)
        return data


def _read_payload(filename: str) -> dict:
    path = Path(filename)
    if not path.is_file():
        raise ValueError("payload must be an existing regular local file")
    with path.open("rb") as source:
        raw = source.read(MAX_BATCH_BYTES + 1)
    if len(raw) > MAX_BATCH_BYTES:
        raise ValueError("payload file exceeds 256 KiB")
    try:
        payload = _strict_json(raw)
    except (ValueError, UnicodeError, RecursionError):
        raise ValueError("payload must be strict JSON without duplicate fields or non-finite values") from None
    _encode_batch(payload)
    return payload


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True, help="HTTPS API origin, e.g. https://raytrain.wellspiking.ai")
    actions = parser.add_subparsers(dest="action", required=True)
    listing = actions.add_parser("list", help="read the recent run window (not a full inventory)")
    listing.add_argument("--limit", type=int, default=100)
    for action in ("read", "log-batch"):
        command = actions.add_parser(action, help="read an exact run" if action == "read" else "explicitly write one local JSON batch")
        command.add_argument("--job-id", required=True)
        command.add_argument("--run-id", required=True)
        if action == "log-batch":
            command.add_argument("--payload", required=True, help="path to an existing local JSON file (read only)")
    args = parser.parse_args(argv)
    credential = os.environ.get("RAYTRAIN_PAT", "")
    try:
        if not credential:
            raise ValueError("set RAYTRAIN_PAT using an authorized secret channel")
        payload = _read_payload(args.payload) if args.action == "log-batch" else None
        integration = RayTrainMLflowClient(args.base_url, credential)
        if args.action == "list":
            result = {"data": integration.list_runs(args.limit), "complete": False, "window_limit": args.limit, "notice": WINDOW_NOTICE}
        elif args.action == "read":
            result = integration.read_run(args.job_id, args.run_id)
        else:
            result = integration.log_batch(args.job_id, args.run_id, payload)
        print(json.dumps(result, ensure_ascii=False, allow_nan=False, indent=2).replace(credential, "[REDACTED]"))
        return 0
    except PlatformAPIError as error:
        print(str(error).replace(credential, "[REDACTED]") if credential else str(error), file=sys.stderr)
    except ValueError as error:
        print(str(error).replace(credential, "[REDACTED]") if credential else str(error), file=sys.stderr)
    except OSError:
        print("Could not read the local payload file.", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
