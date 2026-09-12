"""Transport-mocked artifact client acceptance. Execute on the builder only."""
import argparse
import contextlib
import copy
import hashlib
import io
import json
import os
from pathlib import Path
import stat
import tempfile
import unittest
from unittest.mock import patch
import urllib.error

try:
    from . import integration_artifacts as subject
except ImportError:
    import integration_artifacts as subject

TOKEN = "rt_test_DO_NOT_PRINT_credential"
ORIGIN = "https://raytrain.example.test"
RUN_ID = "1" * 32
ARTIFACT_ID = "2" * 32
KEY = "acceptance-artifact-v1"


class Response(io.BytesIO):
    def __init__(self, content, status=200, headers=None):
        super().__init__(content)
        self.code = status
        self.headers = headers or {}


class Opener:
    def __init__(self, result):
        self.result = result
        self.calls = []

    def open(self, request, timeout):
        self.calls.append((request, timeout))
        if isinstance(self.result, Exception):
            raise self.result
        return self.result


class UploadAPI:
    """Only our dedicated Run is accepted; each call retains its exact bytes."""
    _base_url = ORIGIN

    def __init__(self, data, name, uploaded=()):
        self.calls = []
        self.failure = None
        self.artifact = {
            "id": ARTIFACT_ID, "runId": RUN_ID, "name": name,
            "sizeBytes": len(data), "sha256": hashlib.sha256(data).hexdigest(),
            "state": "PENDING", "partSizeBytes": subject.PART_BYTES,
            "totalParts": (len(data) + subject.PART_BYTES - 1) // subject.PART_BYTES,
            "uploadedParts": list(uploaded),
        }

    def call(self, method, path, body=None, **kwargs):
        self.calls.append((method, path, body, kwargs))
        if self.failure:
            raise self.failure
        if method == "PUT":
            index = int(path.rsplit("/", 1)[1])
            self.artifact["uploadedParts"].append({"index": index, "sizeBytes": len(body), "sha256": kwargs["part_sha"]})
        elif path.endswith("/complete"):
            self.artifact["state"] = "READY"
        return copy.deepcopy(self.artifact)


class ArtifactClientTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "checkpoint.pt"
        self.receipt = self.root / "upload.json"
        self.output = self.root / "download.pt"

    def args(self, resume=False):
        return argparse.Namespace(file=str(self.source), receipt=str(self.receipt),
                                  idempotency_key=KEY, run_id=RUN_ID, resume=resume)

    def receipt_payload(self, data):
        return {"origin": ORIGIN, "runId": RUN_ID, "name": self.source.name,
                "sizeBytes": len(data), "sha256": hashlib.sha256(data).hexdigest(),
                "idempotencyKey": KEY, "artifactId": ARTIFACT_ID}

    def real_client(self, response):
        client = subject.ArtifactClient(ORIGIN, TOKEN)
        client._opener = Opener(response)
        return client

    def metadata(self, content):
        return {"id": ARTIFACT_ID, "runId": RUN_ID, "state": "READY",
                "sizeBytes": len(content), "sha256": hashlib.sha256(content).hexdigest()}

    def test_upload_streams_fixed_parts_then_completes_and_receipt_is_private(self):
        content = b"a" * subject.PART_BYTES + b"end"
        self.source.write_bytes(content)
        api = UploadAPI(content, self.source.name)
        result = subject.upload(api, self.args())
        self.assertEqual(result["state"], "READY")
        self.assertEqual([item[0] for item in api.calls], ["POST", "PUT", "PUT", "POST"])
        self.assertEqual(api.calls[0][3]["key"], KEY)
        puts = [item for item in api.calls if item[0] == "PUT"]
        self.assertEqual([len(item[2]) for item in puts], [subject.PART_BYTES, 3])
        self.assertTrue(puts[0][1].endswith("/parts/1"))
        self.assertTrue(puts[1][1].endswith("/parts/2"))
        for item in puts:
            self.assertEqual(item[3]["part_sha"], hashlib.sha256(item[2]).hexdigest())
        self.assertEqual(api.calls[-1][3]["timeout"], 960)
        receipt = json.loads(self.receipt.read_text())
        self.assertEqual(receipt, self.receipt_payload(content))
        self.assertEqual(stat.S_IMODE(self.receipt.stat().st_mode), 0o600)
        self.assertNotIn(TOKEN, self.receipt.read_text())
        self.assertNotIn("token", receipt)
        self.assertNotIn("pat", receipt)

    def test_resume_reads_status_skips_verified_first_part_and_never_reinitializes(self):
        content = b"b" * subject.PART_BYTES + b"tail"
        self.source.write_bytes(content)
        subject.save_receipt(self.receipt, self.receipt_payload(content), new=True)
        first = {"index": 1, "sizeBytes": subject.PART_BYTES, "sha256": hashlib.sha256(content[:subject.PART_BYTES]).hexdigest()}
        api = UploadAPI(content, self.source.name, [first])
        subject.upload(api, self.args(resume=True))
        self.assertEqual([item[0] for item in api.calls], ["GET", "PUT", "POST"])
        self.assertTrue(api.calls[1][1].endswith("/parts/2"))
        self.assertEqual(api.calls[1][2], b"tail")

    def test_resume_rejects_changed_file_before_any_network(self):
        self.source.write_bytes(b"changed")
        subject.save_receipt(self.receipt, self.receipt_payload(b"original"), new=True)
        api = UploadAPI(b"original", self.source.name)
        with self.assertRaisesRegex(ValueError, "differs from receipt"):
            subject.upload(api, self.args(resume=True))
        self.assertEqual(api.calls, [])

    def test_resume_rejects_remote_part_hash_mismatch_without_upload_or_complete(self):
        content = b"abc"
        self.source.write_bytes(content)
        subject.save_receipt(self.receipt, self.receipt_payload(content), new=True)
        api = UploadAPI(content, self.source.name, [{"index": 1, "sizeBytes": 3, "sha256": "0" * 64}])
        with self.assertRaisesRegex(ValueError, "differs from local file"):
            subject.upload(api, self.args(resume=True))
        self.assertEqual([item[0] for item in api.calls], ["GET"])

    def test_ready_resume_does_not_send_writes(self):
        content = b"abc"
        self.source.write_bytes(content)
        subject.save_receipt(self.receipt, self.receipt_payload(content), new=True)
        api = UploadAPI(content, self.source.name)
        api.artifact["state"] = "READY"
        self.assertEqual(subject.upload(api, self.args(True))["state"], "READY")
        self.assertEqual([item[0] for item in api.calls], ["GET"])

    def test_uncertain_init_is_not_retried_and_receipt_retains_original_key(self):
        self.source.write_bytes(b"abc")
        api = UploadAPI(b"abc", self.source.name)
        api.failure = ValueError("Network outcome unknown")
        with self.assertRaisesRegex(ValueError, "outcome unknown"):
            subject.upload(api, self.args())
        self.assertEqual(len(api.calls), 1)
        receipt = json.loads(self.receipt.read_text())
        self.assertEqual(receipt["idempotencyKey"], KEY)
        self.assertIsNone(receipt["artifactId"])
        api.calls.clear()
        with self.assertRaisesRegex(ValueError, "initialization outcome unknown"):
            subject.upload(api, self.args(True))
        self.assertEqual(api.calls, [])

    def test_transport_network_error_is_single_attempt_and_does_not_expose_details(self):
        client = self.real_client(urllib.error.URLError("Authorization: Bearer " + TOKEN))
        with self.assertRaises(ValueError) as raised:
            client.call("PUT", "/api/v1/mlflow/runs/" + RUN_ID + "/artifacts/" + ARTIFACT_ID + "/parts/1", b"abc", part_sha=hashlib.sha256(b"abc").hexdigest())
        self.assertEqual(len(client._opener.calls), 1)
        self.assertNotIn(TOKEN, str(raised.exception))
        request = client._opener.calls[0][0]
        self.assertEqual(request.get_header("Authorization"), "Bearer " + TOKEN)
        self.assertEqual(request.get_header("Content-type"), "application/octet-stream")
        self.assertEqual(request.data, b"abc")

    def test_error_envelope_does_not_leak_raw_payload_or_credentials(self):
        body = json.dumps({"success": False, "error": {"code": "DENIED", "message": TOKEN}, "request_id": TOKEN}).encode()
        client = self.real_client(Response(body, 403))
        with self.assertRaises(subject.PlatformAPIError) as raised:
            client.call("POST", "/api/v1/mlflow/runs/" + RUN_ID + "/artifacts", {})
        self.assertEqual(raised.exception.status, 403)
        self.assertNotIn(TOKEN, str(raised.exception))
        self.assertNotIn(TOKEN, raised.exception.request_id)
        self.assertEqual(len(client._opener.calls), 1)

    def test_redirect_never_gets_followed(self):
        client = self.real_client(Response(b"not json", 302, {"Location": "https://attacker.invalid/"}))
        with self.assertRaisesRegex(ValueError, "redirect refused"):
            client.call("GET", "/api/v1/mlflow/runs/" + RUN_ID + "/artifacts")
        self.assertEqual(len(client._opener.calls), 1)
        # The configured production opener uses the shared no-redirect handler.
        actual = subject.ArtifactClient(ORIGIN, TOKEN)
        handlers = [handler for handler in actual._opener.handlers if isinstance(handler, subject.urllib.request.HTTPRedirectHandler)]
        self.assertEqual(len(handlers), 1)
        self.assertIsNone(handlers[0].redirect_request(None, None, 302, "", {}, "https://attacker.invalid/"))

    def test_download_verifies_file_and_creates_private_output(self):
        content = b"download-body\x00"
        client = self.real_client(Response(content))
        result = client.download("/artifact", self.metadata(content), self.output)
        self.assertTrue(result["verified"])
        self.assertEqual(self.output.read_bytes(), content)
        self.assertEqual(stat.S_IMODE(self.output.stat().st_mode), 0o600)
        self.assertEqual(client._opener.calls[0][1], 960)

    def test_download_never_overwrites_existing_file(self):
        self.output.write_bytes(b"existing-user-file")
        client = self.real_client(Response(b"new"))
        with self.assertRaises(FileExistsError):
            client.download("/artifact", self.metadata(b"new"), self.output)
        self.assertEqual(self.output.read_bytes(), b"existing-user-file")
        self.assertEqual(client._opener.calls, [])

    def test_download_rejects_pending_before_creating_output(self):
        meta = self.metadata(b"abc")
        meta["state"] = "PENDING"
        client = self.real_client(Response(b"abc"))
        with self.assertRaisesRegex(ValueError, "only READY"):
            client.download("/artifact", meta, self.output)
        self.assertFalse(self.output.exists())
        self.assertEqual(client._opener.calls, [])

    def test_download_checksum_mismatch_removes_only_new_incomplete_file(self):
        client = self.real_client(Response(b"bad"))
        with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
            client.download("/artifact", self.metadata(b"abc"), self.output)
        self.assertFalse(self.output.exists())

    def test_download_truncation_and_oversize_both_fail(self):
        for content in (b"a", b"abcd"):
            with self.subTest(length=len(content)):
                client = self.real_client(Response(content))
                with self.assertRaises(ValueError):
                    client.download("/artifact", self.metadata(b"abc"), self.output)
                self.assertFalse(self.output.exists())

    def test_download_http_error_does_not_expose_response(self):
        response = urllib.error.HTTPError(ORIGIN, 403, TOKEN, {}, io.BytesIO(TOKEN.encode()))
        client = self.real_client(response)
        with self.assertRaises(subject.PlatformAPIError) as raised:
            client.download("/artifact", self.metadata(b"abc"), self.output)
        self.assertNotIn(TOKEN, str(raised.exception))
        self.assertFalse(self.output.exists())

    def test_cli_unexpected_exception_is_isolated_and_secret_free(self):
        stderr = io.StringIO()
        with patch.dict(os.environ, {"RAYTRAIN_PAT": TOKEN, "RAYTRAIN_API": ORIGIN}), patch.object(subject.ArtifactClient, "call", side_effect=RuntimeError(TOKEN)), contextlib.redirect_stderr(stderr):
            result = subject.main(["list", "--run-id", RUN_ID])
        self.assertEqual(result, 1)
        self.assertNotIn(TOKEN, stderr.getvalue())
        self.assertIn("No automatic retry", stderr.getvalue())

    def test_untrusted_origin_and_invalid_ids_are_rejected(self):
        for origin in ("http://raytrain.example.test", "https://user:secret@example.test", ORIGIN + "/mlflow", ORIGIN + "?token=bad"):
            with self.subTest(origin=origin), self.assertRaises(ValueError):
                subject.ArtifactClient(origin, TOKEN)
        stderr = io.StringIO()
        with patch.dict(os.environ, {"RAYTRAIN_PAT": TOKEN, "RAYTRAIN_API": ORIGIN}), patch.object(subject.ArtifactClient, "call") as transport, contextlib.redirect_stderr(stderr):
            self.assertEqual(subject.main(["status", "--run-id", "../../job", "--artifact-id", ARTIFACT_ID]), 1)
        transport.assert_not_called()


if __name__ == "__main__":
    unittest.main()
