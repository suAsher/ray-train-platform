import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from storage_sync.model import SyncError
from storage_sync.transport import Reporter
from storage_sync.worker import run


class StartupTests(unittest.TestCase):
    def test_duplicate_pod_claim_rejected_without_files_or_writer(self):
        with tempfile.TemporaryDirectory() as directory:
            calls = []
            class DeniedReporter(Reporter):
                def claim(self):
                    calls.append('claim')
                    raise SyncError('ATTEMPT_ALREADY_CLAIMED')
                def start(self):
                    calls.append('start')
                def finish(self, **fields):
                    calls.append('finish')
            spec = {'runId': 'r', 'attempt': 1, 'generation': 1, 'phase': 'TRANSFER', 'callbackUrl': 'http://backend/report'}
            with patch('storage_sync.worker.from_config') as writer:
                code = run(spec, 'secret', Path(directory), '/credentials', reporter_factory=DeniedReporter)
            self.assertEqual(code, 2)
            self.assertEqual(calls, ['claim'])
            writer.assert_not_called()
            self.assertEqual(list(Path(directory).iterdir()), [])

    def test_claim_and_every_report_include_pod_uid(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {'STORAGE_SYNC_POD_UID': 'pod-uid'}):
            requests = []
            reporter = Reporter({'runId': 'r', 'attempt': 2, 'generation': 3, 'phase': 'PREVIEW', 'callbackUrl': 'http://backend/path/report'},
                                'secret', Path(directory), request=lambda url, token, body: requests.append((url, body)) or {})
            reporter.claim()
            reporter.send()
            self.assertEqual(requests[0][0], 'http://backend/path/claim')
            self.assertEqual(requests[0][1]['workerId'], 'pod-uid')
            self.assertEqual(requests[1][1]['workerId'], 'pod-uid')


if __name__ == '__main__':
    unittest.main()

from dataclasses import asdict
from test_engine import MemoryStore
from storage_sync.checkpoint import load_json, save_json
from storage_sync.engine import make_plan, scan_tos
from storage_sync.worker import _preview, _transfer, manifest_summary


class RecordingReporter:
    def __init__(self):
        self.values = {}
        self.events = []
        self.files = []
        self.command = ''
        self.writer = None
    def update(self, **fields):
        self.values = {**self.values, **fields}
        self.events.append(self.values)
    def control(self):
        return self.command
    def add_file(self, value):
        self.files.append(value)


def work_spec(mappings=None):
    return {'runId': 'r', 'attempt': 1, 'generation': 1, 'phase': 'PREVIEW',
            'config': {'mode': 'INCREMENTAL', 'verification': 'CONTENT', 'conflictPolicy': 'UPDATE'},
            'mappings': mappings or [{'source': {'kind': 'TOS', 'bucket': 'src', 'prefix': 'data'},
                                     'destination': {'bucket': 'dst', 'prefix': 'out/data'}, 'layout': 'DIRECTORY'}]}


