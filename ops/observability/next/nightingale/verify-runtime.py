#!/usr/bin/env python3
"""Run only on the authorized builder after the candidate bundle is reviewed.

Creates a unique internal Docker network, no host ports, and bounded temporary
containers. Tests native Redis/Sentinel mTLS discovery/failover, real PostgreSQL
client certificates, N9E migrations/login, and the actual Envoy candidate. This
is not CNPG PostgreSQL HA or Kubernetes admission acceptance.
"""

import argparse
import json
from pathlib import Path
import shutil
import signal

from runtime_helpers import (
    ENVOY, N9E, N9EHOST, NS, PG, PGHOST, REDIS, REDIS_HOSTS, URI_ROOT, Runtime, command,
)

SOURCE = Path(__file__).resolve().parent


def certificates(rt):
    # Preserve the real separation: CNPG server CA is distinct from client CA.
    # Here both are temporary test CAs; CNPG's real server issuance is a live gate.
    mesh, redis_ca = rt.make_ca("mesh"), rt.make_ca("redis")
    pg_server_ca, pg_client_ca = rt.make_ca("cnpg-server"), rt.make_ca("postgres-client")
    ids = {
        "pg-server": rt.leaf("pg-server", pg_server_ca, dns=[PGHOST], usages="serverAuth", uid=26),
        "pg-app": rt.leaf("pg-app", pg_client_ca, cn="n9e"),
        "pg-bootstrap": rt.leaf("pg-bootstrap", pg_client_ca, cn="n9e", uid=26),
        "pg-wrong": rt.leaf("pg-wrong", pg_client_ca, cn="wrong-user", uid=26),
        "redis-peer": rt.leaf("redis-peer", redis_ca, dns=REDIS_HOSTS + [f"n9e-redis.{NS}.svc"], usages="serverAuth,clientAuth"),
        "redis-client": rt.leaf("redis-client", redis_ca),
        "n9e": rt.leaf("n9e", mesh, dns=[N9EHOST], usages="serverAuth,clientAuth", uri=URI_ROOT + NS + "/nightingale"),
        "ui": rt.leaf("ui", mesh, uri=URI_ROOT + NS + "/n9e-ui-gateway"),
        "monitor": rt.leaf("monitor", mesh, uri=URI_ROOT + "observability-metrics/vmagent"),
        "wrong": rt.leaf("wrong", mesh, uri=URI_ROOT + NS + "/unauthorized-test"),
        "cnpg": rt.leaf("cnpg", mesh, dns=[f"n9e-cnpg-metrics.{NS}.svc"], usages="serverAuth", uri=URI_ROOT + NS + "/cnpg-operator"),
    }
    # PostgreSQL clients verify its SERVER CA, not their own issuing client CA.
    for name in ("pg-app", "pg-bootstrap", "pg-wrong"):
        shutil.copyfile(pg_server_ca / "ca.crt", ids[name] / "ca.crt")
    client_ca = rt.directory("pg-client-ca", uid=26)
    shutil.copyfile(pg_client_ca / "ca.crt", client_ca / "ca.crt")
    (client_ca / "ca.crt").chmod(0o444)
    ids["pg-client-ca"] = client_ca
    return ids


