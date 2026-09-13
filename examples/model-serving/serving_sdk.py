"""Dependency-free adapter helper for the managed RayTrain serving worker.

User adapter code and weights remain immutable job inputs, outside the runtime
image. Only a job-scoped mounted credential is used for downloading its model.
"""
import base64
import hashlib
import http.client
import json
import os
import re
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

MAX_MODEL_BYTES = 20 * 1024**3
MAX_REQUEST_BYTES = 1024 * 1024


class ServingError(Exception):
    """Safe error without upstream bodies, credentials or local file contents."""


class NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('duplicate JSON key')
        result[key] = value
    return result


def _invalid_constant(_value):
    raise ValueError('non-finite JSON value')


def _json(raw):
    return json.loads(raw, object_pairs_hook=_unique_object, parse_constant=_invalid_constant)


class ServingClient:
    @classmethod
    def from_environment(cls):
        values = {key: os.environ.get('MODEL_SERVING_' + key, '') for key in (
            'ID', 'MODEL_SHA256', 'MODEL_SIZE_BYTES', 'PROTOCOL', 'BASE_URL', 'PORT')}
        if (not re.fullmatch(r'[A-Za-z0-9._-]{1,128}', values['ID'])
                or not re.fullmatch(r'[0-9a-f]{64}', values['MODEL_SHA256'])
                or values['PROTOCOL'] != 'model-serving-http/v1' or values['PORT'] != '8000'):
            raise ValueError('missing or invalid trusted serving worker context')
        if not re.fullmatch(r'[0-9]{1,11}', values['MODEL_SIZE_BYTES']):
            raise ValueError('invalid model size')
        size = int(values['MODEL_SIZE_BYTES'])
        if not 0 < size <= MAX_MODEL_BYTES:
            raise ValueError('model size exceeds 20 GiB')
        base = values['BASE_URL']
        parsed = urllib.parse.urlsplit(base)
        if (any(char.isspace() or ord(char) < 32 for char in base)
                or parsed.scheme not in ('http', 'https')
                or not (parsed.hostname or '').endswith(('.svc', '.svc.cluster.local'))
                or parsed.username is not None or parsed.password is not None
                or parsed.query or parsed.fragment
                or not re.fullmatch(r'/api/v1/internal/jobs/job-[0-9a-f]{24}/model-serving', parsed.path)):
            raise ValueError('serving model endpoint must be the injected internal job service')
        if parsed.port is not None and not 1 <= parsed.port <= 65535:
            raise ValueError('invalid internal service port')
        try:
            with Path(os.environ['RAYTRAIN_EVENT_TOKEN_FILE']).open('rb') as source:
                raw = source.read(129)
        except (KeyError, OSError):
            raise ServingError('mounted serving job credential is unavailable') from None
        if len(raw) != 43 or not re.fullmatch(rb'[A-Za-z0-9_-]{43}', raw):
            raise ValueError('invalid mounted job credential')
        decoded = base64.urlsafe_b64decode(raw + b'=')
        if len(decoded) != 32 or base64.urlsafe_b64encode(decoded).rstrip(b'=') != raw:
            raise ValueError('invalid mounted job credential')
        instance = cls()
        instance.deployment_id = values['ID']
        instance.model_sha256 = values['MODEL_SHA256']
        instance.model_size = size
        instance._base = base
        instance._credential = raw.decode('ascii')
        instance._opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirectHandler())
        return instance

    def download_model(self, target):
        target = Path(target)
        if target.exists() or target.is_symlink():
            raise ValueError('model target exists; choose a new worker-local path')
        request = urllib.request.Request(self._base + '/model', headers={
            'Authorization': 'Bearer ' + self._credential, 'Accept': 'application/octet-stream'})
        temporary = None
        try:
            try:
                response = self._opener.open(request, timeout=30)
            except urllib.error.HTTPError as error:
                error.close()
                raise ServingError('serving model request rejected (HTTP %d)' % error.code) from None
            with response:
                if response.code != 200:
                    raise ServingError('unexpected model response; redirects are disabled')
                digest, total, started = hashlib.sha256(), 0, time.monotonic()
                with tempfile.NamedTemporaryFile(dir=target.parent, prefix='.serving-', delete=False) as output:
                    temporary = Path(output.name)
                    while True:
                        if time.monotonic() - started > 600:
                            raise ServingError('model download exceeded ten-minute deadline')
                        chunk = response.read(min(1024 * 1024, self.model_size - total + 1))
                        if not chunk:
                            break
                        total += len(chunk)
                        if total > self.model_size:
                            raise ServingError('model exceeds frozen size')
                        digest.update(chunk)
                        output.write(chunk)
                    if total != self.model_size or digest.hexdigest() != self.model_sha256:
                        raise ServingError('model size or SHA-256 does not match the frozen version')
                    output.flush()
                    os.fsync(output.fileno())
            os.link(temporary, target)
            return target
        except (OSError, urllib.error.URLError, http.client.HTTPException):
            raise ServingError('model download or worker-local write failed') from None
        finally:
            if temporary is not None:
                try:
                    temporary.unlink(missing_ok=True)
                except OSError:
                    raise ServingError('model temporary file cleanup failed') from None

    def handler(self, predict):
        """predict accepts one JSON object and returns a JSON object or list.

        Initialize the model before calling serve. An adapter exception yields an
        opaque 500; payloads and credentials are never logged by this helper.
        """
        client = self

        class Handler(BaseHTTPRequestHandler):
            server_version = 'RayTrainServing/1'
            sys_version = ''

            def setup(self):
                super().setup()
                self.connection.settimeout(30)

            def log_message(self, *_args):
                pass

            def _send(self, status, value):
                raw = json.dumps(value, allow_nan=False, separators=(',', ':')).encode()
                if len(raw) > MAX_REQUEST_BYTES:
                    status, raw = 500, b'{"error":"inference response exceeds limit"}'
                self.send_response(status)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def do_GET(self):
                if self.path != '/healthz':
                    return self._send(404, {'error': 'route not found'})
                return self._send(200, {'ready': True, 'protocol': 'model-serving-http/v1',
                    'deploymentId': client.deployment_id, 'modelSha256': client.model_sha256})

            def do_POST(self):
                if self.path != '/invocations':
                    return self._send(404, {'error': 'route not found'})
                lengths = self.headers.get_all('Content-Length', [])
                if self.headers.get('Transfer-Encoding') or len(lengths) != 1 or not re.fullmatch(r'[0-9]{1,8}', lengths[0]):
                    return self._send(400, {'error': 'one Content-Length is required'})
                size = int(lengths[0])
                if not 0 < size <= MAX_REQUEST_BYTES:
                    return self._send(413, {'error': 'request must be at most 1 MiB'})
                if self.headers.get('Content-Type', '').split(';')[0].strip().lower() != 'application/json':
                    return self._send(415, {'error': 'application/json required'})
                try:
                    raw = self.rfile.read(size)
                    if len(raw) != size:
                        raise ValueError('truncated request')
                    payload = _json(raw)
                    if not isinstance(payload, dict):
                        raise ValueError('request must be an object')
                except (ValueError, UnicodeError, RecursionError, OSError):
                    return self._send(400, {'error': 'invalid JSON request'})
                try:
                    result = predict(payload)
                    if not isinstance(result, (dict, list)):
                        raise ValueError('adapter response must be an object or array')
                    return self._send(200, result)
                except Exception:
                    return self._send(500, {'error': 'model adapter failed'})
        return Handler

    def serve(self, predict):
        # One inference request at a time gives adapters predictable GPU memory
        # usage. The platform gateway bounds concurrent callers and timeouts.
        with HTTPServer(('0.0.0.0', 8000), self.handler(predict)) as server:
            server.serve_forever(poll_interval=0.5)
