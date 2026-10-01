"""Regression for service metadata without a user-controlled SHA256 field."""
import hashlib
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from storage_sync.checkpoint import load_json
from storage_sync.engine import execute, make_plan
from storage_sync.filesystem import scan_idc
from storage_sync.model import ObjectInfo, SyncError, plan_from_dict
from storage_sync.worker import _scan


class MetadataOnlyStore:
    """Like TOS HEAD, expose a service checksum and identity but no SHA256."""
    def __init__(self, objects=None):
        self.objects = dict(objects or {})
        self.reads = 0

    def head(self, bucket, key):
        data = self.objects.get((bucket, key))
        if data is None:
            return None
        digest = hashlib.sha256(data).digest()
        # An opaque service checksum fixture, distinct for our two payloads.
        return ObjectInfo(key=key, size=len(data), etag=digest.hex(),
                          crc64=str(int.from_bytes(digest[:8], 'big')))

    def list_page(self, bucket, prefix, token='', limit=1000):
        keys = sorted(key for candidate, key in self.objects if candidate == bucket and key.startswith(prefix))
        return [self.head(bucket, key) for key in keys], ''

    def read(self, bucket, obj):
        current = self.head(bucket, obj.key)
        if current is None or current.etag != obj.etag:
            raise SyncError('SOURCE_CHANGED')
        self.reads += 1
        return io.BytesIO(self.objects[bucket, obj.key])

    def put(self, bucket, key, stream, expected, sha256=''):
        current = self.head(bucket, key)
        if (current.etag if current else None) != (expected.etag if expected else None):
            raise SyncError('TARGET_CHANGED')
        self.objects[bucket, key] = stream.read()
        return self.head(bucket, key)


class BaselineTests(unittest.TestCase):
    def test_verified_idc_baseline_reuses_head_without_sha_and_detects_target_change(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'source').mkdir()
            (root / 'source' / 'a').write_bytes(b'original')
            entries = scan_idc(root / 'source', 'a', 'METADATA')
            store = MetadataOnlyStore()
            execute(make_plan(entries, store, 'dst', 'out'), store, store, root / 'work', 'run', 'config')
            saved = plan_from_dict(load_json(root / 'work' / 'baseline.json'))
            self.assertTrue(saved.entries[0].target.sha256)
            self.assertFalse(store.head('dst', 'out/a').sha256)
            reads_before = store.reads
            baseline = {item.target_key: item for item in saved.entries}
            unchanged = make_plan(entries, store, 'dst', 'out', baseline=baseline)
            self.assertEqual(unchanged.pending_bytes, 0)
            self.assertEqual(unchanged.reused_files, 1)
            self.assertEqual(store.reads, reads_before)
            store.objects['dst', 'out/a'] = b'modified'
            changed = make_plan(entries, store, 'dst', 'out', baseline=baseline)
            self.assertEqual(changed.pending_bytes, len(b'original'))
            self.assertEqual(changed.reused_files, 0)

    def test_first_identical_idc_target_establishes_content_proof_without_conflict(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'a').write_bytes(b'original')
            entries = scan_idc(root, 'a', 'METADATA')
            store = MetadataOnlyStore({('dst', 'out/a'): b'original'})
            plan = make_plan(entries, store, 'dst', 'out', policy='FAIL_IF_DIFFERENT')
            self.assertEqual(plan.pending_bytes, 0)
            self.assertEqual(plan.reused_files, 1)
            self.assertEqual(store.reads, 1)

    def test_worker_loads_verified_mapping_baseline_reference(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / 'source'
            source.mkdir()
            (source / 'a').write_bytes(b'original')
            store = MetadataOnlyStore()
            entries = scan_idc(source, 'a', 'METADATA')
            execute(make_plan(entries, store, 'dst', 'out'), store, store, root / 'work', 'run', 'config')
            baseline = load_json(root / 'work' / 'baseline.json')
            spec = {'config': {'mode': 'INCREMENTAL', 'verification': 'METADATA'},
                    'baselineRef': '/work/ssr-previous',
                    'mappings': [{'source': {'kind': 'IDC', 'spaceId': 'source', 'relativePath': 'a'},
                                  'destination': {'bucket': 'dst', 'prefix': 'out'}, 'layout': 'CONTENTS'}]}
            reporter = type('Reporter', (), {'control': lambda self: '', 'update': lambda self, **fields: None})()
            reads_before = store.reads
            with patch('storage_sync.worker._source_root', return_value=source), \
                    patch('storage_sync.worker.load_json', return_value=baseline) as load:
                plans = _scan(spec, store, reporter)
            load.assert_called_once_with(Path('/work/ssr-previous/mapping-0/baseline.json'))
            self.assertEqual(plans[0].pending_bytes, 0)
            self.assertEqual(plans[0].reused_files, 1)
            self.assertEqual(store.reads, reads_before)


if __name__ == '__main__':
    unittest.main()
