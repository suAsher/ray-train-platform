import base64
import hashlib
import importlib.util
import io
import json
import os
import tempfile
import threading
import unittest
import urllib.request
from email.message import Message
from http.server import HTTPServer
from pathlib import Path
from unittest import mock
from urllib.response import addinfourl

spec = importlib.util.spec_from_file_location('serving_sdk', Path(__file__).with_name('serving_sdk.py'))
sdk = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sdk)
CONTENT = b'fixed-model-fixture'
BASE = 'http://backend.platform.svc:8080/api/v1/internal/jobs/job-' + '1'*24 + '/model-serving'


class ServingSDKTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.credential = base64.urlsafe_b64encode(bytes(range(32))).rstrip(b'=')
        self.token_file = self.root/'credential'
        self.token_file.write_bytes(self.credential)
        self.environment = {
            'MODEL_SERVING_ID': 'deployment-1',
            'MODEL_SERVING_MODEL_SHA256': hashlib.sha256(CONTENT).hexdigest(),
            'MODEL_SERVING_MODEL_SIZE_BYTES': str(len(CONTENT)),
            'MODEL_SERVING_PROTOCOL': 'model-serving-http/v1',
            'MODEL_SERVING_PORT': '8000', 'MODEL_SERVING_BASE_URL': BASE,
            'RAYTRAIN_EVENT_TOKEN_FILE': str(self.token_file),
        }

    def client(self, **changes):
        with mock.patch.dict(os.environ, {**self.environment, **changes}, clear=True):
            return sdk.ServingClient.from_environment()

    def response(self, content=CONTENT, status=200):
        return addinfourl(io.BytesIO(content), Message(), BASE+'/model', status)

    def test_scoped_download_verifies_bytes_and_never_replaces_target(self):
        client = self.client()
        target = self.root/'model.bin'
        with mock.patch.object(client._opener, 'open', return_value=self.response()) as call:
            self.assertEqual(client.download_model(target).read_bytes(), CONTENT)
            request = call.call_args.args[0]
            self.assertEqual(request.full_url, BASE+'/model')
            self.assertEqual(request.get_header('Authorization'), 'Bearer '+self.credential.decode())
            self.assertIsNone(request.get_header('Cookie'))
        with self.assertRaises(ValueError):
            client.download_model(target)
        self.assertEqual(target.read_bytes(), CONTENT)
        target.unlink()
        target.symlink_to(self.root/'absent')
        with self.assertRaises(ValueError):
            client.download_model(target)

    def test_invalid_worker_context_and_cross_job_path_fail_closed(self):
        for changes in [
            {'MODEL_SERVING_BASE_URL': 'https://outside.example/api/v1/internal/jobs/job-'+'1'*24+'/model-serving'},
            {'MODEL_SERVING_BASE_URL': BASE+'/../other'},
            {'MODEL_SERVING_BASE_URL': BASE+'?target=another'},
            {'MODEL_SERVING_BASE_URL': BASE.replace('backend.', 'user:secret@backend.')},
            {'MODEL_SERVING_BASE_URL': BASE.replace('job-'+'1'*24, '../other')},
            {'MODEL_SERVING_MODEL_SIZE_BYTES': str(20*1024**3+1)},
            {'MODEL_SERVING_MODEL_SHA256': 'bad'},
            {'MODEL_SERVING_PORT': '9999'}, {'MODEL_SERVING_PROTOCOL': 'unknown'},
        ]:
            with self.assertRaises(ValueError):
                self.client(**changes)
        self.token_file.write_bytes(b'invalid-pat')
        with self.assertRaises(ValueError):
            self.client()

    def test_failed_download_removes_partial_bytes_without_leaking_credentials(self):
        for body, status in [(b'truncated', 200), (CONTENT+b'overflow', 200), (b'x'*len(CONTENT), 200), (self.credential, 302)]:
            client = self.client()
            target = self.root/'model.bin'
            with mock.patch.object(client._opener, 'open', return_value=self.response(body, status)):
                with self.assertRaises(sdk.ServingError) as caught:
                    client.download_model(target)
            self.assertNotIn(self.credential.decode(), str(caught.exception))
            self.assertFalse(target.exists())
            self.assertEqual([entry.name for entry in self.root.iterdir()], ['credential'])

    def test_http_ready_and_real_invocation_match_model_context(self):
        client = self.client()
        seen = []
        def predict(payload):
            seen.append(payload)
            return {'predictions': [value*2 for value in payload['inputs']]}
        server = HTTPServer(('127.0.0.1', 0), client.handler(predict))
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(thread.join, 2)
        self.addCleanup(server.shutdown)
        base = 'http://127.0.0.1:'+str(server.server_port)
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open(base+'/healthz') as response:
            health = json.load(response)
        self.assertTrue(health['ready'])
        self.assertEqual(health['deploymentId'], 'deployment-1')
        self.assertEqual(health['modelSha256'], client.model_sha256)
        request = urllib.request.Request(base+'/invocations', data=b'{"inputs":[2,3]}', headers={'Content-Type':'application/json'})
        with opener.open(request) as response:
            self.assertEqual(json.load(response), {'predictions':[4,6]})
        self.assertEqual(seen, [{'inputs':[2,3]}])
        for body, expected in [(b'{"inputs":NaN}',400), (b'{"inputs":[],"inputs":[]}',400), (b'[]',400), (b'{"inputs":null}',500)]:
            request = urllib.request.Request(base+'/invocations', data=body, headers={'Content-Type':'application/json'})
            with self.assertRaises(urllib.error.HTTPError) as caught:
                opener.open(request)
            self.assertEqual(caught.exception.code, expected)
            caught.exception.close()

class ServingRequestBoundaryTest(unittest.TestCase):
    def setUp(self):
        self.client = sdk.ServingClient()
        self.client.deployment_id = 'deployment-boundary'
        self.client.model_sha256 = 'a'*64
        self.seen = []
        def predict(payload):
            self.seen.append(payload)
            if payload.get('oversize'):
                return {'value':'x'*(sdk.MAX_REQUEST_BYTES+1)}
            return {'ok':True}
        self.server = HTTPServer(('127.0.0.1',0),self.client.handler(predict))
        self.thread = threading.Thread(target=self.server.serve_forever,daemon=True)
        self.thread.start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.thread.join,2)
        self.addCleanup(self.server.shutdown)

    def request(self, path='/invocations', body=b'{}', headers=None):
        import http.client
        connection = http.client.HTTPConnection('127.0.0.1',self.server.server_port,timeout=3)
        self.addCleanup(connection.close)
        connection.request('POST',path,body,headers or {'Content-Type':'application/json'})
        response = connection.getresponse()
        return response.status,response.read()

    def test_wrong_path_type_and_size_never_reach_predict(self):
        self.assertEqual(self.request(path='/../invocations')[0],404)
        self.assertEqual(self.request(headers={'Content-Type':'text/plain'})[0],415)
        self.assertEqual(self.request(headers={'Content-Type':'application/json','Content-Length':str(sdk.MAX_REQUEST_BYTES+1)})[0],413)
        self.assertEqual(self.seen,[])

    def test_oversized_model_output_is_bounded_and_opaque(self):
        status,body = self.request(body=b'{"oversize":true}')
        self.assertEqual(status,500)
        self.assertLess(len(body),1024)
        self.assertEqual(json.loads(body),{'error':'inference response exceeds limit'})


if __name__ == '__main__':
    unittest.main()
