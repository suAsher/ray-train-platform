"""A fail-closed reader of the controller's fixed, read-only HTTP gate."""
import asyncio
import re
import time
from dataclasses import dataclass
from datetime import datetime
from urllib.parse import urlsplit

from .protocol import strict_json


@dataclass(frozen=True)
class _Snapshot:
    epoch: str = ""
    expires: float = 0
    allow: bool = False


class GateState:
    def __init__(self):
        self._snapshot = _Snapshot()

    @property
    def epoch(self):
        return self._snapshot.epoch

    def update(self, payload, now_wall, now_monotonic):
        self._snapshot = _Snapshot()
        try:
            if not isinstance(payload, dict) or set(payload) != {"allow", "epoch", "validUntil"}:
                return False
            if type(payload["allow"]) is not bool or not isinstance(payload["epoch"], str) or not 1 <= len(payload["epoch"]) <= 128:
                return False
            if not isinstance(payload["validUntil"], str) or not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})", payload["validUntil"]):
                return False
            until = datetime.fromisoformat(payload["validUntil"].replace("Z", "+00:00")).timestamp()
            ttl = min(3.0, until - now_wall)
            if ttl <= 0:
                return False
            self._snapshot = _Snapshot(payload["epoch"], now_monotonic + ttl, payload["allow"])
            return self.is_open(now_monotonic)
        except (ValueError, TypeError, OverflowError):
            return False

    def is_open(self, now_monotonic):
        return self._snapshot.allow and now_monotonic < self._snapshot.expires


def validate_gate_url(url):
    try:
        parsed = urlsplit(url)
        host = parsed.hostname or ""
        internal = host in {"localhost", "127.0.0.1", "::1"} or host.endswith(".svc.cluster.local")
        if not internal or parsed.scheme not in {"http", "https"} or parsed.path != "/gate":
            raise ValueError("invalid fixed gate URL")
        if parsed.username is not None or parsed.password is not None or parsed.query or parsed.fragment or "?" in url or "#" in url:
            raise ValueError("invalid fixed gate URL")
        if any(ord(char) < 33 for char in url) or len(url) > 2048:
            raise ValueError("invalid fixed gate URL")
        _ = parsed.port
        return url
    except (ValueError, TypeError):
        raise ValueError("invalid fixed gate URL") from None


class HTTPGate:
    def __init__(self, url):
        import httpx
        self.url = validate_gate_url(url)
        self.state = GateState()
        self.lock = asyncio.Lock()
        self.client = httpx.AsyncClient(timeout=0.5, follow_redirects=False, trust_env=False,
                                        limits=httpx.Limits(max_connections=1, max_keepalive_connections=1))

    @property
    def epoch(self):
        return self.state.epoch

    def is_open(self):
        return self.state.is_open(time.monotonic())

    async def refresh(self):
        try:
            # Includes time waiting for the lock: concurrent health/query
            # callers cannot postpone revocation beyond the request timeout.
            payload = await asyncio.wait_for(self._serialized_fetch(), 0.5)
        except asyncio.CancelledError:
            self.state.update(None, time.time(), time.monotonic())
            raise
        except Exception:
            payload = None
        return self.state.update(payload, time.time(), time.monotonic())

    async def _serialized_fetch(self):
        async with self.lock:
            return await self._fetch()

    async def _fetch(self):
        async with self.client.stream("GET", self.url) as response:
            if response.status_code != 200:
                return None
            body = bytearray()
            async for chunk in response.aiter_bytes():
                body.extend(chunk)
                if len(body) > 4096:
                    return None
            return strict_json(bytes(body).decode("utf-8"))

    async def close(self):
        await self.client.aclose()
