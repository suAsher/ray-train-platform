"""Authenticated control-plane transport, independent of writable credentials."""
import json
import os
import threading
import urllib.error
import urllib.request
from .checkpoint import save_json
from .model import ObjectInfo, SyncError


def _json_request(url, token, payload, timeout=30):
    request = urllib.request.Request(url, data=json.dumps(payload).encode(),
                                     headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            value = json.load(response)
    except Exception:
        raise SyncError('CONTROL_PLANE_UNAVAILABLE') from None
    if isinstance(value, dict) and value.get('success') is False:
        raise SyncError('CONTROL_PLANE_REJECTED')
    return value.get('data', value) if isinstance(value, dict) else value


def object_from_wire(value):
    if value is None:
        return None
    aliases = {'versionId': 'version_id', 'lastModified': 'last_modified', 'contentType': 'content_type',
               'contentEncoding': 'content_encoding', 'contentDisposition': 'content_disposition',
               'contentLanguage': 'content_language', 'cacheControl': 'cache_control'}
    fields = {aliases.get(key, key): val for key, val in value.items()}
    return ObjectInfo(**{key: val for key, val in fields.items() if key in ObjectInfo.__dataclass_fields__})


class ReadGateway:
    def __init__(self, url, reporter):
        self.url, self.reporter = url, reporter

    def request(self, operation, **values):
        return _json_request(self.url, self.reporter.token, {'operation': operation, **values})

    def head(self, bucket, key):
        return object_from_wire(self.request('head', bucket=bucket, key=key))

    def list_page(self, bucket, prefix, token='', limit=1000):
        value = self.request('list', bucket=bucket, prefix=prefix, continuationToken=token, limit=limit)
        return [object_from_wire(item) for item in value['entries']], value.get('nextToken', '')

    def read(self, bucket, obj):
        value = self.request('read', bucket=bucket, key=obj.key, etag=obj.etag, versionId=obj.version_id)
        try:
            request = urllib.request.Request(value['url'], headers=value.get('headers') or {})
            return urllib.request.urlopen(request, timeout=60)
        except Exception:
            raise SyncError('SOURCE_READ_FAILED') from None


class Reporter:
    """One serial callback stream, durable before send; heartbeat contains no secrets."""
    def __init__(self, spec, token, work_dir, interval=5, request=_json_request):
        self.spec, self.token, self.work_dir = spec, token, work_dir
        self.interval, self.request = interval, request
        self.lock = threading.RLock()
        self.stop = threading.Event()
        self.command = ''
        self.sequence = 0
        self.payload = {'state': 'RUNNING', 'phase': spec['phase'], 'progress': {}}
        self.thread = None
        self.failure = None
        self.pending_files = []
        self.worker_id = os.environ.get('STORAGE_SYNC_POD_UID', '')

    def claim(self):
        if not self.worker_id or not self.spec['callbackUrl'].endswith('/report'):
            raise SyncError('WORKER_CLAIM_UNAVAILABLE')
        self.request(self.spec['callbackUrl'][:-len('/report')] + '/claim', self.token,
                     {'runId': self.spec['runId'], 'attempt': self.spec['attempt'],
                      'generation': self.spec['generation'], 'workerId': self.worker_id})

    def control(self):
        return self.command

    def update(self, **fields):
        with self.lock:
            self.payload = {**self.payload, **fields}

    def add_file(self, value):
        with self.lock:
            self.pending_files = [*self.pending_files, value]
            if len(self.pending_files) >= 500:
                self.send()

    def send(self, final=False):
        with self.lock:
            self.sequence += 1
            value = {**self.payload, 'runId': self.spec['runId'], 'attempt': self.spec['attempt'],
                     'generation': self.spec['generation'], 'sequence': self.sequence,
                     'fileResults': self.pending_files[:1000], 'workerId': self.worker_id}
            if self.spec.get('previewId'):
                value['previewId'] = self.spec['previewId']
            save_json(self.work_dir / ('result.json' if final else 'heartbeat.json'), value)
            response = self.request(self.spec['callbackUrl'], self.token, value) or {}
            self.pending_files = self.pending_files[len(value['fileResults']):]
            command = response.get('control')
            if command in ('PAUSE', 'CANCEL'):
                self.command = command
            if response.get('callbackToken'):
                self.token = response['callbackToken']

    def start(self):
        self.send()
        self.thread = threading.Thread(target=self._heartbeat, name='storage-sync-heartbeat', daemon=False)
        self.thread.start()

    def _heartbeat(self):
        while not self.stop.wait(self.interval):
            try:
                self.send()
            except SyncError as error:
                self.failure = error
                self.command = self.command or 'PAUSE'

    def finish(self, **fields):
        self.stop.set()
        if self.thread:
            self.thread.join()
        self.update(**fields)
        self.send(final=True)