def start_postgres(rt, ids, upstream, db_password, admin_password):
    rt.write("pgpass", rt.password(), uid=26)
    seed_path = rt.private / "seed.sql"
    command(["python3", SOURCE / "prepare-seed.py", upstream, seed_path])
    seed_path.chmod(0o444)
    hba = "local all all trust\nhostnossl all all all reject\n"
    hba += "hostssl n9e n9e all scram-sha-256 clientcert=verify-full\n"
    hba += "hostssl postgres n9e all scram-sha-256 clientcert=verify-full\nhostssl all all all reject\n"
    rt.write("pg_hba.conf", hba, uid=26)
    startup = "initdb -D /data --encoding=UTF8 --username=postgres --auth-local=trust --auth-host=scram-sha-256 --pwfile=/pgpass >/tmp/initdb.log; "
    startup += "cp /config/pg_hba.conf /data/pg_hba.conf; "
    startup += "exec postgres -D /data -c listen_addresses=* -c unix_socket_directories=/tmp "
    startup += "-c ssl=on -c ssl_cert_file=/server-tls/tls.crt -c ssl_key_file=/server-tls/tls.key -c ssl_ca_file=/client-ca/ca.crt"
    mounts = [(ids["pg-server"], "/server-tls"), (ids["pg-client-ca"], "/client-ca"),
              (ids["pg-bootstrap"], "/bootstrap-client"), (ids["pg-wrong"], "/wrong-client"),
              (rt.private / "pgpass", "/pgpass"), (seed_path, "/seed.sql"),
              (rt.private / "pg_hba.conf", "/config/pg_hba.conf")]
    extras = ["--network-alias", PGHOST, "--tmpfs", "/data:rw,size=512m,uid=26,gid=26,mode=0700",
              "--tmpfs", "/tmp:rw,size=64m,uid=26,gid=26"]
    for source, destination in mounts:
        extras += ["-v", f"{source}:{destination}:ro"]
    pg = rt.start("pg", PG, ["sh", "-ec", startup], uid=26, memory="768m", cpu="1", extras=extras)
    rt.wait("postgres_ready", lambda: rt.docker("exec", pg, "pg_isready", "-h", "/tmp", check=False).returncode == 0)
    rt.docker("exec", "-i", pg, "psql", "-h", "/tmp", "-U", "postgres", "-v", "ON_ERROR_STOP=1",
              data=f"CREATE ROLE n9e LOGIN PASSWORD '{db_password}';\nCREATE DATABASE n9e OWNER n9e;\n")
    base = f"PGHOST={PGHOST}\nPGUSER=n9e\nPGDATABASE=n9e\nPGPASSWORD={db_password}\nPGSSLMODE=verify-full\n"
    base += "PGSSLROOTCERT=/server-tls/ca.crt\n"
    rt.write("bootstrap.env", base + "PGSSLCERT=/bootstrap-client/tls.crt\nPGSSLKEY=/bootstrap-client/tls.key\nN9E_ADMIN_PASSWORD=" + admin_password + "\n", uid=0)
    rt.write("pg-nocert.env", base, uid=0)
    rt.write("pg-wrongcert.env", base + "PGSSLCERT=/wrong-client/tls.crt\nPGSSLKEY=/wrong-client/tls.key\n", uid=0)
    for name, reason in (("pg-nocert.env", "valid client certificate"),
                         ("pg-wrongcert.env", 'password authentication failed for user "n9e"')):
        denied = rt.docker("exec", "--env-file", rt.private / name, pg, "psql", "-X", "-tAc", "SELECT 1", check=False)
        rejected = denied.returncode != 0 and reason in denied.stderr
        if name == "pg-wrongcert.env":
            # PG17 deliberately reports the selected SCRAM method to clients.
            # Prove that the certificate CN guard caused this rejection using
            # this fresh test server's exact diagnostic, not any connection error.
            server_log = rt.docker("logs", pg)
            diagnostic = server_log.stdout + server_log.stderr
            rejected = rejected and (
                'certificate validation (clientcert=verify-full) failed for user "n9e": CN mismatch' in diagnostic
                and 'provided user name (n9e) and authenticated user name (wrong-user) do not match' in diagnostic
            )
        rt.check(name + "_rejected", rejected)
    seed_args = ["exec", "--env-file", rt.private / "bootstrap.env", pg, "psql", "-X", "--quiet", "--set", "ON_ERROR_STOP=1", "--single-transaction", "--file", "/seed.sql"]
    rt.docker(*seed_args)
    rt.check("fresh_seed_clientcert_passed", True)
    repeat = rt.docker(*seed_args, check=False)
    rt.check("reseed_rejected", repeat.returncode != 0 and "refusing to reseed" in repeat.stderr)
    preflight = rt.docker("exec", "--env-file", rt.private / "bootstrap.env", pg,
        "psql", "-X", "--dbname", "postgres", "-tAc", "SELECT count(*) FROM pg_database WHERE datname='n9e'")
    rt.check("maintenance_database_clientcert_preflight", preflight.stdout.strip() == "1")
    denied = rt.docker("exec", "--env-file", rt.private / "pg-nocert.env", pg,
        "psql", "-X", "--dbname", "postgres", "-tAc", "SELECT 1", check=False)
    rt.check("maintenance_database_no_certificate_denied", denied.returncode != 0 and "valid client certificate" in denied.stderr)
    unrelated = rt.docker("exec", "--env-file", rt.private / "bootstrap.env", pg,
        "psql", "-X", "--dbname", "template1", "-tAc", "SELECT 1", check=False)
    rt.check("unrelated_database_hba_rejected", unrelated.returncode != 0 and "pg_hba.conf rejects connection" in unrelated.stderr)
    return pg


