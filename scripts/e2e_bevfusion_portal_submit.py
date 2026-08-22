#!/usr/bin/env python3
"""Submit a real BEVFusion acceptance job through the Portal API contract.

This client intentionally uses only the Python 3 standard library. Workspace
uploads and snapshots are interactive-session endpoints: a normal platform PAT
is not sufficient for this workflow.
"""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import hashlib
import json
import mimetypes
import os
from pathlib import Path, PurePosixPath
import re
import shlex
import stat
import subprocess
import sys
import time
from typing import Any, BinaryIO, Iterable, Mapping, Protocol, TextIO
import urllib.error
import urllib.parse
import urllib.request


DEFAULT_API_URL = "https://raytrain.wellspiking.ai"
DEFAULT_TIMEOUT_SECONDS = 7200
DEFAULT_REQUEST_TIMEOUT_SECONDS = 600
POLL_INTERVAL_SECONDS = 5
SUCCESS_TTL_SECONDS = 60
FAILURE_TTL_SECONDS = 86400
MAX_RESPONSE_BYTES = 1 << 20
TERMINAL_STATES = frozenset({"SUCCEEDED", "FAILED", "CANCELED", "TIMED_OUT"})
INTERACTIVE_LOGIN_COMMAND = (
    "spk-rayjob login --username guofeng.su --password-stdin "
    "--config <dedicated-path>"
)
INTERACTIVE_LOGIN_ERROR = (
    "Portal workspace access requires an interactive session token, not a "
    f"platform PAT. Create a dedicated config with: {INTERACTIVE_LOGIN_COMMAND}"
)
IMAGE_PATTERN = re.compile(r"^[^@\s]+@sha256:[0-9a-fA-F]{64}$")
IDENTIFIER_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
MEMORY_PATTERN = re.compile(r"^[1-9][0-9]*(?:Ki|Mi|Gi|Ti)$")


class AcceptanceError(Exception):
    """A deliberately credential-free error suitable for terminal output."""


class HTTPStatusError(Exception):
    """A sanitized HTTP failure that never retains a URL or response body."""

    def __init__(self, status: int):
        super().__init__(f"HTTP {status}")
        self.status = status


class HTTPClient(Protocol):
    def request(
        self,
        method: str,
        url: str,
        *,
        headers: Mapping[str, str] | None = None,
        json_body: Mapping[str, Any] | None = None,
        body: BinaryIO | None = None,
        timeout: int | float | None = None,
    ) -> Any:
        """Send one HTTP request and return decoded JSON or response bytes."""


@dataclass(frozen=True)
class SourceFile:
    absolute_path: Path
    relative_path: str
    size_bytes: int
    mode: int
    device: int
    inode: int
    modified_ns: int
    sha256: str


@dataclass(frozen=True)
class JobOptions:
    name: str
    image: str
    entrypoint: str
    input_path: str
    output_path: str
    workers: int = 2
    gpus_per_worker: int = 8
    cpu_per_worker: int = 32
    memory_per_worker: str = "128Gi"
    execution_mode: str = "ray_train"
    timeout: int = DEFAULT_TIMEOUT_SECONDS


class UrllibHTTPClient:
    """Small urllib transport whose errors retain no credentials or URLs."""

    def request(
        self,
        method: str,
        url: str,
        *,
        headers: Mapping[str, str] | None = None,
        json_body: Mapping[str, Any] | None = None,
        body: BinaryIO | None = None,
        timeout: int | float | None = None,
    ) -> Any:
        if json_body is not None and body is not None:
            raise AcceptanceError("HTTP request cannot contain two bodies")
        payload: bytes | BinaryIO | None = body
        request_headers = dict(headers or {})
        if json_body is not None:
            payload = json.dumps(json_body, separators=(",", ":")).encode("utf-8")
            request_headers.setdefault("Content-Type", "application/json")
        request = urllib.request.Request(
            url,
            data=payload,  # type: ignore[arg-type]
            headers=request_headers,
            method=method,
        )
        try:
            with urllib.request.urlopen(
                request, timeout=timeout or DEFAULT_REQUEST_TIMEOUT_SECONDS
            ) as response:
                contents = response.read(MAX_RESPONSE_BYTES + 1)
                if len(contents) > MAX_RESPONSE_BYTES:
                    raise AcceptanceError("HTTP response exceeded the safe size limit")
                content_type = response.headers.get_content_type()
                if content_type == "application/json" or content_type.endswith("+json"):
                    try:
                        return json.loads(contents) if contents else {}
                    except (UnicodeDecodeError, json.JSONDecodeError):
                        raise AcceptanceError("HTTP response contained invalid JSON") from None
                return contents
        except urllib.error.HTTPError as error:
            if error.fp is not None:
                error.close()
            raise HTTPStatusError(error.code) from None
        except urllib.error.URLError:
            raise AcceptanceError("network request failed") from None
        except TimeoutError:
            raise AcceptanceError("network request timed out") from None


