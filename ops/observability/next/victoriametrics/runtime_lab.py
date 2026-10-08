"""Private Docker protocol lab helpers; only execute through verify-runtime.py on builder."""

import http.client
import json
import os
from pathlib import Path
import re
import secrets
import socket
import ssl
import subprocess
import time

import yaml


class LabError(Exception):
    pass


def command(argv, timeout=60, check=True):
    result = subprocess.run(argv, capture_output=True, text=True, timeout=timeout, check=False)
    if check and result.returncode:
        # Do not echo commands, environment, configuration or private diagnostics.
        raise LabError(f"Command failed: {Path(argv[0]).name} (exit {result.returncode})")
    return result


def eventually(operation, timeout=90):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            value = operation()
            if value:
                return value
        except (OSError, ValueError, LabError, http.client.HTTPException) as error:
            last = type(error).__name__
        time.sleep(2)
    raise LabError(f"Protocol condition did not become ready ({last or 'condition false'})")


def await_http_status(operation, expected, label, timeout=90):
    """Bound startup races without hiding authorization or certificate defects."""
    deadline = time.monotonic() + timeout
    attempts, transients, last = 0, set(), "no response"
    while time.monotonic() < deadline:
        attempts += 1
        try:
            status, _ = operation()
        except ssl.SSLError as error:
            raise LabError(f"{label}: TLS verification failed ({type(error).__name__})") from None
        except (ConnectionError, TimeoutError, http.client.HTTPException) as error:
            last = type(error).__name__
        else:
            last = f"HTTP {status}"
            if status == expected:
                return {"attempts": attempts, "http_status": status, "transient_statuses": sorted(transients)}
            if status not in (502, 503, 504):
                raise LabError(f"{label}: unexpected HTTP {status}")
            transients.add(status)
        time.sleep(2)
    raise LabError(f"{label}: readiness deadline exceeded ({last}; attempts={attempts})")


def auth_users(objects, tokens):
    """Model exactly the two rendered VMUsers without inventing extra grants."""
    users = [item for item in objects if item.get("kind") == "VMUser"]
    names = [item["metadata"]["name"] for item in users]
    if len(names) != 2 or set(names) != {"self-read", "self-write"}:
        raise LabError("Auth fixture requires exactly two distinct rendered VMUsers")
    if set(tokens) != set(names) or len(set(tokens.values())) != 2 or not all(tokens.values()):
        raise LabError("Auth fixture requires two distinct nonempty tokens")
    result = []
    for item in users:
        name, spec = item["metadata"]["name"], item["spec"]
        if spec.get("tokenRef") != {"name": "vm-" + name, "key": "token"}:
            raise LabError("Auth fixture Secret reference differs from candidate")
        targets = spec.get("targetRefs", [])
        if len(targets) != 1 or not targets[0].get("paths") or not targets[0].get("static", {}).get("url"):
            raise LabError("Auth fixture cannot represent candidate targetRefs")
        target = targets[0]
        result.append({"bearer_token": tokens[name], "url_map": [{
            "src_paths": list(target["paths"]), "url_prefix": target["static"]["url"]}]})
    return result


def private_file(path, text, group_read=False):
    path = Path(path)
    path.parent.mkdir(mode=0o750, parents=True, exist_ok=True)
    path.write_text(text)
    path.chmod(0o640 if group_read else 0o600)
    if group_read:
        os.chown(path, 0, 65534)
        os.chown(path.parent, 0, 65534)
        path.parent.chmod(0o750)
    return path


class TLSConnection(http.client.HTTPSConnection):
    def __init__(self, address, port, server_name, context):
        super().__init__(address, port=port, context=context, timeout=5)
        self.server_name = server_name

    def connect(self):
        self.sock = self._context.wrap_socket(
            socket.create_connection((self.host, self.port), self.timeout),
            server_hostname=self.server_name,
        )


class UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("localhost", timeout=3)
        self.path = str(path)

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


