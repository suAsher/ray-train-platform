"""Private, bounded Docker helpers for the isolated N9E acceptance harness."""

import json
import os
from pathlib import Path
import re
import secrets
import shutil
import string
import subprocess
import time
import uuid

N9E = "harbor.wellspiking.ai/hub/flashcatcloud/nightingale:9.1.1@sha256:81b9aa562549b5ee9d36283a11a14bf257e62fa71eb181277ae4841f7c80e2f9"
PG = "ghcr.io/cloudnative-pg/postgresql:17.11-202608170816-standard-trixie@sha256:6b7e6a1267254902840efa9936bbeb0a398a79b5b9cb2e82218e4a92b602e97c"
REDIS = "harbor.wellspiking.ai/hub/library/redis:8.8.0-alpine@sha256:9d317178eceac8454a2284a9e6df2466b93c745529947f0cd42a0fa9609d7005"
ENVOY = "harbor.wellspiking.ai/hub/envoyproxy/envoy:v1.39.3@sha256:dd85940439de19a0b6ae8419610363ea0ad351d9a994ea007161c206ec1e1865"
NS = "observability-alerting"
PGHOST = "n9e-postgres-rw." + NS + ".svc"
N9EHOST = "nightingale." + NS + ".svc"
REDIS_HOSTS = [f"n9e-redis-announce-{index}.{NS}.svc" for index in range(3)]
URI_ROOT = "spiffe://observability.raytrain.local/"


def command(args, *, data=None, check=True, timeout=120):
    result = subprocess.run(list(map(str, args)), input=data, text=True,
                            capture_output=True, timeout=timeout, check=False)
    if check and result.returncode:
        # Arguments and stderr may contain temporary credentials. Never print them.
        raise RuntimeError(f"{Path(args[0]).name} failed with exit {result.returncode}")
    return result


