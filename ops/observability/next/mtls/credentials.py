#!/usr/bin/env python3
"""Create new observability mTLS identities; default is an offline plan.

CA keys stay in a root-owned directory on the approved builder, never in Pods.
Leaf Secrets travel to kubectl on stdin and are not printed or archived.
Existing Secret names are never overwritten. Partial creation requires review.
"""
import argparse
import copy
import fcntl
import json
import os
from pathlib import Path
import re
import secrets
import string
import subprocess
import sys
import tempfile

NAMESPACES = frozenset(("victoriametrics", "clickhouse", "opentelemetry", "nightingale"))
# Offline runtime acceptance needs these exact new client identities. Keep the
# legacy Secret application scope unchanged: production issuance uses reviewed
# cert-manager Policies, not this bootstrap helper.
RUNTIME_CLIENTS = frozenset((("grafana", "grafana-query"),
                            ("ray-train-platform", "backend-telemetry")))
TRUST_DOMAINS = frozenset(("mesh", "am-gossip", "redis", "postgres-client"))
URI_ROOT = "spiffe://observability.raytrain.local"
LEAF_DAYS = 90
CA_DAYS = 1095
LABEL = "observability.raytrain.io/credential-owner"
FIELDS = frozenset(("namespace", "role", "trust_domain", "dns_names", "usages", "common_name"))
PUBLIC_CA_NAMES = {"obs-mtls-cnpg-metrics-ca": "mesh",
                   "obs-mtls-postgres-client-ca": "postgres-client"}
KUBE_CONTEXT = "cd9q3b7rf9aidnnbd0je0@81533555-kd9q3q0frjmp21sk5lklg"


class CredentialError(Exception):
    """Redacted failure safe to show operators."""


def validate_plan(entries):
    if not isinstance(entries, list) or not 1 <= len(entries) <= 80:
        raise CredentialError("Identity plan must contain 1 to 80 entries")
    seen = set()
    for item in entries:
        if not isinstance(item, dict) or set(item) - FIELDS:
            raise CredentialError("Unknown identity plan fields")
        ns, role = item.get("namespace"), item.get("role")
        runtime_client = isinstance(ns, str) and isinstance(role, str) and (ns, role) in RUNTIME_CLIENTS
        if (ns not in NAMESPACES and not runtime_client) or not isinstance(role, str) or not re.fullmatch(r"[a-z][a-z0-9-]{0,48}", role):
            raise CredentialError("Identity is outside the allowed namespace/name scope")
        if (ns, role) in seen:
            raise CredentialError("Duplicate Secret identity")
        seen.add((ns, role))
        domain = item.get("trust_domain")
        if runtime_client and (domain != "mesh" or item.get("usages") != ["clientAuth"]
                               or item.get("dns_names") != [] or item.get("common_name", role) != role):
            raise CredentialError("Runtime-only identities must be exact mesh clients without DNS aliases")
        if domain not in TRUST_DOMAINS:
            raise CredentialError("Unknown signing trust domain")
        if domain == "am-gossip" and (ns != "victoriametrics" or role != "alertmanager-gossip"):
            raise CredentialError("Alertmanager peer CA may only issue its dedicated peer identity")
        if domain == "redis" and (ns != "nightingale" or role not in ("redis-ha-peer", "nightingale-redis")):
            raise CredentialError("Redis CA may only issue Redis peers and its Nightingale client")
        if domain == "postgres-client" and (ns != "nightingale" or role not in
                ("n9e-postgres-client", "n9e-postgres-bootstrap", "n9e-postgres-replication")):
            raise CredentialError("PostgreSQL client CA identity is outside its dedicated scope")
        cn = item.get("common_name", role)
        if not isinstance(cn, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,63}", cn):
            raise CredentialError("Invalid certificate common name")
        usages = item.get("usages")
        if not isinstance(usages, list) or not usages or len(set(usages)) != len(usages) or not set(usages) <= {"serverAuth", "clientAuth"}:
            raise CredentialError("Explicit serverAuth/clientAuth certificate usages required")
        dns = item.get("dns_names")
        if not isinstance(dns, list) or len(dns) > 80 or any(not isinstance(x, str) for x in dns):
            raise CredentialError("Invalid DNS identity list")
        if len(set(dns)) != len(dns) or ("serverAuth" in usages and not dns):
            raise CredentialError("Server identity requires unique explicit DNS names")
        for host in dns:
            if not re.fullmatch(r"[a-z0-9](?:[a-z0-9.-]{0,250}[a-z0-9])?", host) or ".." in host:
                raise CredentialError("DNS names must be exact, without wildcards or extensions")
            if not any(host.endswith("." + namespace + suffix) for namespace in NAMESPACES
                       for suffix in (".svc", ".svc.cluster.local")):
                raise CredentialError("DNS name is outside the new observability Services")
    return copy.deepcopy(entries)