class DockerLab:
    def __init__(self, directory, vm_root, credentials):
        self.directory = Path(directory)
        self.vm_root = Path(vm_root)
        self.credentials = credentials
        self.prefix = "rt-vm-mtls-" + secrets.token_hex(5)
        self.network = self.prefix + "-net"
        self.containers = []
        self.materials = {}
        self.redactions = []
        self.results = []
        self.admin_paths = {}
        self.images = yaml.safe_load((self.vm_root / "resources-hub-values.yaml").read_text())["images"]
        self.envoy = yaml.safe_load((self.vm_root / "resources/values.yaml").read_text())["mtls"]["image"]
        self.plan = json.loads((self.vm_root / "mtls-identities.json").read_text())

    def image(self, role):
        item = self.images[role]
        return item if isinstance(item, str) else item["repository"] + ":" + item["tag"]

    def prepare(self):
        command(["docker", "network", "create", "--internal", "--label", "raytrain.mtls-lab=" + self.prefix, self.network])
        for item in self.plan:
            self.issue(item["role"])
        rendered = command(["helm", "template", "raytrain-next", str(self.vm_root / "resources"),
                            "--namespace", "observability-metrics"]).stdout
        self.objects = [item for item in yaml.safe_load_all(rendered) if item]

    def issue(self, role, alternate=False):
        item = next(item for item in self.plan if item["role"] == role)
        pki = self.directory / ("foreign-pki" if alternate else "pki")
        material = self.credentials.issue_material(item, pki)
        self.redactions.extend(material.values())
        name = "foreign-" + role if alternate else role
        target = self.directory / "identities" / name
        for key, value in material.items():
            private_file(target / key, value, group_read=True)
        self.materials[name] = target
        return target

    def issue_expired_canary(self):
        """Sign via CSR: OpenSSL 3.0 rejects re-signing a non-self-signed leaf."""
        source = self.materials["canary"]
        target = self.directory / "identities" / "expired-canary"
        for name in ("tls.key", "ca.crt"):
            private_file(target / name, (source / name).read_text(), True)
        csr, cert = target / "tls.csr", target / "tls.crt"
        command(["openssl", "x509", "-x509toreq", "-in", str(source / "tls.crt"),
                 "-signkey", str(target / "tls.key"), "-copy_extensions", "copy", "-out", str(csr)])
        command(["openssl", "x509", "-req", "-in", str(csr), "-copy_extensions", "copy",
                 "-CA", str(self.directory / "pki/mesh.crt"), "-CAkey", str(self.directory / "pki/mesh.key"),
                 "-set_serial", "0x" + secrets.token_hex(12), "-days", "-1", "-out", str(cert)])
        csr.chmod(0o600)
        cert.chmod(0o600)
        verify = ["openssl", "verify", "-CAfile", str(target / "ca.crt"), "-purpose", "sslclient"]
        command([*verify, "-no_check_time", str(cert)])
        expired = command([*verify, str(cert)], check=False)
        if expired.returncode == 0 or "error 10 " not in expired.stderr:
            raise LabError("Expired fixture did not fail specifically for certificate expiry")
        self.materials["expired-canary"] = target
        return target

    def native(self, name, image, args, aliases=(), mounts=(), memory="512m"):
        # A POSIX shell builtin blocks on open stdin as a tiny namespace holder.
        # Restarting an app therefore models a K8s container restart correctly:
        # the sidecar and app keep sharing the same stable network namespace.
        full_name = self.prefix + "-" + name + "-pod"
        argv = ["docker", "run", "-d", "--name", full_name, "--label", "raytrain.mtls-lab=" + self.prefix,
                "--network", self.network, "--cpus", "0.05", "--memory", "32m", "--pids-limit", "16", "-i",
                "--read-only", "--user", "65534:65534", "--cap-drop", "ALL",
                "--security-opt", "no-new-privileges", "--entrypoint", "/bin/sh"]
        for alias in aliases:
            argv += ["--network-alias", alias]
        self.containers.append(full_name)
        command([*argv, self.envoy, "-c", "read -r namespace_lifetime"])
        address = json.loads(command(["docker", "inspect", full_name]).stdout)[0]["NetworkSettings"]["Networks"][self.network]["IPAddress"]
        pod = {"name": full_name, "ip": address}
        return {**pod, "app": self.peer(pod, name + "-app", image, args, mounts, memory)}

    def peer(self, pod, name, image, args, mounts=(), memory="128m", entrypoint=None):
        full_name = self.prefix + "-" + name
        argv = ["docker", "run", "-d", "--name", full_name, "--label", "raytrain.mtls-lab=" + self.prefix,
                "--network", "container:" + pod["name"], "--cpus", "0.3", "--memory", memory,
                "--pids-limit", "64", "--read-only", "--user", "65534:65534", "--cap-drop", "ALL",
                "--security-opt", "no-new-privileges", "--tmpfs", "/tmp:rw,noexec,nosuid,size=8m"]
        if entrypoint:
            argv += ["--entrypoint", entrypoint]
        for source, target, access in mounts:
            argv += ["-v", f"{source}:{target}:{access}"]
        self.containers.append(full_name)
        command([*argv, image, *args])
        return full_name

    def proxy(self, role, pod, suffix):
        config = next(item["data"]["envoy.yaml"] for item in self.objects
                      if item["kind"] == "ConfigMap" and item["metadata"]["name"] == f"raytrain-next-{role}-mtls")
        # Lab-only admin on a private filesystem Unix socket, never a TCP port.
        # Keep production ConfigMaps unchanged; collect numeric stats only, not
        # config dumps, request headers or HTTP access logs.
        admin_dir = self.directory / "diagnostics" / suffix
        admin_dir.mkdir(mode=0o700, parents=True)
        os.chown(admin_dir, 65534, 65534)
        config_data = yaml.safe_load(config.replace("__POD_IP__", pod["ip"]))
        config_data["admin"] = {"address": {"pipe": {"path": "/diagnostics/admin.sock", "mode": 0o600}}}
        config_path = private_file(self.directory / "configs" / (suffix + ".yaml"),
                                   yaml.safe_dump(config_data), group_read=True)
        identity = self.materials[role]
        container = self.peer(pod, suffix + "-proxy", self.envoy,
                              ["-c", "/config/envoy.yaml", "--concurrency", "1", "--disable-hot-restart", "--log-level", "warning",
                               "--component-log-level", "connection:debug"],
                              [(config_path, "/config/envoy.yaml", "ro"), (identity, "/etc/obs-mtls", "ro"),
                               (admin_dir, "/diagnostics", "rw")], entrypoint="/usr/local/bin/envoy")
        self.admin_paths[container] = admin_dir / "admin.sock"
        return container

    def request(self, pod, port, path, role="admin", server="vmauth", method="GET", body=None, headers=None):
        context = ssl.create_default_context(cafile=str(self.materials["vmauth"] / "ca.crt"))
        if role:
            material = self.materials[role]
            context.load_cert_chain(material / "tls.crt", material / "tls.key")
        connection = TLSConnection(pod["ip"], port, server + ".observability-metrics.svc", context)
        try:
            connection.request(method, path, body=body, headers=headers or {})
            response = connection.getresponse()
            return response.status, response.read(1048576)
        finally:
            connection.close()

    def denied(self, label, operation):
        try:
            status, _ = operation()
        except (OSError, http.client.HTTPException):
            self.results.append({"check": label, "result": "denied"})
            return
        if status not in (400, 401, 403, 404, 405, 426):
            raise LabError(f"Unexpected response for rejection check: {label}, HTTP {status}")
        self.results.append({"check": label, "result": "denied", "http_status": status})

    def closed(self, pod, ports):
        for port in ports:
            try:
                with socket.create_connection((pod["ip"], port), timeout=2):
                    raise LabError(f"Native bypass remained reachable: {port}")
            except (ConnectionRefusedError, TimeoutError):
                self.results.append({"check": f"native-{port}", "result": "closed"})

    def cleanup(self):
        failed = []
        for name in reversed(self.containers):
            result = command(["docker", "inspect", name], check=False)
            if result.returncode == 0 and json.loads(result.stdout)[0]["Config"]["Labels"].get("raytrain.mtls-lab") == self.prefix:
                if command(["docker", "rm", "-f", name], check=False).returncode:
                    failed.append(name)
        result = command(["docker", "network", "inspect", self.network], check=False)
        if result.returncode == 0 and json.loads(result.stdout)[0]["Labels"].get("raytrain.mtls-lab") == self.prefix:
            if command(["docker", "network", "rm", self.network], check=False).returncode:
                failed.append(self.network)
        if failed:
            raise LabError("Own temporary resource cleanup failed: " + ",".join(failed))

    def diagnostics(self):
        """Read only this lab's logs; remove known generated token/key material."""
        result = []
        for name in self.containers:
            inspected = command(["docker", "inspect", name], check=False)
            if inspected.returncode:
                continue
            container = json.loads(inspected.stdout)[0]
            if container["Config"]["Labels"].get("raytrain.mtls-lab") != self.prefix:
                continue
            log = command(["docker", "logs", "--tail", "40", name], check=False)
            tail = log.stdout + log.stderr
            for value in self.redactions:
                tail = tail.replace(value, "[REDACTED]")
            item = {"container": name, "status": container["State"]["Status"],
                    "exit_code": container["State"]["ExitCode"], "tail": tail[-4096:]}
            if name in self.admin_paths:
                item["proxy_counters"] = self.proxy_counters(self.admin_paths[name])
            result.append(item)
        return result

    @staticmethod
    def proxy_counters(path):
        connection = UnixHTTPConnection(path)
        try:
            connection.request("GET", "/stats?format=json")
            response = connection.getresponse()
            if response.status != 200:
                return {"diagnostic_error": "stats-unavailable"}
            stats = json.loads(response.read(4 * 1024 * 1024)).get("stats", [])
            pattern = re.compile(r"(ssl\.|upstream_cx|membership_|update_(attempt|success|failure)|rbac\.(allowed|denied))")
            return {item["name"]: item["value"] for item in stats
                    if pattern.search(item.get("name", "")) and isinstance(item.get("value"), (int, float))}
        except (OSError, ValueError, http.client.HTTPException) as error:
            return {"diagnostic_error": type(error).__name__}
        finally:
            connection.close()