def _safe_path_text(value: str) -> str:
    if not value or value.startswith("/") or "\\" in value or "\x00" in value:
        raise AcceptanceError("source path must be a safe relative path")
    if any(ord(character) < 32 or ord(character) == 127 for character in value):
        raise AcceptanceError("source path contains control characters")
    parts = value.split("/")
    if any(part in {"", ".", ".."} for part in parts):
        raise AcceptanceError("source path must not contain empty, dot, or parent segments")
    path = PurePosixPath(value)
    if path.is_absolute():
        raise AcceptanceError("source path must be relative")
    return path.as_posix()


def _source_file(root: Path, relative_path: str) -> SourceFile:
    normalized = _safe_path_text(relative_path)
    absolute_path = root.joinpath(*PurePosixPath(normalized).parts)
    try:
        metadata = absolute_path.lstat()
    except OSError:
        raise AcceptanceError(f"source file is unavailable: {normalized}") from None
    if stat.S_ISLNK(metadata.st_mode):
        raise AcceptanceError(f"source symlink is not allowed: {normalized}")
    if not stat.S_ISREG(metadata.st_mode):
        raise AcceptanceError(f"source is not a regular file: {normalized}")
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(absolute_path, flags)
        with os.fdopen(descriptor, "rb") as source_file:
            opened = os.fstat(source_file.fileno())
            if (
                not stat.S_ISREG(opened.st_mode)
                or opened.st_dev != metadata.st_dev
                or opened.st_ino != metadata.st_ino
                or opened.st_size != metadata.st_size
                or opened.st_mtime_ns != metadata.st_mtime_ns
            ):
                raise AcceptanceError(f"source changed during discovery: {normalized}")
            digest = hashlib.sha256()
            while chunk := source_file.read(1024 * 1024):
                digest.update(chunk)
            finished = os.fstat(source_file.fileno())
            if (
                finished.st_size != opened.st_size
                or finished.st_mtime_ns != opened.st_mtime_ns
            ):
                raise AcceptanceError(f"source changed during discovery: {normalized}")
    except AcceptanceError:
        raise
    except OSError:
        raise AcceptanceError(f"source file is unavailable: {normalized}") from None
    return SourceFile(
        absolute_path,
        normalized,
        metadata.st_size,
        metadata.st_mode,
        metadata.st_dev,
        metadata.st_ino,
        metadata.st_mtime_ns,
        digest.hexdigest(),
    )


def _git_paths(root: Path) -> list[str] | None:
    try:
        checkout = subprocess.run(
            ["git", "-C", str(root), "rev-parse", "--is-inside-work-tree"],
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
        )
    except OSError:
        raise AcceptanceError("git is required to inspect a Git checkout") from None
    if checkout.returncode != 0 or checkout.stdout.strip() != "true":
        return None
    try:
        listed = subprocess.run(
            [
                "git",
                "-C",
                str(root),
                "ls-files",
                "--cached",
                "--others",
                "--exclude-standard",
                "-z",
                "--",
            ],
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
        )
    except OSError:
        raise AcceptanceError("git could not enumerate source files") from None
    if listed.returncode != 0:
        raise AcceptanceError("git could not enumerate source files")
    return [os.fsdecode(item) for item in listed.stdout.split(b"\x00") if item]