def identity(item):
    return f"{URI_ROOT}/{item['namespace']}/{item['role']}"


def openssl(arguments):
    try:
        result = subprocess.run(["openssl", *map(str, arguments)], capture_output=True,
                                text=True, timeout=60, check=False)
    except (OSError, subprocess.TimeoutExpired):
        raise CredentialError("OpenSSL failed; private diagnostics withheld") from None
    if result.returncode:
        raise CredentialError("OpenSSL validation/signing failed; private diagnostics withheld")
    return result.stdout


def private_write(path, text):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as stream:
        stream.write(text)


def private_directory(path):
    path = Path(path)
    if not path.is_absolute() or any(p.is_symlink() for p in (path, *path.parents)):
        raise CredentialError("PKI path must be absolute without symlink ancestors")
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    stat = path.stat()
    if not path.is_dir() or stat.st_uid != os.geteuid() or stat.st_mode & 0o077:
        raise CredentialError("PKI directory must be privately owned with mode 0700")
    return path


def keypair(path):
    openssl(["genpkey", "-algorithm", "RSA", "-pkeyopt", "rsa_keygen_bits:2048", "-out", path])
    path.chmod(0o600)


def signing_ca(directory, domain):
    key, cert = directory / f"{domain}.key", directory / f"{domain}.crt"
    if key.is_symlink() or cert.is_symlink():
        raise CredentialError("CA files may not be symlinks")
    if key.exists() != cert.exists():
        raise CredentialError("Incomplete CA state; preserve files and review before retrying")
    if not key.exists():
        keypair(key)
        openssl(["req", "-new", "-x509", "-sha256", "-key", key, "-out", cert,
                 "-days", str(CA_DAYS), "-subj", f"/CN=raytrain-observability-{domain}",
                 "-addext", "basicConstraints=critical,CA:TRUE,pathlen:0",
                 "-addext", "keyUsage=critical,keyCertSign,cRLSign",
                 "-addext", "subjectKeyIdentifier=hash"])
        cert.chmod(0o600)
    if key.stat().st_mode & 0o077 or key.stat().st_uid != os.geteuid():
        raise CredentialError("CA key permissions or ownership are unsafe")
    if openssl(["pkey", "-in", key, "-pubout"]) != openssl(["x509", "-in", cert, "-pubkey", "-noout"]):
        raise CredentialError("CA key does not match its certificate")
    openssl(["x509", "-in", cert, "-checkend", str((LEAF_DAYS + 1) * 86400), "-noout"])
    openssl(["verify", "-CAfile", cert, cert])
    return key, cert


