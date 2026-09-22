"""Container-only integration smoke: actual Ray ingress + HTTP dependencies.

Run explicitly after stdlib unit tests. It uses no cluster, GPU or socket;
only the vLLM engine and controller transport are replaced with test doubles.
"""
import asyncio
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import httpx
from ray import serve

from assistant_serve import app as service
from helpers import Tokenizer, request
from test_runtime import Engine, Gate


class LocalEngine(Engine):
    def __init__(self, path):
        super().__init__()
        self.tokenizer = Tokenizer()

    def close(self):
        pass


class LocalGate(Gate):
    def __init__(self, url):
        super().__init__()

    async def close(self):
        pass


class IngressSmoke(unittest.IsolatedAsyncioTestCase):
    async def test_real_ingress_routes_lifecycle_and_cancellation(self):
        with patch.object(service, "VLLMEngine", LocalEngine), patch.object(service, "HTTPGate", LocalGate):
            replica = service.AssistantDeployment.func_or_class()
        self.assertTrue(hasattr(replica, "runtime"), "Ray ingress must initialize the runtime synchronously")
        await replica._run_asgi_lifespan_startup()
        try:
            transport = httpx.ASGITransport(app=replica)
            with patch.object(serve, "get_replica_context", return_value=SimpleNamespace(servable_object=replica)):
                async with httpx.AsyncClient(transport=transport, base_url="http://assistant.test") as client:
                    alive = await client.get("/livez")
                    self.assertEqual(alive.status_code, 200)
                    self.assertEqual(alive.json(), {"alive": True})
                    response = await client.post("/v1/chat/completions", content=request(), headers={"Content-Type": "application/json"})
                    self.assertEqual(response.status_code, 200, response.text)
                    self.assertEqual(response.json()["choices"][0]["message"]["content"], "基于证据回答")
                    self.assertEqual((await client.get("/docs")).status_code, 404)
                    rejected = await client.post("/v1/chat/completions", content=request(tools=[]), headers={"Content-Type": "application/json"})
                    self.assertEqual(rejected.status_code, 400)
                    replica.runtime.gate.opened = False
                    self.assertEqual((await client.get("/healthz")).status_code, 503)
                    self.assertEqual((await client.get("/livez")).status_code, 200)
                    replica.runtime.gate.opened = True
                    engine = replica.runtime.engine
                    while not engine.entered.empty():
                        engine.entered.get_nowait()
                    engine.blocked = True
                    pending = asyncio.create_task(client.post("/v1/chat/completions", content=request(), headers={"Content-Type": "application/json"}))
                    request_id = await asyncio.wait_for(engine.entered.get(), 2)
                    pending.cancel()
                    with self.assertRaises(asyncio.CancelledError):
                        await pending
                    self.assertIn(request_id, engine.aborted)
                    self.assertFalse(replica.runtime.active)
        finally:
            await replica.__del__()


if __name__ == "__main__":
    unittest.main()
