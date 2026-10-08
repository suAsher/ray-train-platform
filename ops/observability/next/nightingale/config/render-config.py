"""Render the fixed Nightingale config inside the Pod, never on stdout."""

import json
import os
import pathlib
import re
import shutil


def secret_value(directory, name):
    value = (pathlib.Path(directory) / name).read_text().strip()
    if not value or "\n" in value or "\r" in value:
        raise SystemExit("invalid required Secret value")
    return value


target = pathlib.Path("/run/n9e/etc")
target.mkdir(mode=0o750, parents=True, exist_ok=True)
shutil.copyfile("/app/etc/metrics.yaml", target / "metrics.yaml")
username = secret_value("/secrets/db", "username")
password = secret_value("/secrets/db", "password")
# v9.1.1 inspects dbname with a space split before opening PostgreSQL.
# A URI is misparsed and can expose its password in a connection error.
if username != "n9e" or not re.fullmatch(r"[A-Za-z0-9_-]{32,128}", password):
    raise SystemExit("database Secret does not match the fixed n9e credential contract")
dsn = (
    "host=n9e-postgres-rw.observability-alerting.svc port=5432 "
    "user=n9e dbname=n9e password="
    + password
    + " sslmode=verify-full sslrootcert=/etc/n9e-ca/ca.crt connect_timeout=5"
)
values = {
    "DB_DSN": dsn,
    "REDIS_PASSWORD": secret_value("/secrets/redis", "auth"),
    "SENTINEL_PASSWORD": secret_value("/secrets/redis", "sentinel-password"),
    "POD_IP": os.environ["POD_IP"],
}
config = pathlib.Path("/config/config.toml.template").read_text()
for key, value in values.items():
    config = config.replace("@@" + key + "@@", json.dumps(value))
if "@@" in config:
    raise SystemExit("unresolved configuration marker")
output = target / "config.toml"
output.write_text(config)
output.chmod(0o600)
