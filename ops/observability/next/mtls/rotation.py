#!/usr/bin/env python3
"""Serial, fail-closed leaf renewal consumption for fixed observability workloads."""
import base64
import copy
import datetime as dt
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ANNOTATION = "observability.raytrain.io/native-certificates"
LEASE_NAMESPACE = "cert-manager"
LEASE_NAME = "native-certificate-rotation"
LEASE_SECONDS = 120
ALLOWED = {
    "keeper": ("clickhouse", ("obs-mtls-keeper",),
               tuple(("StatefulSet", f"chk-clickhouse-keeper-0-{i}", 1) for i in range(3))),
    "clickhouse": ("clickhouse", ("obs-mtls-clickhouse",),
                   tuple(("StatefulSet", f"chi-clickhouse-telemetry-0-{i}", 1) for i in range(2))),
    "redis": ("nightingale", ("obs-mtls-redis-ha-peer",),
              (("StatefulSet", "nightingale-redis-server", 3),)),
    "nightingale": ("nightingale", ("obs-mtls-n9e-postgres-client", "obs-mtls-nightingale-redis"),
                    (("Deployment", "nightingale", 2),)),
    "alertmanager": ("victoriametrics", ("obs-mtls-alertmanager-gossip",),
                     (("StatefulSet", "vmalertmanager-victoriametrics-cluster", 3),)),
}
AM_SOURCE = "obs-mtls-alertmanager-gossip"
AM_ASSETS = {AM_SOURCE: {
    "secret": "vmalertmanager-victoriametrics-cluster-config",
    "certificate_key": "victoriametrics_obs-mtls-alertmanager-gossip_tls.crt",
    "ca_key": "victoriametrics_obs-mtls-alertmanager-gossip_ca.crt",
    "key_key": "victoriametrics_obs-mtls-alertmanager-gossip_tls.key",
    "config_key": "gossip_config.yaml", "mount_path": "/etc/alertmanager/tls_assets",
    "container": "alertmanager",
}}
PLURALS = {"StatefulSet": "statefulsets", "Deployment": "deployments", "ReplicaSet": "replicasets"}


class RotationError(Exception):
    """Only redacted operational failures are exposed."""


class APIError(RotationError):
    def __init__(self, status):
        self.status = status
        super().__init__(f"Kubernetes request rejected (HTTP {status}); no fallback mutation")


class NotReady(RotationError):
    pass


def utcnow():
    return dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")


def timestamp(value):
    try:
        parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
        if parsed.tzinfo is None:
            raise ValueError()
        return parsed.timestamp()
    except (ValueError, AttributeError, TypeError):
        raise RotationError("Invalid Kubernetes timestamp") from None


def validate_policy(policy):
    if set(policy) != {"version", "groups"} or policy["version"] != 1:
        raise RotationError("Unsupported rotation policy")
    if [g.get("name") for g in policy["groups"]] != list(ALLOWED):
        raise RotationError("Policy group order or scope changed")
    for group in policy["groups"]:
        if set(group) != {"name", "namespace", "controllers", "replicas", "selector", "secrets", "pdb", "expected_ca_sha256", "generated_assets"}:
            raise RotationError("Unexpected rotation policy fields")
        namespace, secrets, controllers = ALLOWED[group["name"]]
        ca_pins(group)
        asset_mappings(group)
        actual = tuple((c.get("kind"), c.get("name"), c.get("replicas")) for c in group["controllers"])
        if (group["namespace"] != namespace or tuple(group["secrets"]) != secrets or actual != controllers
                or group["replicas"] != sum(c[2] for c in controllers)):
            raise RotationError("Policy escapes fixed certificate/workload scope")
        if not re.fullmatch(r"[a-z0-9][a-z0-9.-]{0,252}", group["pdb"]):
            raise RotationError("Invalid PDB name")
        if not isinstance(group["selector"], dict) or not group["selector"] or any(
            not isinstance(k, str) or not isinstance(v, str) or not k or not v
            for k, v in group["selector"].items()
        ):
            raise RotationError("Explicit component selector required")
    return copy.deepcopy(policy)