def _recursive_paths(root: Path) -> list[str]:
    paths: list[str] = []
    for directory, directory_names, file_names in os.walk(root, topdown=True, followlinks=False):
        current = Path(directory)
        kept_directories: list[str] = []
        for name in sorted(directory_names):
            if name == ".git":
                continue
            relative = (current / name).relative_to(root).as_posix()
            try:
                mode = (current / name).lstat().st_mode
            except OSError:
                raise AcceptanceError(f"source directory is unavailable: {relative}") from None
            if stat.S_ISLNK(mode):
                raise AcceptanceError(f"source symlink is not allowed: {relative}")
            if not stat.S_ISDIR(mode):
                raise AcceptanceError(f"source entry is not a directory: {relative}")
            kept_directories.append(name)
        directory_names[:] = kept_directories
        for name in sorted(file_names):
            if name == ".git":
                continue
            paths.append((current / name).relative_to(root).as_posix())
    return paths


def discover_source_files(source_dir: str | os.PathLike[str]) -> list[SourceFile]:
    """Enumerate uploadable source files with Git's ignore semantics when available."""

    root = Path(source_dir).expanduser()
    try:
        root_metadata = root.lstat()
    except OSError:
        raise AcceptanceError("source directory does not exist") from None
    if stat.S_ISLNK(root_metadata.st_mode) or not stat.S_ISDIR(root_metadata.st_mode):
        raise AcceptanceError("source directory must be a real directory, not a symlink")
    root = root.resolve()
    relative_paths = _git_paths(root)
    if relative_paths is None:
        relative_paths = _recursive_paths(root)
    files = [_source_file(root, path) for path in sorted(set(relative_paths))]
    if not files:
        raise AcceptanceError("source directory contains no uploadable files")
    return files


def load_token(config_path: str | os.PathLike[str]) -> str:
    """Read an interactive session token from an owner-only spk-rayjob config."""

    path = Path(config_path).expanduser()
    try:
        metadata = path.lstat()
    except OSError:
        raise AcceptanceError("spk-rayjob config could not be read") from None
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise AcceptanceError("spk-rayjob config must be an owner-only regular file")
    if metadata.st_mode & 0o077:
        raise AcceptanceError("spk-rayjob config must be owner-only (mode 0600)")
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
        with os.fdopen(descriptor, "rb") as config_file:
            opened_metadata = os.fstat(config_file.fileno())
            if not stat.S_ISREG(opened_metadata.st_mode) or opened_metadata.st_mode & 0o077:
                raise AcceptanceError("spk-rayjob config must remain owner-only while read")
            contents = config_file.read(MAX_RESPONSE_BYTES + 1)
    except OSError:
        raise AcceptanceError("spk-rayjob config could not be read securely") from None
    if len(contents) > MAX_RESPONSE_BYTES:
        raise AcceptanceError("spk-rayjob config is too large")
    try:
        config = json.loads(contents)
    except (UnicodeDecodeError, json.JSONDecodeError):
        raise AcceptanceError("spk-rayjob config is invalid JSON") from None
    token = config.get("token") if isinstance(config, dict) else None
    if not isinstance(token, str) or not token.strip():
        raise AcceptanceError("spk-rayjob config has no interactive session token")
    return token.strip()


def sanitize_name(value: str) -> str:
    normalized = re.sub(r"[^a-z0-9]+", "-", value.strip().lower()).strip("-")
    normalized = normalized[:63].rstrip("-")
    if not normalized:
        raise AcceptanceError("job name must contain letters or digits")
    return normalized


def normalize_data_path(value: str, label: str) -> str:
    normalized = value.strip().rstrip("/")
    try:
        return _safe_path_text(normalized)
    except AcceptanceError:
        raise AcceptanceError(f"{label} must stay within its data space") from None


