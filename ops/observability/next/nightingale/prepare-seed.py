#!/usr/bin/env python3
"""Builder-only: verify the pinned upstream seed and emit a non-secret SQL file."""

import hashlib
import pathlib
import sys

if len(sys.argv) != 3:
    raise SystemExit("usage: prepare-seed.py UPSTREAM_SQL OUTPUT_SQL")
source = pathlib.Path(sys.argv[1]).read_bytes()
blob = b"blob " + str(len(source)).encode() + b"\0" + source
if hashlib.sha1(blob).hexdigest() != "1856523e10eb1401ea619a822462bc4ccda00ce0":
    raise SystemExit("upstream v9.1.1 PostgreSQL seed checksum mismatch")
sql = source.decode()
old = "'root', 'Admin', 'root.2020', 'Admin'"
if sql.count(old) != 1:
    raise SystemExit("unexpected upstream root seed")
sql = sql.replace(old, "'root', 'Admin', :'N9E_ADMIN_PASSWORD', 'Admin'")
prefix = r"""\getenv N9E_ADMIN_PASSWORD N9E_ADMIN_PASSWORD
SELECT pg_advisory_xact_lock(90317000);
DO $$ BEGIN
  IF to_regclass('public.users') IS NOT NULL THEN
    RAISE EXCEPTION 'database already initialized; refusing to reseed';
  END IF;
END $$;
"""
suffix = """
SELECT setval(pg_get_serial_sequence('users','id'), (SELECT max(id) FROM users));
SELECT setval(pg_get_serial_sequence('user_group','id'), (SELECT max(id) FROM user_group));
SELECT setval(pg_get_serial_sequence('busi_group','id'), (SELECT max(id) FROM busi_group));
"""
pathlib.Path(sys.argv[2]).write_text(prefix + sql + suffix)
