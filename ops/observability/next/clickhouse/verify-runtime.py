#!/usr/bin/env python3
"""Builder-only synthetic CH/Keeper/OTel acceptance; no host ports or Kubernetes.

Run as root on the existing Linux Docker builder with --output /private/path.
This exercises candidate native/proxy configs; it is not evidence of the
operator's generated Kubernetes resources, which require a separate gate.
"""

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import traceback
import xml.etree.ElementTree as ET

import yaml

from runtime_support import (RuntimeCheckError, Sandbox, data_dir, load_module,
                             port_closed, run, wait, write)


HERE = Path(__file__).resolve().parent
ROOT = HERE.parent
NS = "observability-analytics.svc.cluster.local"
UID = 10001


def load_cr(name):
    return next(yaml.safe_load_all((HERE / f"{name}.yaml").read_text()))


def config_value(root, path, value):
    parts = path.split("/")
    parent = root
    for part in parts[:-1]:
        found = parent.find(part)
        parent = found if found is not None else ET.SubElement(parent, part)
    leaf = ET.SubElement(parent, parts[-1])
    leaf.text = str(value).lower() if isinstance(value, bool) else str(value)
    return leaf


def xml_text(root):
    return ET.tostring(root, encoding="unicode")


def image(cr, name):
    pod = cr["spec"]["templates"]["podTemplates"][0]["spec"]
    return next(c["image"] for c in pod["containers"] if c["name"] == name)


def keeper_config(index, candidate):
    root = ET.fromstring(candidate["spec"]["configuration"]["files"]["zz-mtls.xml"])
    for key, value in candidate["spec"]["configuration"]["settings"].items():
        if key != "listen_host":
            config_value(root, key, value)
    values = {"keeper_server/server_id": index + 1, "keeper_server/tcp_port_secure": 2281,
              "keeper_server/log_storage_path": "/var/lib/clickhouse-keeper/log",
              "keeper_server/snapshot_storage_path": "/var/lib/clickhouse-keeper/snapshots",
              "keeper_server/http_control/port": 9182,
              "keeper_server/coordination_settings/operation_timeout_ms": 10000,
              "keeper_server/coordination_settings/session_timeout_ms": 30000,
              "keeper_server/coordination_settings/heart_beat_interval_ms": 500}
    for path, value in values.items():
        config_value(root, path, value)
    raft = root.find("keeper_server/raft_configuration")
    if raft is None or raft.findtext("secure") != "true":
        raise RuntimeCheckError("Keeper global Raft TLS flag missing from candidate")
    for peer in range(3):
        server = ET.SubElement(raft, "server")
        for key, value in {"id": peer + 1, "hostname": f"chk-observability-keeper-0-{peer}.{NS}",
                           "port": 9234, "secure": 1}.items():
            config_value(server, key, value)
    return xml_text(root)


def ch_configs(index, candidate, passwords):
    config = candidate["spec"]["configuration"]
    root = ET.fromstring(config["files"]["config.d/zz-mtls.xml"])
    values = dict(config["settings"])
    values.update({"max_server_memory_usage": 1610612736, "logger/level": "warning",
                   "logger/console": True, "interserver_http_host": f"chi-observability-telemetry-0-{index}.{NS}"})
    for key, value in values.items():
        config_value(root, key, value)
    logger = root.find("logger")
    ET.SubElement(logger, "log", {"remove": "1"})
    ET.SubElement(logger, "errorlog", {"remove": "1"})
    for key, value in {"installation": "observability", "cluster": "telemetry",
                       "shard": "0", "replica": str(index)}.items():
        config_value(root, "macros/" + key, value)
    zoo = ET.SubElement(root, "zookeeper")
    for member in config["zookeeper"]["nodes"]:
        node = ET.SubElement(zoo, "node")
        config_value(node, "host", member["host"])
        config_value(node, "port", member["port"])
    config_value(root, "distributed_ddl/path", "/clickhouse/observability/task_queue/ddl")
    remote = ET.SubElement(ET.SubElement(root, "remote_servers"), "telemetry")
    ET.SubElement(remote, "secret", {"from_env": "CLICKHOUSE_INTERNODE_CLUSTER_SECRET"})
    shard = ET.SubElement(remote, "shard")
    config_value(shard, "internal_replication", True)
    for peer in range(2):
        replica = ET.SubElement(shard, "replica")
        config_value(replica, "host", f"chi-observability-telemetry-0-{peer}.{NS}")
        config_value(replica, "port", 9440)
        config_value(replica, "secure", 1)
    users_root = ET.Element("clickhouse")
    users = ET.SubElement(users_root, "users")
    for user, password in passwords.items():
        record = ET.SubElement(users, user, {"replace": "replace"})
        config_value(record, "password_sha256_hex", hashlib.sha256(password.encode()).hexdigest())
        config_value(record, "networks/ip", "127.0.0.1")
        config_value(record, "profile", config["users"].get(user + "/profile", "default"))
        grants = config["users"].get(user + "/grants/query", ["GRANT ALL ON *.*"])
        for grant in grants:
            config_value(record, "grants/query", grant)
    for key, value in config["profiles"].items():
        config_value(users_root, "profiles/" + key, value)
    return xml_text(root), xml_text(users_root)


