import io
import json
import os
import tempfile
import unittest
import urllib.request
from contextlib import redirect_stderr, redirect_stdout
from email.message import Message
from pathlib import Path
from unittest import mock
from urllib.response import addinfourl

import client
import external_tracking as external

BASE = "https://raytrain.example"
AUTH_FIXTURE = "example-not-a-real-pat"
EXP = "1" * 32
RUN = "2" * 32
UPSTREAM = "3" * 32
BATCH = {"tags": [{"key": "external.source", "value": "test"}]}


class Transport(urllib.request.HTTPSHandler):
    def __init__(self, data=None, status=200, headers=None, raw=None):
        super().__init__()
        self.requests = []
        self.body = raw if raw is not None else json.dumps({"success": status < 300, "data": data, "error": {"code": "SCOPE_REQUIRED", "message": AUTH_FIXTURE}, "request_id": "trace"}).encode()
        self.status, self.headers = status, headers or {}

    def https_open(self, request):
        self.requests.append(request)
        headers = Message()
        for key, value in self.headers.items():
            headers[key] = value
        result = addinfourl(io.BytesIO(self.body), headers, request.full_url, self.status)
        result.msg = "test"
        return result


class ExternalTrackingTest(unittest.TestCase):
    def make_client(self, data=None, **kwargs):
        transport = Transport(data, **kwargs)
        opener = urllib.request.build_opener(client.NoRedirectHandler(), transport)
        with mock.patch.object(client.urllib.request, "build_opener", return_value=opener):
            instance = external.RayTrainExternalTracking(BASE, AUTH_FIXTURE)
        return instance, transport

    def test_create_uses_explicit_same_key_without_automatic_retry(self):
        instance, transport = self.make_client({"id": EXP, "name": "quality", "mlflowExperimentId": "42"})
        for _ in range(2):
            self.assertEqual(instance.create_experiment("quality", "operation:one" )["id"], EXP)
        self.assertEqual(len(transport.requests), 2)
        for request in transport.requests:
            self.assertEqual(request.get_header("Idempotency-key"), "operation:one")
            self.assertEqual(json.loads(request.data), {"name": "quality"})
            self.assertIsNone(request.get_header("Cookie"))
        instance, transport = self.make_client({"id": RUN, "experimentId": EXP, "name": "trial", "mlflowRunId": UPSTREAM})
        self.assertEqual(instance.create_run(EXP, "trial", "run-one")["id"], RUN)
        self.assertIn(EXP + "/runs", transport.requests[0].full_url)

    def test_invalid_keys_names_ids_and_pages_never_send_http(self):
        instance, transport = self.make_client()
        for key in ["", "x\nHeader: y", "a" * 129, None, "a b", "中文"]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                instance.create_experiment("quality", key)
        for name in ["", " spaced", "bad\nname", "界" * 129, None]:
            with self.subTest(name=name), self.assertRaises(ValueError):
                instance.create_experiment(name, "one")
        for identifier in ["42", "../runs", RUN.upper().replace("2", "A"), None]:
            with self.assertRaises(ValueError):
                instance.get_run(identifier)
        for limit in [0, 101, True, "1"]:
            with self.assertRaises(ValueError):
                instance.list_experiments(limit)
        for cursor in ["a" * 2049, "a\n", None]:
            with self.assertRaises(ValueError):
                instance.list_runs(EXP, cursor=cursor)
        for state in ["RUNNING", "", None]:
            with self.assertRaises(ValueError):
                instance.finish_run(RUN, state)
        for path in ["@outside.example", "//outside.example/api", "/api/v1/jobs"]:
            with self.assertRaises(ValueError):
                instance.request("GET", path)
        self.assertFalse(transport.requests)

    def test_exact_platform_identifier_is_used_and_response_verified(self):
        instance, transport = self.make_client({"run": {"id": RUN, "mlflowRunId": UPSTREAM}})
        self.assertEqual(instance.get_run(RUN)["run"]["id"], RUN)
        self.assertTrue(transport.requests[0].full_url.endswith("/" + RUN))
        for method, data in [(lambda c: c.get_run(RUN), {"run": {"id": UPSTREAM}}), (lambda c: c.log_batch(RUN, BATCH), {"runId": UPSTREAM, "logged": True}), (lambda c: c.finish_run(RUN, "FINISHED"), {"id": RUN, "state": "FAILED"}), (lambda c: c.create_run(EXP, "trial", "one"), {"id": RUN, "experimentId": UPSTREAM, "name": "trial"}), (lambda c: c.create_experiment("quality", "one"), {"id": "42", "name": "quality"})]:
            instance, _ = self.make_client(data)
            with self.assertRaises(client.PlatformAPIError) as caught:
                method(instance)
            self.assertEqual(caught.exception.code, "INVALID_RESPONSE")
            self.assertEqual(caught.exception.request_id, "trace")

    def test_scope_error_redacts_credential_and_bounded_retry_metadata(self):
        for status, retry, expected in [(403, "60", 60), (429, "999999999999", None), (429, "60", 60)]:
            instance, transport = self.make_client(status=status, headers={"Retry-After": retry})
            with self.assertRaises(client.PlatformAPIError) as caught:
                instance.create_experiment("quality", "one")
            self.assertNotIn(AUTH_FIXTURE, str(caught.exception))
            self.assertEqual(caught.exception.retry_after, expected)
            self.assertEqual(len(transport.requests), 1)

    def test_redirect_and_oversize_response_are_rejected_once(self):
        for kwargs, code in [({"status": 302, "headers": {"Location": "https://other.example/collect"}}, "REDIRECT_BLOCKED"), ({"raw": b" " * (client.MAX_RESPONSE_BYTES + 1)}, "INVALID_RESPONSE")]:
            instance, transport = self.make_client(**kwargs)
            with self.assertRaises(client.PlatformAPIError) as caught:
                instance.create_experiment("quality", "one")
            self.assertEqual(caught.exception.code, code)
            self.assertEqual(len(transport.requests), 1)

    def test_bounded_payload_read_and_body(self):
        instance, transport = self.make_client()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "batch.json"
            path.write_bytes(json.dumps(BATCH).encode())
            self.assertEqual(external.read_json_file(str(path)), BATCH)
            for raw in [b" " * (client.MAX_BATCH_BYTES + 1), b'{"tags":[],"tags":[]}', b'{"metrics":[NaN]}']:
                path.write_bytes(raw)
                with self.assertRaises(ValueError):
                    external.read_json_file(str(path))
            with self.assertRaises(ValueError):
                external.read_json_file(directory)
        for batch in [{}, {"tags": []}, {"other": []}, {"tags": [1] * 101}, {"tags": [{"value": "a" * client.MAX_BATCH_BYTES}]}, {"metrics": [float("nan")]}]:
            with self.assertRaises(ValueError):
                instance.log_batch(RUN, batch)
        with self.assertRaises(ValueError):
            instance.request("POST", "/api/v1/mlflow/experiments", {"large": "a" * client.MAX_BATCH_BYTES})
        self.assertFalse(transport.requests)

    def test_read_page_does_not_follow_cursor_and_writes_are_explicit(self):
        instance, transport = self.make_client({"items": [], "nextCursor": "another-page"})
        self.assertEqual(instance.list_experiments(100, "cursor.value")["nextCursor"], "another-page")
        self.assertEqual(len(transport.requests), 1)
        self.assertIn("limit=100&cursor=cursor.value", transport.requests[0].full_url)
        instance.list_runs(EXP)
        instance, transport = self.make_client({"runId": RUN, "logged": True})
        self.assertTrue(instance.log_batch(RUN, BATCH)["logged"])
        self.assertEqual(json.loads(transport.requests[0].data), BATCH)
        instance, transport = self.make_client({"id": RUN, "state": "FINISHED"})
        instance.finish_run(RUN, "finished")
        self.assertEqual(json.loads(transport.requests[0].data), {"status": "FINISHED"})

    def test_cli_dispatch_and_sdk_example_do_not_write_implicitly(self):
        cases = [("capabilities", [], "capabilities"), ("list-experiments", [], "list_experiments"), ("create-experiment", ["--name", "quality", "--idempotency-key", "one"], "create_experiment"), ("list-runs", ["--experiment-id", EXP], "list_runs"), ("create-run", ["--experiment-id", EXP, "--name", "trial", "--idempotency-key", "one"], "create_run"), ("get-run", ["--run-id", RUN], "get_run"), ("log-batch", ["--run-id", RUN, "--payload", "batch.json"], "log_batch"), ("finish", ["--run-id", RUN, "--status", "FINISHED"], "finish_run")]
        for command, args, method in cases:
            with self.subTest(command=command), mock.patch.dict(os.environ, {"RAYTRAIN_API": BASE, "RAYTRAIN_PAT": AUTH_FIXTURE}), mock.patch.object(external, "RayTrainExternalTracking") as factory, mock.patch.object(external, "read_json_file", return_value=BATCH), redirect_stdout(io.StringIO()) as output:
                getattr(factory.return_value, method).return_value = {"note": AUTH_FIXTURE}
                self.assertEqual(external.main([command] + args), 0)
                getattr(factory.return_value, method).assert_called_once()
                self.assertEqual(len(factory.return_value.method_calls), 1)
                self.assertNotIn(AUTH_FIXTURE, output.getvalue())
        with mock.patch.object(external, "RayTrainExternalTracking") as factory, redirect_stdout(io.StringIO()):
            self.assertEqual(external.main(["sdk-example"]), 0)
            factory.assert_not_called()
        for failure, expected in [(client.PlatformAPIError(403, "DENIED", "denied"), 1), (ValueError("invalid"), 2), (OSError("private path"), 2)]:
            with mock.patch.object(external, "RayTrainExternalTracking", side_effect=failure), redirect_stderr(io.StringIO()) as output:
                self.assertEqual(external.main(["capabilities"]), expected)
                self.assertNotIn("private path", output.getvalue())


if __name__ == "__main__":
    unittest.main()
