#!/usr/bin/env python3
"""Plan or execute safe CA import into cert-manager.

The actual private key material is only read by the operator running the import.
Normal summaries and command previews must not contain certificate or key bytes.
"""

import argparse
import os
from pathlib import Path
import json
import subprocess
import sys

import yaml


TRUST_DOMAINS = ("mesh", "am-gossip", "redis", "postgres-client", "postgres-server")
TARGET_NAMESPACE = "cert-manager"
KUBE_CONTEXT = "cd9q3b7rf9aidnnbd0je0@81533555-kd9q3q0frjmp21sk5lklg"
CA_DAYS = "1095"
MIN_VALID_SECONDS = str(91 * 24 * 60 * 60)


class ImportErrorSafe(RuntimeError):
    """Redacted import failure safe to show operators."""


def secret_name(domain):
    if domain not in TRUST_DOMAINS:
        raise ValueError("unknown trust domain")
    return f"obs-mtls-ca-{domain}"


def plan_imports(pki_dir):
    base = Path(pki_dir)
    return [{
        "trust_domain": domain,
        "namespace": TARGET_NAMESPACE,
        "secretName": secret_name(domain),
        "certPath": base / f"{domain}.crt",
        "keyPath": base / f"{domain}.key",
    } for domain in TRUST_DOMAINS]


def openssl(args):
    result = subprocess.run(["openssl", *map(str, args)], capture_output=True, text=True, check=False, timeout=60)
    if result.returncode:
        raise ImportErrorSafe("OpenSSL failed; diagnostics withheld")
    return result.stdout


def private_directory(path):
    path = Path(path)
    if not path.is_absolute() or any(parent.is_symlink() for parent in (path, *path.parents)):
        raise ImportErrorSafe("PKI directory must be absolute and without symlink ancestors")
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    stat = path.stat()
    if not path.is_dir() or stat.st_uid != os.geteuid() or stat.st_mode & 0o077:
        raise ImportErrorSafe("PKI directory must be owner-private")
    return path


def ensure_postgres_server_ca(pki_dir):
    base = private_directory(pki_dir)
    domain = "postgres-server"
    key = base / f"{domain}.key"
    cert = base / f"{domain}.crt"
    if key.exists() != cert.exists():
        raise ImportErrorSafe("postgres-server CA state is partial; inspect before retrying")
    if key.exists():
        if key.stat().st_mode & 0o077:
            raise ImportErrorSafe("postgres-server CA key permissions are too broad")
        return {"trust_domain": domain, "keyPath": key, "certPath": cert, "created": False}
    old_umask = os.umask(0o077)
    try:
        openssl(["genpkey", "-algorithm", "RSA", "-pkeyopt", "rsa_keygen_bits:2048", "-out", key])
        key.chmod(0o600)
        openssl([
            "req", "-new", "-x509", "-sha256", "-key", key, "-out", cert,
            "-days", CA_DAYS, "-subj", "/CN=raytrain-observability-postgres-server",
            "-addext", "basicConstraints=critical,CA:TRUE,pathlen:0",
            "-addext", "keyUsage=critical,keyCertSign,cRLSign",
            "-addext", "subjectKeyIdentifier=hash",
        ])
        cert.chmod(0o600)
    finally:
        os.umask(old_umask)
    return {"trust_domain": domain, "keyPath": key, "certPath": cert, "created": True}


def validate_ca_pair(cert, key):
    cert = Path(cert)
    key = Path(key)
    if cert.is_symlink() or key.is_symlink() or not cert.is_file() or not key.is_file():
        raise ImportErrorSafe("CA cert/key paths are invalid")
    if key.stat().st_mode & 0o077:
        raise ImportErrorSafe("CA private key permissions are too broad")
    key_pub = openssl(["pkey", "-in", key, "-pubout"])
    cert_pub = openssl(["x509", "-in", cert, "-pubkey", "-noout"])
    if key_pub != cert_pub:
        raise ImportErrorSafe("CA private key does not match certificate")
    text = openssl(["x509", "-in", cert, "-noout", "-text"])
    if "CA:TRUE" not in text:
        raise ImportErrorSafe("certificate is not a CA")
    openssl(["x509", "-in", cert, "-checkend", MIN_VALID_SECONDS, "-noout"])
    openssl(["verify", "-CAfile", cert, cert])