def _validate_job_options(options: JobOptions) -> tuple[str, list[str], str, str]:
    if not IMAGE_PATTERN.fullmatch(options.image.strip()):
        raise AcceptanceError("image must be pinned with @sha256:<64 hex characters>")
    if options.timeout < 1:
        raise AcceptanceError("timeout must be a positive integer")
    for label, value in (
        ("workers", options.workers),
        ("gpus per worker", options.gpus_per_worker),
        ("CPU per worker", options.cpu_per_worker),
    ):
        if not isinstance(value, int) or isinstance(value, bool) or value < 1:
            raise AcceptanceError(f"{label} must be a positive integer")
    if not MEMORY_PATTERN.fullmatch(options.memory_per_worker.strip()):
        raise AcceptanceError("memory per worker must be a positive Kubernetes quantity such as 128Gi")
    if options.execution_mode not in {"single_gpu", "torchrun", "ray_train"}:
        raise AcceptanceError("execution mode must be single_gpu, torchrun, or ray_train")
    if options.execution_mode == "single_gpu" and (
        options.workers != 1 or options.gpus_per_worker != 1
    ):
        raise AcceptanceError("single_gpu mode requires exactly 1 worker and 1 GPU")
    if options.execution_mode == "torchrun" and (
        options.workers != 1 or options.gpus_per_worker < 2
    ):
        raise AcceptanceError("torchrun mode requires 1 worker and at least 2 GPUs")
    if options.execution_mode == "ray_train" and options.workers < 2:
        raise AcceptanceError("ray_train mode requires at least 2 workers")
    try:
        entrypoint = shlex.split(options.entrypoint, posix=True)
    except ValueError:
        raise AcceptanceError("entrypoint contains invalid shell quoting") from None
    if not entrypoint:
        raise AcceptanceError("entrypoint must contain a command")
    return (
        sanitize_name(options.name),
        entrypoint,
        normalize_data_path(options.input_path, "input path"),
        normalize_data_path(options.output_path, "output path"),
    )


def build_job_payload(options: JobOptions, snapshot_id: str) -> dict[str, Any]:
    name, entrypoint, input_path, output_path = _validate_job_options(options)
    if not IDENTIFIER_PATTERN.fullmatch(snapshot_id):
        raise AcceptanceError("workspace snapshot ID is invalid")
    return {
        "spec": {
            "name": name,
            "image": options.image.strip(),
            "source": {"type": "workspace", "snapshot": snapshot_id},
            "entrypoint": {"command": [entrypoint[0]], "args": entrypoint[1:]},
            "execution": {"mode": options.execution_mode},
            "resources": {
                "workerReplicas": options.workers,
                "gpusPerWorker": options.gpus_per_worker,
                "cpuPerWorker": options.cpu_per_worker,
                "memoryPerWorker": options.memory_per_worker.strip(),
            },
            "queue": "",
            "input": {"space": "public", "relativePath": input_path},
            "checkpoint": {},
            "output": {"space": "my-runs", "relativePath": output_path},
            "timeoutSeconds": options.timeout,
            "retryPolicy": {"maxRetries": 0},
            "cleanupPolicy": {
                "successTtlSeconds": SUCCESS_TTL_SECONDS,
                "failureTtlSeconds": FAILURE_TTL_SECONDS,
            },
        }
    }


def _platform_origin(api_url: str) -> str:
    parsed = urllib.parse.urlsplit(api_url.strip())
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or parsed.path not in {"", "/"}
    ):
        raise AcceptanceError("api URL must be an HTTPS origin without credentials")
    return urllib.parse.urlunsplit((parsed.scheme, parsed.netloc, "", "", ""))


def _envelope_data(response: Any, operation: str) -> Any:
    if not isinstance(response, dict) or response.get("success") is not True or "data" not in response:
        raise AcceptanceError(f"{operation} returned an invalid platform response")
    return response["data"]


