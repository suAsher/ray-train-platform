import unittest

from assistant_serve.errors import ServiceError
from assistant_serve.http_boundary import read_body
from assistant_serve.protocol import BODY_LIMIT


class Request:
    def __init__(self, chunks, **headers):
        self.headers = {"content-type": "application/json", **headers}
        self.chunks = chunks
        self.read = False

    async def stream(self):
        self.read = True
        for chunk in self.chunks:
            yield chunk


class HTTPBoundaryTests(unittest.IsolatedAsyncioTestCase):
    async def test_incremental_body_limit_applies_without_content_length(self):
        request = Request([b"x" * BODY_LIMIT, b"x"])
        with self.assertRaises(ServiceError) as raised:
            await read_body(request)
        self.assertEqual(raised.exception.status, 413)

    async def test_rejects_bad_headers_before_reading(self):
        for headers in ({"content-type": "text/plain"}, {"content-length": str(BODY_LIMIT + 1)}, {"content-length": "invalid"}):
            request = Request([b"{}"], **headers)
            with self.assertRaises(ServiceError):
                await read_body(request)
            self.assertFalse(request.read)

    async def test_small_json_body_is_preserved(self):
        self.assertEqual(await read_body(Request([b'{"x":', b'1}'])), b'{"x":1}')


if __name__ == "__main__":
    unittest.main()