def proxy(sandbox, role, native, name):
    text = (HERE / f"proxy-{role}.yaml").read_text().replace("__POD_IP__", sandbox.ip(native))
    config = write(sandbox.work / "configs" / f"{name}-envoy.yaml", text)
    sandbox.start(name + "-envoy", sandbox.images["envoy"],
                  ["-c", "/runtime/envoy.yaml", "--concurrency", "1", "--disable-hot-restart"],
                  [(config, "/runtime/envoy.yaml", False),
                   (sandbox.leaves[role], "/etc/obs-mtls", False)],
                  network_mode="container:" + native, memory="256m", cpus="0.25")


def sql_client(sandbox, query, replica=0, role="query-admin", check=True):
    users = {"schema-admin": "schema_admin", "query-admin": "observability_query", "otel-gateway": "otel_ingest"}
    username = users[role]
    root = ET.fromstring((HERE / "schema-client.xml").read_text())
    root.find("host").text = f"chi-observability-telemetry-0-{replica}.{NS}"
    root.find("user").text = username
    path = write(sandbox.work / "client-configs" / f"{role}-{replica}.xml", xml_text(root))
    client_name = sandbox.prefix + "-sql-" + secrets.token_hex(4)
    sandbox.containers.append(client_name)
    command = ["docker", "run", "--rm", "--name", client_name, "--label",
               "observability-test=" + sandbox.prefix, "--network", sandbox.network, "--user", "10001:10001",
               "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
               "--cpus", "0.25", "--memory", "256m", "--pids-limit", "64",
               "--tmpfs", "/tmp:rw,size=16m,uid=10001,gid=10001", "--env", "CLICKHOUSE_PASSWORD",
               "--mount", f"type=bind,src={path},dst=/client.xml,readonly",
               "--mount", f"type=bind,src={sandbox.leaves[role]},dst=/etc/obs-mtls,readonly",
               "--entrypoint", "clickhouse-client", sandbox.images["clickhouse"],
               "--config-file", "/client.xml", "--multiquery", "--query", query]
    environment = {**os.environ, "CLICKHOUSE_PASSWORD": sandbox.passwords[username]}
    result = run(command, check=check, env=environment)
    return result.stdout.strip() if check else result


