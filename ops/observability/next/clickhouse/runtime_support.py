"""Builder-only isolated Docker test utilities; never call Kubernetes."""

import http.client
import importlib.util
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import ssl
import subprocess
import time


class RuntimeCheckError(Exception):
    """A deliberately redacted test failure."""


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def run(args, check=True, **kwargs):
    result = subprocess.run(list(map(str, args)), text=True, capture_output=True,
                            timeout=kwargs.pop("timeout", 180), **kwargs)
    if check and result.returncode:
        # Do not echo command arguments, XML, environment, or TLS material.
        raise RuntimeCheckError(f"{Path(str(args[0])).name} failed with exit {result.returncode}")
    return result


def write(path, text, writable=False):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    path.chmod(0o640)
    os.chown(path, 10001, 10001)
    if writable:
        path.chmod(0o600)
    return path


def data_dir(path):
    path.mkdir(parents=True, exist_ok=True)
    path.chmod(0o750)
    os.chown(path, 10001, 10001)
    return path


def wait(description, probe, timeout=90):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            if probe():
                return
        except (OSError, RuntimeCheckError, http.client.HTTPException):
            pass
        time.sleep(2)
    raise RuntimeCheckError(f"Timed out: {description}")


def port_closed(address, port):
    try:
        with socket.create_connection((address, port), timeout=2):
            return False
    except ConnectionRefusedError:
        return True


class Sandbox:
    def __init__(self, directory):
        self.work = directory
        self.prefix = "obs-ch-mtls-" + secrets.token_hex(5)
        self.network = self.prefix
        self.containers = []
        self.leaves = {}
        self.env_values = {}
        self.stage = "initialized"
        self.tls_results = []
        self.hop_results = []

    def setup(self):
        run(["docker", "network", "create", "--internal", "--label",
             "observability-test=" + self.prefix, self.network])

    def start(self, name, image, args, mounts, aliases=(), env=None,
              memory="512m", cpus="0.5", network_mode=None, entrypoint=None):
        full = self.prefix + "-" + name
        command = ["docker", "run", "-d", "--name", full, "--label",
                   "observability-test=" + self.prefix, "--network", network_mode or self.network,
                   "--user", "10001:10001", "--read-only", "--cap-drop", "ALL",
                   "--security-opt", "no-new-privileges", "--pids-limit", "256",
                   "--memory", memory, "--cpus", cpus, "--tmpfs", "/tmp:rw,size=64m,uid=10001,gid=10001"]
        for alias in aliases:
            command += ["--network-alias", alias]
        for source, target, writable in mounts:
            if source.resolve() == self.work.resolve() or "pki" in source.parts:
                raise RuntimeCheckError("Refusing broad or CA private directory mount")
            command += ["--mount", f"type=bind,src={source},dst={target}" + ("" if writable else ",readonly")]
        environment = os.environ.copy()
        for key, value in (env or {}).items():
            environment[key] = str(value)
            command += ["--env", key]
        if entrypoint:
            command += ["--entrypoint", entrypoint]
        command += [image, *args]
        # Register before launch so a timed-out Docker client cannot orphan it.
        self.containers.append(full)
        run(command, env=environment)
        return full

    def ip(self, container):
        value = run(["docker", "inspect", "--format", "{{json .NetworkSettings.Networks}}", container]).stdout
        return json.loads(value)[self.network]["IPAddress"]

    def issue(self, root, plans):
        credentials = load_module("obs_runtime_credentials", root / "mtls" / "credentials.py")
        for item in plans:
            material = credentials.issue_material(item, self.work / "pki")
            role = item["role"]
            leaf = data_dir(self.work / "leaves" / role)
            for key, value in material.items():
                write(leaf / key, value)
            self.leaves[role] = leaf

    def context(self, role, tls12=False):
        leaf = self.leaves[role or "query-admin"]
        context = ssl.create_default_context(cafile=leaf / "ca.crt")
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        if tls12:
            context.maximum_version = ssl.TLSVersion.TLSv1_2
        if role:
            context.load_cert_chain(leaf / "tls.crt", leaf / "tls.key")
        return context

    def request(self, address, server_name, port, path, role, body=None):
        context = self.context(role)
        connection = http.client.HTTPSConnection(server_name, port, context=context, timeout=5)
        connection.sock = context.wrap_socket(socket.create_connection((address, port), 5),
                                              server_hostname=server_name)
        try:
            connection.request("POST" if body is not None else "GET", path, body=body,
                               headers={"Content-Type": "application/json"})
            response = connection.getresponse()
            return response.status, response.read(65536)
        finally:
            connection.close()

    def tls_accepts(self, address, server_name, port, role):
        record = {"server_name": server_name, "port": port, "role": role,
                  "protocol_attempt": "TLSv1.2"}
        try:
            with socket.create_connection((address, port), timeout=3) as sock:
                with self.context(role, tls12=True).wrap_socket(sock, server_hostname=server_name) as stream:
                    record.update({"accepted": True, "protocol": stream.version()})
                    self.tls_results.append(record)
                    return True
        except ssl.SSLError as exc:
            record.update({"accepted": False, "type": type(exc).__name__,
                           "library": getattr(exc, "library", None),
                           "reason": getattr(exc, "reason", None),
                           "verify_code": getattr(exc, "verify_code", None)})
            self.tls_results.append(record)
            return False
        except (ConnectionError, OSError) as exc:
            record.update({"accepted": False, "type": type(exc).__name__, "errno": exc.errno})
            self.tls_results.append(record)
            raise RuntimeCheckError("TLS matrix transport unavailable; not evidence of certificate rejection") from None

    def health(self, address, path="/readyz"):
        connection = http.client.HTTPConnection(address, 18080, timeout=3)
        try:
            connection.request("GET", path)
            response = connection.getresponse()
            return response.status, response.read(4096)
        finally:
            connection.close()

    def cleanup(self):
        diagnostics = self.work / "private-diagnostics"
        diagnostics.mkdir(mode=0o700, exist_ok=True)
        for container in reversed(self.containers):
            # Local test logs are private evidence; never display secret-bearing diagnostics.
            logs = run(["docker", "logs", "--tail", "1000", container], check=False)
            log_path = diagnostics / (container + ".log")
            log_path.write_text(logs.stdout + logs.stderr)
            log_path.chmod(0o600)
            run(["docker", "rm", "-f", container], check=False)
        run(["docker", "network", "rm", self.network], check=False)
        # Disposable leaf keys and synthetic data are ours; no production storage is mounted.
        for name in ("pki", "leaves", "data", "client-configs", "configs"):
            shutil.rmtree(self.work / name, ignore_errors=True)
        for path in self.work.glob("otel-runtime-*"):
            if path.is_dir() and not path.is_symlink():
                shutil.rmtree(path)