def issue_material(item, pki_directory):
    item = validate_plan([item])[0]
    directory = private_directory(pki_directory)
    old_umask = os.umask(0o077)
    try:
        lock_fd = os.open(directory / ".signing.lock", os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        with os.fdopen(lock_fd, "a+") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            ca_key, ca_cert = signing_ca(directory, item["trust_domain"])
            with tempfile.TemporaryDirectory(prefix="leaf-", dir=directory) as temporary:
                leaf = Path(temporary)
                key, cert, csr, config = (leaf / name for name in ("tls.key", "tls.crt", "tls.csr", "extensions.cnf"))
                keypair(key)
                openssl(["req", "-new", "-sha256", "-key", key, "-out", csr,
                         "-subj", "/CN=" + item.get("common_name", item["role"])])
                sans = ["URI:" + identity(item), *["DNS:" + host for host in item["dns_names"]]]
                private_write(config, "\n".join(("basicConstraints=critical,CA:FALSE",
                    "keyUsage=critical,digitalSignature,keyEncipherment",
                    "extendedKeyUsage=" + ",".join(item["usages"]), "subjectKeyIdentifier=hash",
                    "authorityKeyIdentifier=keyid,issuer", "subjectAltName=" + ",".join(sans))) + "\n")
                openssl(["x509", "-req", "-sha256", "-in", csr, "-CA", ca_cert, "-CAkey", ca_key,
                         "-set_serial", "0x" + secrets.token_hex(16), "-days", str(LEAF_DAYS),
                         "-extfile", config, "-out", cert])
                for usage in item["usages"]:
                    openssl(["verify", "-CAfile", ca_cert, "-purpose",
                             "sslserver" if usage == "serverAuth" else "sslclient", cert])
                return {"tls.crt": cert.read_text(), "tls.key": key.read_text(), "ca.crt": ca_cert.read_text()}
    finally:
        os.umask(old_umask)


def make_secret(item, material):
    item = validate_plan([item])[0]
    if set(material) != {"tls.crt", "tls.key", "ca.crt"} or not all(isinstance(v, str) and v for v in material.values()):
        raise CredentialError("Only nonempty leaf certificate/key and public CA are allowed")
    return {"apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/tls",
        "metadata": {"namespace": item["namespace"], "name": "obs-mtls-" + item["role"],
            "labels": {LABEL: "mtls", "app.kubernetes.io/part-of": "raytrain-observability-additive",
                       "observability.raytrain.io/trust-domain": item["trust_domain"]},
            "annotations": {"observability.raytrain.io/identity": identity(item)}},
        "stringData": dict(material)}


def check_absent(namespace, name):
    try:
        result = subprocess.run(["kubectl", "--context", KUBE_CONTEXT, "--request-timeout=20s", "-n", namespace,
            "get", "secret", name, "--ignore-not-found", "-o", "name"], capture_output=True,
            text=True, timeout=30, check=False)
    except (OSError, subprocess.TimeoutExpired):
        raise CredentialError("Cannot check target Secret; nothing will be overwritten") from None
    if result.returncode or result.stdout.strip():
        raise CredentialError(f"Cannot confirm absence of {namespace}/{name}; refusing overwrite")


def preflight_absent(plan):
    for item in validate_plan(plan):
        check_absent(item["namespace"], "obs-mtls-" + item["role"])


def create_secrets(bundle):
    if not isinstance(bundle, list) or not 1 <= len(bundle) <= 83:
        raise CredentialError("Invalid Secret bundle size")
    seen = set()
    for secret in bundle:
        meta = secret.get("metadata", {})
        name = meta.get("name", "")
        namespace = meta.get("namespace")
        labels = meta.get("labels", {})
        data = secret.get("stringData", {})
        key = (namespace, name)
        if namespace not in NAMESPACES or key in seen or labels.get(LABEL) != "mtls":
            raise CredentialError("Secret identity/owner is outside the new mTLS scope")
        seen.add(key)
        if secret.get("kind") != "Secret" or secret.get("apiVersion") != "v1" or "data" in secret:
            raise CredentialError("Only v1 Secret stringData is accepted")
        if not all(isinstance(v, str) and v for v in data.values()):
            raise CredentialError("Empty or invalid Secret material")
        if name in PUBLIC_CA_NAMES:
            if (namespace != "nightingale" or secret.get("type") != "Opaque" or
                    set(data) != {"ca.crt"} or labels.get("observability.raytrain.io/trust-domain") != PUBLIC_CA_NAMES[name]):
                raise CredentialError("Public CA projection violates its fixed contract")
        elif name == "obs-clickhouse-interserver-auth":
            if (namespace != "clickhouse" or secret.get("type") != "Opaque" or
                    set(data) != {"username", "password"} or data["username"] != "replica"):
                raise CredentialError("Interserver Secret violates its fixed contract")
        else:
            match = re.fullmatch(r"obs-mtls-([a-z][a-z0-9-]{0,48})", name)
            expected_uri = f"{URI_ROOT}/{namespace}/{match[1]}" if match else None
            if (not match or secret.get("type") != "kubernetes.io/tls" or
                    set(data) != {"tls.crt", "tls.key", "ca.crt"} or
                    labels.get("observability.raytrain.io/trust-domain") not in TRUST_DOMAINS or
                    meta.get("annotations", {}).get("observability.raytrain.io/identity") != expected_uri):
                raise CredentialError("Leaf Secret violates its fixed identity/material contract")
    try:
        result = subprocess.run(["kubectl", "--context", KUBE_CONTEXT, "--request-timeout=20s", "create", "-f", "-", "-o", "name"],
            input=json.dumps({"apiVersion": "v1", "kind": "List", "items": bundle}),
            capture_output=True, text=True, timeout=60, check=False)
    except (OSError, subprocess.TimeoutExpired):
        raise CredentialError("Creation may be partial; inspect names only before retrying") from None
    if result.returncode:
        raise CredentialError("Creation may be partial; diagnostics withheld; do not overwrite or delete")


def public_ca_secret(name, domain, certificate):
    if PUBLIC_CA_NAMES.get(name) != domain or not isinstance(certificate, str) or not certificate:
        raise CredentialError("Unknown or invalid public CA projection")
    return {"apiVersion": "v1", "kind": "Secret", "type": "Opaque", "metadata": {
        "namespace": "nightingale", "name": name, "labels": {
            LABEL: "mtls", "app.kubernetes.io/part-of": "raytrain-observability-additive",
            "observability.raytrain.io/trust-domain": domain}}, "stringData": {"ca.crt": certificate}}


def interserver_secret():
    return {"apiVersion": "v1", "kind": "Secret", "type": "Opaque", "metadata": {
        "namespace": "clickhouse", "name": "obs-clickhouse-interserver-auth",
        "labels": {LABEL: "mtls", "app.kubernetes.io/part-of": "raytrain-observability-additive"}},
        "stringData": {"username": "replica", "password": "".join(secrets.choice(string.ascii_letters + string.digits) for _ in range(48))}}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifests", nargs="+")
    parser.add_argument("--create", action="store_true")
    parser.add_argument("--pki-dir", type=Path)
    parser.add_argument("--include-clickhouse-interserver", action="store_true")
    parser.add_argument("--include-n9e-ca-projections", action="store_true")
    args = parser.parse_args(argv)
    try:
        plan = validate_plan([entry for path in args.manifests for entry in json.loads(Path(path).read_text())])
        if not args.create:
            for item in plan:
                print(f"{item['namespace']}/obs-mtls-{item['role']} ca={item['trust_domain']} identity={identity(item)}")
            if args.include_clickhouse_interserver:
                print("clickhouse/obs-clickhouse-interserver-auth keys=username,password")
            if args.include_n9e_ca_projections:
                for name in PUBLIC_CA_NAMES:
                    print(f"nightingale/{name} keys=ca.crt (public CA only)")
            return 0
        if args.pki_dir is None:
            raise CredentialError("--create requires the approved private --pki-dir")
        preflight_absent(plan)
        if args.include_clickhouse_interserver:
            check_absent("clickhouse", "obs-clickhouse-interserver-auth")
        if args.include_n9e_ca_projections:
            if not {"mesh", "postgres-client"} <= {item["trust_domain"] for item in plan}:
                raise CredentialError("N9E CA projections require both domains in the same reviewed plan")
            for name in PUBLIC_CA_NAMES:
                check_absent("nightingale", name)
        bundle = [make_secret(item, issue_material(item, args.pki_dir)) for item in plan]
        if args.include_clickhouse_interserver:
            bundle.append(interserver_secret())
        if args.include_n9e_ca_projections:
            for name, domain in PUBLIC_CA_NAMES.items():
                certificate = next(secret["stringData"]["ca.crt"] for secret in bundle
                    if secret["metadata"]["labels"].get("observability.raytrain.io/trust-domain") == domain)
                bundle.append(public_ca_secret(name, domain, certificate))
        create_secrets(bundle)
        print(f"Created {len(bundle)} new Secrets; values withheld; CA keys retained only in private builder PKI")
        return 0
    except CredentialError as error:
        print(f"ERROR: {error}", file=sys.stderr)
    except Exception:
        print("ERROR: identity setup stopped; diagnostics withheld; preserve PKI and inspect target names", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
