"""Small stdlib helper for an explicitly created RayTrain evaluation worker.

The mounted job credential authorizes only this job's model and report. This
module never creates jobs, reads a PAT, loads model code, or retries HTTP calls.
"""
from __future__ import annotations

import base64
import hashlib
import json
import http.client
import os
import re
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

MAX_REPORT_BYTES = 256 * 1024
MAX_MODEL_BYTES = 20 * 1024**3
HASH_PATTERN = re.compile(r'[0-9a-f]{64}\Z')
ID_PATTERN = re.compile(r'[A-Za-z0-9._-]{1,128}\Z')


class EvaluationError(Exception):
    """Opaque transport/protocol error without credentials or upstream bodies."""


class NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('duplicate JSON field')
        result[key] = value
    return result


def _reject_constant(_value):
    raise ValueError('non-finite JSON is unsupported')


def _strict_json(raw):
    return json.loads(raw, object_pairs_hook=_unique_object, parse_constant=_reject_constant)


class EvaluationClient:
    @classmethod
    def from_environment(cls):
        """Call inside the managed worker; a driver or local shell fails closed."""
        instance = cls()
        required = ['ID', 'MODEL_SHA256', 'DATASET_MANIFEST_SHA256', 'DATASET_SPLIT',
                    'CONFIG_JSON', 'CONFIG_SHA256', 'EVALUATOR_ID', 'PROTOCOL',
                    'BASE_URL', 'DATASET_SAMPLE_COUNT']
        values = {key: os.environ.get('MODEL_EVALUATION_' + key, '') for key in required}
        if not all(values.values()) or not os.environ.get('RAYTRAIN_EVENT_TOKEN_FILE'):
            raise ValueError('evaluation worker context is missing; run inside the managed Ray Train worker')
        if any(not HASH_PATTERN.fullmatch(values[key]) for key in ['MODEL_SHA256', 'DATASET_MANIFEST_SHA256', 'CONFIG_SHA256']):
            raise ValueError('invalid evaluation digest')
        if any(not ID_PATTERN.fullmatch(values[key]) for key in ['ID', 'EVALUATOR_ID']):
            raise ValueError('invalid evaluation identifier')
        if values['PROTOCOL'] != 'model-evaluation-report/v1':
            raise ValueError('unsupported evaluation protocol')
        config_bytes = values['CONFIG_JSON'].encode('utf-8')
        if len(config_bytes) > 16 * 1024 or hashlib.sha256(config_bytes).hexdigest() != values['CONFIG_SHA256']:
            raise ValueError('evaluation config digest does not match')
        try:
            instance.config = _strict_json(config_bytes)
            instance.sample_count = int(values['DATASET_SAMPLE_COUNT'])
        except (ValueError, UnicodeError, RecursionError):
            raise ValueError('invalid evaluation config or sample count') from None
        if not isinstance(instance.config, dict) or instance.sample_count <= 0 or values['DATASET_SPLIT'] not in ('val', 'test'):
            raise ValueError('invalid evaluation config or sample count')
        parsed = urllib.parse.urlsplit(values['BASE_URL'])
        internal_http = parsed.scheme == 'http' and parsed.hostname and parsed.hostname.endswith(('.svc', '.svc.cluster.local'))
        if (parsed.scheme != 'https' and not internal_http) or not parsed.hostname or parsed.username is not None or parsed.password is not None or parsed.query or parsed.fragment:
            raise ValueError('evaluation endpoint must be HTTPS or the injected internal cluster service')
        match = re.fullmatch(r'/api/v1/internal/jobs/([a-z0-9-]+)/model-evaluation', parsed.path)
        if not match or any(char.isspace() or ord(char) < 32 for char in values['BASE_URL']):
            raise ValueError('evaluation endpoint must select one internal platform job')
        instance.job_id = match.group(1)
        with Path(os.environ['RAYTRAIN_EVENT_TOKEN_FILE']).open('rb') as source:
            raw = source.read(129)
        if len(raw) != 43 or not re.fullmatch(rb'[A-Za-z0-9_-]{43}', raw):
            raise ValueError('invalid mounted worker credential')
        decoded = base64.urlsafe_b64decode(raw + b'=')
        if len(decoded) != 32 or base64.urlsafe_b64encode(decoded).rstrip(b'=') != raw:
            raise ValueError('invalid mounted worker credential')
        instance._credential = raw.decode('ascii')
        instance._base = values['BASE_URL']
        instance._context = dict(values)
        instance._opener = urllib.request.build_opener(NoRedirectHandler())
        return instance

    def _open(self, action, body=None):
        request = urllib.request.Request(self._base + '/' + action, data=body,
            method='GET' if body is None else 'POST',
            headers={'Authorization': 'Bearer ' + self._credential,
                     'Content-Type': 'application/json', 'Accept': 'application/octet-stream' if body is None else 'application/json'})
        try:
            response = self._opener.open(request, timeout=30)
        except urllib.error.HTTPError as error:
            error.close()
            raise EvaluationError('Evaluation request rejected (HTTP %d); verify job state before retrying.' % error.code) from None
        except (urllib.error.URLError, OSError, http.client.HTTPException):
            raise EvaluationError('Evaluation request outcome is unknown; verify job state before retrying.') from None
        if response.code != 200:
            response.close()
            raise EvaluationError('Unexpected evaluation response; redirects and retries are disabled.')
        return response

    def download_model(self, target, *, max_bytes=MAX_MODEL_BYTES):
        """Stream to a private temporary file, verify the whole SHA, then rename.

        Each worker may download independently. Never unpickle arbitrary model
        files here: model architecture/loading belong to the pinned evaluator.
        """
        if type(max_bytes) is not int or not 0 < max_bytes <= MAX_MODEL_BYTES:
            raise ValueError('max_bytes must be positive and at most 20 GiB')
        target = Path(target)
        if target.exists():
            raise ValueError('model target already exists; choose a new worker-local path')
        temp_path = None
        try:
            digest, total, started = hashlib.sha256(), 0, time.monotonic()
            with self._open('model') as response, tempfile.NamedTemporaryFile(dir=target.parent, prefix='.evaluation-', delete=False) as output:
                temp_path = Path(output.name)
                while True:
                    if time.monotonic() - started > 600:
                        raise EvaluationError('Model download exceeded its ten-minute deadline.')
                    chunk = response.read(min(1024 * 1024, max_bytes - total + 1))
                    if not chunk:
                        break
                    total += len(chunk)
                    if total > max_bytes:
                        raise EvaluationError('Model download exceeded the configured byte limit.')
                    digest.update(chunk)
                    output.write(chunk)
                if digest.hexdigest() != self._context['MODEL_SHA256']:
                    raise EvaluationError('Model SHA-256 mismatch; downloaded bytes were discarded.')
                output.flush()
                os.fsync(output.fileno())
            os.replace(temp_path, target)
            temp_path = None
            return target
        except (OSError, http.client.HTTPException):
            raise EvaluationError('Model transfer or local file write failed; incomplete bytes were discarded.') from None
        finally:
            if temp_path is not None:
                temp_path.unlink(missing_ok=True)

    def report(self, metrics, slices=None, *, world_rank):
        """Explicitly send one bounded report from rank zero after aggregation.

        A valid report can remain pending until the actual job succeeds. HTTP
        failure may follow acceptance; read platform state before retrying.
        """
        if type(world_rank) is not int or world_rank != 0:
            raise ValueError('only Ray Train world rank zero may submit the aggregated report')
        if not isinstance(metrics, list) or not 1 <= len(metrics) <= 128:
            raise ValueError('report requires 1 to 128 metrics')
        if slices is not None and (not isinstance(slices, list) or len(slices) > 128):
            raise ValueError('report supports at most 128 slices')
        context = self._context
        payload = {'protocol': context['PROTOCOL'], 'evaluationId': context['ID'],
            'modelSha256': context['MODEL_SHA256'], 'datasetManifestSha256': context['DATASET_MANIFEST_SHA256'],
            'evaluatorId': context['EVALUATOR_ID'], 'configSha256': context['CONFIG_SHA256'], 'metrics': metrics}
        if slices is not None:
            payload['slices'] = slices
        try:
            body = json.dumps(payload, ensure_ascii=False, allow_nan=False).encode('utf-8')
        except (TypeError, ValueError, UnicodeError):
            raise ValueError('report must contain finite valid JSON values') from None
        if len(body) > MAX_REPORT_BYTES:
            raise ValueError('report exceeds 256 KiB')
        try:
            with self._open('report', body) as response:
                raw = response.read(MAX_REPORT_BYTES + 1)
            if len(raw) > MAX_REPORT_BYTES:
                raise EvaluationError('Evaluation report response exceeded the size limit.')
            envelope = _strict_json(raw)
        except (ValueError, UnicodeError, RecursionError, OSError, http.client.HTTPException):
            raise EvaluationError('Report response was invalid or interrupted; verify platform state before retrying.') from None
        data = envelope.get('data') if isinstance(envelope, dict) else None
        if (not isinstance(data, dict) or envelope.get('success') is not True or data.get('id') != context['ID']
                or data.get('jobId') != self.job_id or data.get('reportState') != 'VALID'):
            raise EvaluationError('Report response did not confirm this evaluation; verify platform state before retrying.')
        return data
