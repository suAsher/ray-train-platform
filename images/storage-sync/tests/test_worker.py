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