def matches(labels, selector):
    if not selector or set(selector) - {"matchLabels", "matchExpressions"}:
        raise RotationError("Empty or unsupported selector")
    if any(labels.get(k) != v for k, v in selector.get("matchLabels", {}).items()):
        return False
    for item in selector.get("matchExpressions", []):
        key, op, values = item.get("key"), item.get("operator"), item.get("values", [])
        if op == "In" and labels.get(key) not in values:
            return False
        if op == "NotIn" and labels.get(key) in values:
            return False
        if op == "Exists" and key not in labels:
            return False
        if op == "DoesNotExist" and key in labels:
            return False
        if op not in ("In", "NotIn", "Exists", "DoesNotExist"):
            raise RotationError("Unsupported selector operator")
    return True


def core_path(namespace, resource, name=""):
    return f"/api/v1/namespaces/{namespace}/{resource}" + (f"/{name}" if name else "")


def app_path(namespace, kind, name):
    return f"/apis/apps/v1/namespaces/{namespace}/{PLURALS[kind]}/{name}"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Kubernetes:
    def __init__(self):
        host = os.environ["KUBERNETES_SERVICE_HOST"]
        ip = ipaddress.ip_address(host)
        host = f"[{ip}]" if ip.version == 6 else str(ip)
        port = int(os.environ.get("KUBERNETES_SERVICE_PORT_HTTPS", "443"))
        if not 1 <= port <= 65535:
            raise RotationError("Invalid API service port")
        self.base = f"https://{host}:{port}"
        directory = Path("/var/run/secrets/kubernetes.io/serviceaccount")
        self.token = directory / "token"
        context = ssl.create_default_context(cafile=str(directory / "ca.crt"))
        self.opener = urllib.request.build_opener(NoRedirect(), urllib.request.HTTPSHandler(context=context))

    def request(self, method, path, body=None, content_type=None):
        if method not in ("GET", "POST", "PATCH", "PUT") or not path.startswith(("/api/", "/apis/")):
            raise RotationError("Unsupported Kubernetes operation")
        headers = {"Authorization": "Bearer " + self.token.read_text().strip(), "Accept": "application/json"}
        if body is not None:
            headers["Content-Type"] = content_type or "application/json"
        request = urllib.request.Request(self.base + path, method=method, headers=headers,
            data=None if body is None else json.dumps(body).encode())
        try:
            with self.opener.open(request, timeout=15) as response:
                content = response.read(4 * 1024 * 1024 + 1)
                if len(content) > 4 * 1024 * 1024:
                    raise RotationError("Kubernetes response exceeds safe size")
                return json.loads(content) if content else {}
        except urllib.error.HTTPError as error:
            raise APIError(error.code) from None
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError):
            raise RotationError("Kubernetes transport or response failure") from None

    def get(self, path):
        return self.request("GET", path)

    def list_pods(self, namespace, selector=None):
        query = ""
        if selector:
            query = "?" + urllib.parse.urlencode({"labelSelector": ",".join(f"{k}={v}" for k, v in sorted(selector.items()))})
        response = self.get(core_path(namespace, "pods") + query)
        if response.get("metadata", {}).get("continue"):
            raise RotationError("Unexpected paginated Pod response")
        return response["items"]


class Lease:
    def __init__(self, api, identity, utc=utcnow):
        self.api, self.identity, self.utc = api, identity, utc
        self.path = f"/apis/coordination.k8s.io/v1/namespaces/{LEASE_NAMESPACE}/leases/{LEASE_NAME}"
        self.uid = None

    def _write(self, lease):
        updated = {"apiVersion": "coordination.k8s.io/v1", "kind": "Lease",
            "metadata": {k: lease["metadata"][k] for k in ("name", "namespace", "uid", "resourceVersion")},
            "spec": {**lease.get("spec", {}), "holderIdentity": self.identity,
                     "leaseDurationSeconds": LEASE_SECONDS, "renewTime": self.utc()}}
        self.api.request("PUT", self.path, updated)

    def acquire(self):
        lease = self.api.get(self.path)
        meta, spec = lease["metadata"], lease.get("spec", {})
        if meta.get("namespace") != LEASE_NAMESPACE or meta.get("name") != LEASE_NAME:
            raise RotationError("Unexpected Lease scope")
        if spec.get("holderIdentity"):
            if timestamp(spec.get("renewTime")) + spec.get("leaseDurationSeconds", LEASE_SECONDS) >= timestamp(self.utc()):
                raise RotationError("Lease has an active holder")
        self.uid = meta["uid"]
        self._write(lease)

    def renew(self):
        lease = self.api.get(self.path)
        spec = lease.get("spec", {})
        if (lease["metadata"]["uid"] != self.uid or spec.get("holderIdentity") != self.identity
                or timestamp(spec.get("renewTime")) + LEASE_SECONDS <= timestamp(self.utc())):
            raise RotationError("Lease lost or expired")
        self._write(lease)


