import tempfile
import unittest
from pathlib import Path

from storage_sync.checkpoint import load_json
from storage_sync.model import SyncError
from storage_sync.transport import Reporter, object_from_wire


class TransportTests(unittest.TestCase):
    def test_stale_callback_does_not_ack_file_results(self):
        with tempfile.TemporaryDirectory() as directory:
            def reject(*args):
                raise SyncError('CONTROL_PLANE_REJECTED')
            report = Reporter({'runId': 'r', 'attempt': 2, 'generation': 3, 'phase': 'TRANSFER', 'callbackUrl': 'http://internal'},
                              'secret', Path(directory), request=reject)
            report.add_file({'relativePath': 'a', 'state': 'VERIFIED'})
            with self.assertRaises(SyncError):
                report.send()
            self.assertEqual(len(report.pending_files), 1)
            receipt = load_json(Path(directory) / 'heartbeat.json')
            self.assertEqual(receipt['attempt'], 2)
            self.assertEqual(receipt['generation'], 3)
            self.assertNotIn('secret', str(receipt))

    def test_progress_sequences_and_control_response(self):
        with tempfile.TemporaryDirectory() as directory:
            calls = []
            def receive(url, token, payload):
                calls.append(payload)
                return {'control': 'CANCEL'}
            report = Reporter({'runId': 'r', 'attempt': 1, 'generation': 1, 'phase': 'TRANSFER', 'callbackUrl': 'http://internal'},
                              'secret', Path(directory), request=receive)
            report.add_file({'relativePath': 'a', 'state': 'VERIFIED'})
            report.send()
            report.finish(state='CANCELLED', requestsDrained=True)
            self.assertEqual(report.control(), 'CANCEL')
            self.assertEqual([value['sequence'] for value in calls], [1, 2])
            self.assertEqual(report.pending_files, [])
            self.assertTrue(load_json(Path(directory) / 'result.json')['requestsDrained'])

    def test_wire_camelcase_metadata(self):
        obj = object_from_wire({'key': '中文 %/file', 'size': 3, 'versionId': 'v1', 'crc64': '99', 'lastModified': 'now'})
        self.assertEqual(obj.version_id, 'v1')
        self.assertEqual(obj.last_modified, 'now')
        self.assertEqual(obj.key, '中文 %/file')


if __name__ == '__main__':
    unittest.main()

from unittest.mock import patch
from storage_sync.transport import ReadGateway


class GatewayTests(unittest.TestCase):
    def test_read_gateway_binds_every_request_to_claimed_worker(self):
        reporter = type('Reporter', (), {'token': 'token', 'worker_id': 'claimed-pod'})()
        with patch('storage_sync.transport._json_request', return_value=None) as request:
            ReadGateway('http://backend/metadata', reporter).head('bucket', 'key')
        self.assertEqual(request.call_args.args[2]['workerId'], 'claimed-pod')

import io
import json
from storage_sync.transport import _json_request
from storage_sync.checkpoint import save_json


class ProtocolBoundaryTests(unittest.TestCase):
    def test_request_envelope_and_secret_not_in_error(self):
        response = io.BytesIO(json.dumps({'success': True, 'data': {'control': 'PAUSE'}}).encode())
        with patch('urllib.request.urlopen', return_value=response) as opener:
            result = _json_request('http://internal/report', 'token', {'runId': 'r'})
        self.assertEqual(result, {'control': 'PAUSE'})
        self.assertEqual(opener.call_args.args[0].get_header('Authorization'), 'Bearer token')
        with patch('urllib.request.urlopen', side_effect=OSError('secret URL')):
            with self.assertRaisesRegex(SyncError, '^CONTROL_PLANE_UNAVAILABLE$'):
                _json_request('http://internal/report', 'token', {})

    def test_rejected_envelope_is_not_acknowledged(self):
        with patch('urllib.request.urlopen', return_value=io.BytesIO(b'{"success":false,"error":"private"}')):
            with self.assertRaisesRegex(SyncError, '^CONTROL_PLANE_REJECTED$'):
                _json_request('http://internal/report', 'token', {})

    def test_gateway_list_and_signed_read_use_conditions(self):
        reporter = type('Reporter', (), {'token': 'token', 'worker_id': 'pod'})()
        gateway = ReadGateway('http://backend/metadata', reporter)
        with patch('storage_sync.transport._json_request', return_value={'entries': [{'key': 'x', 'size': 1, 'etag': 'v'}], 'nextToken': 'next'}):
            entries, cursor = gateway.list_page('b', 'x', 'previous', 2)
        self.assertEqual(cursor, 'next')
        with patch('storage_sync.transport._json_request', return_value={'url': 'https://tos.example/key', 'headers': {'If-Match': 'v'}}) as signer:
            with patch('urllib.request.urlopen', return_value=io.BytesIO(b'x')) as opener:
                with gateway.read('b', entries[0]) as stream:
                    self.assertEqual(stream.read(), b'x')
                self.assertEqual(opener.call_args.args[0].get_header('If-match'), 'v')
        self.assertEqual(signer.call_args.args[2]['etag'], 'v')

    def test_corrupt_checkpoint_never_silently_resets(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'checkpoint.json'
            save_json(path, {'ownedUpload': 'id'})
            self.assertEqual(load_json(path), {'ownedUpload': 'id'})
            path.write_text('{truncated')
            with self.assertRaisesRegex(SyncError, '^CHECKPOINT_UNREADABLE$'):
                load_json(path)
