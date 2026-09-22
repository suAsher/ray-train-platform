import asyncio
import json
import ssl
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from assistant_serve import standalone
from assistant_serve.gate import HTTPGate


TOKEN = "synthetic-test-token-" + "a" * 48


class Runtime:
    def __init__(self):
        self.calls = 0
        self.cancelled = asyncio.Event()
        self.started = asyncio.Event()
        self.wait = False

    async def livez(self):
        return True

    async def healthz(self):
        return False

    async def complete(self, body):
        self.calls += 1
        self.started.set()
        try:
            if self.wait:
                await asyncio.Event().wait()
            return {"choices": [{"message": {"content": "synthetic answer"}}]}
        except asyncio.CancelledError:
            self.cancelled.set()
            raise


async def invoke(app, headers=(), path="/v1/chat/completions", body=b"{}", method="POST", disconnect=None):
    messages = []
    queue = asyncio.Queue()
    await queue.put({"type": "http.request", "body": body, "more_body": False})

    async def receive():
        if disconnect and queue.empty():
            await disconnect.wait()
            return {"type": "http.disconnect"}
        return await queue.get()

    async def send(message):
        messages.append(message)

    await app({"type": "http", "asgi": {"version": "3.0"}, "http_version": "1.1", "method": method,
               "scheme": "https", "path": path, "raw_path": path.encode(), "query_string": b"",
               "headers": [(b"content-type", b"application/json"), *headers],
               "server": ("test", 8443), "client": ("127.0.0.1", 1)}, receive, send)
    return messages


class StandaloneTests(unittest.IsolatedAsyncioTestCase):
    async def test_authorization_before_reading_or_generation(self):
        runtime = Runtime()
        app = standalone.create_app(runtime, TOKEN)
        for headers in ([], [(b"authorization", b"Bearer wrong")],
                        [(b"authorization", ("Bearer " + TOKEN).encode())] * 2):
            messages = await invoke(app, headers, body=b"x" * 131073)
            self.assertEqual(messages[0]["status"], 401)
            self.assertNotIn(TOKEN.encode(), b"".join(m.get("body", b"") for m in messages))
        self.assertEqual(runtime.calls, 0)

    async def test_authenticated_chat_and_only_boolean_public_health(self):
        runtime = Runtime()
        app = standalone.create_app(runtime, TOKEN)
        messages = await invoke(app, [(b"authorization", ("Bearer " + TOKEN).encode())])
        self.assertEqual(messages[0]["status"], 200)
        self.assertEqual(runtime.calls, 1)
        for path, status, payload in (("/livez", 200, {"alive": True}), ("/healthz", 503, {"ready": False})):
            messages = await invoke(app, path=path, method="GET")
            self.assertEqual(messages[0]["status"], status)
            self.assertEqual(json.loads(messages[1]["body"]), payload)
        for path in ("/docs", "/openapi.json", "/v1/models", "/api/jobs/"):
            messages = await invoke(app, path=path, method="GET")
            self.assertIn(messages[0]["status"], (401, 404))

    async def test_client_disconnect_cancels_generation(self):
        runtime = Runtime()
        runtime.wait = True
        app = standalone.create_app(runtime, TOKEN)
        gone = asyncio.Event()
        task = asyncio.create_task(invoke(app, [(b"authorization", ("Bearer " + TOKEN).encode())], disconnect=gone))
        await asyncio.wait_for(runtime.started.wait(), 1)
        gone.set()
        await asyncio.wait_for(task, 1)
        self.assertTrue(runtime.cancelled.is_set())

    async def test_real_runtime_admission_disconnect_and_gate_abort(self):
        from assistant_serve.runtime import AssistantRuntime
        from helpers import Tokenizer, request
        from test_runtime import Engine, Gate

        class ClosingGate(Gate):
            async def close(self):
                pass

        engine, gate = Engine(True), ClosingGate()
        runtime = AssistantRuntime(engine, Tokenizer(), gate)
        app = standalone.create_app(runtime, TOKEN)
        headers = [(b"authorization", ("Bearer " + TOKEN).encode())]
        gone = asyncio.Event()
        tasks = [asyncio.create_task(invoke(app, headers, body=request(), disconnect=gone)),
                 asyncio.create_task(invoke(app, headers, body=request()))]
        try:
            ids = [await asyncio.wait_for(engine.entered.get(), 1) for _ in tasks]
            busy = await invoke(app, headers, body=request())
            self.assertEqual(busy[0]["status"], 429)
            gone.set()
            first = await asyncio.wait_for(tasks[0], 1)
            self.assertEqual(first[0]["status"], 499)
            gate.opened = False
            await runtime.refresh_gate()
            second = await asyncio.wait_for(tasks[1], 1)
            self.assertEqual(second[0]["status"], 503)
            self.assertTrue(set(ids).issubset(engine.aborted))
            self.assertEqual(runtime.active, {})
            self.assertEqual(app.active, 0)
        finally:
            for task in tasks:
                task.cancel()
            await asyncio.gather(*tasks, return_exceptions=True)
            await runtime.close()

    async def test_body_limit_still_applies_after_authentication(self):
        runtime = Runtime()
        messages = await invoke(standalone.create_app(runtime, TOKEN), [(b"authorization", ("Bearer " + TOKEN).encode())], body=b"x" * 131073)
        self.assertEqual(messages[0]["status"], 413)
        self.assertEqual(runtime.calls, 0)


class SecretAndStartupTests(unittest.TestCase):
    def test_token_requires_bounded_regular_file(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "token"
            path.write_text(TOKEN + "\n")
            self.assertEqual(standalone.load_token(str(path)), TOKEN)
            for value in ("", "short", "a" * 513, TOKEN + "\nsecond-line"):
                path.write_text(value)
                with self.assertRaisesRegex(ValueError, "invalid authentication file"):
                    standalone.load_token(str(path))
            with self.assertRaises(ValueError):
                standalone.load_token(directory)

    def test_private_gate_rejects_cleartext_and_missing_trust(self):
        for url, ca in (("http://controller.ns.svc.cluster.local/gate", "/missing"),
                        ("https://controller.ns.svc.cluster.local/gate", None),
                        ("https://controller.ns.svc.cluster.local/gate", "/missing")):
            with self.assertRaises(ValueError):
                HTTPGate(url, ca_file=ca, require_tls=True)

    def test_launcher_is_one_tls_worker_without_ray(self):
        with patch.object(standalone, "tls_settings", return_value={"ssl_certfile": "/cert", "ssl_keyfile": "/key"}), \
             patch("uvicorn.Config") as config, patch("uvicorn.Server") as server:
            standalone.main()
        args = config.call_args.kwargs
        self.assertEqual(config.return_value.ssl.minimum_version, ssl.TLSVersion.TLSv1_2)
        server.return_value.run.assert_called_once()
        self.assertEqual(args["port"], 8443)
        self.assertEqual(args["workers"], 1)
        self.assertFalse(args["access_log"])
        self.assertFalse(args["proxy_headers"])
        self.assertEqual(args["ssl_keyfile"], "/key")
        self.assertNotIn("ray", standalone.__dict__)