class PortalAcceptanceClient:
    def __init__(
        self,
        api_url: str,
        token: str,
        *,
        http_client: HTTPClient | None = None,
        output: TextIO | None = None,
        sleeper=time.sleep,
        monotonic=time.monotonic,
        request_timeout: int = DEFAULT_REQUEST_TIMEOUT_SECONDS,
    ):
        if not token.strip():
            raise AcceptanceError("interactive session token is required")
        self._origin = _platform_origin(api_url)
        self._token = token
        self._http = http_client or UrllibHTTPClient()
        self._output = output or sys.stdout
        self._sleep = sleeper
        self._monotonic = monotonic
        self._request_timeout = request_timeout

    def _redact(self, value: str) -> str:
        return value.replace(self._token, "[REDACTED]") if self._token else value

    def _write(self, message: str) -> None:
        print(self._redact(message), file=self._output, flush=True)

    def _platform_json(
        self,
        method: str,
        path: str,
        operation: str,
        *,
        json_body: Mapping[str, Any] | None = None,
        headers: Mapping[str, str] | None = None,
    ) -> Any:
        request_headers = {
            "Accept": "application/json",
            "Authorization": f"Bearer {self._token}",
            **dict(headers or {}),
        }
        try:
            response = self._http.request(
                method,
                self._origin + path,
                headers=request_headers,
                json_body=json_body,
                timeout=self._request_timeout,
            )
        except HTTPStatusError as error:
            if error.status in {401, 403}:
                raise AcceptanceError(INTERACTIVE_LOGIN_ERROR) from None
            raise AcceptanceError(f"{operation} failed (HTTP {error.status})") from None
        except AcceptanceError as error:
            raise AcceptanceError(f"{operation} failed: {self._redact(str(error))}") from None
        except Exception:
            raise AcceptanceError(f"{operation} failed due to a transport error") from None
        return _envelope_data(response, operation)

    def _open_verified(self, source: SourceFile) -> BinaryIO:
        flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
        try:
            descriptor = os.open(source.absolute_path, flags)
            metadata = os.fstat(descriptor)
            if (
                not stat.S_ISREG(metadata.st_mode)
                or metadata.st_size != source.size_bytes
                or metadata.st_dev != source.device
                or metadata.st_ino != source.inode
                or metadata.st_mtime_ns != source.modified_ns
            ):
                os.close(descriptor)
                raise AcceptanceError("source changed after discovery")
            source_file = os.fdopen(descriptor, "rb")
            digest = hashlib.sha256()
            while chunk := source_file.read(1024 * 1024):
                digest.update(chunk)
            finished = os.fstat(source_file.fileno())
            if (
                digest.hexdigest() != source.sha256
                or finished.st_size != metadata.st_size
                or finished.st_mtime_ns != metadata.st_mtime_ns
            ):
                source_file.close()
                raise AcceptanceError("source changed after discovery")
            source_file.seek(0)
            return source_file
        except AcceptanceError:
            raise
        except OSError:
            raise AcceptanceError("source could not be opened securely") from None

    def _upload_file(self, source: SourceFile, workspace_path: str) -> None:
        content_type = mimetypes.guess_type(source.relative_path)[0] or "application/octet-stream"
        upload = self._platform_json(
            "POST",
            "/api/v1/data-spaces/workspace/uploads",
            "workspace upload authorization",
            json_body={
                "path": f"{workspace_path}/{source.relative_path}",
                "contentType": content_type,
                "sizeBytes": source.size_bytes,
            },
        )
        if not isinstance(upload, dict):
            raise AcceptanceError("workspace upload authorization returned invalid data")
        signed_url = upload.get("url")
        required_headers = upload.get("requiredHeaders") or {}
        if not isinstance(signed_url, str) or not isinstance(required_headers, dict):
            raise AcceptanceError("workspace upload authorization returned invalid data")
        parsed_url = urllib.parse.urlsplit(signed_url)
        if parsed_url.scheme != "https" or not parsed_url.hostname or parsed_url.username is not None:
            raise AcceptanceError("workspace upload authorization returned an unsafe URL")
        upload_headers: dict[str, str] = {}
        for key, value in required_headers.items():
            if not isinstance(key, str) or not isinstance(value, str):
                raise AcceptanceError("workspace upload authorization returned invalid headers")
            if key.lower() == "authorization":
                raise AcceptanceError("workspace upload authorization requested a forbidden header")
            if any(character in key + value for character in "\r\n"):
                raise AcceptanceError("workspace upload authorization returned unsafe headers")
            upload_headers[key] = value
        if not any(key.lower() == "content-length" for key in upload_headers):
            upload_headers["Content-Length"] = str(source.size_bytes)
        try:
            with self._open_verified(source) as source_file:
                self._http.request(
                    "PUT",
                    signed_url,
                    headers=upload_headers,
                    body=source_file,
                    timeout=self._request_timeout,
                )
        except HTTPStatusError as error:
            raise AcceptanceError(f"object upload failed (HTTP {error.status})") from None
        except AcceptanceError:
            raise
        except Exception:
            raise AcceptanceError("object upload failed due to a transport error") from None

    def upload_sources(self, sources: Iterable[SourceFile], workspace_path: str) -> None:
        source_list = list(sources)
        if not source_list:
            raise AcceptanceError("source directory contains no uploadable files")
        workspace_path = normalize_data_path(workspace_path, "workspace path")
        total = len(source_list)
        for index, source in enumerate(source_list, start=1):
            try:
                self._upload_file(source, workspace_path)
            except AcceptanceError as error:
                relative_path = self._redact(source.relative_path)
                detail = self._redact(str(error))
                raise AcceptanceError(f"upload failed for {relative_path}: {detail}") from None
            self._write(
                f"UPLOAD {index}/{total} PATH={source.relative_path} BYTES={source.size_bytes}"
            )

    def create_snapshot(self, workspace_path: str) -> str:
        normalized_path = normalize_data_path(workspace_path, "workspace path")
        snapshot = self._platform_json(
            "POST",
            "/api/v1/workspace-snapshots",
            "workspace snapshot creation",
            json_body={"sourcePath": normalized_path},
        )
        snapshot_id = snapshot.get("id") if isinstance(snapshot, dict) else None
        if not isinstance(snapshot_id, str) or not IDENTIFIER_PATTERN.fullmatch(snapshot_id):
            raise AcceptanceError("workspace snapshot creation returned an invalid ID")
        self._write(f"SNAPSHOT_ID={snapshot_id}")
        return snapshot_id

    def create_job(self, payload: Mapping[str, Any]) -> str:
        spec = payload.get("spec")
        name = spec.get("name") if isinstance(spec, dict) else None
        if not isinstance(name, str):
            raise AcceptanceError("job payload has no valid name")
        job = self._platform_json(
            "POST",
            "/api/v1/jobs",
            "job submission",
            json_body=payload,
            headers={"Idempotency-Key": f"portal-acceptance-{name}"},
        )
        job_id = job.get("id") if isinstance(job, dict) else None
        if not isinstance(job_id, str) or not IDENTIFIER_PATTERN.fullmatch(job_id):
            raise AcceptanceError("job submission returned an invalid ID")
        self._write(f"JOB_ID={job_id}")
        return job_id

    @staticmethod
    def _time_fields(detail: Mapping[str, Any]) -> str:
        fields = []
        for label, key in (
            ("CREATED_AT", "createdAt"),
            ("STARTED_AT", "startedAt"),
            ("FINISHED_AT", "finishedAt"),
        ):
            value = detail.get(key)
            rendered = str(value) if isinstance(value, str) and value else "-"
            rendered = "".join(
                character if 32 <= ord(character) < 127 else "?" for character in rendered
            )[:128]
            fields.append(f"{label}={rendered}")
        return " ".join(fields)

    def poll_job(self, job_id: str, timeout: int) -> Mapping[str, Any]:
        if not IDENTIFIER_PATTERN.fullmatch(job_id) or timeout < 1:
            raise AcceptanceError("job polling parameters are invalid")
        deadline = self._monotonic() + timeout
        last_state = None
        while True:
            detail = self._platform_json(
                "GET",
                f"/api/v1/jobs/{urllib.parse.quote(job_id, safe='')}",
                "job status request",
            )
            if not isinstance(detail, dict):
                raise AcceptanceError("job status request returned invalid data")
            state = detail.get("observedState")
            if not isinstance(state, str) or not state:
                raise AcceptanceError("job status request returned no state")
            if state != last_state:
                self._write(f"STATE={state} {self._time_fields(detail)}")
                last_state = state
            if state in TERMINAL_STATES:
                if state != "SUCCEEDED":
                    raise AcceptanceError(f"job {job_id} ended in {state}")
                return detail
            if self._monotonic() >= deadline:
                raise AcceptanceError(
                    f"job {job_id} did not finish within {timeout}s (last state {state})"
                )
            self._sleep(POLL_INTERVAL_SECONDS)

    def write_final_summary(
        self, job_id: str, snapshot_id: str, detail: Mapping[str, Any]
    ) -> None:
        state = detail.get("observedState")
        if not isinstance(state, str):
            state = "UNKNOWN"
        self._write(
            f"JOB_ID={job_id} SNAPSHOT_ID={snapshot_id} STATE={state} "
            f"{self._time_fields(detail)}"
        )


