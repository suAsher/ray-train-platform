import unittest

from assistant_serve.engine import VLLMEngine
from assistant_serve.runtime import AssistantRuntime
from helpers import Tokenizer
from test_runtime import Gate


class NativeEngine:
    def __init__(self, *, errored=False, stopped=False, fail_during_probe=False):
        self.errored = errored
        self.is_stopped = stopped
        self.fail_during_probe = fail_during_probe
        self.probes = 0

    async def check_health(self):
        # vLLM 0.8.5 V1's method itself does not test engine liveness.
        self.probes += 1
        if self.fail_during_probe:
            self.errored = True


class EngineHealthTests(unittest.IsolatedAsyncioTestCase):
    def adapter(self, native):
        adapter = VLLMEngine.__new__(VLLMEngine)
        adapter.engine = native
        return adapter

    async def test_noop_native_health_cannot_hide_errored_or_stopped_engine(self):
        for native in (NativeEngine(errored=True), NativeEngine(stopped=True)):
            with self.subTest(native=native):
                adapter = self.adapter(native)
                with self.assertRaisesRegex(RuntimeError, "^engine_unhealthy$"):
                    await adapter.check_health()
                runtime = AssistantRuntime(adapter, Tokenizer(), Gate(True))
                self.assertFalse(await runtime.livez())
                self.assertFalse(await runtime.healthz())
                runtime.tokenizer_pool.shutdown(wait=False)

    async def test_engine_that_fails_during_probe_is_unhealthy(self):
        with self.assertRaisesRegex(RuntimeError, "^engine_unhealthy$"):
            await self.adapter(NativeEngine(fail_during_probe=True)).check_health()

    async def test_healthy_native_probe_still_runs(self):
        native = NativeEngine()
        await self.adapter(native).check_health()
        self.assertEqual(native.probes, 1)


if __name__ == "__main__":
    unittest.main()
