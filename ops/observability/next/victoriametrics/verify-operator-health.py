#!/usr/bin/env python3
"""Read real Operator endpoints through coordinator-owned loopback port forwards.

Run on the builder after forwarding this candidate Pod's native 8081 and proxy
18081 ports. This script creates no forwards, containers, credentials or cluster
resources. Controller leadership/cache synchronization remains a separate gate.
"""

import argparse
import http.client
import json
import os
from pathlib import Path
import platform


def get_status(port, path):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=15)
    try:
        connection.request("GET", path)
        response = connection.getresponse()
        response.read(4096)
        return response.status
    finally:
        connection.close()


def inspect_health(native_port, proxy_port):
    expected = {"native:/health": 200, "native:/readyz": 404,
                "proxy:/healthz": 200, "proxy:/app-healthz": 200,
                "proxy:/metrics": 404}
    ports = {"native": native_port, "proxy": proxy_port}
    observed = {name: get_status(ports[name.split(":", 1)[0]], name.split(":", 1)[1])
                for name in expected}
    # Diagnostic only: upstream /ready waits for cache sync. It is intentionally
    # not a replacement for the chart's process-health readiness during rolling.
    observed["native:/ready"] = get_status(native_port, "/ready")
    return {"status": "passed" if all(observed[key] == code for key, code in expected.items()) else "failed",
            "expected": expected, "observed": observed,
            "scope": "real Operator native and proxy endpoints; leadership not asserted"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--native-port", type=int, required=True)
    parser.add_argument("--proxy-port", type=int, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if platform.system() != "Linux" or any(not 1024 <= port <= 65535 for port in (args.native_port, args.proxy_port)):
        parser.error("requires Linux builder and explicit unprivileged loopback ports")
    if args.output.exists():
        parser.error("output already exists")
    os.umask(0o077)
    try:
        report = inspect_health(args.native_port, args.proxy_port)
    except (OSError, http.client.HTTPException) as error:
        report = {"status": "failed", "failure_type": type(error).__name__}
    with args.output.open("x") as output:
        json.dump(report, output, indent=2)
        output.write("\n")
    print(json.dumps(report))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
