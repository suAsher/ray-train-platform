#!/usr/bin/env python3
"""One-time credentials for the additive observability stack.

Default/--plan is offline and prints names and keys only. --create uses the
builder's already-verified kubectl context, checks ALL target names are absent,
then creates new Secrets through stdin. It never updates, rotates, reads Secret
payloads, or writes a Secret manifest artifact. Creation is not transactional:
on a partial failure stop and review names; do not delete or rerun blindly.

Two temporary CA keys are destroyed with their restricted temporary directory.
Certificates expire after 90 days; later rotation is a separate reviewed task.
CNPG owns nightingale-postgres-ca, which this tool must never create.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import string
import subprocess
import sys
import tempfile


NAMESPACES = frozenset({"victoriametrics", "clickhouse",
                        "opentelemetry", "nightingale"})
ALPHABET = string.ascii_letters + string.digits
CERTIFICATE_DAYS = 90
CLICKHOUSE_SANS = tuple("clickhouse-clickhouse" + suffix for suffix in (
    "", ".clickhouse", ".clickhouse.svc",
    ".clickhouse.svc.cluster.local"))
OTEL_SERVER_SANS = tuple(name + suffix for name in (
    "otel-gateway", "otel-gateway-headless", "otel-gateway-0.otel-gateway-headless",
    "otel-gateway-1.otel-gateway-headless") for suffix in (
        "", ".opentelemetry", ".opentelemetry.svc",
        ".opentelemetry.svc.cluster.local"))


class BootstrapError(Exception):
    """Safe, deliberately redacted error suitable for operator output."""


def secret_plan():
    rows = (
        ("victoriametrics", "vm-self-write", ("token",)),
        ("victoriametrics", "vm-self-read", ("token",)),
        ("clickhouse", "clickhouse-operator-auth", ("username", "password")),
        ("clickhouse", "clickhouse-auth", ("default-password-sha256", "admin-password-sha256",
                                           "ingest-password-sha256", "query-password-sha256")),
        ("clickhouse", "clickhouse-bootstrap-auth", ("password",)),
        ("clickhouse", "clickhouse-query-auth", ("password",)),
        ("clickhouse", "clickhouse-server-tls", ("tls.crt", "tls.key", "ca.crt")),
        ("opentelemetry", "otel-clickhouse-auth", ("password",)),
        ("opentelemetry", "clickhouse-server-ca", ("ca.crt",)),
        ("opentelemetry", "otel-canary-server-tls", ("tls.crt", "tls.key", "ca.crt")),
        ("opentelemetry", "otel-canary-client-tls", ("tls.crt", "tls.key", "ca.crt")),
        ("nightingale", "n9e-db-app", ("username", "password")),
        ("nightingale", "nightingale-redis-auth", ("auth", "sentinel-password")),
        ("nightingale", "n9e-bootstrap-admin", ("password",)),
        ("nightingale", "n9e-vm-read", ("token",)),
    )
    return [{"namespace": ns, "name": name, "keys": keys}
            for ns, name, keys in rows]


def random_value(length=48):
    return "".join(secrets.choice(ALPHABET) for _ in range(length))


def run_openssl(arguments):
    try:
        result = subprocess.run(["openssl", *map(str, arguments)], capture_output=True,
                                text=True, check=False, timeout=60)
    except (OSError, subprocess.TimeoutExpired):
        raise BootstrapError("OpenSSL execution failed; diagnostic content withheld") from None
    if result.returncode:
        raise BootstrapError("OpenSSL execution failed; diagnostic content withheld")
    return result.stdout


def private_write(path, content):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as stream:
        stream.write(content)


def generate_key(path):
    run_openssl(["genpkey", "-algorithm", "RSA", "-pkeyopt", "rsa_keygen_bits:2048", "-out", path])
    path.chmod(0o600)


def generate_ca(directory, name):
    key, certificate = directory / f"{name}.key", directory / f"{name}.crt"
    generate_key(key)
    run_openssl(["req", "-new", "-x509", "-sha256", "-key", key, "-out", certificate,
                 "-days", str(CERTIFICATE_DAYS), "-subj", f"/CN=raytrain-additive-{name}",
                 "-addext", "basicConstraints=critical,CA:TRUE,pathlen:0",
                 "-addext", "keyUsage=critical,keyCertSign,cRLSign",
                 "-addext", "subjectKeyIdentifier=hash"])
    certificate.chmod(0o600)
    return key, certificate


def issue_certificate(directory, name, ca, purpose, dns_names=()):
    key, certificate = directory / f"{name}.key", directory / f"{name}.crt"
    csr, config = directory / f"{name}.csr", directory / f"{name}.cnf"
    generate_key(key)
    run_openssl(["req", "-new", "-sha256", "-key", key, "-out", csr,
                 "-subj", f"/CN={dns_names[0] if dns_names else name}"])
    extensions = ["basicConstraints=critical,CA:FALSE",
                  "keyUsage=critical,digitalSignature,keyEncipherment",
                  f"extendedKeyUsage={purpose}", "subjectKeyIdentifier=hash",
                  "authorityKeyIdentifier=keyid,issuer"]
    if dns_names:
        extensions.append("subjectAltName=" + ",".join("DNS:" + name for name in dns_names))
    private_write(config, "\n".join(extensions) + "\n")
    serial = "0x" + secrets.token_hex(16)
    run_openssl(["x509", "-req", "-sha256", "-in", csr, "-CA", ca[1], "-CAkey", ca[0],
                 "-set_serial", serial, "-days", str(CERTIFICATE_DAYS),
                 "-extfile", config, "-out", certificate])
    certificate.chmod(0o600)
    run_openssl(["verify", "-CAfile", ca[1], "-purpose",
                 "sslserver" if purpose == "serverAuth" else "sslclient", certificate])
    return {"tls.crt": certificate.read_text(encoding="utf-8"),
            "tls.key": key.read_text(encoding="utf-8"),
            "ca.crt": ca[1].read_text(encoding="utf-8")}


def generate_tls():
    previous_umask = os.umask(0o077)
    try:
        with tempfile.TemporaryDirectory(prefix="raytrain-additive-tls-") as temporary:
            directory = Path(temporary)
            directory.chmod(0o700)
            clickhouse_ca = generate_ca(directory, "clickhouse-ca")
            canary_ca = generate_ca(directory, "otel-canary-ca")
            return {
                "clickhouse": issue_certificate(directory, "clickhouse-server", clickhouse_ca,
                                                  "serverAuth", CLICKHOUSE_SANS),
                "otel_server": issue_certificate(directory, "otel-server", canary_ca,
                                                  "serverAuth", OTEL_SERVER_SANS),
                "otel_client": issue_certificate(directory, "raytrain-observability-canary",
                                                  canary_ca, "clientAuth"),
            }
    finally:
        os.umask(previous_umask)


def make_secret(namespace, name, data, secret_type="Opaque"):
    if namespace not in NAMESPACES or name == "nightingale-postgres-ca":
        raise BootstrapError("Secret target is outside the fixed bootstrap scope")
    return {"apiVersion": "v1", "kind": "Secret", "metadata": {
        "namespace": namespace, "name": name, "labels": {
            "app.kubernetes.io/part-of": "raytrain-observability-additive",
            "observability.wellspiking.ai/phase": "additive"}},
        "type": secret_type, "stringData": dict(data)}


def build_secrets():
    read_token, write_token = random_value(), random_value()
    passwords = {role: random_value() for role in ("default", "admin", "ingest", "query")}
    tls = generate_tls()
    data = {
        "vm-self-read": {"token": read_token}, "vm-self-write": {"token": write_token},
        "clickhouse-operator-auth": {"username": "clickhouse_operator", "password": random_value()},
        "clickhouse-auth": {role + "-password-sha256": hashlib.sha256(value.encode()).hexdigest()
                            for role, value in passwords.items()},
        "clickhouse-bootstrap-auth": {"password": passwords["admin"]},
        "clickhouse-query-auth": {"password": passwords["query"]},
        "clickhouse-server-tls": tls["clickhouse"],
        "otel-clickhouse-auth": {"password": passwords["ingest"]},
        "clickhouse-server-ca": {"ca.crt": tls["clickhouse"]["ca.crt"]},
        "otel-canary-server-tls": tls["otel_server"],
        "otel-canary-client-tls": tls["otel_client"],
        "n9e-db-app": {"username": "n9e", "password": random_value()},
        "nightingale-redis-auth": {"auth": random_value(), "sentinel-password": random_value()},
        "n9e-bootstrap-admin": {"password": random_value(24)},
        "n9e-vm-read": {"token": read_token},
    }
    result = []
    for entry in secret_plan():
        name = entry["name"]
        secret_type = "kubernetes.io/tls" if name.endswith("-tls") else "Opaque"
        if name == "n9e-db-app":
            secret_type = "kubernetes.io/basic-auth"
        result.append(make_secret(entry["namespace"], name, data[name], secret_type))
    validate_bundle(result)
    return result


def validate_bundle(bundle):
    expected = {(entry["namespace"], entry["name"]): set(entry["keys"]) for entry in secret_plan()}
    if len(bundle) != len(expected):
        raise BootstrapError("Generated bundle does not match the fixed Secret plan")
    seen = set()
    for secret in bundle:
        metadata = secret.get("metadata", {})
        identity = (metadata.get("namespace"), metadata.get("name"))
        if identity in seen or identity not in expected or set(secret.get("stringData", {})) != expected[identity]:
            raise BootstrapError("Generated bundle does not match the fixed Secret plan")
        if secret.get("kind") != "Secret" or secret.get("apiVersion") != "v1":
            raise BootstrapError("Only v1 Secrets may be created")
        if any(not isinstance(value, str) or not value for value in secret["stringData"].values()):
            raise BootstrapError("Generated Secret contains an empty or invalid value")
        seen.add(identity)


def preflight_absent():
    for entry in secret_plan():
        namespace, name = entry["namespace"], entry["name"]
        arguments = ["kubectl", "--request-timeout=20s", "-n", namespace,
                     "get", "secret", name, "--ignore-not-found", "-o", "name"]
        try:
            result = subprocess.run(arguments, capture_output=True, text=True, check=False, timeout=30)
        except (OSError, subprocess.TimeoutExpired):
            raise BootstrapError(f"Cannot check {namespace}/{name}; no credentials generated") from None
        if result.returncode:
            raise BootstrapError(f"Cannot confirm absence of {namespace}/{name}; no credentials generated")
        if result.stdout.strip():
            raise BootstrapError(f"Secret already exists: {namespace}/{name}; refusing rotation or overwrite")


def create_bundle(bundle):
    validate_bundle(bundle)
    payload = json.dumps({"apiVersion": "v1", "kind": "List", "items": bundle})
    try:
        result = subprocess.run(["kubectl", "--request-timeout=20s", "create", "-f", "-", "-o", "name"],
                                input=payload, capture_output=True, text=True, check=False, timeout=60)
    except (OSError, subprocess.TimeoutExpired):
        raise BootstrapError("Secret creation may have partially completed; inspect names only and stop before any retry") from None
    if result.returncode:
        raise BootstrapError("Secret creation may have partially completed; inspect names only and stop before any retry")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--plan", action="store_true", help="offline name/key inventory (default)")
    mode.add_argument("--create", action="store_true", help="create only if every fixed target is absent")
    args = parser.parse_args(argv)
    if not args.create:
        for entry in secret_plan():
            print(f"{entry['namespace']}/{entry['name']}: {', '.join(entry['keys'])}")
        return 0
    try:
        preflight_absent()
        bundle = build_secrets()
        create_bundle(bundle)
    except BootstrapError as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 1
    except Exception:
        print("ERROR: credential bootstrap stopped; diagnostics withheld; inspect target names before retrying", file=sys.stderr)
        return 1
    print(f"Created {len(bundle)} new Secrets; values withheld; CA private keys were not published.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
