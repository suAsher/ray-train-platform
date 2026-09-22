import asyncio
import json
import sys
from datetime import datetime, timedelta, timezone
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from assistant_serve.gate import HTTPGate


class Response:
    def __init__(self, status=200, body=None):
        self.status_code = status
        self.body = body if body is not None else json.dumps({"allow": True, "epoch": "e1", "validUntil": (datetime.now(timezone.utc) + timedelta(seconds=30)).isoformat()}).encode()

    async def __aenter__(self):
        return self

    async def __aexit__(self, *_):
        return None

    async def aiter_bytes(self):
        yield self.body


class Client:
    def __init__(self, **options):
        self.options = options
        self.response = Response()
        self.requests = []

    def stream(self, method, url):
        self.requests.append((method, url))
        return self.response

    async def aclose(self):
        pass


class GateTransportTests(unittest.IsolatedAsyncioTestCase):
    def make_gate(self):
        module = SimpleNamespace(AsyncClient=Client, Limits=lambda **kwargs: kwargs)
        with patch.dict(sys.modules, {"httpx": module}):
            return HTTPGate("http://controller.ns.svc.cluster.local:8080/gate")

    async def test_transport_never_uses_proxy_or_redirects(self):
        gate = self.make_gate()
        self.assertFalse(gate.client.options["trust_env"])
        self.assertFalse(gate.client.options["follow_redirects"])
        self.assertEqual(gate.client.options["timeout"], 0.5)
        self.assertTrue(await gate.refresh())
        self.assertEqual(gate.client.requests, [("GET", gate.url)])
        for response in (Response(302), Response(500), Response(body=b"x" * 4097), Response(body=b"not JSON")):
            gate.client.response = response
            self.assertFalse(await gate.refresh())
            self.assertFalse(gate.is_open())

    async def test_gate_timeout_is_total_even_when_waiting_for_lock(self):
        gate = self.make_gate()
        self.assertTrue(await gate.refresh())
        await gate.lock.acquire()
        try:
            self.assertFalse(await asyncio.wait_for(gate.refresh(), 0.7))
            self.assertFalse(gate.is_open())
        finally:
            gate.lock.release()


if __name__ == "__main__":
    unittest.main()
