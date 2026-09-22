"""Private single-process HTTPS endpoint; never starts Ray management services."""
import asyncio
import hmac
import os
import re
import ssl
import stat
from pathlib import Path

from starlette.requests import ClientDisconnect, Request
from starlette.responses import JSONResponse

from .errors import ServiceError
from .http_boundary import read_body


def bounded_file(path, limit):
    """Secret projections may be symlinks; validate the opened target as regular."""
    if not Path(path).is_absolute():
        raise ValueError("invalid mounted file")
    descriptor = os.open(path, os.O_RDONLY | os.O_NONBLOCK)
    try:
        info = os.fstat(descriptor)
        if not stat.S_ISREG(info.st_mode) or not 0 < info.st_size <= limit:
            raise ValueError("invalid mounted file")
        data = os.read(descriptor, limit + 1)
        if not 0 < len(data) <= limit:
            raise ValueError("invalid mounted file")
        return data
    finally:
        os.close(descriptor)


def load_token(path):
    try:
        value = bounded_file(path, 512).decode("ascii").removesuffix("\n")
        if re.fullmatch(r"[A-Za-z0-9_-]{43,256}", value) is None:
            raise ValueError()
        return value
    except (OSError, ValueError, UnicodeError):
        raise ValueError("invalid authentication file") from None


def tls_settings():
    certificate = os.environ.get("ASSISTANT_TLS_CERT_FILE", "/run/assistant/tls/tls.crt")
    key = os.environ.get("ASSISTANT_TLS_KEY_FILE", "/run/assistant/tls/tls.key")
    try:
        bounded_file(certificate, 128 * 1024)
        bounded_file(key, 32 * 1024)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        context.load_cert_chain(certificate, key)
    except (OSError, ValueError, ssl.SSLError):
        raise ValueError("invalid mounted TLS certificate or key") from None
    return {"ssl_certfile": certificate, "ssl_keyfile": key,
            "ssl_version": ssl.PROTOCOL_TLS_SERVER}


async def complete_until_disconnect(runtime, body, receive):
    async def disconnected():
        while True:
            if (await receive())["type"] == "http.disconnect":
                return

    generation = asyncio.create_task(runtime.complete(body))
    watcher = asyncio.create_task(disconnected())
    try:
        await asyncio.wait((generation, watcher), return_when=asyncio.FIRST_COMPLETED)
        if watcher.done():
            raise ClientDisconnect()
        return await generation
    finally:
        for task in (generation, watcher):
            if not task.done():
                task.cancel()
        await asyncio.gather(generation, watcher, return_exceptions=True)


class PrivateApplication:
    def __init__(self, runtime, token, startup=None):
        self.runtime = runtime
        self.authorization = ("Bearer " + token).encode("ascii")
        self.startup = startup
        self.active = 0

    async def __call__(self, scope, receive, send):
        if scope["type"] == "lifespan":
            await self.lifespan(receive, send)
            return
        if scope["type"] != "http":
            return
        path, method = scope.get("path", ""), scope.get("method", "")
        if path in {"/livez", "/healthz"} and method == "GET":
            ready = False
            if self.runtime is not None:
                try:
                    ready = await (self.runtime.livez() if path == "/livez" else self.runtime.healthz())
                except Exception:
                    pass
            response = JSONResponse({"alive" if path == "/livez" else "ready": ready},
                                    status_code=200 if ready else 503, headers={"Cache-Control": "no-store"})
        else:
            supplied = [value for key, value in scope.get("headers", ()) if key.lower() == b"authorization"]
            if len(supplied) != 1 or not hmac.compare_digest(supplied[0], self.authorization):
                response = self.error(401, "unauthorized")
            elif path != "/v1/chat/completions" or method != "POST":
                response = self.error(404, "not_found")
            elif self.active >= 8:
                response = self.error(429, "rate_limited")
            else:
                self.active += 1
                try:
                    if self.runtime is None:
                        raise ServiceError(503, "engine_unavailable")
                    body = await read_body(Request(scope, receive))
                    result = await complete_until_disconnect(self.runtime, body, receive)
                    response = JSONResponse(result, headers={"Cache-Control": "no-store"})
                except ClientDisconnect:
                    # The peer is gone. A bounded empty response avoids logging
                    # cancellation as an application traceback at the server.
                    response = self.error(499, "client_disconnected")
                except ServiceError as error:
                    response = self.error(error.status, error.code)
                except Exception:
                    response = self.error(503, "engine_unavailable")
                finally:
                    self.active -= 1
        await response(scope, receive, send)

    @staticmethod
    def error(status, code):
        headers = {"Cache-Control": "no-store"}
        if status in {429, 503}:
            headers["Retry-After"] = "1"
        return JSONResponse({"error": {"type": code, "code": code, "message": code}},
                            status_code=status, headers=headers)

    async def lifespan(self, receive, send):
        while True:
            event = await receive()
            if event["type"] == "lifespan.startup":
                try:
                    if self.startup is not None:
                        self.runtime = self.startup()
                    await send({"type": "lifespan.startup.complete"})
                except Exception:
                    await send({"type": "lifespan.startup.failed", "message": "assistant startup failed"})
                    return
            elif event["type"] == "lifespan.shutdown":
                if self.runtime is not None:
                    try:
                        await self.runtime.close()
                    finally:
                        self.runtime.engine.close()
                await send({"type": "lifespan.shutdown.complete"})
                return


def create_app(runtime, token):
    return PrivateApplication(runtime, token)


def create_production_app():
    # Credentials and TLS settings are read only from mounted files. Rotation
    # requires replacing this Pod; environment contains paths, never key data.
    token = load_token(os.environ.get("ASSISTANT_AUTH_TOKEN_FILE", "/run/assistant/auth/token"))

    def startup():
        from .engine import VLLMEngine
        from .gate import HTTPGate
        from .runtime import AssistantRuntime
        gate = HTTPGate(os.environ.get("ASSISTANT_GATE_URL", ""),
                        ca_file=os.environ.get("ASSISTANT_GATE_CA_FILE", "/run/assistant/gate-ca/ca.crt"),
                        require_tls=True)
        engine = VLLMEngine(os.environ.get("ASSISTANT_MODEL_PATH", "/models/Qwen3-8B-AWQ"))
        runtime = AssistantRuntime(engine, engine.tokenizer, gate)
        runtime.start()
        return runtime

    return PrivateApplication(None, token, startup)


def server_config(application, factory=False, **settings):
    import uvicorn
    config = uvicorn.Config(application, factory=factory,
                            host="0.0.0.0", port=8443, workers=1, access_log=False, log_level="warning",
                            proxy_headers=False, server_header=False, timeout_keep_alive=2,
                            timeout_graceful_shutdown=15, limit_concurrency=32, backlog=32,
                            **settings)
    config.load()
    # Do not rely on OpenSSL build defaults: the pinned Uvicorn creates its
    # own context, independently of the preflight certificate validation.
    config.ssl.minimum_version = ssl.TLSVersion.TLSv1_2
    return config


def main():
    import uvicorn
    config = server_config("assistant_serve.standalone:create_production_app", factory=True, **tls_settings())
    uvicorn.Server(config).run()


if __name__ == "__main__":
    main()