def ca_pins(group):
    pins = group.get("expected_ca_sha256", {})
    if not isinstance(pins, dict) or set(pins) != set(group["secrets"]) or any(
            not isinstance(value, str) or not re.fullmatch(r"[0-9a-f]{64}", value) for value in pins.values()):
        raise RotationError("Approved public CA pins missing or unfilled")
    return pins


def asset_mappings(group):
    assets = group.get("generated_assets", {})
    if assets != (AM_ASSETS if group["name"] == "alertmanager" else {}):
        raise RotationError("Unexpected generated certificate asset mapping")
    return assets


def public_bytes(secret, key):
    try:
        data = base64.b64decode(secret["data"][key], validate=True)
        if not data or len(data) > 1024 * 1024:
            raise ValueError()
        # VMOperator v0.75 LoadKeyFromSecret trims trailing whitespace before copy.
        return data.rstrip()
    except (KeyError, ValueError):
        raise RotationError("Invalid public certificate/config material") from None


def validate_gossip_config(secret, mapping):
    # VMOperator emits this shallow YAML. Reject alternate syntax/duplicates rather
    # than approximating a YAML parser that might accept a different consumer path.
    try:
        text = public_bytes(secret, mapping["config_key"]).decode("utf-8")
    except UnicodeDecodeError:
        raise RotationError("Invalid generated gossip configuration") from None
    sections, section = {}, None
    for line in text.splitlines():
        if not line.strip():
            continue
        header = re.fullmatch(r"(tls_server_config|tls_client_config):", line)
        scalar = re.fullmatch(r"  ([a-z_]+): ([^\n]+)", line)
        if header and header[1] not in sections:
            section = header[1]
            sections[section] = {}
        elif scalar and section and scalar[1] not in sections[section]:
            value = scalar[2]
            if len(value) > 1 and value[0] == value[-1] and value[0] in ("'", '"'):
                value = value[1:-1]
            sections[section][scalar[1]] = value
        else:
            raise RotationError("Unsupported generated gossip configuration syntax")
    path = mapping["mount_path"] + "/"
    expected = {
        "tls_server_config": {"cert_file": path + mapping["certificate_key"],
            "key_file": path + mapping["key_key"], "client_ca_file": path + mapping["ca_key"],
            "client_auth_type": "RequireAndVerifyClientCert"},
        "tls_client_config": {"cert_file": path + mapping["certificate_key"],
            "key_file": path + mapping["key_key"], "ca_file": path + mapping["ca_key"]},
    }
    if (sections.get("tls_client_config", {}).get("insecure_skip_verify", "false") != "false"
            or set(sections) != set(expected)) or any(
            sections[section].get(key) != value for section, fields in expected.items() for key, value in fields.items()):
        raise RotationError("Generated gossip config does not consume the approved TLS assets")


def generated_asset_uid(api, group, mapping, leaf, ca):
    secret = api.get(core_path(group["namespace"], "secrets", mapping["secret"]))
    meta = secret["metadata"]
    if meta.get("name") != mapping["secret"] or meta.get("namespace") != group["namespace"]:
        raise RotationError("Unexpected generated certificate Secret scope")
    if public_bytes(secret, mapping["certificate_key"]) != leaf or public_bytes(secret, mapping["ca_key"]) != ca:
        raise RotationError("Operator generated public certificate assets are not synchronized")
    validate_gossip_config(secret, mapping)
    return meta["uid"]


