"""Render the fixed Nightingale config inside the Pod, never on stdout."""

import json
import os
import pathlib
import shutil
import urllib.parse


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
dsn = (
    "postgresql://"
    + urllib.parse.quote(username, safe="")
    + ":"
    + urllib.parse.quote(password, safe="")
    + "@n9e-postgres-rw.observability-alerting.svc:5432/n9e"
    + "?sslmode=verify-full&sslrootcert=/etc/n9e-ca/ca.crt"
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