def create_cluster(sandbox):
    sandbox.stage = "create_keeper_topology"
    ch, keeper = load_cr("clickhouse"), load_cr("keeper")
    sandbox.images = {"clickhouse": image(ch, "clickhouse"), "keeper": image(keeper, "clickhouse-keeper"),
                      "envoy": image(ch, "envoy")}
    sts = next(d for d in yaml.safe_load_all((ROOT / "otel" / "gateway.yaml").read_text())
               if d and d["kind"] == "StatefulSet")
    sandbox.images["otel"] = next(c["image"] for c in sts["spec"]["template"]["spec"]["containers"]
                                  if c["name"] == "collector")
    sandbox.passwords = {user: secrets.token_urlsafe(24) for user in
                         ("default", "schema_admin", "observability_query", "otel_ingest", "clickhouse_operator")}
    sandbox.native_keeper = []
    for index in range(3):
        config = write(sandbox.work / "configs" / f"keeper-{index}.xml", keeper_config(index, keeper))
        data = data_dir(sandbox.work / "data" / f"keeper-{index}")
        host = f"chk-observability-keeper-0-{index}"
        native = sandbox.start(f"keeper-{index}", sandbox.images["keeper"],
                               ["--config-file=/runtime/keeper.xml"],
                               [(config, "/runtime/keeper.xml", False), (data, "/var/lib/clickhouse-keeper", True),
                                (sandbox.leaves["keeper"], "/etc/obs-mtls", False)],
                               aliases=[f"{host}.{NS}", f"{host}-client.{NS}"],
                               entrypoint="/usr/bin/clickhouse-keeper", memory="512m")
        sandbox.native_keeper.append(native)
        proxy(sandbox, "keeper", native, f"keeper-{index}")
    for native in sandbox.native_keeper:
        sandbox.stage = "keeper_quorum_and_readiness"
        wait("Keeper quorum and local HTTP control", lambda n=native: sandbox.health(sandbox.ip(n))[0] == 200, 150)
    sandbox.stage = "create_clickhouse_topology"
    sandbox.native_ch = []
    cluster_secret = secrets.token_hex(24)
    replica_password = secrets.token_hex(24)
    for index in range(2):
        config, users = ch_configs(index, ch, sandbox.passwords)
        config_dir = data_dir(sandbox.work / "configs" / f"ch-{index}")
        users_dir = data_dir(sandbox.work / "configs" / f"users-{index}")
        write(config_dir / "zz-mtls.xml", config)
        write(users_dir / "zz-users.xml", users)
        data = data_dir(sandbox.work / "data" / f"ch-{index}")
        aliases = [f"chi-observability-telemetry-0-{index}.{NS}", f"obs-ch-r{index}-mtls.{NS}"]
        if index == 0:
            aliases.append(f"clickhouse-observability.{NS}")
        native = sandbox.start(f"ch-{index}", sandbox.images["clickhouse"],
                               ["--config-file=/etc/clickhouse-server/config.xml"],
                               [(config_dir, "/etc/clickhouse-server/config.d", False),
                                (users_dir, "/etc/clickhouse-server/users.d", False),
                                (data, "/var/lib/clickhouse", True), (sandbox.leaves["clickhouse"], "/etc/obs-mtls", False)],
                               aliases=aliases, entrypoint="/usr/bin/clickhouse-server", memory="2g", cpus="1",
                               env={"INTERSERVER_USER": "replica", "INTERSERVER_PASSWORD": replica_password,
                                    "CLICKHOUSE_INTERNODE_CLUSTER_SECRET": cluster_secret})
        sandbox.native_ch.append(native)
        proxy(sandbox, "clickhouse", native, f"ch-{index}")
    for native in sandbox.native_ch:
        sandbox.stage = "clickhouse_readiness"
        wait("ClickHouse local readiness", lambda n=native: sandbox.health(sandbox.ip(n))[0] == 200, 150)


def assert_boundaries(sandbox):
    evidence = {}
    for index, native in enumerate(sandbox.native_keeper):
        address = sandbox.ip(native)
        server = f"chk-observability-keeper-0-{index}.{NS}"
        assert all(port_closed(address, port) for port in (2181, 9182)), "Keeper native bypass"
        for port, role in ((2281, "clickhouse"), (9234, "keeper"), (7000, "vmagent")):
            assert sandbox.tls_accepts(address, server, port, role), "valid Keeper role rejected"
            assert not sandbox.tls_accepts(address, server, port, None), "Keeper accepted no certificate"
        assert not sandbox.tls_accepts(address, server, 9234, "clickhouse"), "CH may access Raft"
        assert not sandbox.tls_accepts(address, server, 2281, "canary"), "canary may access Keeper"
    for index, native in enumerate(sandbox.native_ch):
        address = sandbox.ip(native)
        server = f"chi-observability-telemetry-0-{index}.{NS}"
        assert all(port_closed(address, port) for port in (8123, 9000, 9009)), "CH native bypass"
        for port, role in ((8443, "ch-operator"), (9440, "query-admin"), (9010, "clickhouse"), (9363, "vmagent")):
            assert sandbox.tls_accepts(address, server, port, role), "valid CH role rejected"
            assert not sandbox.tls_accepts(address, server, port, None), "CH accepted no certificate"
        assert not sandbox.tls_accepts(address, server, 9010, "query-admin"), "query may replicate"
        assert not sandbox.tls_accepts(address, server, 8443, "otel-gateway"), "ingester may access admin HTTP"
        assert sandbox.health(address, "/config_dump")[0] == 404, "public health leaked admin"
        assert sandbox.health(address, "/metrics")[0] == 404, "public health leaked metrics"
    evidence["native_bypass_and_role_matrix"] = "passed"
    return evidence


