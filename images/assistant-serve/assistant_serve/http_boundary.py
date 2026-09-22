"""HTTP framing stays bounded before JSON parsing or tokenization starts."""
import asyncio

from .errors import ServiceError
from .protocol import BODY_LIMIT


async def read_body(request):
    media_type = request.headers.get("content-type", "").split(";", 1)[0].strip().lower()
    if media_type != "application/json":
        raise ServiceError(415, "invalid_content_type")
    declared = request.headers.get("content-length")
    if declared is not None:
        try:
            length = int(declared)
        except ValueError:
            raise ServiceError(400, "invalid_request") from None
        if length < 0:
            raise ServiceError(400, "invalid_request")
        if length > BODY_LIMIT:
            raise ServiceError(413, "request_too_large")
    try:
        async with asyncio.timeout(5):
            body = bytearray()
            async for chunk in request.stream():
                if len(body) + len(chunk) > BODY_LIMIT:
                    raise ServiceError(413, "request_too_large")
                body.extend(chunk)
            return bytes(body)
    except TimeoutError:
        raise ServiceError(408, "body_timeout") from None
