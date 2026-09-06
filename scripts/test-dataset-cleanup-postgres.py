#!/usr/bin/env python3
"""Exercise cleanup migration/locks in an EMPTY, isolated Docker PostgreSQL.

Usage: python3 scripts/test-dataset-cleanup-postgres.py rtp-cleanup-pg-20260906
Only a container with no network and no persistent mounts is accepted. This is
SQL integration coverage, not a replacement for Go repository/API tests.
"""

import argparse
import json
from pathlib import Path
import queue
import re
import subprocess
import threading
import time


class Session:
    def __init__(self, command):
        self.process = subprocess.Popen(command, stdin=subprocess.PIPE,
                                        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                        text=True, bufsize=1)
        self.lines = queue.Queue()
        threading.Thread(target=self._read, daemon=True).start()

    def _read(self):
        for line in self.process.stdout:
            self.lines.put(line)

    def send(self, sql):
        self.process.stdin.write(sql + "\n")
        self.process.stdin.flush()

    def until(self, marker, timeout=10):
        deadline = time.monotonic() + timeout
        output = []
        while time.monotonic() < deadline:
            try:
                line = self.lines.get(timeout=max(.01, deadline - time.monotonic()))
            except queue.Empty:
                break
            output.append(line)
            if marker in line:
                return "".join(output)
        raise AssertionError(f"session did not reach {marker}: {''.join(output)}")

    def close(self):
        if self.process.poll() is None:
            self.send("ROLLBACK;\n\\q")
            self.process.wait(timeout=10)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("container")
    parser.add_argument("--database", default="cleanup_test")
    args = parser.parse_args()
    if not re.fullmatch(r"rtp-cleanup-pg-[A-Za-z0-9_-]+", args.container):
        parser.error("container must have the isolated-test prefix rtp-cleanup-pg-")
    if args.database != "cleanup_test":
        parser.error("only the disposable cleanup_test database is allowed")
    inspected = json.loads(subprocess.check_output(
        ["docker", "inspect", args.container], text=True))[0]
    if inspected["HostConfig"]["NetworkMode"] != "none":
        raise RuntimeError("refusing container with network access")
    if any(mount["Type"] != "tmpfs" for mount in inspected.get("Mounts", [])):
        raise RuntimeError("refusing persistent or host bind mounts")
    command = ["docker", "exec", "-i", args.container, "psql", "-X", "-qAt",
               "-U", "postgres", "-d", args.database, "-v", "VERBOSITY=verbose"]

    def sql(statement, state=None):
        result = subprocess.run(command + ["-v", "ON_ERROR_STOP=1", "-1"],
                                input=statement, text=True, capture_output=True, timeout=70)
        if state:
            assert result.returncode != 0 and state in result.stderr, result
        else:
            assert result.returncode == 0, result.stderr
        return result.stdout.strip()

    assert sql("SELECT count(*) FROM pg_tables WHERE schemaname='public';") == "0", \
        "database must be empty; create a fresh disposable container"
    migrations = sorted((Path(__file__).resolve().parents[1] / "backend/db/migrations").glob("*.up.sql"))
    for migration in migrations:
        sql(migration.read_text())
        print(f"PASS migration {migration.name}", flush=True)

    sql("""
      INSERT INTO datasets(id,slug,name,source_space,source_relative_path,visibility,schema_version)
        VALUES ('cleanup-fixture','cleanup-fixture','Cleanup fixture','public','fixture','PUBLIC','v1');
      INSERT INTO dataset_versions(id,dataset_id,version,state,schema_version)
        SELECT id,'cleanup-fixture',id,'FAILED','v1'
        FROM unnest(ARRAY['failed-version','run-only-version','lock-version']) AS id;
      INSERT INTO dataset_publication_runs(id,dataset_id,dataset_version_id,state)
        SELECT id || '-run','cleanup-fixture',id,'FAILED'
        FROM unnest(ARRAY['failed-version','run-only-version','lock-version']) AS id;
      INSERT INTO dataset_partitions(id,dataset_version_id,name)
        SELECT id || '-part',id,'part'
        FROM unnest(ARRAY['failed-version','run-only-version','lock-version']) AS id;
      INSERT INTO dataset_publication_partition_attempts(dataset_version_id,partition_id,state,input_fingerprint,plan_sha256)
        SELECT id,id || '-part','FAILED',repeat('a',64),repeat('b',64)
        FROM unnest(ARRAY['failed-version','run-only-version','lock-version']) AS id;
    """)
    sql("UPDATE dataset_versions SET deleted_at=NOW() WHERE id='failed-version';", "23514")
    sql("""
      UPDATE dataset_publication_runs SET deleted_at=NOW(),deleted_by='test-admin'
        WHERE dataset_version_id IN ('failed-version','run-only-version');
      UPDATE dataset_versions SET deleted_at=NOW(),deleted_by='test-admin' WHERE id='failed-version';
    """)
    assert sql("SELECT count(*) FROM dataset_versions WHERE deleted_at IS NULL;") == "2"
    assert sql("SELECT deleted_by FROM dataset_versions WHERE id='failed-version';") == "test-admin"
    for table, identity in [("dataset_versions", "failed-version"),
                            ("dataset_publication_runs", "failed-version-run")]:
        sql(f"UPDATE {table} SET deleted_at=NULL,deleted_by='' WHERE id='{identity}';", "23514")
        sql(f"UPDATE {table} SET state='DISCOVERING' WHERE id='{identity}';", "23514")
        sql(f"DELETE FROM {table} WHERE id='{identity}';", "23514")
    sql("""INSERT INTO dataset_versions(id,dataset_id,version,state,schema_version)
          VALUES ('failed-version','cleanup-fixture','failed-version','FAILED','v1');""", "23505")
    for identity in ["failed-version", "run-only-version"]:
        sql(f"UPDATE dataset_publication_partition_attempts SET attempt=attempt+1 WHERE dataset_version_id='{identity}';", "23514")
        sql(f"""INSERT INTO dataset_publication_partition_attempts
          (dataset_version_id,partition_id,state,input_fingerprint,plan_sha256)
          VALUES ('{identity}','{identity}-part','FAILED',repeat('a',64),repeat('b',64));""", "23514")
    sql("""
      UPDATE dataset_publication_runs SET deleted_at=NOW(),deleted_by='test-admin'
        WHERE id='lock-version-run';
      UPDATE dataset_versions SET deleted_at=NOW() WHERE id='lock-version';
    """, "23514")
    assert sql("SELECT count(*) FROM dataset_publication_runs WHERE id='lock-version-run' AND deleted_at IS NULL;") == "1"
    print("PASS audit, retained identity, immutable tombstones, version/run-only attempt guards", flush=True)

    cleanup, worker = Session(command), Session(command)
    try:
        cleanup.send("BEGIN; SET LOCAL statement_timeout='8s'; SELECT id FROM dataset_publication_runs WHERE id='lock-version-run' FOR UPDATE; SELECT id FROM dataset_versions WHERE id='lock-version' FOR UPDATE;\n\\echo CLEANUP_LOCKED")
        assert "ERROR" not in cleanup.until("CLEANUP_LOCKED")
        worker.send("SET application_name='cleanup-test-worker'; BEGIN; SET LOCAL statement_timeout='8s'; SELECT partition_id FROM dataset_publication_partition_attempts WHERE dataset_version_id='lock-version' FOR UPDATE;\n\\echo ATTEMPT_LOCKED")
        assert "ERROR" not in worker.until("ATTEMPT_LOCKED")
        worker.send("UPDATE dataset_publication_partition_attempts SET attempt=attempt+1 WHERE dataset_version_id='lock-version';\n\\echo WORKER_UPDATED")
        deadline = time.monotonic() + 5
        while sql("SELECT count(*) FROM pg_stat_activity WHERE application_name='cleanup-test-worker' AND wait_event_type='Lock';") != "1":
            if time.monotonic() > deadline:
                raise AssertionError("worker did not wait for cleanup's version lock")
            time.sleep(.05)
        cleanup.send("SELECT partition_id FROM dataset_publication_partition_attempts WHERE dataset_version_id='lock-version' FOR UPDATE NOWAIT;\n\\echo CLEANUP_CONFLICT")
        assert "55P03" in cleanup.until("CLEANUP_CONFLICT")
        cleanup.send("ROLLBACK;\n\\echo CLEANUP_ROLLED_BACK")
        cleanup.until("CLEANUP_ROLLED_BACK")
        assert "ERROR" not in worker.until("WORKER_UPDATED")
        worker.send("ROLLBACK;\n\\echo WORKER_ROLLED_BACK")
        worker.until("WORKER_ROLLED_BACK")
    finally:
        cleanup.close()
        worker.close()
    assert sql("SELECT attempt FROM dataset_publication_partition_attempts WHERE dataset_version_id='lock-version';") == "0"
    assert sql("SELECT count(*) FROM dataset_versions WHERE id='lock-version' AND deleted_at IS NULL;") == "1"
    print("PASS concurrent attempt/version lock: 55P03, no deadlock, both transactions rolled back", flush=True)
    print("PASS all isolated PostgreSQL cleanup checks (SQL layer only)", flush=True)


if __name__ == "__main__":
    main()
