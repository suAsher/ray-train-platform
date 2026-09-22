import asyncio
import unittest

from assistant_serve.errors import ServiceError
from assistant_serve.runtime import AssistantRuntime
from helpers import Tokenizer, request


class Gate:
    def __init__(self, opened=True):
        self.opened = opened
        self.epoch = "epoch-1"
        self.refreshes = 0

    async def refresh(self):
        self.refreshes += 1
        return self.opened

    def is_open(self):
        return self.opened


class Engine:
    def __init__(self, blocked=False):
        self.blocked = blocked
        self.entered = asyncio.Queue()
        self.release = asyncio.Event()
        self.aborted = []
        self.health_checks = 0

    async def generate(self, token_ids, max_tokens, request_id):
        await self.entered.put(request_id)
        if self.blocked:
            await self.release.wait()
        return {"text": "<think>private</think>基于证据回答", "completion_tokens": 12, "finish_reason": "stop"}

    async def abort(self, request_id):
        self.aborted.append(request_id)

    async def check_health(self):
        self.health_checks += 1


class RuntimeTests(unittest.IsolatedAsyncioTestCase):
    async def test_success_uses_actual_usage_and_no_reasoning(self):
        gate = Gate()
        engine = Engine()
        runtime = AssistantRuntime(engine, Tokenizer(), gate)
        response = await runtime.complete(request())
        self.assertEqual(response["choices"][0]["message"], {"role": "assistant", "content": "基于证据回答"})
        self.assertEqual(response["usage"]["completion_tokens"], 12)
        self.assertGreater(response["usage"]["prompt_tokens"], 0)
        self.assertGreater(gate.refreshes, 0)
        self.assertNotIn("reasoning_content", response["choices"][0]["message"])

    async def test_gate_closed_is_not_engine_unhealthy(self):
        engine = Engine()
        runtime = AssistantRuntime(engine, Tokenizer(), Gate(False))
        await runtime.check_health()
        self.assertTrue(await runtime.livez())
        self.assertFalse(await runtime.healthz())
        with self.assertRaises(ServiceError) as error:
            await runtime.complete(request())
        self.assertEqual(error.exception.status, 503)
        self.assertTrue(engine.entered.empty())

    async def test_only_two_generations_are_admitted(self):
        engine = Engine(True)
        runtime = AssistantRuntime(engine, Tokenizer(), Gate())
        tasks = [asyncio.create_task(runtime.complete(request())) for _ in range(2)]
        try:
            for _ in tasks:
                await asyncio.wait_for(engine.entered.get(), 1)
            with self.assertRaises(ServiceError) as error:
                await runtime.complete(request())
            self.assertEqual(error.exception.status, 429)
        finally:
            engine.release.set()
            await asyncio.gather(*tasks, return_exceptions=True)

    async def test_gate_revocation_aborts_inflight_generation(self):
        engine, gate = Engine(True), Gate()
        runtime = AssistantRuntime(engine, Tokenizer(), gate)
        task = asyncio.create_task(runtime.complete(request()))
        try:
            request_id = await asyncio.wait_for(engine.entered.get(), 1)
            gate.opened = False
            await runtime.refresh_gate()
            with self.assertRaises(ServiceError) as error:
                await asyncio.wait_for(task, 1)
            self.assertEqual(error.exception.status, 503)
            self.assertIn(request_id, engine.aborted)
        finally:
            task.cancel()
            await asyncio.gather(task, return_exceptions=True)

    async def test_gate_epoch_change_also_revokes_existing_generation(self):
        engine, gate = Engine(True), Gate()
        runtime = AssistantRuntime(engine, Tokenizer(), gate)
        task = asyncio.create_task(runtime.complete(request()))
        try:
            request_id = await asyncio.wait_for(engine.entered.get(), 1)
            gate.epoch = "epoch-2"
            await runtime.refresh_gate()
            with self.assertRaises(ServiceError):
                await asyncio.wait_for(task, 1)
            self.assertIn(request_id, engine.aborted)
        finally:
            task.cancel()
            await asyncio.gather(task, return_exceptions=True)

    async def test_client_cancellation_aborts_engine_and_releases_slot(self):
        engine = Engine(True)
        runtime = AssistantRuntime(engine, Tokenizer(), Gate())
        task = asyncio.create_task(runtime.complete(request()))
        request_id = await asyncio.wait_for(engine.entered.get(), 1)
        task.cancel()
        with self.assertRaises(asyncio.CancelledError):
            await task
        self.assertIn(request_id, engine.aborted)
        engine.release.set()
        self.assertEqual((await runtime.complete(request()))["choices"][0]["message"]["content"], "基于证据回答")


if __name__ == "__main__":
    unittest.main()
