import base64
import hashlib
import io
import json
import os
import tempfile
import types
import unittest
import urllib.request
from email.message import Message
from pathlib import Path
from unittest import mock
from urllib.response import addinfourl

import evaluation_sdk as sdk

MODEL_BYTES = b'model-content-fixture'

class Transport(urllib.request.HTTPSHandler):
    def __init__(self, body, status=200, headers=None):
        super().__init__()
        self.body, self.status, self.headers = body, status, headers or {}
        self.requests = []
    def https_open(self, request):
        self.requests.append(request)
        headers = Message()
        for key, value in self.headers.items():
            headers[key] = value
        response = addinfourl(io.BytesIO(self.body), headers, request.full_url, self.status)
        response.msg = 'test'
        return response

class EvaluationSDKTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.credential_file = Path(self.directory.name) / 'worker-credential'
        self.credential_file.write_bytes(base64.urlsafe_b64encode(bytes(range(32))).rstrip(b'='))
        self.env = {
            'RAYTRAIN_EVENT_TOKEN_FILE': str(self.credential_file),
            'MODEL_EVALUATION_BASE_URL': 'https://backend.example/api/v1/internal/jobs/job-1/model-evaluation',
            'MODEL_EVALUATION_ID': 'evaluation-1',
            'MODEL_EVALUATION_MODEL_SHA256': hashlib.sha256(MODEL_BYTES).hexdigest(),
            'MODEL_EVALUATION_DATASET_MANIFEST_SHA256': 'b' * 64,
            'MODEL_EVALUATION_DATASET_SPLIT': 'test',
            'MODEL_EVALUATION_CONFIG_JSON': '{}',
            'MODEL_EVALUATION_CONFIG_SHA256': hashlib.sha256(b'{}').hexdigest(),
            'MODEL_EVALUATION_EVALUATOR_ID': 'evaluator-1',
            'MODEL_EVALUATION_PROTOCOL': 'model-evaluation-report/v1',
            'MODEL_EVALUATION_DATASET_SAMPLE_COUNT': '10',
        }
    def make_client(self, body=MODEL_BYTES, **kwargs):
        transport = Transport(body, **kwargs)
        with mock.patch.dict(os.environ, self.env, clear=True):
            instance = sdk.EvaluationClient.from_environment()
        instance._opener = urllib.request.build_opener(sdk.NoRedirectHandler(), transport)
        return instance, transport
    def test_stream_model_checks_whole_sha_before_atomic_publish(self):
        instance, transport = self.make_client()
        target = Path(self.directory.name) / 'weights.pth'
        self.assertEqual(instance.download_model(target), target)
        self.assertEqual(target.read_bytes(), MODEL_BYTES)
        self.assertEqual(len(transport.requests), 1)
        self.assertTrue(transport.requests[0].full_url.endswith('/jobs/job-1/model-evaluation/model'))
        self.assertEqual(transport.requests[0].get_header('Authorization'), 'Bearer ' + self.credential_file.read_text())
        self.assertIsNone(transport.requests[0].get_header('Cookie'))
    def test_hash_mismatch_and_size_limit_leave_no_model_or_tempfile(self):
        for body, limit in [(b'corrupt-model', 100), (MODEL_BYTES, 1)]:
            with self.subTest(body=body):
                instance, _ = self.make_client(body)
                target = Path(self.directory.name) / 'weights.pth'
                with self.assertRaises(sdk.EvaluationError):
                    instance.download_model(target, max_bytes=limit)
                self.assertFalse(target.exists())
                self.assertEqual(sorted(p.name for p in Path(self.directory.name).iterdir()), ['worker-credential'])
    def test_report_is_explicit_rank_zero_and_frozen_metadata(self):
        response = json.dumps({'success': True, 'data': {'id': 'evaluation-1', 'jobId': 'job-1', 'reportState': 'VALID'}, 'request_id': 'trace'}).encode()
        instance, transport = self.make_client(response)
        metrics = [{'name': 'contract/model_bytes', 'value': len(MODEL_BYTES), 'unit': 'bytes', 'direction': 'neutral'}]
        for rank in [1, -1, True, None]:
            with self.assertRaises(ValueError):
                instance.report(metrics, world_rank=rank)
        self.assertFalse(transport.requests)
        instance.report(metrics, world_rank=0)
        payload = json.loads(transport.requests[0].data)
        self.assertEqual(payload['evaluationId'], 'evaluation-1')
        self.assertEqual(payload['configSha256'], self.env['MODEL_EVALUATION_CONFIG_SHA256'])
        self.assertEqual(payload['metrics'], metrics)
        self.assertEqual(len(transport.requests), 1)
    def test_empty_wrong_metrics_and_oversize_report_do_not_send(self):
        instance, transport = self.make_client()
        for metrics in [[], [{'name': 'x', 'value': float('nan'), 'unit': '', 'direction': 'neutral'}], [{'name': 'x', 'value': 1, 'unit': 'a' * 262145, 'direction': 'neutral'}]]:
            with self.assertRaises(ValueError):
                instance.report(metrics, world_rank=0)
        self.assertFalse(transport.requests)
    def test_redirects_errors_never_replay_or_leak_worker_secret(self):
        for status in [302, 403, 503]:
            instance, transport = self.make_client(self.credential_file.read_bytes(), status=status, headers={'Location': 'https://attacker.example'})
            with self.assertRaises(sdk.EvaluationError) as caught:
                instance.download_model(Path(self.directory.name) / 'weights.pth')
            self.assertNotIn(self.credential_file.read_text(), str(caught.exception))
            self.assertEqual(len(transport.requests), 1)
    def test_wrong_context_and_invalid_environment_fail_closed(self):
        for changes in [{'MODEL_EVALUATION_ID': ''}, {'MODEL_EVALUATION_CONFIG_SHA256': '0' * 64}, {'MODEL_EVALUATION_BASE_URL': 'http://attacker.example/jobs/job-1/model-evaluation'}, {'MODEL_EVALUATION_BASE_URL': 'https://user:password@backend.example/jobs/job-1/model-evaluation'}, {'MODEL_EVALUATION_PROTOCOL': 'other'}, {'RAYTRAIN_EVENT_TOKEN_FILE': ''}]:
            with mock.patch.dict(os.environ, {**self.env, **changes}, clear=True), self.assertRaises(ValueError):
                sdk.EvaluationClient.from_environment()
        self.credential_file.write_text('not-a-worker-credential')
        with mock.patch.dict(os.environ, self.env, clear=True), self.assertRaises(ValueError):
            sdk.EvaluationClient.from_environment()
    def test_report_response_must_match_evaluation_id(self):
        for body in [b'{"success":true,"data":{"id":"another-evaluation"}}', b'{"success":false}', b'x' * (262144 + 1)]:
            instance, transport = self.make_client(body)
            with self.assertRaises(sdk.EvaluationError):
                instance.report([{'name': 'x', 'value': 1, 'unit': 'count', 'direction': 'neutral'}], world_rank=0)
            self.assertEqual(len(transport.requests), 1)

    def test_download_limits_and_existing_target_fail_without_http(self):
        instance, transport = self.make_client()
        target = Path(self.directory.name) / 'existing.pth'
        target.write_bytes(b'existing user file')
        for value in [0, -1, True, 20 * 1024**3 + 1]:
            with self.assertRaises(ValueError):
                instance.download_model(target, max_bytes=value)
        with self.assertRaises(ValueError):
            instance.download_model(target)
        self.assertEqual(target.read_bytes(), b'existing user file')
        self.assertFalse(transport.requests)

    def test_smoke_entrypoint_runs_in_worker_and_reports_only_rank_zero(self):
        import smoke_evaluator
        for rank in [0, 1]:
            context = types.SimpleNamespace(get_world_rank=lambda: rank)
            ray = types.SimpleNamespace(train=types.SimpleNamespace(get_context=lambda: context))
            with mock.patch.dict('sys.modules', {'ray': ray}), mock.patch.object(smoke_evaluator.EvaluationClient, 'from_environment') as factory:
                def write_model(target):
                    target.write_bytes(MODEL_BYTES)
                    return target
                factory.return_value.download_model.side_effect = write_model
                smoke_evaluator.main()
                factory.return_value.download_model.assert_called_once()
                self.assertEqual(factory.return_value.report.call_count, 1 if rank == 0 else 0)

if __name__ == '__main__':
    unittest.main()