def verify(sandbox):
    sandbox.stage = "issue_synthetic_certificates"
    plans = json.loads((HERE / "mtls-identities.json").read_text())
    plans += json.loads((ROOT / "otel" / "mtls-identities.json").read_text())
    plans.append({"namespace": "observability-metrics", "role": "vmagent", "trust_domain": "mesh",
                  "dns_names": [], "usages": ["clientAuth"]})
    sandbox.issue(ROOT, plans)
    create_cluster(sandbox)
    sandbox.stage = "tls_and_native_bypass_matrix"
    evidence = assert_boundaries(sandbox)
    sandbox.stage = "bootstrap_schema"
    sql_client(sandbox, (HERE / "schema.sql").read_text(), role="schema-admin")
    sandbox.stage = "schema_and_sql_grants"
    for replica in range(2):
        result = sql_client(sandbox, "SELECT count() FROM system.replicas WHERE database='observability_canary' AND is_readonly=0 AND is_session_expired=0", replica, "schema-admin")
        if result != "3":
            raise RuntimeCheckError("Three replicated local tables not healthy")
        sql_client(sandbox, "SELECT count() FROM observability_canary.otel_logs", replica)
    denied = sql_client(sandbox, "CREATE TABLE observability_canary.forbidden (id UInt64) ENGINE=Memory", role="otel-gateway", check=False)
    if denied.returncode == 0:
        raise RuntimeCheckError("Ingest identity unexpectedly obtained DDL")
    denied = sql_client(sandbox, "SELECT * FROM observability_canary.otel_logs LIMIT 1", role="otel-gateway", check=False)
    if denied.returncode == 0:
        raise RuntimeCheckError("Ingest identity unexpectedly obtained SELECT")
    denied = sql_client(sandbox, "INSERT INTO observability_canary.otel_logs (Timestamp,Body) VALUES(now64(9),'forbidden')", check=False)
    if denied.returncode == 0:
        raise RuntimeCheckError("Query identity unexpectedly obtained INSERT")
    evidence["schema_two_replicas_and_sql_roles"] = "passed"
    otel = load_module("obs_runtime_otel", ROOT / "otel" / "verify_runtime.py")
    sandbox.stage = "otel_low_volume_acceptance"
    context = {"work": sandbox.work, "network": sandbox.network, "images": sandbox.images,
               "leaf_dirs": sandbox.leaves, "run": run, "start": sandbox.start,
               "wait": wait, "ip": sandbox.ip, "request": sandbox.request,
               "port_closed": port_closed, "ingest_password": sandbox.passwords["otel_ingest"],
               "sql": lambda query, replica=0, role="query-admin": sql_client(sandbox, query, replica, role)}
    evidence["otel"] = otel.verify_otel(context)
    return evidence


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if os.geteuid() != 0 or sys.platform != "linux":
        raise RuntimeCheckError("Run only on the approved Linux Docker builder as root")
    if not args.output.is_absolute() or args.output.exists() or args.output.is_symlink():
        raise RuntimeCheckError("Output must be a new absolute directory")
    os.umask(0o077)
    args.output.mkdir(mode=0o700, parents=True)
    sandbox = Sandbox(args.output)
    evidence = {"status": "failed", "scope": "disposable Docker network; not Kubernetes generated-resource acceptance"}
    try:
        sandbox.stage = "create_isolated_network"
        sandbox.setup()
        evidence.update(verify(sandbox))
        evidence["status"] = "passed"
    except BaseException as exc:
        # Stack locations only: no locals, source lines, argv, TLS or SQL data.
        evidence["failure"] = {
            "stage": sandbox.stage, "type": type(exc).__name__,
            "reason": str(exc) if isinstance(exc, RuntimeCheckError) else "see private diagnostics",
            "frames": [{"file": Path(frame.filename).name, "line": frame.lineno, "function": frame.name}
                       for frame in traceback.extract_tb(exc.__traceback__)]}
        raise
    finally:
        sandbox.cleanup()
        (args.output / "result.json").write_text(json.dumps(evidence, indent=2) + "\n")
    print(json.dumps(evidence))


if __name__ == "__main__":
    try:
        main()
    except (RuntimeCheckError, AssertionError, OSError, subprocess.TimeoutExpired) as exc:
        sys.exit(f"Isolated runtime verification failed: {type(exc).__name__}; inspect private evidence directory")
