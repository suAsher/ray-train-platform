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