def certificate_state(api, group):
    pins, assets = ca_pins(group), asset_mappings(group)
    leaves, roots, identities = [], [], []
    for name in group["secrets"]:
        secret = api.get(core_path(group["namespace"], "secrets", name))
        meta = secret["metadata"]
        if (meta.get("name") != name or meta.get("namespace") != group["namespace"]
                or meta.get("annotations", {}).get("cert-manager.io/certificate-name") != name):
            raise RotationError("Unexpected certificate Secret owner/scope")
        identities.append(meta["uid"])
        leaf, ca = public_bytes(secret, "tls.crt"), public_bytes(secret, "ca.crt")
        ca_hash = hashlib.sha256(ca).hexdigest()
        if ca_hash != pins[name]:
            raise RotationError("Public CA pin mismatch; separate trust migration required")
        leaves.append(name + ":" + hashlib.sha256(leaf).hexdigest())
        roots.append(name + ":" + ca_hash)
        if name in assets:
            identities.append(generated_asset_uid(api, group, assets[name], leaf, ca))
    leaf = hashlib.sha256("\n".join(leaves).encode()).hexdigest()
    trust = hashlib.sha256("\n".join(roots).encode()).hexdigest()
    return {"annotation": leaf + ":" + trust, "trust": trust, "uids": tuple(identities)}


def generated_mounts(pod, group):
    for mapping in asset_mappings(group).values():
        volumes = [v for v in pod["spec"].get("volumes", [])
                   if v.get("secret", {}).get("secretName") == mapping["secret"]]
        containers = [c for c in pod["spec"]["containers"] if c["name"] == mapping["container"]]
        if len(volumes) != 1 or len(containers) != 1:
            raise RotationError("Generated TLS assets are not mounted by the native consumer")
        volume, container = volumes[0], containers[0]
        keys = [mapping[k] for k in ("certificate_key", "ca_key", "key_key", "config_key")]
        items = volume["secret"].get("items")
        if items is not None and not set(keys) <= {i["key"] for i in items if i.get("key") == i.get("path")}:
            raise RotationError("Generated TLS asset projection omits or renames required files")
        mounts = [m for m in container.get("volumeMounts", [])
                  if m.get("name") == volume["name"] and m.get("mountPath") == mapping["mount_path"]
                  and not m.get("subPath") and not m.get("subPathExpr")]
        expected_arg = "--cluster.tls-config=" + mapping["mount_path"] + "/" + mapping["config_key"]
        if len(mounts) != 1 or expected_arg not in container.get("args", []):
            raise RotationError("Native consumer does not use the approved generated gossip config")


def controller_owner(item):
    owners = [o for o in item.get("metadata", {}).get("ownerReferences", []) if o.get("controller") is True]
    if len(owners) != 1 or owners[0].get("apiVersion") != "apps/v1":
        raise RotationError("Unexpected Pod/controller owner")
    return owners[0]


def ready(pod):
    metadata, status = pod["metadata"], pod.get("status", {})
    names = {c["name"] for c in pod["spec"]["containers"]}
    statuses = status.get("containerStatuses", [])
    return (not metadata.get("deletionTimestamp") and status.get("phase") == "Running"
        and any(c.get("type") == "Ready" and c.get("status") == "True" for c in status.get("conditions", []))
        and {c.get("name") for c in statuses} == names
        and all(c.get("ready") and "running" in c.get("state", {}) for c in statuses))