def secret_manifest(namespace, domain, certificate, key):
    if namespace != TARGET_NAMESPACE:
        raise ValueError("CA secrets may only be imported into cert-manager")
    return {
        "apiVersion": "v1",
        "kind": "Secret",
        "type": "kubernetes.io/tls",
        "metadata": {
            "namespace": TARGET_NAMESPACE,
            "name": secret_name(domain),
            "labels": {
                "app.kubernetes.io/part-of": "raytrain-observability-cert-manager",
                "observability.raytrain.io/trust-domain": domain,
            },
        },
        "stringData": {"tls.crt": certificate, "tls.key": key},
    }


def redacted_manifest_summary(manifest):
    meta = manifest.get("metadata", {})
    return json.dumps({
        "kind": manifest.get("kind"),
        "namespace": meta.get("namespace"),
        "name": meta.get("name"),
        "type": manifest.get("type"),
        "keys": sorted(manifest.get("stringData", {})),
    }, sort_keys=True)


def kubectl_create_command(namespace, name):
    if namespace != TARGET_NAMESPACE:
        raise ValueError("unexpected namespace")
    return {
        "preflight": [
            "kubectl", "--context", KUBE_CONTEXT, "--request-timeout=20s", "-n", namespace, "get", "secret", name,
            "--ignore-not-found", "-o", "name",
        ],
        "create": ["kubectl", "--context", KUBE_CONTEXT, "--request-timeout=20s", "create", "-f", "-"],
    }


def create_secret_from_files(item):
    cert = Path(item["certPath"])
    key = Path(item["keyPath"])
    validate_ca_pair(cert, key)
    manifest = secret_manifest(item["namespace"], item["trust_domain"], cert.read_text(), key.read_text())
    commands = kubectl_create_command(item["namespace"], item["secretName"])
    preflight = subprocess.run(commands["preflight"], capture_output=True, text=True, check=False, timeout=30)
    if preflight.returncode:
        raise ImportErrorSafe("cannot confirm target Secret absence")
    if preflight.stdout.strip():
        raise ImportErrorSafe("target Secret already exists; refusing overwrite")
    create = subprocess.run(commands["create"], input=yaml.safe_dump(manifest, sort_keys=False),
                            capture_output=True, text=True, check=False, timeout=60)
    if create.returncode:
        raise ImportErrorSafe("kubectl create failed; output withheld")
    return redacted_manifest_summary(manifest)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("plan", "ensure-postgres-server-ca", "create"))
    parser.add_argument("--pki-dir", required=True, type=Path)
    parser.add_argument("--domain", choices=TRUST_DOMAINS + ("all",), default="all")
    parser.add_argument("--context", default=KUBE_CONTEXT)
    args = parser.parse_args(argv)
    if args.context != KUBE_CONTEXT:
        raise SystemExit("refusing unexpected kube context")
    if args.action == "ensure-postgres-server-ca":
        print(json.dumps({**ensure_postgres_server_ca(args.pki_dir), "keyPath": "redacted"}, default=str, sort_keys=True))
        return 0
    if args.action == "create":
        private_directory(args.pki_dir)
    items = plan_imports(args.pki_dir)
    if args.domain != "all":
        items = [item for item in items if item["trust_domain"] == args.domain]
    if args.action == "plan":
        for item in items:
            print(redacted_manifest_summary({
                "kind": "Secret",
                "type": "kubernetes.io/tls",
                "metadata": {"namespace": item["namespace"], "name": item["secretName"]},
                "stringData": {"tls.crt": "", "tls.key": ""},
            }))
        return 0
    for item in items:
        print(create_secret_from_files(item))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main(sys.argv[1:]))
    except ImportErrorSafe as error:
        print(f"ERROR: {error}", file=sys.stderr)
        raise SystemExit(1)
