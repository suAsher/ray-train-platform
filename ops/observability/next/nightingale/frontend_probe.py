#!/usr/bin/env python3
"""Check the locked UI over the isolated harness's authenticated HTTPS route."""

import hashlib
from html.parser import HTMLParser
import json
import re
import ssl
import sys
import urllib.error
import urllib.parse
import urllib.request


def require(condition, message):
    if not condition:
        raise ValueError(message)


class Scripts(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.sources = []

    def handle_starttag(self, tag, attrs):
        if tag == "script":
            self.sources.extend(value for key, value in attrs if key == "src" and value)


def local_script_path(path):
    require(isinstance(path, str) and re.fullmatch(r"/[A-Za-z0-9_./@-]+\.js", path)
            and not path.startswith("//") and ".." not in path.split("/"),
            "expected one local JavaScript path without authority, query or traversal")
    return path


def verify_frontend(fetch, lock):
    expected = lock["frontend_javascript"]
    path = local_script_path(expected["path"])
    page = fetch("/", 128 * 1024, True)
    require(page["status"] == 200 and page["content_type"] == "text/html",
            "homepage is not a successful HTML response")
    require(page["sha256"] == lock["frontend_index_html_sha256"],
            "homepage differs from the pinned original binary")
    parser = Scripts()
    parser.feed(page["body"])
    require(path in parser.sources, "locked JavaScript is not referenced by the actual homepage")
    asset = fetch(path, 32 * 1024 * 1024, False)
    require(asset["status"] == 200 and asset["content_type"] in
            ("text/javascript", "application/javascript", "application/x-javascript"),
            "referenced JavaScript did not load as JavaScript")
    require(asset["bytes"] == expected["bytes"] and asset["bytes"] > 0
            and asset["sha256"] == expected["sha256"],
            "referenced JavaScript differs from the pinned original binary")
    return {"checks": {"homepage_sha256_matches_original": True,
                       "referenced_javascript_loaded": True,
                       "referenced_javascript_sha256_matches_original": True},
            "homepage_sha256": page["sha256"],
            "javascript": {"path": path, "sha256": asset["sha256"], "bytes": asset["bytes"]}}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, new_url):
        raise ValueError("frontend probe refuses redirects")


def fetcher(payload):
    origin = urllib.parse.urlsplit(payload["origin"])
    require(origin.scheme == "https" and origin.hostname ==
            "nightingale.observability-alerting.svc" and origin.port == 17443
            and origin.path in ("", "/") and not origin.username and not origin.password
            and not origin.query and not origin.fragment, "unexpected isolated frontend origin")
    context = ssl.create_default_context(cafile=payload["ca"])
    context.load_cert_chain(payload["cert"], payload["key"])
    context.minimum_version = ssl.TLSVersion.TLSv1_3
    context.maximum_version = ssl.TLSVersion.TLSv1_3
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(),
                                        urllib.request.HTTPSHandler(context=context))

    def fetch(path, maximum, include_body):
        request = urllib.request.Request(payload["origin"].rstrip("/") + path,
                                         headers={"Accept-Encoding": "identity"})
        digest, size, body = hashlib.sha256(), 0, bytearray()
        with opener.open(request, timeout=15) as response:
            require(response.headers.get("Content-Encoding", "identity") == "identity",
                    "unexpected encoded frontend response")
            while True:
                chunk = response.read(65536)
                if not chunk:
                    break
                size += len(chunk)
                require(size <= maximum, "frontend resource exceeds probe limit")
                digest.update(chunk)
                if include_body:
                    body.extend(chunk)
            return {"status": response.status, "content_type": response.headers.get_content_type(),
                    "sha256": digest.hexdigest(), "bytes": size,
                    "body": body.decode("utf-8") if include_body else None}
    return fetch


if __name__ == "__main__":
    try:
        payload = json.load(sys.stdin)
        print(json.dumps(verify_frontend(fetcher(payload), payload["lock"])))
    except (ValueError, OSError, KeyError, urllib.error.URLError):
        # Never include request objects or certificate paths in failure output.
        print(json.dumps({"error": "original frontend content or authenticated asset loading failed"}))
        raise SystemExit(1)
