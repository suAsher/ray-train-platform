"""Ray Serve import path: assistant_serve.app:deployment."""
import os

from fastapi import FastAPI, Request
from ray import serve
from starlette.responses import JSONResponse

from .engine import VLLMEngine
from .errors import ServiceError
from .gate import HTTPGate
from .http_boundary import read_body
from .runtime import AssistantRuntime

app = FastAPI(docs_url=None, redoc_url=None, openapi_url=None)


@serve.deployment(
    num_replicas=1,
    ray_actor_options={"num_gpus": 1, "num_cpus": 4},
    # Admission inside AssistantRuntime limits inference to two. Extra HTTP
    # slots keep liveness/readiness responsive while those generations run.
    max_ongoing_requests=8,
    max_queued_requests=1,
    graceful_shutdown_timeout_s=15,
    health_check_period_s=10,
    health_check_timeout_s=3,
    logging_config={"log_level": "WARNING", "enable_access_log": False},
)
@serve.ingress(app)
class AssistantDeployment:
    def __init__(self):
        # Ray 2.43 ingress calls this constructor synchronously. Replica
        # initialization runs in its event loop, where start creates the poller.
        gate = HTTPGate(os.environ.get("ASSISTANT_GATE_URL", ""))
        engine = VLLMEngine(os.environ.get("ASSISTANT_MODEL_PATH", "/models/Qwen3-8B-AWQ"))
        self.runtime = AssistantRuntime(engine, engine.tokenizer, gate)
        self.runtime.start()

    async def check_health(self):
        # RayService readiness must not depend on the controller's traffic gate.
        await self.runtime.check_health()

    @app.get("/livez")
    async def livez(self):
        ready = await self.runtime.livez()
        return JSONResponse({"alive": ready}, status_code=200 if ready else 503, headers={"Cache-Control": "no-store"})

    @app.get("/healthz")
    async def healthz(self):
        ready = await self.runtime.healthz()
        return JSONResponse({"ready": ready}, status_code=200 if ready else 503, headers={"Cache-Control": "no-store"})

    @app.post("/v1/chat/completions")
    async def complete(self, request: Request):
        try:
            body = await read_body(request)
            result = await self.runtime.complete(body)
            return JSONResponse(result, headers={"Cache-Control": "no-store"})
        except ServiceError as error:
            headers = {"Cache-Control": "no-store"}
            if error.status in {429, 503}:
                headers["Retry-After"] = "1"
            return JSONResponse({"error": {"type": error.code, "code": error.code, "message": error.code}},
                                status_code=error.status, headers=headers)
        except Exception:
            return JSONResponse({"error": {"type": "engine_unavailable", "message": "engine_unavailable"}},
                                status_code=503, headers={"Cache-Control": "no-store"})

    async def __del__(self):
        runtime = getattr(self, "runtime", None)
        if runtime is not None:
            await runtime.close()
            runtime.engine.close()


deployment = AssistantDeployment.bind()


if __name__ == "__main__":
    import ray
    ray.init(address=os.environ.get("RAY_ADDRESS", "auto"))
    serve.run(deployment, route_prefix="/", blocking=True)
