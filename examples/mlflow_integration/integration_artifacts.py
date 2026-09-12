"""Bounded REST artifacts for an existing external platform Run (Python 3.10+).

Credentials come only from RAYTRAIN_PAT. No MLflow SDK artifact calls, automatic
retries, training submissions, implicit Run finish or credential management.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import stat
import sys
import tempfile
import urllib.error
import urllib.request
from pathlib import Path

try:
    from .client import RayTrainMLflowClient, PlatformAPIError, MAX_RESPONSE_BYTES
except ImportError:
    from client import RayTrainMLflowClient, PlatformAPIError, MAX_RESPONSE_BYTES

PART_BYTES = 8 * 1024 * 1024
MAX_BYTES = 20 * 1024 * 1024 * 1024
# A maximum-sized artifact includes 2,560 part hashes. Keep listing responses
# within the bounded JSON reader even when every item is a full 20 GiB file.
LIST_LIMIT = 20
ID = re.compile(r"[0-9a-f]{32}\Z")
SHA = re.compile(r"[0-9a-f]{64}\Z")
KEY = re.compile(r"[A-Za-z0-9._:-]{8,128}\Z")


def require(value, message):
    if not value:
        raise ValueError(message)


def digest_file(path):
    total, digest = 0, hashlib.sha256()
    with path.open("rb") as source:
        require(stat.S_ISREG(os.fstat(source.fileno()).st_mode), "input must be a regular file")
        while chunk := source.read(PART_BYTES):
            total += len(chunk)
            require(total <= MAX_BYTES, "file exceeds 20 GiB")
            digest.update(chunk)
    require(total > 0, "empty files are not supported")
    return total, digest.hexdigest()


class ArtifactClient(RayTrainMLflowClient):
    def call(self, method, path, body=None, *, key=None, part_sha=None, timeout=60):
        headers = {"Authorization": "Bearer " + self._credential, "Accept": "application/json"}
        if part_sha is not None:
            headers.update({"Content-Type": "application/octet-stream", "X-Content-SHA256": part_sha})
            encoded = body
        else:
            headers["Content-Type"] = "application/json"
            encoded = None if body is None else json.dumps(body, allow_nan=False).encode()
        if key is not None:
            headers["Idempotency-Key"] = key
        request = urllib.request.Request(self._base_url + path, data=encoded, method=method, headers=headers)
        try:
            try:
                response = self._opener.open(request, timeout=timeout)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                status, raw = response.code, response.read(MAX_RESPONSE_BYTES + 1)
                request_id = response.headers.get("X-Request-ID", "")
        except (OSError, urllib.error.URLError):
            raise ValueError("Network outcome unknown. Read status before retrying; preserve receipt, file, key and hashes.") from None
        require(not 300 <= status < 400, "redirect refused: use the approved HTTPS origin directly")
        require(len(raw) <= MAX_RESPONSE_BYTES, "response exceeded size limit; inspect status before retry")
        try:
            payload = json.loads(raw)
        except (ValueError, UnicodeError):
            raise ValueError("invalid JSON response; inspect status before retry") from None
        require(isinstance(payload, dict), "invalid response envelope")
        request_id = self._safe_text(payload.get("request_id", request_id), "", 128)
        if not 200 <= status < 300 or payload.get("success") is not True:
            error = payload.get("error") or {}
            code = self._safe_text(error.get("code") if isinstance(error, dict) else None, "REQUEST_FAILED", 128)
            raise PlatformAPIError(status, code, "Request failed; read status before retrying the same operation.", request_id)
        require(isinstance(payload.get("data"), dict), "response data missing")
        return payload["data"]

    def download(self, path, artifact, destination):
        require(artifact.get("state") == "READY", "only READY artifacts can be downloaded")
        require(SHA.fullmatch(artifact.get("sha256", "")), "invalid artifact SHA256")
        require(type(artifact.get("sizeBytes")) is int and 0 < artifact["sizeBytes"] <= MAX_BYTES, "invalid artifact size")
        request = urllib.request.Request(self._base_url + path + "/content", headers={"Authorization": "Bearer " + self._credential})
        # Exclusive creation never follows an existing symlink or overwrites a file.
        fd = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        created = os.fstat(fd)
        try:
            with os.fdopen(fd, "wb") as target:
                try:
                    response = self._opener.open(request, timeout=960)
                except urllib.error.HTTPError as error:
                    error.close()
                    raise PlatformAPIError(error.code, "DOWNLOAD_FAILED", "Download failed; inspect artifact status before retrying.") from None
                with response:
                    require(response.code == 200, "download response must be HTTP 200")
                    total, digest = 0, hashlib.sha256()
                    while chunk := response.read(1024 * 1024):
                        total += len(chunk)
                        require(total <= artifact["sizeBytes"], "download exceeded declared size")
                        digest.update(chunk)
                        target.write(chunk)
                    require(total == artifact["sizeBytes"] and digest.hexdigest() == artifact["sha256"], "download size/SHA256 mismatch")
                target.flush()
                os.fsync(target.fileno())
        except Exception:
            # A concurrent replacement is somebody else's file; never remove it.
            try:
                current = os.lstat(destination)
                if (current.st_dev, current.st_ino) == (created.st_dev, created.st_ino):
                    Path(destination).unlink()
            except FileNotFoundError:
                pass
            raise
        return {"artifactId": artifact["id"], "sizeBytes": total, "sha256": digest.hexdigest(), "verified": True}


def save_receipt(path, payload, *, new=False):
    encoded = json.dumps(payload, ensure_ascii=False, indent=2).encode()
    if new:
        fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        with os.fdopen(fd, "wb") as target:
            target.write(encoded)
            target.flush()
            os.fsync(target.fileno())
        return
    descriptor, temporary = tempfile.mkstemp(prefix=".artifact-receipt-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as target:
            target.write(encoded)
            target.flush()
            os.fsync(target.fileno())
        os.replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)


def checked_artifact(data, receipt):
    require(ID.fullmatch(data.get("id", "")), "invalid artifact ID")
    for key in ("runId", "name", "sizeBytes", "sha256"):
        require(data.get(key) == receipt[key], "artifact metadata differs from receipt: " + key)
    require(data.get("partSizeBytes") == PART_BYTES, "unexpected part size")
    require(data.get("totalParts") == (receipt["sizeBytes"] + PART_BYTES - 1) // PART_BYTES, "unexpected part count")
    return data


def upload(client, args):
    source, receipt_path = Path(args.file), Path(args.receipt)
    require(KEY.fullmatch(args.idempotency_key), "key must be 8–128 ASCII letters, digits, . _ : or -")
    size, sha = digest_file(source)
    receipt = {"origin": client._base_url, "runId": args.run_id, "name": source.name,
               "sizeBytes": size, "sha256": sha, "idempotencyKey": args.idempotency_key, "artifactId": None}
    path = "/api/v1/mlflow/runs/" + args.run_id + "/artifacts"
    if args.resume:
        require(receipt_path.is_file() and not receipt_path.is_symlink(), "resume requires a regular receipt file")
        previous = json.loads(receipt_path.read_text())
        require(all(previous.get(k) == v for k, v in receipt.items() if k != "artifactId"), "resume file/key/origin/Run differs from receipt")
        receipt = previous
        require(receipt.get("artifactId") and ID.fullmatch(receipt["artifactId"]),
                "initialization outcome unknown: list artifacts and verify before explicitly repeating init with the original key")
        artifact = client.call("GET", path + "/" + receipt["artifactId"])
    else:
        save_receipt(receipt_path, receipt, new=True)
        artifact = client.call("POST", path, {k: receipt[k] for k in ("name", "sizeBytes", "sha256")}, key=args.idempotency_key)
        checked_artifact(artifact, receipt)
        receipt = {**receipt, "artifactId": artifact["id"]}
        save_receipt(receipt_path, receipt)
    artifact = checked_artifact(artifact, receipt)
    path += "/" + artifact["id"]
    if artifact["state"] == "READY":
        return {"artifactId": artifact["id"], "state": "READY", "sha256": sha}
    require(artifact["state"] == "PENDING", "artifact is not uploadable; do not replace its key blindly")
    parts = artifact.get("uploadedParts", [])
    require(isinstance(parts, list), "invalid uploadedParts")
    require(all(isinstance(part, dict) and type(part.get("index")) is int and
                1 <= part["index"] <= artifact["totalParts"] for part in parts), "invalid uploaded part index")
    uploaded = {part["index"]: part for part in parts}
    require(len(uploaded) == len(parts), "duplicate uploaded part index")
    whole, total = hashlib.sha256(), 0
    with source.open("rb") as stream:
        for index in range(1, artifact["totalParts"] + 1):
            chunk = stream.read(PART_BYTES)
            expected = min(PART_BYTES, size - (index - 1) * PART_BYTES)
            require(len(chunk) == expected, "source changed size while uploading")
            part_sha = hashlib.sha256(chunk).hexdigest()
            total += len(chunk)
            whole.update(chunk)
            expected_part = {"index": index, "sizeBytes": len(chunk), "sha256": part_sha}
            if index in uploaded:
                require(uploaded[index] == expected_part, "uploaded part differs from local file")
            else:
                client.call("PUT", path + "/parts/" + str(index), chunk, part_sha=part_sha)
        require(stream.read(1) == b"" and total == size and whole.hexdigest() == sha, "source changed while uploading; completion refused")
    result = checked_artifact(client.call("POST", path + "/complete", {}, timeout=960), receipt)
    require(result["state"] == "READY", "completion not confirmed; read status before retry")
    return {"artifactId": result["id"], "state": result["state"], "sizeBytes": size, "sha256": sha}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    uploading = commands.add_parser("upload", help="initialize/upload/complete one authorized external-Run file")
    uploading.add_argument("--file", required=True)
    uploading.add_argument("--receipt", required=True, help="0600 receipt; contains no credentials")
    uploading.add_argument("--idempotency-key", required=True)
    uploading.add_argument("--resume", action="store_true", help="read receipt and server status before sending only missing parts")
    for name in ("list", "status", "download", "cancel"):
        command = commands.add_parser(name)
        if name == "list":
            command.add_argument("--cursor", help="previous same-Run nextCursor, without modification")
        if name != "list":
            command.add_argument("--artifact-id", required=True)
        if name == "download":
            command.add_argument("--output", required=True, help="new destination path; existing files are never overwritten")
    for command in commands.choices.values():
        command.add_argument("--run-id", required=True, help="existing external platform Run ID, not job_id/upstream ID")
    args = parser.parse_args(argv)
    token = os.environ.get("RAYTRAIN_PAT", "")
    try:
        require(ID.fullmatch(args.run_id), "invalid external platform Run ID")
        client = ArtifactClient(os.environ.get("RAYTRAIN_API", ""), token)
        path = "/api/v1/mlflow/runs/" + args.run_id + "/artifacts"
        if args.command == "upload":
            result = upload(client, args)
        elif args.command == "list":
            require(not args.cursor or ID.fullmatch(args.cursor), "invalid artifact list cursor")
            query = "?limit=" + str(LIST_LIMIT) + ("&cursor=" + args.cursor if args.cursor else "")
            result = client.call("GET", path + query)
        else:
            require(ID.fullmatch(args.artifact_id), "invalid artifact ID")
            path += "/" + args.artifact_id
            if args.command == "cancel":
                result = client.call("DELETE", path)
            else:
                result = client.call("GET", path)
                require(result.get("id") == args.artifact_id and result.get("runId") == args.run_id,
                        "artifact response does not match requested Run and artifact")
                if args.command == "download":
                    result = client.download(path, result, args.output)
        print(json.dumps(result, ensure_ascii=False).replace(token, "[REDACTED]"))
        return 0
    except (ValueError, PlatformAPIError) as error:
        print(str(error).replace(token, "[REDACTED]") if token else str(error), file=sys.stderr)
    except Exception:
        print("Operation failed; read status before retrying. No automatic retry; existing files were not overwritten.", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