def pg_query(rt, pg, sql):
    return rt.docker("exec", pg, "psql", "-h", "/tmp", "-U", "n9e", "-d", "n9e", "-tAc", sql).stdout.strip()


def start_redis(rt, ids, redis_password, sentinel_password):
    rt.write("redis.env", "REDISCLI_AUTH=" + redis_password + "\n", uid=0)
    rt.write("sentinel.env", "REDISCLI_AUTH=" + sentinel_password + "\n", uid=0)
    containers = []
    tls = "port 0\ntls-auth-clients yes\ntls-cert-file /tls/tls.crt\ntls-key-file /tls/tls.key\ntls-ca-cert-file /tls/ca.crt\ntls-replication yes\ntls-protocols \"TLSv1.3\"\n"
    startup = "cp /config/redis.conf /data/redis.conf; cp /config/sentinel.conf /data/sentinel.conf; chmod 600 /data/*.conf; "
    startup += "redis-server /data/redis.conf & redis_pid=$!; redis-server /data/sentinel.conf --sentinel & sentinel_pid=$!; "
    startup += "trap 'kill $redis_pid $sentinel_pid 2>/dev/null || true' TERM INT EXIT; wait $redis_pid"
    for index, host in enumerate(REDIS_HOSTS):
        directory = rt.directory("redis-" + str(index))
        conf = "bind 0.0.0.0\ndir /data\nprotected-mode yes\n" + tls + "tls-port 6379\n"
        conf += f"requirepass {redis_password}\nmasterauth {redis_password}\nreplica-announce-ip {host}\nreplica-announce-port 6379\n"
        conf += "min-replicas-to-write 1\nmin-replicas-max-lag 5\nappendonly yes\nappendfsync everysec\nsave \"\"\n"
        if index:
            conf += f"replicaof {REDIS_HOSTS[0]} 6379\n"
        rt.write(f"redis-{index}/redis.conf", conf)
        sentinel = "bind 0.0.0.0\ndir /data\nprotected-mode yes\n" + tls + "tls-port 26379\n"
        sentinel += f"requirepass {sentinel_password}\nsentinel resolve-hostnames yes\nsentinel announce-hostnames yes\nsentinel announce-ip {host}\nsentinel announce-port 26379\n"
        sentinel += f"sentinel monitor n9e {REDIS_HOSTS[0]} 6379 2\nsentinel auth-pass n9e {redis_password}\n"
        sentinel += "sentinel down-after-milliseconds n9e 10000\nsentinel failover-timeout n9e 180000\nsentinel parallel-syncs n9e 1\n"
        rt.write(f"redis-{index}/sentinel.conf", sentinel)
        containers.append(rt.start("redis-" + str(index), REDIS, ["sh", "-ec", startup],
            extras=["--network-alias", host, "-v", f"{directory}:/config:ro", "-v", f"{ids['redis-peer']}:/tls:ro",
                    "--tmpfs", "/data:rw,size=64m,uid=10001,gid=10001"], memory="256m"))
    for index, host in enumerate(REDIS_HOSTS):
        rt.wait(f"redis_{index}_mtls_auth_ping", lambda index=index, host=host: rt.redis(containers[index], host, "PING") == "PONG")
        rt.wait(f"sentinel_{index}_quorum", lambda index=index, host=host: rt.redis(containers[index], host, "SENTINEL", "CKQUORUM", "n9e", sentinel=True).startswith("OK"))
    rt.wait("redis_three_members_replicating", lambda: "connected_slaves:2" in rt.redis(containers[0], REDIS_HOSTS[0], "INFO", "replication"))
    # Redis replication and Sentinel voting do not imply that Sentinel has read
    # the complete replica list from its periodic master INFO yet. A failover
    # before discovery can leave an unknown healthy replica on the old master.
    for index, host in enumerate(REDIS_HOSTS):
        rt.wait(f"sentinel_{index}_complete_replica_topology", lambda index=index, host=host:
                rt.sentinel_topology_ready(containers[index], host, REDIS_HOSTS[0]))
    return containers