def _positive_integer(value: str) -> int:
    try:
        parsed = int(value)
    except ValueError:
        raise argparse.ArgumentTypeError("must be a positive integer") from None
    if parsed < 1:
        raise argparse.ArgumentTypeError("must be a positive integer")
    return parsed


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description=(
            "Upload a complete source checkout, snapshot it, submit a governed "
            "BEVFusion job, and wait for its terminal state."
        ),
        epilog=(
            "Authentication:\n"
            "  This Portal/API acceptance requires an interactive session token; a PAT will fail.\n"
            f"  {INTERACTIVE_LOGIN_COMMAND}\n"
            "  Short-lived PATs are only for spk-rayjob/native submission flows.\n"
            "  Use a dedicated config path so the two authentication flows remain separate."
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("--source-dir", required=True, help="source checkout or directory")
    parser.add_argument(
        "--config", required=True, help="owner-only dedicated interactive-session config JSON"
    )
    parser.add_argument("--api-url", default=DEFAULT_API_URL, help="platform HTTPS origin")
    parser.add_argument("--image", required=True, help="immutable image reference using @sha256")
    parser.add_argument("--name", required=True, help="job and acceptance workspace name")
    parser.add_argument("--entrypoint", required=True, help="command parsed with shlex, never a shell")
    parser.add_argument("--input-path", required=True, help="path within the public data space")
    parser.add_argument("--output-path", required=True, help="path within the my-runs data space")
    parser.add_argument("--workers", type=_positive_integer, default=2)
    parser.add_argument("--gpus-per-worker", type=_positive_integer, default=8)
    parser.add_argument("--cpu-per-worker", type=_positive_integer, default=32)
    parser.add_argument("--memory-per-worker", default="128Gi")
    parser.add_argument(
        "--execution-mode",
        choices=("single_gpu", "torchrun", "ray_train"),
        default="ray_train",
    )
    parser.add_argument("--timeout", type=_positive_integer, default=DEFAULT_TIMEOUT_SECONDS)
    return parser


def run(args: argparse.Namespace, *, http_client: HTTPClient | None = None) -> int:
    token = load_token(args.config)
    try:
        files = discover_source_files(args.source_dir)
        options = JobOptions(
            name=args.name,
            image=args.image,
            entrypoint=args.entrypoint,
            input_path=args.input_path,
            output_path=args.output_path,
            workers=args.workers,
            gpus_per_worker=args.gpus_per_worker,
            cpu_per_worker=args.cpu_per_worker,
            memory_per_worker=args.memory_per_worker,
            execution_mode=args.execution_mode,
            timeout=args.timeout,
        )
        sanitized_name, _, _, _ = _validate_job_options(options)
        workspace_path = f"acceptance/{sanitized_name}"
        client = PortalAcceptanceClient(
            args.api_url, token, http_client=http_client, request_timeout=DEFAULT_REQUEST_TIMEOUT_SECONDS
        )
        client.upload_sources(files, workspace_path)
        snapshot_id = client.create_snapshot(workspace_path)
        payload = build_job_payload(options, snapshot_id)
        job_id = client.create_job(payload)
        detail = client.poll_job(job_id, args.timeout)
        client.write_final_summary(job_id, snapshot_id, detail)
        return 0
    except AcceptanceError as error:
        raise AcceptanceError(str(error).replace(token, "[REDACTED]")) from None


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return run(args)
    except AcceptanceError as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 1
    except Exception:
        print("ERROR: unexpected failure; credentials and response details were suppressed", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
