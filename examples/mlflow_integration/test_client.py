import io
import json
import os
import tempfile
import unittest
import urllib.error
import urllib.request
from contextlib import redirect_stderr, redirect_stdout
from email.message import Message
from pathlib import Path
from unittest import mock
from urllib.response import addinfourl

import client


BASE = "https://raytrain.example"
RUN = "0123456789abcdef0123456789abcdef"
AUTH_FIXTURE = "example-not-a-real-pat"


def response(body, status=200, headers=None, url=BASE):
    metadata = Message()
    for key, value in (headers or {}).items():
        metadata[key] = value
    value = addinfourl(io.BytesIO(body), metadata, url, status)
    value.msg = "test response"
    return value


class RecordingHTTPSHandler(urllib.request.HTTPSHandler):
    def __init__(self, body, status=200, headers=None):
        super().__init__()
        self.body, self.status, self.headers = body, status, headers
        self.requests = []

    def https_open(self, request):
        self.requests.append(request)
        return response(self.body, self.status, self.headers, request.full_url)


class ClientTest(unittest.TestCase):
    def make_client(self, body=None, status=200, headers=None):
        if body is None:
            body = json.dumps({"success": True, "data": {"run": {"id": RUN}}, "request_id": "request-1"}).encode()
        transport = RecordingHTTPSHandler(body, status, headers)
        opener = urllib.request.build_opener(client.NoRedirectHandler(), transport)
        with mock.patch.object(client.urllib.request, "build_opener", return_value=opener):
            instance = client.RayTrainMLflowClient(BASE, AUTH_FIXTURE)
        return instance, transport

    def test_https_origin_and_header_safe_token_required(self):
        for base in ["http://raytrain.example", "https://user:pass@raytrain.example", "https://raytrain.example/api/v1", "https://raytrain.example?token=x", "https://raytrain.example/#fragment", "https://", "https://raytrain.example\n"]:
            with self.subTest(base=base), self.assertRaises(ValueError):
                client.RayTrainMLflowClient(base, AUTH_FIXTURE)
        for token in ["", "secret\r\nInjected: true", " secret "]:
            with self.subTest(token=repr(token)), self.assertRaises(ValueError):
                client.RayTrainMLflowClient(BASE, token)

    def test_exact_read_uses_explicit_pair_and_bearer_header(self):
        instance, transport = self.make_client()
        data = instance.read_run("job-01", RUN)
        self.assertEqual(data["run"]["id"], RUN)
        request = transport.requests[0]
        self.assertEqual(request.full_url, BASE + "/api/v1/jobs/job-01/mlflow/runs/" + RUN)
        self.assertEqual(request.get_method(), "GET")
        self.assertEqual(request.get_header("Authorization"), "Bearer " + AUTH_FIXTURE)
        self.assertIsNone(request.get_header("Cookie"))
        self.assertNotIn(AUTH_FIXTURE, request.full_url)

    def test_job_and_run_cannot_select_another_endpoint(self):
        instance, transport = self.make_client()
        for job, run in [("../admin", RUN), ("job?tenant=other", RUN), ("job-01", "../run"), ("job-01", RUN.upper()), ("", RUN)]:
            with self.subTest(job=job, run=run), self.assertRaises(ValueError):
                instance.read_run(job, run)
        self.assertFalse(transport.requests)

    def test_read_checks_returned_run_identifier(self):
        instance, _ = self.make_client(b'{"success":true,"data":{"run":{"id":"different"}},"request_id":"trace"}')
        with self.assertRaises(client.PlatformAPIError) as caught:
            instance.read_run("job-01", RUN)
        self.assertEqual(caught.exception.code, "INVALID_RESPONSE")

    def test_list_preserves_bounded_recent_window(self):
        instance, transport = self.make_client(b'{"success":true,"data":{"runs":[]},"request_id":"trace"}')
        self.assertEqual(instance.list_runs(100), {"runs": []})
        self.assertEqual(transport.requests[0].full_url, BASE + "/api/v1/experiments?limit=100")
        for limit in [0, 101, -1, True]:
            with self.assertRaises(ValueError):
                instance.list_runs(limit)
        self.assertEqual(len(transport.requests), 1)

    def test_redirect_never_forwards_pat_even_to_same_origin(self):
        for location in ["https://other.example/collect", BASE + "/other", "http://raytrain.example/insecure"]:
            for method in ["read", "write"]:
                with self.subTest(location=location, method=method):
                    instance, transport = self.make_client(b"redirect", 302, {"Location": location})
                    with self.assertRaises(client.PlatformAPIError) as caught:
                        if method == "read":
                            instance.read_run("job-01", RUN)
                        else:
                            instance.log_batch("job-01", RUN, {"tags": [{"key": "external.source", "value": "test"}]})
                    self.assertEqual(caught.exception.code, "REDIRECT_BLOCKED")
                    self.assertEqual(len(transport.requests), 1)
                    self.assertNotIn(AUTH_FIXTURE, str(caught.exception))

    def test_platform_error_preserves_safe_code_request_id_and_uncertain_write(self):
        body = json.dumps({"success": False, "error": {"code": "MLFLOW_AUDIT_INCOMPLETE", "message": "verify before retrying " + AUTH_FIXTURE}, "request_id": "req-503"}).encode()
        instance, transport = self.make_client(body, 503)
        with self.assertRaises(client.PlatformAPIError) as caught:
            instance.log_batch("job-01", RUN, {"params": [{"key": "external.version", "value": "v1"}]})
        error = caught.exception
        self.assertEqual((error.status, error.code, error.request_id), (503, "MLFLOW_AUDIT_INCOMPLETE", "req-503"))
        self.assertNotIn(AUTH_FIXTURE, str(error))
        self.assertIn("verify", str(error))
        self.assertEqual(len(transport.requests), 1)

    def test_network_failure_is_not_retried_and_does_not_print_transport_secrets(self):
        instance, _ = self.make_client()
        with mock.patch.object(instance._opener, "open", side_effect=urllib.error.URLError(AUTH_FIXTURE + " network diagnostic")) as send:
            with self.assertRaises(client.PlatformAPIError) as caught:
                instance.log_batch("job-01", RUN, {"tags": [{"key": "external.source", "value": "test"}]})
        self.assertEqual(send.call_count, 1)
        self.assertEqual(caught.exception.code, "NETWORK_ERROR")
        self.assertNotIn(AUTH_FIXTURE, str(caught.exception))

    def test_non_json_error_and_malformed_success_are_sanitized(self):
        for body, status in [(AUTH_FIXTURE.encode(), 502), (b'{"success":true}', 200), (b'[]', 200)]:
            instance, _ = self.make_client(body, status)
            with self.assertRaises(client.PlatformAPIError) as caught:
                instance.read_run("job-01", RUN)
            self.assertNotIn(AUTH_FIXTURE, str(caught.exception))

    def test_invalid_batch_json_never_reaches_network(self):
        instance, transport = self.make_client()
        for payload in [[], {}, {"metrics": [{"key": "loss", "value": float("nan"), "timestamp": 1, "step": 0}]}, {"tags": [{"key": "external.source", "value": "x" * 270000}]}]:
            with self.assertRaises(ValueError):
                instance.log_batch("job-01", RUN, payload)
        self.assertFalse(transport.requests)

    def test_retry_after_is_exposed_but_never_retries_the_write(self):
        body = b'{"success":false,"error":{"code":"RATE_LIMITED","message":"wait before retrying"},"request_id":"rate-1"}'
        for header, expected in [("60", 60), ("0", 0), (None, None), ("-1", None), ("1.5", None), ("Wed, 21 Oct 2015 07:28:00 GMT", None), ("9" * 100, None), ("60\r\nInjected: yes", None)]:
            with self.subTest(header=header):
                instance, transport = self.make_client(body, 429, {} if header is None else {"Retry-After": header})
                with self.assertRaises(client.PlatformAPIError) as caught:
                    instance.log_batch("job-01", RUN, {"tags": [{"key": "external.source", "value": "test"}]})
                self.assertEqual(caught.exception.retry_after, expected)
                self.assertEqual(caught.exception.request_id, "rate-1")
                self.assertEqual(len(transport.requests), 1)

    def test_oversized_response_is_rejected_without_replaying_write(self):
        instance, transport = self.make_client(b"x" * (client.MAX_RESPONSE_BYTES + 1))
        with self.assertRaises(client.PlatformAPIError) as caught:
            instance.log_batch("job-01", RUN, {"tags": [{"key": "external.source", "value": "test"}]})
        self.assertEqual(caught.exception.code, "INVALID_RESPONSE")
        self.assertIn("size limit", str(caught.exception))
        self.assertEqual(len(transport.requests), 1)

    def test_timeout_must_be_finite_positive_and_bounded(self):
        for timeout in [0, -1, 121, float("nan"), float("inf"), True, "30"]:
            with self.subTest(timeout=timeout), self.assertRaises(ValueError):
                client.RayTrainMLflowClient(BASE, AUTH_FIXTURE, timeout=timeout)

    def test_write_response_must_match_exact_target_and_logged_flag(self):
        for data in [{"runId": "another-run", "logged": True}, {"runId": RUN, "logged": False}, {"runId": RUN}]:
            instance, transport = self.make_client(json.dumps({"success": True, "data": data, "request_id": "write-uncertain"}).encode())
            with self.assertRaises(client.PlatformAPIError) as caught:
                instance.log_batch("job-01", RUN, {"tags": [{"key": "external.source", "value": "test"}]})
            self.assertEqual(caught.exception.code, "INVALID_RESPONSE")
            self.assertEqual(caught.exception.request_id, "write-uncertain")
            self.assertIn("Verify", str(caught.exception))
            self.assertEqual(len(transport.requests), 1)

    def test_explicit_batch_is_sent_once_to_the_selected_run(self):
        instance, transport = self.make_client(json.dumps({"success": True, "data": {"runId": RUN, "logged": True}, "request_id": "write-1"}).encode())
        payload = {"tags": [{"key": "external.source", "value": "test"}]}
        self.assertTrue(instance.log_batch("job-01", RUN, payload)["logged"])
        self.assertEqual(len(transport.requests), 1)
        request = transport.requests[0]
        self.assertEqual(request.get_method(), "POST")
        self.assertEqual(request.full_url, BASE + "/api/v1/jobs/job-01/mlflow/runs/" + RUN + "/log-batch")
        self.assertEqual(json.loads(request.data), payload)

    def test_cli_requires_explicit_action_and_does_not_write_on_read(self):
        output, error = io.StringIO(), io.StringIO()
        with mock.patch.dict(os.environ, {"RAYTRAIN_PAT": AUTH_FIXTURE}), mock.patch.object(client, "RayTrainMLflowClient") as factory, redirect_stdout(output), redirect_stderr(error):
            with self.assertRaises(SystemExit):
                client.main(["--base-url", BASE])
            factory.assert_not_called()
            factory.return_value.read_run.return_value = {"run": {"id": RUN}}
            self.assertEqual(client.main(["--base-url", BASE, "read", "--job-id", "job-01", "--run-id", RUN]), 0)
            factory.return_value.log_batch.assert_not_called()
        self.assertNotIn(AUTH_FIXTURE, output.getvalue() + error.getvalue())

    def test_cli_lists_window_notice_without_claiming_empty_means_no_access(self):
        output = io.StringIO()
        with mock.patch.dict(os.environ, {"RAYTRAIN_PAT": AUTH_FIXTURE}), mock.patch.object(client, "RayTrainMLflowClient") as factory, redirect_stdout(output):
            factory.return_value.list_runs.return_value = {"runs": []}
            self.assertEqual(client.main(["--base-url", BASE, "list"]), 0)
        result = json.loads(output.getvalue())
        self.assertFalse(result["complete"])
        self.assertEqual(result["window_limit"], 100)
        self.assertIn("window", result["notice"].lower())
        self.assertIn("permission", result["notice"].lower())

    def test_cli_log_batch_reads_only_explicit_local_payload_and_sends_once(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "batch.json"
            text = '{"tags":[{"key":"external.source","value":"test"}]}'
            path.write_text(text, encoding="utf-8")
            output = io.StringIO()
            with mock.patch.dict(os.environ, {"RAYTRAIN_PAT": AUTH_FIXTURE}), mock.patch.object(client, "RayTrainMLflowClient") as factory, redirect_stdout(output):
                factory.return_value.log_batch.return_value = {"runId": RUN, "logged": True}
                self.assertEqual(client.main(["--base-url", BASE, "log-batch", "--job-id", "job-01", "--run-id", RUN, "--payload", str(path)]), 0)
                factory.return_value.log_batch.assert_called_once_with("job-01", RUN, json.loads(text))
            self.assertEqual(path.read_text(encoding="utf-8"), text)

    def test_cli_missing_pat_or_invalid_payload_does_not_make_request(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "batch.json"
            for body in ['{"tags":[],"tags":[]}', '{"metrics":[{"value":NaN}]}', "[1]", "{}"]:
                path.write_text(body, encoding="utf-8")
                with mock.patch.dict(os.environ, {"RAYTRAIN_PAT": AUTH_FIXTURE}), mock.patch.object(client, "RayTrainMLflowClient") as factory, redirect_stderr(io.StringIO()):
                    self.assertNotEqual(client.main(["--base-url", BASE, "log-batch", "--job-id", "job-01", "--run-id", RUN, "--payload", str(path)]), 0)
                    factory.return_value.log_batch.assert_not_called()
        with mock.patch.dict(os.environ, {}, clear=True), mock.patch.object(client, "RayTrainMLflowClient") as factory, redirect_stderr(io.StringIO()):
            self.assertNotEqual(client.main(["--base-url", BASE, "list"]), 0)
            factory.assert_not_called()


if __name__ == "__main__":
    unittest.main()
