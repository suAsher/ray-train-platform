#!/usr/bin/env python3
"""Negative acceptance checks for cert-manager approver-policy."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import re
import subprocess
import sys
import time
import urllib.parse
import uuid
from dataclasses import dataclass
from typing import Any

EXPECTED_CONTEXT = "cd9q3b7rf9aidnnbd0je0@81533555-kd9q3q0frjmp21sk5lklg"
NEW_NAMESPACES = {"victoriametrics", "clickhouse", "opentelemetry", "nightingale"}
URI_ROOT = "spiffe://observability.raytrain.local"
ISSUER = "obs-mtls-postgres-server"
POLICY = "obs-mtls-nightingale-n9e-postgres-server"
READY_CERT = ("nightingale", "obs-mtls-n9e-postgres-server")
DEPLOYMENTS = (
    "cert-manager",
    "cert-manager-webhook",
    "cert-manager-cainjector",
    "cert-manager-approver-policy",
)
VALID_URI = f"{URI_ROOT}/nightingale/n9e-postgres-server"
VALID_DNS = [
    "nightingale-postgres-rw.nightingale.svc",
    "nightingale-postgres-rw.nightingale.svc.cluster.local",
    "nightingale-postgres-ro.nightingale.svc",
    "nightingale-postgres-ro.nightingale.svc.cluster.local",
    "nightingale-postgres-r.nightingale.svc",
    "nightingale-postgres-r.nightingale.svc.cluster.local",
]

@dataclass(frozen=True)
class Case:
    name: str
    namespace: str
    uris: list[str]
    dns_names: list[str]
    expected: str
    reason: str

@dataclass(frozen=True)
class Created:
    case: Case
    namespace: str
    name: str
    secret: str
    uid: str

class KubeError(RuntimeError):
    def __init__(self, args: list[str], result: subprocess.CompletedProcess[str]):
        super().__init__(f"command failed ({result.returncode}): {' '.join(args[:5])} ...")
        self.stderr = result.stderr

def cases() -> list[Case]:
    return [
        Case(
            "wrong-uri",
            "nightingale",
            [f"{URI_ROOT}/nightingale/n9e-postgres-server-wrong"],
            VALID_DNS,
            "denied",
            "URI outside policy allowlist",
        ),
        Case(
            "wrong-dns",
            "nightingale",
            [VALID_URI],
            [*VALID_DNS[:-1], "nightingale-postgres-r.victoriametrics.svc"],
            "denied",
            "DNS outside policy allowlist",
        ),
        Case(
            "wrong-namespace",
            "victoriametrics",
            [VALID_URI],
            VALID_DNS,
            "pending_no_matching_policy",
            "no postgres-server policy should match this namespace",
        ),
    ]

def run(args: list[str], *, stdin: str | None = None, timeout: int = 30, check: bool = True):
    result = subprocess.run(
        args,
        input=stdin,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=timeout,
        check=False,
    )
    if check and result.returncode != 0:
        raise KubeError(args, result)
    return result

def kubectl(kubectl_bin: str, context: str, args: list[str], **kwargs):
    return run([kubectl_bin, "--context", context, *args], **kwargs)

def kjson(kubectl_bin: str, context: str, args: list[str], *, timeout: int = 30):
    return json.loads(kubectl(kubectl_bin, context, [*args, "-o", "json"], timeout=timeout).stdout)

def is_not_found(error: KubeError) -> bool:
    text = error.stderr.lower()
    return "notfound" in text or "not found" in text

def get_obj(kubectl_bin: str, context: str, resource: str, ns: str, name: str, timeout: int):
    try:
        return kjson(kubectl_bin, context, ["get", resource, "-n", ns, name], timeout=timeout)
    except KubeError as error:
        if is_not_found(error):
            return None
        raise

def cond_true(obj: dict[str, Any], cond_type: str) -> bool:
    return any(
        c.get("type") == cond_type and c.get("status") == "True"
        for c in obj.get("status", {}).get("conditions", [])
    )

def conds(obj: dict[str, Any]) -> list[dict[str, Any]]:
    return [
        {"type": c.get("type"), "status": c.get("status"), "reason": c.get("reason")}
        for c in obj.get("status", {}).get("conditions", [])
    ]

def owned_by(obj: dict[str, Any], uid: str) -> bool:
    return any(
        ref.get("kind") == "Certificate" and ref.get("uid") == uid
        for ref in obj.get("metadata", {}).get("ownerReferences", [])
    )

def metadata_owned_by(meta: dict[str, Any], uid: str) -> bool:
    return any(
        ref.get("kind") == "Certificate" and ref.get("uid") == uid
        for ref in meta.get("ownerReferences", [])
    )

def manifest(case: Case, run_id: str) -> dict[str, Any]:
    name = f"obs-neg-{case.name}-{run_id}"
    return {
        "apiVersion": "cert-manager.io/v1",
        "kind": "Certificate",
        "metadata": {
            "name": name,
            "namespace": case.namespace,
            "labels": {
                "raytrain.wellspiking.ai/acceptance": "negative-certificate",
                "raytrain.wellspiking.ai/run-id": run_id,
            },
            "annotations": {
                "raytrain.wellspiking.ai/negative-case": case.name,
                "raytrain.wellspiking.ai/negative-reason": case.reason,
            },
        },
        "spec": {
            "secretName": f"{name}-secret",
            "duration": "2160h",
            "renewBefore": "720h",
            "issuerRef": {"name": ISSUER, "kind": "ClusterIssuer", "group": "cert-manager.io"},
            "usages": ["server auth"],
            "uris": case.uris,
            "dnsNames": case.dns_names,
            "commonName": "n9e-postgres-server",
            "privateKey": {"algorithm": "RSA", "size": 2048, "rotationPolicy": "Always"},
        },
    }

def certificate_identity_matches(cert: dict[str, Any], doc: dict[str, Any], case: Case, run_id: str) -> bool:
    meta, spec = cert.get("metadata", {}), cert.get("spec", {})
    labels, annotations = meta.get("labels", {}), meta.get("annotations", {})
    wanted = doc["spec"]
    return (
        meta.get("name") == doc["metadata"]["name"]
        and meta.get("namespace") == case.namespace
        and labels.get("raytrain.wellspiking.ai/run-id") == run_id
        and annotations.get("raytrain.wellspiking.ai/negative-case") == case.name
        and spec.get("secretName") == wanted["secretName"]
        and spec.get("issuerRef") == wanted["issuerRef"]
        and spec.get("uris") == wanted["uris"]
        and spec.get("dnsNames") == wanted["dnsNames"]
    )

def assert_context(kubectl_bin: str, context: str, timeout: int):
    current = run([kubectl_bin, "config", "current-context"], timeout=timeout).stdout.strip()
    if context != EXPECTED_CONTEXT or current != EXPECTED_CONTEXT:
        raise SystemExit(f"Refusing context: current={current!r}, requested={context!r}")

def preflight(kubectl_bin: str, context: str, timeout: int):
    assert_context(kubectl_bin, context, timeout)
    if not cond_true(kjson(kubectl_bin, context, ["get", "clusterissuer", ISSUER], timeout=timeout), "Ready"):
        raise SystemExit(f"ClusterIssuer {ISSUER} is not Ready")
    kjson(kubectl_bin, context, ["get", "certificaterequestpolicies.policy.cert-manager.io", POLICY], timeout=timeout)
    ns, cert = READY_CERT
    if not cond_true(kjson(kubectl_bin, context, ["get", "certificate", "-n", ns, cert], timeout=timeout), "Ready"):
        raise SystemExit(f"Certificate {ns}/{cert} is not Ready")
    for name in DEPLOYMENTS:
        deploy = kjson(kubectl_bin, context, ["get", "deployment", "-n", "cert-manager", name], timeout=timeout)
        if not cond_true(deploy, "Available"):
            raise SystemExit(f"Deployment cert-manager/{name} is not Available")

def assert_controller_health(kubectl_bin: str, context: str, timeout: int):
    for name in DEPLOYMENTS:
        deploy = kjson(kubectl_bin, context, ["get", "deployment", "-n", "cert-manager", name], timeout=timeout)
        if not cond_true(deploy, "Available"):
            raise SystemExit(f"Deployment cert-manager/{name} is not Available")

def policy_matches_namespace(policy: dict[str, Any], namespace: str) -> bool:
    issuer = policy.get("spec", {}).get("selector", {}).get("issuerRef", {})
    if issuer != {"name": ISSUER, "kind": "ClusterIssuer", "group": "cert-manager.io"}:
        return False
    names = policy.get("spec", {}).get("selector", {}).get("namespace", {}).get("matchNames")
    return not names or "*" in names or namespace in names

def matching_policy_names(policies: list[dict[str, Any]], namespace: str) -> list[str]:
    return sorted(
        p.get("metadata", {}).get("name", "<unknown>")
        for p in policies
        if policy_matches_namespace(p, namespace)
    )

def assert_no_matching_policy(kubectl_bin: str, context: str, namespace: str, timeout: int):
    policies = kjson(kubectl_bin, context, ["get", "certificaterequestpolicies.policy.cert-manager.io"], timeout=timeout)
    names = matching_policy_names(policies.get("items", []), namespace)
    if names:
        raise SystemExit(f"Expected no postgres-server policy for {namespace}, matched {names}")

def list_owned_requests(kubectl_bin: str, context: str, namespace: str, uid: str, timeout: int):
    items = kjson(kubectl_bin, context, ["get", "certificaterequests.cert-manager.io", "-n", namespace], timeout=timeout)
    return [item for item in items.get("items", []) if owned_by(item, uid)]

def secret_metadata(kubectl_bin: str, context: str, namespace: str, timeout: int) -> list[dict[str, Any]]:
    template = (
        "{range .items[*]}{.metadata.name}{\"\\t\"}{.metadata.uid}{\"\\t\"}"
        "{.metadata.resourceVersion}{\"\\t\"}{range .metadata.ownerReferences[*]}"
        "{.kind}:{.uid},{end}{\"\\n\"}{end}"
    )
    output = kubectl(kubectl_bin, context, ["get", "secrets", "-n", namespace, "-o", f"jsonpath={template}"], timeout=timeout).stdout
    rows = []
    for line in output.splitlines():
        name, uid, rv, owners = (line.split("\t") + ["", "", "", ""])[:4]
        rows.append({"name": name, "uid": uid, "resourceVersion": rv, "owners": owners})
    return rows

def secret_meta_by_name(kubectl_bin: str, context: str, namespace: str, name: str, timeout: int):
    for item in secret_metadata(kubectl_bin, context, namespace, timeout):
        if item["name"] == name:
            return item
    return None

def owner_uid_in_secret_meta(item: dict[str, Any], uid: str) -> bool:
    return f"Certificate:{uid}," in item.get("owners", "")

def owned_secret_metadata(kubectl_bin: str, context: str, namespace: str, uid: str, timeout: int):
    return [item for item in secret_metadata(kubectl_bin, context, namespace, timeout) if owner_uid_in_secret_meta(item, uid)]

def create_case(kubectl_bin: str, context: str, case: Case, run_id: str, timeout: int) -> Created:
    if case.namespace not in NEW_NAMESPACES:
        raise SystemExit(f"Refusing namespace outside new stack: {case.namespace}")
    doc = manifest(case, run_id)
    name, secret = doc["metadata"]["name"], doc["spec"]["secretName"]
    if not name.startswith("obs-neg-") or not secret.startswith("obs-neg-"):
        raise SystemExit("Refusing non obs-neg object names")
    if get_obj(kubectl_bin, context, "certificates.cert-manager.io", case.namespace, name, timeout):
        raise SystemExit(f"Certificate {case.namespace}/{name} already exists")
    if secret_meta_by_name(kubectl_bin, context, case.namespace, secret, timeout):
        raise SystemExit(f"Secret {case.namespace}/{secret} already exists")
    try:
        cert = json.loads(kubectl(kubectl_bin, context, ["create", "-o", "json", "-f", "-"], stdin=json.dumps(doc), timeout=timeout).stdout)
    except (KubeError, json.JSONDecodeError):
        cert = get_obj(kubectl_bin, context, "certificates.cert-manager.io", case.namespace, name, timeout)
        if not cert or not certificate_identity_matches(cert, doc, case, run_id):
            raise
    if not certificate_identity_matches(cert, doc, case, run_id):
        raise SystemExit(f"Created Certificate identity mismatch for {case.namespace}/{name}")
    return Created(case, case.namespace, name, secret, cert["metadata"]["uid"])

def wait_denied(kubectl_bin: str, context: str, created: Created, timeout: int):
    deadline, last = time.time() + timeout, []
    while time.time() < deadline:
        last = list_owned_requests(kubectl_bin, context, created.namespace, created.uid, timeout)
        for req in last:
            if cond_true(req, "Denied"):
                return req
        time.sleep(2)
    raise SystemExit(f"{created.case.name}: no Denied request; last={summarize(last)}")

def wait_pending_no_policy(kubectl_bin: str, context: str, created: Created, stable: int, timeout: int):
    assert_no_matching_policy(kubectl_bin, context, created.namespace, timeout)
    deadline, stable_since, last = time.time() + max(timeout, stable + 30), None, []
    while time.time() < deadline:
        last = list_owned_requests(kubectl_bin, context, created.namespace, created.uid, timeout)
        pending = len(last) == 1 and not any(cond_true(last[0], c) for c in ("Approved", "Denied", "Ready"))
        stable_since = stable_since or time.time() if pending else None
        if pending and time.time() - stable_since >= stable:
            assert_controller_health(kubectl_bin, context, timeout)
            assert_no_matching_policy(kubectl_bin, context, created.namespace, timeout)
            return last[0]
        time.sleep(3)
    raise SystemExit(f"{created.case.name}: not stable pending_no_matching_policy; last={summarize(last)}")

def summarize(requests: list[dict[str, Any]]) -> str:
    return json.dumps(
        [{"name": r.get("metadata", {}).get("name"), "uid": r.get("metadata", {}).get("uid"), "conditions": conds(r)} for r in requests],
        ensure_ascii=False,
    )

def api_path(group_version: str, namespace: str, plural: str, name: str) -> str:
    ns, nm = urllib.parse.quote(namespace, safe=""), urllib.parse.quote(name, safe="")
    if group_version == "v1":
        return f"/api/v1/namespaces/{ns}/{plural}/{nm}"
    return f"/apis/{group_version}/namespaces/{ns}/{plural}/{nm}"

def delete_options(uid: str, rv: str) -> dict[str, Any]:
    return {"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": {"uid": uid, "resourceVersion": rv}}

def raw_delete(kubectl_bin: str, context: str, path: str, uid: str, rv: str, timeout: int):
    try:
        kubectl(
            kubectl_bin,
            context,
            ["delete", "--raw", path, "-f", "-"],
            stdin=json.dumps(delete_options(uid, rv)),
            timeout=timeout,
        )
    except KubeError as error:
        if is_not_found(error):
            return
        raise

def delete_if_uid(kubectl_bin: str, context: str, resource: str, group_version: str, plural: str, ns: str, name: str, uid: str, timeout: int):
    obj = get_obj(kubectl_bin, context, resource, ns, name, timeout)
    if not obj:
        return True
    meta = obj["metadata"]
    if meta["uid"] != uid:
        raise SystemExit(f"Refusing delete {resource} {ns}/{name}: uid mismatch")
    raw_delete(kubectl_bin, context, api_path(group_version, ns, plural, name), uid, meta["resourceVersion"], timeout)
    return False

def wait_gone(kubectl_bin: str, context: str, resource: str, ns: str, name: str, timeout: int):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if not get_obj(kubectl_bin, context, resource, ns, name, timeout):
            return
        time.sleep(2)
    raise SystemExit(f"{resource} {ns}/{name} still exists after cleanup")

def wait_no_owned_secrets(kubectl_bin: str, context: str, namespace: str, uid: str, timeout: int):
    deadline = time.time() + timeout
    while time.time() < deadline:
        items = owned_secret_metadata(kubectl_bin, context, namespace, uid, timeout)
        if not items:
            return
        time.sleep(2)
    raise SystemExit(f"owned temporary key Secrets remain for Certificate uid {uid}")

def wait_secret_metadata_gone(kubectl_bin: str, context: str, namespace: str, name: str, timeout: int):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if not secret_meta_by_name(kubectl_bin, context, namespace, name, timeout):
            return
        time.sleep(2)
    raise SystemExit(f"Secret {namespace}/{name} still exists after cleanup")

def verify_no_secret(kubectl_bin: str, context: str, created: Created, timeout: int):
    if secret_meta_by_name(kubectl_bin, context, created.namespace, created.secret, timeout):
        raise SystemExit(f"{created.case.name}: unexpected Secret {created.namespace}/{created.secret}")

def cleanup(kubectl_bin: str, context: str, created: Created, timeout: int):
    delete_if_uid(kubectl_bin, context, "certificates.cert-manager.io", "cert-manager.io/v1", "certificates", created.namespace, created.name, created.uid, timeout)
    wait_gone(kubectl_bin, context, "certificates.cert-manager.io", created.namespace, created.name, timeout)
    for req in list_owned_requests(kubectl_bin, context, created.namespace, created.uid, timeout):
        meta = req["metadata"]
        raw_delete(kubectl_bin, context, api_path("cert-manager.io/v1", created.namespace, "certificaterequests", meta["name"]), meta["uid"], meta["resourceVersion"], timeout)
        wait_gone(kubectl_bin, context, "certificaterequests.cert-manager.io", created.namespace, meta["name"], timeout)
    for secret in owned_secret_metadata(kubectl_bin, context, created.namespace, created.uid, timeout):
        raw_delete(kubectl_bin, context, api_path("v1", created.namespace, "secrets", secret["name"]), secret["uid"], secret["resourceVersion"], timeout)
    wait_no_owned_secrets(kubectl_bin, context, created.namespace, created.uid, timeout)
    secret = secret_meta_by_name(kubectl_bin, context, created.namespace, created.secret, timeout)
    if secret and owner_uid_in_secret_meta(secret, created.uid):
        raw_delete(kubectl_bin, context, api_path("v1", created.namespace, "secrets", created.secret), secret["uid"], secret["resourceVersion"], timeout)
        wait_secret_metadata_gone(kubectl_bin, context, created.namespace, created.secret, timeout)
    elif secret:
        print(f"LEAVE unknown-owner Secret {created.namespace}/{created.secret}; manual review required")
    if list_owned_requests(kubectl_bin, context, created.namespace, created.uid, timeout):
        raise SystemExit(f"owned CertificateRequests remain for {created.name}")

def execute(kubectl_bin: str, context: str, timeout: int, stable: int, run_id: str, keep: bool):
    failures = []
    for case in cases():
        created = None
        try:
            print(f"CREATE {case.name}: {case.namespace}")
            created = create_case(kubectl_bin, context, case, run_id, timeout)
            req = wait_denied(kubectl_bin, context, created, timeout) if case.expected == "denied" else wait_pending_no_policy(kubectl_bin, context, created, stable, timeout)
            verify_no_secret(kubectl_bin, context, created, timeout)
            print(f"PASS {case.name}: {created.namespace}/{req['metadata']['name']} {case.expected}; no Secret")
        except Exception as error:
            failures.append(f"{case.name}: {error}")
        finally:
            if created and not keep:
                cleanup(kubectl_bin, context, created, timeout)
    if failures:
        raise SystemExit("\n".join(failures))

def plan(run_id: str):
    print("Read-only plan. Add --execute only after readiness is confirmed.")
    for case in cases():
        doc = manifest(case, run_id)
        print(json.dumps({"case": case.name, "namespace": case.namespace, "certificate": doc["metadata"]["name"], "secret": doc["spec"]["secretName"], "expected": case.expected}, ensure_ascii=False))

def default_run_id() -> str:
    return f"{dt.datetime.utcnow().strftime('%H%M%S')}-{uuid.uuid4().hex[:8]}"

def parse_args(argv: list[str]):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubectl", default="kubectl")
    parser.add_argument("--context", default=EXPECTED_CONTEXT)
    parser.add_argument("--timeout-seconds", type=int, default=120)
    parser.add_argument("--stable-seconds", type=int, default=45)
    parser.add_argument("--run-id", default=default_run_id())
    parser.add_argument("--execute", action="store_true")
    parser.add_argument("--keep", action="store_true")
    return parser.parse_args(argv)

def main(argv: list[str]) -> int:
    args = parse_args(argv)
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,20}", args.run_id):
        raise SystemExit("--run-id must be a DNS-label fragment up to 21 characters")
    if args.stable_seconds < 45:
        raise SystemExit("--stable-seconds must be at least 45")
    preflight(args.kubectl, args.context, args.timeout_seconds)
    if args.execute:
        execute(args.kubectl, args.context, args.timeout_seconds, args.stable_seconds, args.run_id, args.keep)
    else:
        plan(args.run_id)
    return 0

if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
