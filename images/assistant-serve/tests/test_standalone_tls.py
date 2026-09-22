"""Real local TLS socket checks with a test-only ephemeral certificate, no GPU."""
import asyncio
import os
import socket
import ssl
import subprocess
import tempfile
import unittest
from pathlib import Path
from datetime import datetime, timedelta, timezone
from starlette.responses import JSONResponse
from assistant_serve.gate import HTTPGate
from unittest.mock import patch

import httpx
import uvicorn

from assistant_serve.standalone import create_app, tls_settings, server_config
from test_standalone import Runtime, TOKEN


class PrivateTLSTests(unittest.IsolatedAsyncioTestCase):
    async def test_real_tls_validates_peer_and_requires_bearer(self):
        with tempfile.TemporaryDirectory() as directory:
            certificate, key = str(Path(directory) / "cert.pem"), str(Path(directory) / "key.pem")
            subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                            "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost,IP:127.0.0.1",
                            "-keyout", key, "-out", certificate], check=True, stdout=subprocess.DEVNULL,
                           stderr=subprocess.DEVNULL, timeout=10)
            with patch.dict(os.environ, {"ASSISTANT_TLS_CERT_FILE": certificate, "ASSISTANT_TLS_KEY_FILE": key}):
                settings = tls_settings()
            listener = socket.socket()
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
            private_app = create_app(Runtime(), TOKEN)
            async def fixture_app(scope, receive, send):
                if scope.get("path") == "/gate":
                    payload = {"allow": True, "epoch": "synthetic-test", "validUntil": (datetime.now(timezone.utc) + timedelta(seconds=3)).isoformat()}
                    await JSONResponse(payload)(scope, receive, send)
                else:
                    await private_app(scope, receive, send)
            config = server_config(fixture_app, lifespan="off", **settings)
            server = uvicorn.Server(config)
            task = asyncio.create_task(server.serve(sockets=[listener]))
            try:
                async with asyncio.timeout(5):
                    while not server.started:
                        await asyncio.sleep(0.01)
                self.assertGreaterEqual(config.ssl.minimum_version, ssl.TLSVersion.TLSv1_2)
                root = f"https://127.0.0.1:{port}"
                gate = HTTPGate(root + "/gate", ca_file=certificate, require_tls=True)
                try:
                    self.assertTrue(await gate.refresh())
                    self.assertTrue(gate.is_open())
                finally:
                    await gate.close()
                trust = ssl.create_default_context(cafile=certificate)
                async with httpx.AsyncClient(verify=trust, trust_env=False, timeout=2) as client:
                    self.assertEqual((await client.get(root + "/livez")).status_code, 200)
                    self.assertEqual((await client.post(root + "/v1/chat/completions", json={})).status_code, 401)
                    accepted = await client.post(root + "/v1/chat/completions", json={}, headers={"Authorization": "Bearer " + TOKEN})
                    self.assertEqual(accepted.status_code, 200)
                    self.assertNotIn(TOKEN, accepted.text)
                async with httpx.AsyncClient(trust_env=False, timeout=2) as untrusted:
                    with self.assertRaises(httpx.ConnectError):
                        await untrusted.get(root + "/livez")
                    with self.assertRaises(httpx.HTTPError):
                        await untrusted.get(f"http://127.0.0.1:{port}/livez")
            finally:
                server.should_exit = True
                await asyncio.wait_for(task, 5)
                listener.close()
