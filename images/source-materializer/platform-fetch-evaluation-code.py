#!/usr/bin/env python3
"""Download a fixed evaluation code snapshot using only a mounted job token.

This generic tool contains no evaluator code. It never follows redirects,
retries requests, reads a PAT, or opens user-selected object-store paths.
"""
import argparse
import base64
import hashlib
import http.client
import os
import re
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

MAX_CODE_BYTES = 64 * 1024 * 1024


class DownloadError(Exception):
    """Safe failure without upstream response content or credentials."""


class NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, message, headers, newurl):
        return None


def _endpoint(base_url, job_id):
    if not isinstance(base_url, str) or any(char.isspace() or ord(char) < 32 for char in base_url):
        raise ValueError('invalid internal code endpoint')
    parsed = urllib.parse.urlsplit(base_url)
    host = parsed.hostname or ''
    if (parsed.scheme not in ('http', 'https') or parsed.username is not None
            or parsed.password is not None or parsed.query or parsed.fragment
            or parsed.path != '/api/v1/internal'
            or not host.endswith(('.svc', '.svc.cluster.local'))):
        raise ValueError('code endpoint must be the internal cluster service')
    if parsed.port is not None and not 1 <= parsed.port <= 65535:
        raise ValueError('invalid code endpoint port')
    if not isinstance(job_id, str) or not re.fullmatch(r'job-[0-9a-f]{24}', job_id):
        raise ValueError('invalid evaluation job ID')
    return base_url + '/jobs/' + job_id + '/model-evaluation/code'


def _credential(token_file):
    try:
        with Path(token_file).open('rb') as source:
            raw = source.read(129)
    except OSError:
        raise DownloadError('mounted job credential is unavailable') from None
    if len(raw) != 43 or not re.fullmatch(rb'[A-Za-z0-9_-]{43}', raw):
        raise ValueError('invalid mounted job credential')
    decoded = base64.urlsafe_b64decode(raw + b'=')
    if len(decoded) != 32 or base64.urlsafe_b64encode(decoded).rstrip(b'=') != raw:
        raise ValueError('invalid mounted job credential')
    return raw.decode('ascii')


def download(*, base_url, job_id, token_file, sha256, size_bytes, output):
    endpoint = _endpoint(base_url, job_id)
    if type(size_bytes) is not int or not 1 <= size_bytes <= MAX_CODE_BYTES:
        raise ValueError('code size must be between 1 byte and 64 MiB')
    if not isinstance(sha256, str) or not re.fullmatch(r'[0-9a-f]{64}', sha256):
        raise ValueError('invalid code SHA-256')
    output = Path(output)
    if output.exists() or output.is_symlink():
        raise ValueError('code output already exists')
    credential = _credential(token_file)
    request = urllib.request.Request(endpoint, headers={
        'Authorization': 'Bearer ' + credential, 'Accept': 'application/zip',
    })
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirectHandler())
    temporary = None
    started = time.monotonic()
    try:
        try:
            response = opener.open(request, timeout=30)
        except urllib.error.HTTPError as error:
            error.close()
            raise DownloadError('evaluation code request rejected (HTTP %d)' % error.code) from None
        with response:
            if response.code != 200:
                raise DownloadError('evaluation code request returned an unexpected status')
            digest, total = hashlib.sha256(), 0
            with tempfile.NamedTemporaryFile(dir=output.parent, prefix='.evaluation-code-', delete=False) as destination:
                temporary = Path(destination.name)
                while True:
                    if time.monotonic() - started > 180:
                        raise DownloadError('evaluation code download exceeded its deadline')
                    chunk = response.read(min(1024 * 1024, size_bytes - total + 1))
                    if not chunk:
                        break
                    total += len(chunk)
                    if total > size_bytes:
                        raise DownloadError('evaluation code size exceeds the frozen snapshot')
                    digest.update(chunk)
                    destination.write(chunk)
                if total != size_bytes or digest.hexdigest() != sha256:
                    raise DownloadError('evaluation code size or SHA-256 does not match the frozen snapshot')
                destination.flush()
                os.fsync(destination.fileno())
        # Same-directory hard-link publication is atomic and fails if a target
        # appeared after validation, so this tool never replaces existing data.
        os.link(temporary, output)
        return output
    except (OSError, urllib.error.URLError, http.client.HTTPException):
        raise DownloadError('evaluation code transfer or local write failed') from None
    finally:
        if temporary is not None:
            try:
                temporary.unlink(missing_ok=True)
            except OSError:
                raise DownloadError('evaluation code temporary file cleanup failed') from None


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-url', required=True)
    parser.add_argument('--job-id', required=True)
    parser.add_argument('--token-file', required=True)
    parser.add_argument('--sha256', required=True)
    parser.add_argument('--size-bytes', type=int, required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args(argv)
    try:
        download(**vars(args))
        return 0
    except (DownloadError, ValueError):
        print('Evaluation code materialization failed; check job state and its fixed code snapshot.', file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