def start_n9e(rt, ids, db_password, redis_password, sentinel_password):
    for directory in ("db", "redis", "runtime"):
        rt.directory(directory)
    rt.write("db/username", "n9e")
    rt.write("db/password", db_password)
    rt.write("redis/auth", redis_password)
    rt.write("redis/sentinel-password", sentinel_password)
    render = rt.start("render", N9E, ["python3", "/config/render-config.py"], network="none", extras=[
        "-e", "POD_IP=192.0.2.20", "-v", f"{SOURCE / 'config'}:/config:ro",
        "-v", f"{rt.private / 'db'}:/secrets/db:ro", "-v", f"{rt.private / 'redis'}:/secrets/redis:ro",
        "-v", f"{rt.private / 'runtime'}:/run/n9e"], memory="128m")
    rt.check("real_config_renderer", rt.docker("wait", render).stdout.strip() == "0")
    n9e = rt.start("n9e", N9E, ["/app/n9e", "-configs", "/run/n9e/etc"], memory="1g", cpu="1", extras=[
        "--network-alias", N9EHOST, "-v", f"{rt.private / 'runtime'}:/run/n9e:ro",
        "-v", f"{ids['pg-app']}:/etc/n9e-pg-mtls:ro", "-v", f"{ids['redis-client']}:/etc/n9e-redis-mtls:ro",
        "--tmpfs", "/var/log/n9e:rw,size=64m,uid=10001,gid=10001", "--tmpfs", "/tmp:rw,size=64m,uid=10001,gid=10001"])
    rt.wait("n9e_native_ping", lambda: rt.probe(n9e, {"url": "http://127.0.0.1:17000/ping", "tls": False}).get("data") == "pong")
    rt.start("envoy", ENVOY, ["/usr/local/bin/envoy", "-c", "/etc/envoy/envoy.yaml", "--concurrency", "1", "--disable-hot-restart", "--log-level", "warning"], network="container:" + n9e, extras=[
        "-v", f"{SOURCE / 'config/envoy.yaml'}:/etc/envoy/envoy.yaml:ro", "-v", f"{ids['n9e']}:/etc/obs-mtls:ro"])
    validation = rt.start("cnpg-envoy-validate", ENVOY, ["/usr/local/bin/envoy", "--mode", "validate", "-c", "/etc/envoy/envoy.yaml", "--concurrency", "1"], network="none", extras=[
        "-v", f"{SOURCE / 'config/cnpg-envoy.yaml'}:/etc/envoy/envoy.yaml:ro", "-v", f"{ids['cnpg']}:/etc/obs-mtls:ro"])
    rt.check("cnpg_envoy_config_valid", rt.docker("wait", validation).stdout.strip() == "0")
    extras = []
    for name in ("ui", "monitor", "wrong", "redis-client"):
        extras += ["-v", f"{ids[name]}:/certs/{name}:ro"]
    client = rt.start("client", N9E, ["python3", "-c", "import time; time.sleep(600)"], extras=extras, memory="128m")
    return n9e, client


def http_payload(role="ui", path="/ping", **extra):
    payload = {"url": f"https://{N9EHOST}:17443{path}", "ca": "/certs/ui/ca.crt"}
    if role:
        payload.update(cert=f"/certs/{role}/tls.crt", key=f"/certs/{role}/tls.key")
    return {**payload, **extra}