class Runtime:
    def __init__(self, output, n9e_image_id=None):
        if n9e_image_id is not None and not re.fullmatch(r"sha256:[0-9a-f]{64}", n9e_image_id):
            raise ValueError("N9E test override must be an exact local image ID")
        self.n9e_image = n9e_image_id or N9E
        self.output = Path(output).resolve()
        if os.geteuid() != 0:
            raise RuntimeError("run only as root on the authorized isolated builder")
        if self.output.exists() or any(path.is_symlink() for path in self.output.parents):
            raise RuntimeError("output must be a new path without symlink parents")
        self.output.mkdir(mode=0o700, parents=True)
        self.run = "obs-n9e-test-" + uuid.uuid4().hex[:10]
        self.private = self.output / "private"
        self.private.mkdir(mode=0o700)
        self.network = self.run + "-net"
        self.created = []
        self.secret_values = []
        self.checks = {}

    def password(self, length=48):
        value = "".join(secrets.choice(string.ascii_letters + string.digits) for _ in range(length))
        self.secret_values.append(value)
        return value

    def write(self, relative, content, uid=10001, mode=0o400):
        path = self.private / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)
        os.chown(path, uid, uid)
        path.chmod(mode)
        return path

    def directory(self, relative, uid=10001):
        path = self.private / relative
        path.mkdir(parents=True, exist_ok=True)
        path.chmod(0o750)
        os.chown(path, uid, uid)
        return path

    def docker(self, *args, **kwargs):
        return command(["docker", *args], **kwargs)

    def initialize(self):
        for image in (self.n9e_image, PG, REDIS, ENVOY):
            self.docker("image", "inspect", image)
        self.docker("network", "create", "--internal", "--label", "obs.n9e.test=" + self.run, self.network)

    def start(self, suffix, image, args, *, extras=(), network=None, uid=10001, memory="256m", cpu="0.5"):
        name = self.run + "-" + suffix
        self.created.append(name)
        self.docker("run", "-d", "--pull=never", "--name", name,
                    "--label", "obs.n9e.test=" + self.run,
                    "--network", network or self.network,
                    "--cpus", cpu, "--memory", memory, "--pids-limit", "128",
                    "--user", f"{uid}:{uid}", "--cap-drop", "ALL",
                    "--security-opt", "no-new-privileges", "--read-only", *extras, image, *args)
        return name

    def wait(self, name, predicate, seconds=90):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            try:
                result = predicate()
                if result:
                    self.checks[name] = True
                    return result
            except (RuntimeError, ValueError, KeyError, json.JSONDecodeError):
                pass
            time.sleep(1)
        raise RuntimeError(name + " timed out")

    def check(self, name, condition):
        self.checks[name] = bool(condition)
        if not condition:
            raise RuntimeError(name + " failed")

    def make_ca(self, name):
        ca = self.directory("ca/" + name, uid=0)
        command(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
                 "-subj", "/CN=isolated-n9e-test-" + name,
                 "-addext", "basicConstraints=critical,CA:TRUE",
                 "-addext", "keyUsage=critical,keyCertSign,cRLSign",
                 "-keyout", ca / "ca.key", "-out", ca / "ca.crt"])
        (ca / "ca.key").chmod(0o600)
        return ca

    def leaf(self, name, ca, *, cn=None, dns=(), uri=None, usages="clientAuth", uid=10001):
        path = self.directory("identities/" + name, uid=uid)
        command(["openssl", "req", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=" + (cn or name),
                 "-keyout", path / "tls.key", "-out", path / "request.csr"])
        extension = "basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\n"
        extension += "extendedKeyUsage=" + usages + "\n"
        sans = ["DNS:" + item for item in dns] + (["URI:" + uri] if uri else [])
        if sans:
            extension += "subjectAltName=" + ",".join(sans) + "\n"
        (path / "extensions").write_text(extension)
        command(["openssl", "x509", "-req", "-in", path / "request.csr", "-CA", ca / "ca.crt",
                 "-CAkey", ca / "ca.key", "-CAcreateserial", "-days", "1",
                 "-extfile", path / "extensions", "-out", path / "tls.crt"])
        shutil.copyfile(ca / "ca.crt", path / "ca.crt")
        for item in path.iterdir():
            os.chown(item, uid, uid)
            item.chmod(0o400)
        return path

    def redis(self, container, host, *args, sentinel=False, check=True, json_output=False):
        key = "sentinel.env" if sentinel else "redis.env"
        return self.docker("exec", "--env-file", self.private / key, container,
                           "redis-cli", "--json" if json_output else "--raw", "--tls", "--cacert", "/tls/ca.crt",
                           "--cert", "/tls/tls.crt", "--key", "/tls/tls.key",
                           "--sni", host, "-h", host, "-p", "26379" if sentinel else "6379",
                           *args, check=check).stdout.strip()

    def master(self, live_container, host):
        reply = self.redis(live_container, host, "SENTINEL", "get-master-addr-by-name", "n9e", sentinel=True)
        values = reply.splitlines()
        if len(values) != 2 or values[0] not in REDIS_HOSTS or values[1] != "6379":
            raise RuntimeError("Sentinel returned an unexpected master identity")
        return values[0]

    def resolved_addresses(self, client, host):
        script = "import json,socket,sys; print(json.dumps(sorted({item[4][0] for item in socket.getaddrinfo(sys.argv[1], None, type=socket.SOCK_STREAM)})))"
        reply = self.docker("exec", client, "python3", "-c", script, host, timeout=15)
        return json.loads(reply.stdout)

    def replica_ready(self, client, replica, master):
        reply = self.redis(client, replica, "INFO", "replication")
        fields = dict(line.split(":", 1) for line in reply.splitlines()
                      if line and not line.startswith("#") and ":" in line)
        return all(fields.get(key) == value for key, value in {
            "role": "slave", "master_host": master, "master_link_status": "up",
            "master_sync_in_progress": "0",
        }.items())

    def sentinel_topology_ready(self, client, host, master):
        reply = self.redis(client, host, "SENTINEL", "REPLICAS", "n9e", sentinel=True, json_output=True)
        rows = json.loads(reply)
        peers = [dict(zip(row[::2], row[1::2])) if isinstance(row, list) else row for row in rows]
        if len(peers) != 2 or {peer.get("ip") for peer in peers} != set(REDIS_HOSTS) - {master}:
            return False
        return self.master(client, host) == master and all(
            not ({"s_down", "o_down", "disconnected"} & set(peer.get("flags", "").split(",")))
            and peer.get("master-link-status") == "ok"
            and peer.get("role-reported") == "slave"
            and peer.get("master-host") == master
            for peer in peers)

    def pause_member(self, container):
        # Keep the network alias: the chart's per-member ClusterIP Service DNS
        # survives Pod/node failure. docker stop instead removes that DNS entry
        # and tests an unrelated DNS outage (blocking Sentinel until TILT).
        if container not in self.created:
            raise RuntimeError("refusing to pause a container outside this run")
        before = json.loads(self.docker("inspect", container).stdout)[0]
        if before["Config"].get("Labels", {}).get("obs.n9e.test") != self.run:
            raise RuntimeError("refusing to pause a container with a different owner")
        self.docker("pause", container)
        after = json.loads(self.docker("inspect", container).stdout)[0]
        self.check("faulted_master_pair_is_paused", after["State"].get("Paused") is True)

    def probe(self, container, payload):
        script = r'''
import json, socket, ssl, sys, urllib.error, urllib.request
p = json.load(sys.stdin)
try:
    context = None
    if p.get('tls', True):
        context = ssl.create_default_context(cafile=p['ca'])
        if p.get('cert'):
            context.load_cert_chain(p['cert'], p['key'])
    if p.get('protocol') == 'redis':
        sock = socket.create_connection((p['host'], p['port']), timeout=3)
        if context:
            sock = context.wrap_socket(sock, server_hostname=p['host'])
        sock.sendall(b'*1\r\n$4\r\nPING\r\n')
        answer = sock.recv(4096).decode()
        sock.close()
        print(json.dumps({'status': 200, 'data': answer}))
    else:
        body = json.dumps(p['body']).encode() if 'body' in p else None
        headers = {'Content-Type': 'application/json', **p.get('headers', {})}
        req = urllib.request.Request(p['url'], data=body, headers=headers)
        with urllib.request.urlopen(req, context=context, timeout=8) as result:
            raw = result.read().decode()
            try: data = json.loads(raw)
            except ValueError: data = raw
            print(json.dumps({'status': result.status, 'data': data}))
except urllib.error.HTTPError as error:
    print(json.dumps({'status': error.code, 'data': None}))
except (OSError, ValueError, urllib.error.URLError):
    print(json.dumps({'status': 0, 'data': None}))
'''
        result = self.docker("exec", "-i", container, "python3", "-c", script, data=json.dumps(payload))
        return json.loads(result.stdout)

    def check_runtime_bounds(self):
        network = json.loads(self.docker("network", "inspect", self.network).stdout)[0]
        self.check("internal_network", network["Internal"])
        specs = [json.loads(self.docker("inspect", name).stdout)[0] for name in self.created]
        self.check("no_host_ports", all(not item["HostConfig"].get("PortBindings") for item in specs))
        self.check("bounded_cpu_memory", all(0 < item["HostConfig"]["NanoCpus"] <= 1_000_000_000
                  and 0 < item["HostConfig"]["Memory"] <= 1024 ** 3 for item in specs))

    def redact(self, text):
        for value in sorted(filter(None, self.secret_values), key=len, reverse=True):
            if len(value) < 12:
                # Short maths-captcha answers must not replace digits inside
                # timestamps, ports, IDs or addresses in the diagnostic logs.
                text = re.sub(r"(?<![A-Za-z0-9])" + re.escape(value) + r"(?![A-Za-z0-9])", "<redacted>", text)
            else:
                text = text.replace(value, "<redacted>")
        return re.sub(r"-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----",
                      "<redacted-private-key>", text, flags=re.S)

    def cleanup(self):
        cleanup_ok = True
        for name in reversed(self.created):
            inspect = self.docker("inspect", name, check=False)
            if inspect.returncode:
                continue
            item = json.loads(inspect.stdout)[0]
            if item["Config"].get("Labels", {}).get("obs.n9e.test") != self.run:
                cleanup_ok = False
                continue
            log = self.docker("logs", name, check=False)
            (self.output / (name + ".log")).write_text(self.redact(log.stdout + log.stderr))
            self.docker("rm", "-f", name, check=False)
        inspect = self.docker("network", "inspect", self.network, check=False)
        if not inspect.returncode:
            item = json.loads(inspect.stdout)[0]
            if item.get("Labels", {}).get("obs.n9e.test") == self.run:
                self.docker("network", "rm", self.network, check=False)
            else:
                cleanup_ok = False
        shutil.rmtree(self.private)
        cleanup_ok = cleanup_ok and all(self.docker("inspect", name, check=False).returncode != 0
                                       for name in self.created)
        return cleanup_ok and self.docker("network", "inspect", self.network, check=False).returncode != 0
