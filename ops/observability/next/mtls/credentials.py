#!/usr/bin/env python3
"""Create new observability mTLS identities; default is an offline plan.

CA keys stay in a root-owned directory on the approved builder, never in Pods.
Leaf Secrets travel to kubectl on stdin and are not printed or archived.
Existing Secret names are never overwritten. Partial creation requires review.
"""
import argparse
import copy
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import string
import subprocess
import sys
import tempfile

NAMESPACES = frozenset("observability-" + name for name in
                       ("metrics", "analytics", "otel", "alerting"))
TRUST_DOMAINS = frozenset(("mesh", "am-gossip", "redis", "postgres-client"))
URI_ROOT = "spiffe://observability.raytrain.local"
LEAF_DAYS = 90
CA_DAYS = 1095
LABEL = "observability.raytrain.io/credential-owner"
FIELDS = frozenset(("namespace", "role", "trust_domain", "dns_names", "usages", "common_name"))


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
        if ns not in NAMESPACES or not isinstance(role, str) or not re.fullmatch(r"[a-z][a-z0-9-]{0,48}", role):
            raise CredentialError("Identity is outside the allowed namespace/name scope")
        if (ns, role) in seen:
            raise CredentialError("Duplicate Secret identity")
        seen.add((ns, role))
        domain = item.get("trust_domain")
        if domain not in TRUST_DOMAINS:
            raise CredentialError("Unknown signing trust domain")
        if domain == "am-gossip" and (ns != "observability-metrics" or role != "alertmanager-gossip"):
            raise CredentialError("Alertmanager peer CA may only issue its dedicated peer identity")
        if domain == "redis" and (ns != "observability-alerting" or role not in ("redis-ha-peer", "nightingale-redis")):
            raise CredentialError("Redis CA may only issue Redis peers and its Nightingale client")
        if domain == "postgres-client" and (ns != "observability-alerting" or role not in
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
        result = subprocess.run(["kubectl", "--request-timeout=20s", "-n", namespace,
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
    for secret in bundle:
        meta = secret.get("metadata", {})
        name = meta.get("name", "")
        if meta.get("namespace") not in NAMESPACES or not (name.startswith("obs-mtls-") or name == "obs-clickhouse-interserver-auth"):
            raise CredentialError("Secret is outside the new mTLS scope")
    try:
        result = subprocess.run(["kubectl", "--request-timeout=20s", "create", "-f", "-", "-o", "name"],
            input=json.dumps({"apiVersion": "v1", "kind": "List", "items": bundle}),
            capture_output=True, text=True, timeout=60, check=False)
    except (OSError, subprocess.TimeoutExpired):
        raise CredentialError("Creation may be partial; inspect names only before retrying") from None
    if result.returncode:
        raise CredentialError("Creation may be partial; diagnostics withheld; do not overwrite or delete")


def interserver_secret():
    return {"apiVersion": "v1", "kind": "Secret", "type": "Opaque", "metadata": {
        "namespace": "observability-analytics", "name": "obs-clickhouse-interserver-auth",
        "labels": {LABEL: "mtls", "app.kubernetes.io/part-of": "raytrain-observability-additive"}},
        "stringData": {"username": "replica", "password": "".join(secrets.choice(string.ascii_letters + string.digits) for _ in range(48))}}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifests", nargs="+")
    parser.add_argument("--create", action="store_true")
    parser.add_argument("--pki-dir", type=Path)
    parser.add_argument("--include-clickhouse-interserver", action="store_true")
    args = parser.parse_args(argv)
    try:
        plan = validate_plan([entry for path in args.manifests for entry in json.loads(Path(path).read_text())])
        if not args.create:
            for item in plan:
                print(f"{item['namespace']}/obs-mtls-{item['role']} ca={item['trust_domain']} identity={identity(item)}")
            if args.include_clickhouse_interserver:
                print("observability-analytics/obs-clickhouse-interserver-auth keys=username,password")
            return 0
        if args.pki_dir is None:
            raise CredentialError("--create requires the approved private --pki-dir")
        preflight_absent(plan)
        if args.include_clickhouse_interserver:
            check_absent("observability-analytics", "obs-clickhouse-interserver-auth")
        bundle = [make_secret(item, issue_material(item, args.pki_dir)) for item in plan]
        if args.include_clickhouse_interserver:
            bundle.append(interserver_secret())
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