def check_boundaries(rt, client):
    rt.wait("envoy_ui_ping", lambda: rt.probe(client, http_payload()).get("data") == "pong")
    rt.check("n9e_native_not_reachable_by_peer", rt.probe(client, {"url": f"http://{N9EHOST}:17000/ping", "tls": False})["status"] == 0)
    rt.check("envoy_no_client_certificate_rejected", rt.probe(client, http_payload(None))["status"] == 0)
    rt.check("envoy_wrong_role_rejected", rt.probe(client, http_payload("wrong"))["status"] == 403)
    rt.check("metrics_role_cannot_access_ui", rt.probe(client, http_payload("monitor"))["status"] == 403)
    rt.check("ui_role_cannot_read_metrics", rt.probe(client, http_payload("ui", "/metrics"))["status"] == 403)
    rt.check("monitor_can_read_metrics", rt.probe(client, http_payload("monitor", "/metrics"))["status"] == 200)
    rt.check("monitor_cannot_post_metrics", rt.probe(client, http_payload("monitor", "/metrics", body={}))["status"] == 403)
    for port in (6379, 26379):
        base = {"protocol": "redis", "host": REDIS_HOSTS[0], "port": port, "ca": "/certs/redis-client/ca.crt"}
        rt.check(f"redis_{port}_no_certificate_denied", rt.probe(client, base)["status"] == 0)
        rt.check(f"redis_{port}_plaintext_denied", rt.probe(client, {**base, "tls": False})["status"] == 0)
        # Same mesh CA used elsewhere is not the dedicated Redis CA.
        wrong = {**base, "cert": "/certs/ui/tls.crt", "key": "/certs/ui/tls.key"}
        rt.check(f"redis_{port}_foreign_ca_denied", rt.probe(client, wrong)["status"] == 0)
        valid = {**base, "cert": "/certs/redis-client/tls.crt", "key": "/certs/redis-client/tls.key"}
        reply = rt.probe(client, valid)
        rt.check(f"redis_{port}_certificate_still_needs_auth", reply["status"] == 200 and "NOAUTH" in reply["data"])


def login(rt, client, redis, redis_host, password, label):
    shown = rt.probe(client, http_payload(path="/api/n9e/auth/ifshowcaptcha"))
    rt.check(label + "_captcha_enabled", shown["status"] == 200 and shown["data"]["dat"]["show"] is True)
    for correct in (False, True):
        challenge = rt.probe(client, http_payload(path="/api/n9e/auth/captcha", body={}))["data"]["dat"]
        rt.check(label + "_real_captcha_image", challenge["imgdata"].startswith("data:image/png;base64,"))
        # White-box oracle in this fresh disposable Redis only. Never disables or
        # alters captcha auth: a wrong answer must fail, a new correct one passes.
        expected = rt.redis(redis, redis_host, "GET", challenge["captchaid"])
        rt.check(label + "_captcha_stored_in_test_redis", bool(expected))
        rt.secret_values.extend([challenge["captchaid"], expected])
        body = {"username": "root", "password": password, "captchaid": challenge["captchaid"],
                "verifyvalue": expected if correct else "not-the-generated-answer"}
        reply = rt.probe(client, http_payload(path="/api/n9e/auth/login", body=body))
        payload = reply.get("data") or {}
        token = (payload.get("dat") or {}).get("access_token", "") if isinstance(payload.get("dat"), dict) else ""
        if not correct:
            rt.check(label + "_wrong_captcha_rejected", reply["status"] == 200 and not token
                     and "incorrect verification code" in str(payload.get("err", "")))
            continue
        rt.check(label + "_login_token", reply["status"] == 200 and bool(token))
        rt.secret_values.append(token)
        refresh = payload.get("dat", {}).get("refresh_token")
        if refresh:
            rt.secret_values.append(refresh)
        profile = rt.probe(client, http_payload(path="/api/n9e/self/profile", headers={"Authorization": "Bearer " + token}))
        rt.check(label + "_profile_root", profile["status"] == 200 and profile["data"]["dat"]["username"] == "root")


