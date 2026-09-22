"""Engine-independent lifecycle and admission limits; no scheduling authority."""
import asyncio
import time
import uuid
from dataclasses import dataclass
from concurrent.futures import ThreadPoolExecutor

from .errors import ServiceError
from .protocol import MODEL_ID, answer_text, prepare_request


@dataclass
class _Active:
    task: object
    epoch: object = None
    revoked: bool = False
    submitted: bool = False


class AssistantRuntime:
    def __init__(self, engine, tokenizer, gate):
        self.engine, self.tokenizer, self.gate = engine, tokenizer, gate
        self.active = {}
        self.poller = None
        self.tokenizer_pool = ThreadPoolExecutor(max_workers=1, thread_name_prefix="assistant-tokenizer")

    def start(self):
        if self.poller is None:
            self.poller = asyncio.create_task(self._poll())

    async def _poll(self):
        while True:
            next_poll = time.monotonic() + 1
            await self.refresh_gate()
            await asyncio.sleep(max(0, next_poll - time.monotonic()))

    async def refresh_gate(self):
        try:
            opened = await self.gate.refresh()
        except asyncio.CancelledError:
            raise
        except Exception:
            opened = False
        for request_id, active in list(self.active.items()):
            if active.epoch is not None and (not opened or not self.gate.is_open() or active.epoch != self.gate.epoch):
                active.revoked = True
                active.task.cancel()
                if active.submitted:
                    await self._abort(request_id)
        return opened and self.gate.is_open()

    async def _abort(self, request_id):
        try:
            await asyncio.wait_for(self.engine.abort(request_id), 0.5)
        except Exception:
            # Readiness must not recover after an engine that cannot abort.
            self.abort_failed = True

    async def complete(self, body):
        # Bound preprocessing, gate lookups and generation together. Reserving
        # the slot before any await prevents bursts from creating tokenizer work.
        if len(self.active) >= 2:
            raise ServiceError(429, "rate_limited")
        request_id = "chatcmpl-" + uuid.uuid4().hex
        active = _Active(asyncio.current_task())
        self.active[request_id] = active
        completed = False
        try:
            async with asyncio.timeout(11):
                if not await self.refresh_gate() or getattr(self, "abort_failed", False):
                    raise ServiceError(503, "gate_closed")
                active.epoch = self.gate.epoch
                prepared = await asyncio.get_running_loop().run_in_executor(self.tokenizer_pool, prepare_request, body, self.tokenizer)
                if active.revoked or not self.gate.is_open():
                    raise ServiceError(503, "gate_closed")
                active.submitted = True
                output = await self.engine.generate(prepared.token_ids, prepared.max_tokens, request_id)
                if active.revoked or not self.gate.is_open() or active.epoch != self.gate.epoch:
                    raise ServiceError(503, "gate_closed")
                answer = answer_text(output.get("text"))
                if not answer:
                    raise ServiceError(503, "no_answer")
                completion_tokens = output.get("completion_tokens")
                if type(completion_tokens) is not int or not 0 <= completion_tokens <= prepared.max_tokens:
                    raise ServiceError(503, "engine_unavailable")
                completed = True
                return self._response(request_id, answer, prepared, output, completion_tokens)
        except asyncio.CancelledError:
            if active.revoked:
                raise ServiceError(503, "gate_closed") from None
            raise
        except TimeoutError:
            raise ServiceError(504, "inference_timeout") from None
        except ServiceError:
            raise
        except Exception:
            raise ServiceError(503, "engine_unavailable") from None
        finally:
            try:
                if not completed and active.submitted:
                    await self._abort(request_id)
            finally:
                self.active.pop(request_id, None)

    @staticmethod
    def _response(request_id, answer, prepared, output, completion_tokens):
        prompt_tokens = len(prepared.token_ids)
        finish = "length" if output.get("finish_reason") == "length" else "stop"
        return {
            "id": request_id, "object": "chat.completion", "created": int(time.time()), "model": MODEL_ID,
            "choices": [{"index": 0, "message": {"role": "assistant", "content": answer}, "finish_reason": finish}],
            "usage": {"prompt_tokens": prompt_tokens, "completion_tokens": completion_tokens, "total_tokens": prompt_tokens + completion_tokens},
            "raytrain": {"evidenceTruncated": prepared.evidence_truncated, "evidenceIds": list(prepared.evidence_ids)},
        }

    async def check_health(self):
        # Never consult gate here: RayService readiness is an input to the gate.
        if getattr(self, "abort_failed", False):
            raise RuntimeError("engine_unhealthy")
        await asyncio.wait_for(self.engine.check_health(), 1)

    async def livez(self):
        try:
            await self.check_health()
            return True
        except Exception:
            return False

    async def healthz(self):
        return await self.livez() and await self.refresh_gate()

    async def close(self):
        if self.poller is not None:
            self.poller.cancel()
            await asyncio.gather(self.poller, return_exceptions=True)
        for request_id, active in list(self.active.items()):
            active.revoked = True
            active.task.cancel()
            await self._abort(request_id)
        self.tokenizer_pool.shutdown(wait=False, cancel_futures=True)
        await self.gate.close()