class WorkerFlowTests(unittest.TestCase):
    def test_resolved_directory_prefix_is_not_appended_twice(self):
        with tempfile.TemporaryDirectory() as directory:
            store = MemoryStore({('src', 'data/a'): b'a'})
            reporter = RecordingReporter()
            _preview(work_spec(), store, reporter, Path(directory))
            manifest = load_json(Path(directory) / 'manifest.json')
            self.assertEqual(manifest['plans'][0]['entries'][0]['target_key'], 'out/data/a')
            self.assertEqual(reporter.values['mappingProgress'][0]['progress']['sourceFiles'], 1)

    def test_resolved_file_prefix_is_exact_key(self):
        with tempfile.TemporaryDirectory() as directory:
            store = MemoryStore({('src', 'data/a'): b'a'})
            spec = work_spec([{'source': {'kind': 'TOS', 'bucket': 'src', 'prefix': 'data/a'},
                              'destination': {'bucket': 'dst', 'prefix': 'out/a'}, 'layout': 'DIRECTORY'}])
            reporter = RecordingReporter()
            _preview(spec, store, reporter, Path(directory))
            manifest = load_json(Path(directory) / 'manifest.json')
            self.assertEqual(manifest['plans'][0]['entries'][0]['target_key'], 'out/a')

    def test_preview_expired_or_target_changed_before_start_has_no_writes(self):
        with tempfile.TemporaryDirectory() as directory:
            store = MemoryStore({('src', 'data/a'): b'a'})
            reporter = RecordingReporter()
            spec = work_spec()
            _preview(spec, store, reporter, Path(directory))
            accepted = {key: reporter.values[key] for key in ('manifestDigest', 'sourceFingerprint', 'targetFingerprint')}
            store.objects['dst', 'out/data/a'] = b'changed'
            with self.assertRaisesRegex(SyncError, 'PREVIEW_SNAPSHOT_CHANGED'):
                _preview({**spec, **accepted}, store, RecordingReporter(), Path(directory))
            self.assertEqual(store.writes, [])

    def test_scheduled_preflight_conflict_has_no_writes(self):
        with tempfile.TemporaryDirectory() as directory:
            store = MemoryStore({('src', 'data/a'): b'a', ('dst', 'out/data/a'): b'other'})
            spec = work_spec()
            spec = {**spec, 'config': {**spec['config'], 'conflictPolicy': 'FAIL'}}
            with self.assertRaisesRegex(SyncError, 'TARGET_CONFLICT'):
                _preview(spec, store, RecordingReporter(), Path(directory))
            self.assertEqual(store.writes, [])

    def test_multimapping_totals_frozen_and_file_results_delivered(self):
        with tempfile.TemporaryDirectory() as directory:
            store = MemoryStore({('src', 'one/a'): b'a', ('src', 'two/b'): b'bb'})
            plans = [make_plan(scan_tos(store, 'src', source, True, 'CONTENT'), store, 'dst', target)
                     for source, target in (('one', 'out/one'), ('two', 'out/two'))]
            manifest, summary = manifest_summary(plans)
            save_json(Path(directory) / 'manifest.json', manifest)
            spec = {**work_spec(), **summary, 'phase': 'TRANSFER'}
            reporter = RecordingReporter()
            with patch('storage_sync.worker.from_config', return_value=store):
                _transfer(spec, reporter, Path(directory), '/unused')
            self.assertTrue(all(event['progress']['sourceFiles'] == 2 for event in reporter.events if 'progress' in event))
            self.assertTrue(all(event['progress']['transferBytes'] == 3 for event in reporter.events if 'progress' in event))
            self.assertEqual(reporter.values['progress']['verifiedFiles'], 2)
            self.assertEqual(len(reporter.files), 2)
            self.assertEqual([item['progress']['verifiedFiles'] for item in reporter.values['mappingProgress']], [1, 1])

    def test_transfer_manifest_tamper_before_credentials(self):
        with tempfile.TemporaryDirectory() as directory:
            save_json(Path(directory) / 'manifest.json', {'plans': []})
            with patch('storage_sync.worker.from_config') as writer, self.assertRaisesRegex(SyncError, 'MANIFEST_MISMATCH'):
                _transfer({**work_spec(), 'manifestDigest': 'wrong'}, RecordingReporter(), Path(directory), '/unused')
            writer.assert_not_called()


class RecoveryTests(unittest.TestCase):
    def spec(self):
        return {'runId': 'ssr-12345678-1234-1234-1234-123456789012', 'attempt': 2, 'generation': 4,
                'phase': 'RECOVER', 'callbackUrl': 'http://backend/report',
                'checkpointRef': '/work/ssr-12345678-1234-1234-1234-123456789012'}

    def test_recovery_replays_exact_original_receipt_without_claim_or_writer(self):
        spec = self.spec()
        receipt = {'runId': spec['runId'], 'attempt': 2, 'generation': 4, 'sequence': 19,
                   'workerId': 'original-pod', 'state': 'FAILED', 'phase': 'VERIFYING', 'requestsDrained': False,
                   'failureReason': 'UNKNOWN_WRITE_OUTCOME'}
        with (patch('storage_sync.worker.load_json', return_value=receipt), patch('storage_sync.worker._json_request', create=True) as request,
              patch('storage_sync.worker.Reporter') as reporter, patch('storage_sync.worker.from_config') as writer):
            code = run(spec, 'fresh-scoped-token', Path(spec['checkpointRef']))
        self.assertEqual(code, 0)
        self.assertEqual(request.call_args.args[2], receipt)
        reporter.assert_not_called()
        writer.assert_not_called()

    def test_recovery_rejects_wrong_attempt_or_nonterminal_or_wrong_path(self):
        spec = self.spec()
        base = {'runId': spec['runId'], 'attempt': 2, 'generation': 4, 'sequence': 19,
                'workerId': 'original', 'state': 'SUCCEEDED', 'requestsDrained': True}
        cases = [({**base, 'attempt': 1}, spec['checkpointRef']),
                 ({**base, 'state': 'RUNNING'}, spec['checkpointRef']), (base, '/work/other')]
        for receipt, path in cases:
            with (self.subTest(path=path, receipt=receipt), patch('storage_sync.worker.load_json', return_value=receipt),
                  patch('storage_sync.worker._json_request', create=True) as request):
                self.assertEqual(run(spec, 'fresh', Path(path)), 2)
                request.assert_not_called()