def verify(rt, upstream):
    rt.initialize()
    ids = certificates(rt)
    db_password, admin_password = rt.password(), rt.password(24)
    redis_password, sentinel_password = rt.password(), rt.password()
    pg = start_postgres(rt, ids, upstream, db_password, admin_password)
    rt.check("n9e_has_no_superuser_or_createdb", pg_query(rt, pg,
        "SELECT rolsuper OR rolcreatedb FROM pg_roles WHERE rolname='n9e'") == "f")
    rt.check("root_seed_random24", pg_query(rt, pg, "SELECT password FROM users WHERE username='root'") == admin_password)
    count_before = int(pg_query(rt, pg, "SELECT count(*) FROM pg_tables WHERE schemaname='public'"))
    redis = start_redis(rt, ids, redis_password, sentinel_password)
    _, client = start_n9e(rt, ids, db_password, redis_password, sentinel_password)
    check_boundaries(rt, client)
    rt.check("real_migrations_created_tables", int(pg_query(rt, pg, "SELECT count(*) FROM pg_tables WHERE schemaname='public'")) > count_before)
    stored = pg_query(rt, pg, "SELECT password FROM users WHERE username='root'")
    rt.secret_values.append(stored)
    rt.check("random_root_password_hashed", len(stored) > 31 and stored != admin_password)
    rt.check("n9e_database_sessions_use_client_cert", int(pg_query(rt, pg,
        "SELECT count(*) FROM pg_stat_ssl s JOIN pg_stat_activity a USING(pid) WHERE a.usename='n9e' AND s.ssl AND s.client_dn LIKE '%CN=n9e%'") or "0") > 0)
    master = rt.master(redis[1], REDIS_HOSTS[1])
    login(rt, client, redis[1], master, admin_password, "before_failover")
    old_index = REDIS_HOSTS.index(master)
    addresses = rt.resolved_addresses(client, master)
    rt.check("master_service_dns_resolves_before_fault", bool(addresses))
    rt.pause_member(redis[old_index])
    rt.check("master_service_dns_survives_fault", rt.resolved_addresses(client, master) == addresses)
    survivors = [index for index in range(3) if index != old_index]
    survivor = survivors[0]
    rt.wait("sentinel_promoted_new_dns_master", lambda: rt.master(redis[survivor], REDIS_HOSTS[survivor]) != master, seconds=120)
    new_master = rt.master(redis[survivor], REDIS_HOSTS[survivor])
    rt.wait("surviving_sentinels_agree_on_new_master", lambda: all(
        rt.master(redis[index], REDIS_HOSTS[index]) == new_master for index in survivors))
    rt.wait("promoted_member_is_actually_master", lambda: "role:master" in rt.redis(redis[survivor], new_master, "INFO", "replication"))
    rt.wait("new_master_has_replica", lambda: "connected_slaves:1" in rt.redis(redis[survivor], new_master, "INFO", "replication"))
    replica = next(REDIS_HOSTS[index] for index in survivors if REDIS_HOSTS[index] != new_master)
    rt.wait("remaining_replica_synced_to_new_dns_master", lambda: rt.replica_ready(redis[survivor], replica, new_master))
    login(rt, client, redis[survivor], new_master, admin_password, "after_failover")
    old_state = json.loads(rt.docker("inspect", redis[old_index]).stdout)[0]["State"]
    rt.check("old_master_pair_remained_paused_during_recovery", old_state.get("Paused") is True)
    rt.check_runtime_bounds()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path, help="new private builder evidence directory")
    parser.add_argument("--upstream-sql", required=True, type=Path, help="fixed official SQL verified by prepare-seed.py")
    args = parser.parse_args()
    rt = Runtime(args.output)
    def interrupted(signum, frame):
        raise KeyboardInterrupt("isolated acceptance interrupted")
    signal.signal(signal.SIGTERM, interrupted)
    result = {"run": rt.run, "scope": "disposable Docker only; single PG, three Redis/Sentinel pairs; no PostgreSQL HA claim",
              "redis_fault": "pause master Redis/Sentinel pair; retain and verify per-member Service-like DNS; no forced failover",
              "captcha": "normal API challenge and verification, disposable Redis white-box answer oracle",
              "postgres_server_ca": "temporary CNPG-server CA analogue, separate from postgres-client CA; live CNPG issuance not tested"}
    try:
        verify(rt, args.upstream_sql.resolve())
        result["status"] = "passed"
    except (Exception, KeyboardInterrupt) as error:
        result.update(status="failed", error=rt.redact(str(error)))
    finally:
        try:
            result["cleanup_confirmed"] = rt.cleanup()
        except Exception:
            result["cleanup_confirmed"] = False
            result["cleanup_error"] = "cleanup could not finish; inspect only this run's labeled objects"
        result["checks"] = rt.checks
        if not result["cleanup_confirmed"] or not rt.checks or not all(rt.checks.values()):
            result["status"] = "failed"
        # Values here are fixed metadata/boolean checks; error was redacted above.
        # A one-digit captcha answer must not rewrite check names or JSON fields.
        text = json.dumps(result, indent=2) + "\n"
        (rt.output / "result.json").write_text(text)
        print(text, end="")
    return 0 if result["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