class Rotator:
    def __init__(self, api, lease, timeout=600, settle=130, clock=time.monotonic, sleep=time.sleep, utc=utcnow):
        self.api, self.lease, self.timeout, self.settle = api, lease, timeout, settle
        self.clock, self.sleep, self.utc = clock, sleep, utc
        self.deadline = clock() + 6000

    def tick(self):
        if self.clock() >= self.deadline:
            raise RotationError("Overall rotation timeout")
        self.lease.renew()

    def controllers(self, group, baseline=None):
        result = {}
        for expected in group["controllers"]:
            kind, name = expected["kind"], expected["name"]
            item = self.api.get(app_path(group["namespace"], kind, name))
            meta, spec, status = item["metadata"], item["spec"], item.get("status", {})
            if (item.get("kind") != kind or meta.get("namespace") != group["namespace"] or meta.get("name") != name
                    or spec.get("replicas") != expected["replicas"]
                    or not matches(spec["template"]["metadata"]["labels"], {"matchLabels": group["selector"]})):
                raise RotationError("Controller identity/replica/selector mismatch")
            if baseline and (meta["uid"], meta["generation"]) != baseline[name]:
                raise RotationError("Controller changed during rotation")
            if (status.get("observedGeneration", 0) < meta["generation"]
                    or status.get("readyReplicas", 0) != expected["replicas"]
                    or status.get("updatedReplicas", 0) != expected["replicas"]
                    or kind == "StatefulSet" and status.get("currentRevision") != status.get("updateRevision")
                    or kind == "Deployment" and status.get("availableReplicas", 0) != expected["replicas"]):
                raise NotReady("Controller not fully ready or rollout in progress")
            result[name] = item
        return result

    def snapshot(self, group, baseline=None):
        controllers = self.controllers(group, baseline)
        pods = self.api.list_pods(group["namespace"], group["selector"])
        counts = {name: 0 for name in controllers}
        for pod in pods:
            meta, spec = pod["metadata"], pod["spec"]
            if meta.get("namespace") != group["namespace"] or any(spec.get(k) for k in ("hostNetwork", "hostPID", "hostIPC")):
                raise RotationError("Unexpected Pod scope or host namespace")
            mounted = set()
            for volume in spec.get("volumes", []):
                if volume.get("secret"):
                    mounted.add(volume["secret"]["secretName"])
                for source in volume.get("projected", {}).get("sources", []):
                    if source.get("secret"):
                        mounted.add(source["secret"]["name"])
            assets = asset_mappings(group)
            required = {assets[name]["secret"] if name in assets else name for name in group["secrets"]}
            if not required <= mounted:
                raise RotationError("Pod does not mount expected certificate Secrets")
            generated_mounts(pod, group)
            owner = controller_owner(pod)
            parent = None
            if owner["kind"] == "ReplicaSet":
                replica = self.api.get(app_path(group["namespace"], "ReplicaSet", owner["name"]))
                if (replica.get("kind") != "ReplicaSet" or replica["metadata"].get("namespace") != group["namespace"]
                        or replica["metadata"].get("name") != owner["name"] or replica["metadata"]["uid"] != owner["uid"]
                        or not matches(meta.get("labels", {}), replica["spec"]["selector"])):
                    raise RotationError("ReplicaSet owner UID mismatch")
                parent = controller_owner(replica)
            target = parent or owner
            controller = controllers.get(target["name"])
            if (not controller or target["kind"] != controller["kind"] or target["uid"] != controller["metadata"]["uid"]
                    or not matches(meta.get("labels", {}), controller["spec"]["selector"])):
                raise RotationError("Pod owner or controller selector mismatch")
            counts[target["name"]] += 1
        if len(pods) != group["replicas"] or any(not ready(p) for p in pods) or any(
                counts[c["name"]] != c["replicas"] for c in group["controllers"]):
            raise NotReady("Component replicas are not all Ready")
        return pods, {name: (obj["metadata"]["uid"], obj["metadata"]["generation"]) for name, obj in controllers.items()}

    def check_pdb(self, group, pods):
        path = f"/apis/policy/v1/namespaces/{group['namespace']}/poddisruptionbudgets/{group['pdb']}"
        pdb = self.api.get(path)
        status, meta = pdb.get("status", {}), pdb["metadata"]
        selector = pdb.get("spec", {}).get("selector")
        covered = {p["metadata"]["uid"] for p in self.api.list_pods(group["namespace"])
                   if matches(p["metadata"].get("labels", {}), selector)}
        if (meta.get("namespace") != group["namespace"] or meta.get("name") != group["pdb"]
                or covered != {p["metadata"]["uid"] for p in pods}
                or status.get("observedGeneration", 0) < meta["generation"]
                or status.get("expectedPods") != group["replicas"]
                or status.get("currentHealthy", 0) < group["replicas"]
                or status.get("disruptionsAllowed", 0) < 1):
            raise RotationError("PDB stale, scope mismatched, or disruption budget unavailable")

    def unchanged(self, group, state):
        if certificate_state(self.api, group) != state:
            raise RotationError("Public certificate snapshot changed during rotation")

    def settle_secrets(self, group, state):
        deadline = self.clock() + self.settle
        while self.clock() < deadline:
            self.tick()
            self.unchanged(group, state)
            self.sleep(min(5, deadline - self.clock()))
        self.unchanged(group, state)

    def wait_replacement(self, group, baseline, previous, victim, state, started):
        deadline = self.clock() + self.timeout
        old_uids = {p["metadata"]["uid"] for p in previous}
        survivors = old_uids - {victim["metadata"]["uid"]}
        while self.clock() < deadline:
            self.tick()
            self.unchanged(group, state)
            try:
                pods, _ = self.snapshot(group, baseline)
            except NotReady:
                self.sleep(5)
                continue
            current = {p["metadata"]["uid"] for p in pods}
            if not survivors <= current:
                raise RotationError("Unexpected concurrent Pod replacement")
            new = current - old_uids
            if new and victim["metadata"]["uid"] not in current:
                if len(new) != 1:
                    raise RotationError("Unexpected replacement Pod count")
                replacement = next(p for p in pods if p["metadata"]["uid"] in new)
                if timestamp(replacement["metadata"]["creationTimestamp"]) < timestamp(started) - 2:
                    raise RotationError("Replacement predates certificate rotation")
                return replacement
            self.sleep(5)
        raise RotationError("Replacement readiness timeout; no further eviction")

    def record(self, group, pod, state):
        self.tick()
        self.unchanged(group, state)
        meta = pod["metadata"]
        patch = [{"op": "test", "path": "/metadata/uid", "value": meta["uid"]},
                 {"op": "test", "path": "/metadata/resourceVersion", "value": meta["resourceVersion"]}]
        if "annotations" not in meta:
            patch.append({"op": "add", "path": "/metadata/annotations", "value": {}})
        escaped = ANNOTATION.replace("~", "~0").replace("/", "~1")
        patch.append({"op": "add", "path": "/metadata/annotations/" + escaped, "value": state["annotation"]})
        self.api.request("PATCH", core_path(group["namespace"], "pods", meta["name"]), patch,
                         "application/json-patch+json")

    def rotate_group(self, group):
        self.tick()
        pods, baseline = self.snapshot(group)
        state = certificate_state(self.api, group)
        for pod in pods:
            annotation = pod["metadata"].get("annotations", {}).get(ANNOTATION)
            if annotation is not None:
                if not re.fullmatch(r"[0-9a-f]{64}:[0-9a-f]{64}", annotation):
                    raise RotationError("Invalid public certificate annotation")
                if annotation.split(":")[1] != state["trust"]:
                    raise RotationError("CA change requires separate overlapping-trust migration")
        pending = lambda items: [p for p in items if p["metadata"].get("annotations", {}).get(ANNOTATION) != state["annotation"]]
        if not pending(pods):
            return
        self.settle_secrets(group, state)
        for _ in range(group["replicas"]):
            self.tick()
            self.unchanged(group, state)
            pods, _ = self.snapshot(group, baseline)
            if not pending(pods):
                return
            victim = sorted(pending(pods), key=lambda p: p["metadata"]["name"])[0]
            self.check_pdb(group, pods)
            self.tick()
            started = self.utc()
            meta = victim["metadata"]
            eviction = {"apiVersion": "policy/v1", "kind": "Eviction", "metadata": {
                "name": meta["name"], "namespace": group["namespace"]}, "deleteOptions": {
                "preconditions": {"uid": meta["uid"], "resourceVersion": meta["resourceVersion"]}}}
            self.api.request("POST", core_path(group["namespace"], "pods", meta["name"]) + "/eviction", eviction)
            replacement = self.wait_replacement(group, baseline, pods, victim, state, started)
            self.record(group, replacement, state)
        final, _ = self.snapshot(group, baseline)
        if pending(final):
            raise RotationError("Rotation incomplete; resume in next scheduled run")


def main():
    try:
        policy = validate_policy(json.loads(Path("/config/rotation-policy.json").read_text()))
        api = Kubernetes()
        lease = Lease(api, os.environ.get("POD_UID", "") + "-" + uuid.uuid4().hex)
        lease.acquire()
        # Check all approved trust roots and generated assets before any group is disrupted.
        for group in policy["groups"]:
            certificate_state(api, group)
        runner = Rotator(api, lease)
        for group in policy["groups"]:
            runner.rotate_group(group)
            print(f"{group['namespace']}/{group['name']}: certificate restart state current", flush=True)
        return 0
    except RotationError as error:
        print(f"Rotation stopped safely: {error}", file=sys.stderr)
    except Exception:
        print("Rotation stopped safely: unexpected input/runtime error (details suppressed)", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
