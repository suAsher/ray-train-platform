#!/usr/bin/env python3
"""Verify public CLI metadata can be read from Portal without credentialed CORS."""

import argparse
import json
import urllib.error
import urllib.request


def response(base, path, origin, method="GET"):
    request = urllib.request.Request(
        base.rstrip("/") + path, headers={"Origin": origin}, method=method
    )
    try:
        return urllib.request.urlopen(request, timeout=15)
    except urllib.error.HTTPError as error:
        return error


def verify(base):
    metadata = "/downloads/spk-rayjob/release.json"
    for origin in (
        "https://spiking.wellspiking.ai",
        "https://spiking-dev.wellspiking.ai",
    ):
        with response(base, metadata, origin) as result:
            assert result.status == 200, (origin, result.status)
            assert result.headers.get("Access-Control-Allow-Origin") == "*", origin
            assert not result.headers.get("Access-Control-Allow-Credentials"), origin
            assert result.headers.get("X-Content-Type-Options") == "nosniff"
            assert "application/json" in result.headers.get("Content-Type", "")
            manifest = json.load(result)
            assert manifest["schemaVersion"] == 1
            assert manifest["latestVersion"].startswith("release-")
            assert len(manifest["artifacts"]) == 3
        with response(base, metadata, origin, "HEAD") as result:
            assert result.status == 200
            assert result.headers.get("Access-Control-Allow-Origin") == "*"
        # The public metadata exception must not grant CORS to other paths.
        for path in ("/healthz", "/downloads/spk-rayjob/SHA256SUMS"):
            with response(base, path, origin) as result:
                assert result.status == 200, (path, result.status)
                assert not result.headers.get("Access-Control-Allow-Origin"), path
    print("PASS: both Portal origins can read public CLI metadata; CORS is scoped to metadata only")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("base_url")
    verify(parser.parse_args().base_url)
