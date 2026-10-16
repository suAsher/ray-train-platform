"""The workbench must respond before its activity heartbeat is inspected."""
import io
import json
import unittest
from unittest.mock import patch

import editor_smoke


class Response(io.BytesIO):
    status = 200
    headers = {'Content-Type': 'text/html; charset=utf-8'}


class EditorHTTPContract(unittest.TestCase):
    def probe(self, html=b'<meta id="vscode-workbench-web-configuration">',
              status='alive', heartbeat=123):
        replies = [Response(html), Response(json.dumps(
            {'status': status, 'lastHeartbeat': heartbeat}).encode())]
        with patch.object(editor_smoke.urllib.request, 'urlopen', side_effect=replies) as opened:
            editor_smoke.check_editor_http('http://127.0.0.1:8080')
        self.assertEqual([call.args[0] for call in opened.call_args_list],
                         ['http://127.0.0.1:8080/', 'http://127.0.0.1:8080/healthz'])

    def test_real_workbench_then_active_heartbeat(self):
        self.probe()

    def test_error_page_cannot_satisfy_http_probe(self):
        with self.assertRaises(AssertionError):
            self.probe(html=b'<html>Internal server error</html>')

    def test_idle_or_uninitialized_heartbeat_is_rejected_after_workbench(self):
        for status, heartbeat in [('expired', 123), ('alive', 0)]:
            with self.subTest(status=status, heartbeat=heartbeat), self.assertRaises(AssertionError):
                self.probe(status=status, heartbeat=heartbeat)