class WorkerLifecycleTests(unittest.TestCase):
    def transfer(self, store, directory, response=None):
        plan = make_plan(scan_tos(store, 'src', 'data', True, 'CONTENT'), store, 'dst', 'out', verification='CONTENT')
        manifest, summary = manifest_summary([plan])
        save_json(Path(directory) / 'manifest.json', manifest)
        spec = {**work_spec(), **summary, 'phase': 'TRANSFER', 'callbackUrl': 'http://backend/report'}
        calls = []
        def send(url, token, payload):
            calls.append(payload)
            return response(url, payload) if response else {}
        factory = lambda request, token, work: Reporter(request, token, work, request=send)
        with patch.dict(os.environ, {'STORAGE_SYNC_POD_UID': 'claimed-pod'}), patch('storage_sync.worker.from_config', return_value=store):
            code = run(spec, 'token', Path(directory), '/unused', reporter_factory=factory)
        return code, calls, load_json(Path(directory) / 'result.json')

    def test_success_requires_verified_terminal_receipt_and_claim(self):
        with tempfile.TemporaryDirectory() as directory:
            store = MemoryStore({('src', 'data/a'): b'a'}); store.uncertain_write = False
            code, calls, receipt = self.transfer(store, directory)
            self.assertEqual(code, 0)
            self.assertEqual(calls[0]['workerId'], 'claimed-pod')
            self.assertNotIn('sequence', calls[0])
            self.assertEqual(receipt['state'], 'SUCCEEDED')
            self.assertEqual(receipt['progress']['verifiedFiles'], 1)
            self.assertTrue(receipt['requestsDrained'])

    def test_pause_response_before_transfer_preserves_target(self):
        with tempfile.TemporaryDirectory() as directory:
            store = MemoryStore({('src', 'data/a'): b'a'}); store.uncertain_write = False
            code, calls, receipt = self.transfer(store, directory, lambda url, payload: {'control': 'PAUSE'} if url.endswith('/report') else {})
            self.assertEqual(code, 2)
            self.assertEqual(receipt['state'], 'PAUSED')
            self.assertTrue(receipt['requestsDrained'])
            self.assertEqual(store.writes, [])

    def test_unknown_write_outcome_keeps_drain_false(self):
        class Uncertain(MemoryStore):
            def copy(self, *args, **kwargs):
                self.uncertain_write = True
                raise SyncError('UNKNOWN_WRITE_OUTCOME')
        with tempfile.TemporaryDirectory() as directory:
            store = Uncertain({('src', 'data/a'): b'a'}); store.uncertain_write = False
            code, calls, receipt = self.transfer(store, directory)
            self.assertEqual(code, 2)
            self.assertEqual(receipt['state'], 'FAILED')
            self.assertEqual(receipt['failureReason'], 'UNKNOWN_WRITE_OUTCOME')
            self.assertFalse(receipt['requestsDrained'])

    def test_lost_final_callback_leaves_durable_receipt_and_nonzero_exit(self):
        def response(url, payload):
            if payload.get('state') == 'SUCCEEDED':
                raise SyncError('CONTROL_PLANE_UNAVAILABLE')
            return {}
        with tempfile.TemporaryDirectory() as directory:
            store = MemoryStore({('src', 'data/a'): b'a'}); store.uncertain_write = False
            code, calls, receipt = self.transfer(store, directory, response)
            self.assertEqual(code, 2)
            self.assertEqual(receipt['state'], 'SUCCEEDED')
            self.assertTrue(receipt['requestsDrained'])
            self.assertEqual(receipt['workerId'], 'claimed-pod')

    def test_browse_readonly_phase_never_initializes_writer(self):
        spec = {**work_spec(), 'phase': 'BROWSE', 'callbackUrl': 'http://backend/report', 'metadataUrl': 'http://backend/metadata', 'limit': 2}
        gateway = type('Gateway', (), {'request': lambda self, *args, **kwargs: {'entries': [{'name': 'a', 'relativePath': 'a', 'kind': 'FILE', 'sizeBytes': 1}], 'nextToken': 'next'}})()
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {'STORAGE_SYNC_POD_UID': 'pod'}):
            factory = lambda request, token, work: Reporter(request, token, work, request=lambda *args: {})
            with patch('storage_sync.worker.ReadGateway', return_value=gateway), patch('storage_sync.worker.from_config') as writer:
                self.assertEqual(run(spec, 'token', Path(directory), reporter_factory=factory), 0)
            writer.assert_not_called()
            receipt = load_json(Path(directory) / 'result.json')
            self.assertEqual(receipt['nextCursor'], 'next')
            self.assertEqual(len(receipt['browseEntries']), 1)

    def test_readonly_preview_persists_manifest_and_verified_fingerprints(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {'STORAGE_SYNC_POD_UID': 'pod'}):
            store = MemoryStore({('src', 'data/a'): b'a'})
            spec = {**work_spec(), 'callbackUrl': 'http://backend/report', 'metadataUrl': 'http://backend/metadata'}
            factory = lambda request, token, work: Reporter(request, token, work, request=lambda *args: {})
            with patch('storage_sync.worker.ReadGateway', return_value=store), patch('storage_sync.worker.from_config') as writer:
                self.assertEqual(run(spec, 'token', Path(directory), reporter_factory=factory), 0)
            writer.assert_not_called()
            receipt = load_json(Path(directory) / 'result.json')
            self.assertTrue(receipt['manifestDigest'])
            self.assertTrue(receipt['sourceFingerprint'])
            self.assertTrue(receipt['targetFingerprint'])
            self.assertTrue((Path(directory) / 'manifest.json').exists())
