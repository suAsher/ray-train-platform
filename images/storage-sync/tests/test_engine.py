"""Behavioral contract; run on the builder with unittest discover."""
import hashlib
import io
import os
import tempfile
import unittest
from dataclasses import replace
from pathlib import Path

from storage_sync.engine import execute, make_plan, scan_tos
from storage_sync.filesystem import scan_idc, open_verified
from storage_sync.model import ObjectInfo, SourceEntry, SyncError, StopRequested


def info(key, content, etag=None):
    return ObjectInfo(key=key, size=len(content), etag=etag or hashlib.sha256(content).hexdigest(),
                      sha256=hashlib.sha256(content).hexdigest())


class MemoryStore:
    def __init__(self, objects=None):
        self.objects = dict(objects or {})
        self.tags = {}
        self.pages = 0
        self.writes = []
        self.uploads = {}
        self.aborted = []
        self.before_complete = None
        self.after_part = None

    def head(self, bucket, key):
        content = self.objects.get((bucket, key))
        return None if content is None else info(key, content, self.tags.get((bucket, key)))

    def list_page(self, bucket, prefix, token='', limit=1000):
        keys = sorted(k for b, k in self.objects if b == bucket and k.startswith(prefix))
        start = int(token or 0)
        page = keys[start:start + limit]
        self.pages += 1
        return [self.head(bucket, key) for key in page], str(start + limit) if start + limit < len(keys) else ''

    def read(self, bucket, obj):
        current = self.head(bucket, obj.key)
        if current is None or current.etag != obj.etag:
            raise SyncError('SOURCE_CHANGED')
        return io.BytesIO(self.objects[bucket, obj.key])

    def _guard(self, bucket, key, expected):
        current = self.head(bucket, key)
        if (current.etag if current else None) != (expected.etag if expected else None):
            raise SyncError('TARGET_CHANGED')

    def put(self, bucket, key, stream, expected, sha256=''):
        content = stream.read()
        self._guard(bucket, key, expected)
        self.objects[bucket, key] = content
        self.tags.pop((bucket, key), None)
        self.writes.append(key)
        return self.head(bucket, key)

    def copy(self, source_bucket, source, bucket, key, expected):
        with self.read(source_bucket, source) as stream:
            return self.put(bucket, key, stream, expected)

    def create_upload(self, bucket, key, expected, metadata):
        upload_id = str(len(self.uploads) + 1)
        self.uploads[upload_id] = {'bucket': bucket, 'key': key, 'parts': {}, 'expected': expected}
        return upload_id

    def list_parts(self, bucket, key, upload_id):
        return {number: {'etag': hashlib.sha256(data).hexdigest(), 'size': len(data)}
                for number, data in self.uploads[upload_id]['parts'].items()}

    def upload_part(self, bucket, key, upload_id, number, stream, size):
        data = stream.read(size)
        self.uploads[upload_id]['parts'][number] = data
        if self.after_part:
            self.after_part(number)
        return {'etag': hashlib.sha256(data).hexdigest(), 'size': len(data)}

    def copy_part(self, source_bucket, source, bucket, key, upload_id, number, offset, size):
        with self.read(source_bucket, source) as stream:
            stream.seek(offset)
            return self.upload_part(bucket, key, upload_id, number, stream, size)

    def complete_upload(self, bucket, key, upload_id, parts, expected):
        if self.before_complete:
            self.before_complete()
        self._guard(bucket, key, expected)
        upload = self.uploads[upload_id]
        self.objects[bucket, key] = b''.join(upload['parts'][n] for n in sorted(parts))
        self.tags.pop((bucket, key), None)
        self.writes.append(key)
        del self.uploads[upload_id]
        return self.head(bucket, key)

    def abort_upload(self, bucket, key, upload_id):
        self.aborted.append(upload_id)
        self.uploads.pop(upload_id, None)


class PlanningTests(unittest.TestCase):
    def test_paginate_more_than_1000_and_preserve_unicode_percent_space(self):
        objects = {('src', f'data/中文 %/{i:04d}.bin'): b'x' for i in range(1005)}
        store = MemoryStore(objects)
        entries = scan_tos(store, 'src', 'data', True, 'METADATA')
        self.assertEqual(len(entries), 1005)
        self.assertEqual(store.pages, 2)
        self.assertEqual(entries[0].relative_path, '中文 %/0000.bin')

    def test_file_and_directory_layout(self):
        source = MemoryStore({('src', 'some/data/a'): b'a'})
        entries = scan_tos(source, 'src', 'some/data', True, 'METADATA')
        kept = make_plan(entries, source, 'dst', 'out', layout='KEEP_DIRECTORY', source_name='data')
        flat = make_plan(entries, source, 'dst', 'out', layout='CONTENTS', source_name='data')
        self.assertEqual(kept.entries[0].target_key, 'out/data/a')
        self.assertEqual(flat.entries[0].target_key, 'out/a')
        single = scan_tos(source, 'src', 'some/data/a', False, 'METADATA')
        self.assertEqual(make_plan(single, source, 'dst', 'out').entries[0].target_key, 'out/a')

    def test_preserve_target_only_map_json_full_and_incremental(self):
        for mode in ('FULL', 'INCREMENTAL'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                store = MemoryStore({('src', 'data/a'): b'a', ('dst', 'out/maps/cnwxijk.json'): b'{"map":1}'})
                before = store.objects['dst', 'out/maps/cnwxijk.json']
                plan = make_plan(scan_tos(store, 'src', 'data', True, 'CONTENT'), store, 'dst', 'out', mode=mode)
                result = execute(plan, store, store, Path(directory), 'run', 'config')
                self.assertEqual(store.objects['dst', 'out/maps/cnwxijk.json'], before)
                self.assertEqual(plan.extra_files, 1)
                self.assertEqual(result['sourceFiles'], 1)
                self.assertEqual(result['completedBytes'], 1)

    def test_zero_change_baseline_and_target_same_size_changed(self):
        store = MemoryStore({('src', 'data/a'): b'a', ('dst', 'out/a'): b'a'})
        source = scan_tos(store, 'src', 'data', True, 'CONTENT')
        first = make_plan(source, store, 'dst', 'out')
        baseline = {item.target_key: item for item in first.entries}
        unchanged = make_plan(source, store, 'dst', 'out', baseline=baseline)
        self.assertEqual(unchanged.pending_bytes, 0)
        self.assertEqual(unchanged.reused_files, 1)
        store.objects['dst', 'out/a'] = b'b'
        changed = make_plan(source, store, 'dst', 'out', baseline=baseline)
        self.assertEqual(changed.pending_bytes, 1)
        self.assertEqual(changed.reused_files, 0)

    def test_fail_if_different_preflight_never_writes(self):
        store = MemoryStore({('src', 'data/a'): b'a', ('dst', 'out/a'): b'b'})
        with self.assertRaisesRegex(SyncError, 'TARGET_CONFLICT'):
            make_plan(scan_tos(store, 'src', 'data', True, 'CONTENT'), store, 'dst', 'out', policy='FAIL_IF_DIFFERENT')
        self.assertEqual(store.writes, [])

    def test_etag_is_not_md5_or_content_checksum(self):
        store = MemoryStore({('src', 'data/a'): b'a', ('dst', 'out/a'): b'b'})
        store.tags = {('src', 'data/a'): 'opaque-5', ('dst', 'out/a'): 'opaque-5'}
        plan = make_plan(scan_tos(store, 'src', 'data', True, 'CONTENT'), store, 'dst', 'out')
        self.assertEqual(plan.pending_bytes, 1)


class FilesystemTests(unittest.TestCase):
    def test_same_size_backdated_modification_is_candidate(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'a').write_bytes(b'old')
            first = scan_idc(root, 'a', 'METADATA')[0]
            (root / 'a').write_bytes(b'new')
            os.utime(root / 'a', ns=(1, 1))
            second = scan_idc(root, 'a', 'METADATA')[0]
            self.assertNotEqual(first.fingerprint, second.fingerprint)

    def test_symlink_special_file_and_parent_escape_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'link').symlink_to('/etc/passwd')
            for relative in ('link', '../etc/passwd', '/etc/passwd'):
                with self.subTest(relative=relative), self.assertRaises(SyncError):
                    scan_idc(root, relative, 'CONTENT')
            (root / 'link').unlink()
            os.mkfifo(root / 'fifo')
            with self.assertRaises(SyncError):
                scan_idc(root, 'fifo', 'CONTENT')

    def test_directory_symlink_not_silently_omitted(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'data').mkdir()
            (root / 'data/link').symlink_to('/tmp', target_is_directory=True)
            with self.assertRaises(SyncError):
                scan_idc(root, 'data', 'CONTENT')

    def test_source_changed_after_scan_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'a').write_bytes(b'old')
            entry = scan_idc(root, 'a', 'CONTENT')[0]
            (root / 'a').write_bytes(b'new')
            with self.assertRaisesRegex(SyncError, 'SOURCE_CHANGED'):
                with open_verified(entry):
                    pass


class TransferTests(unittest.TestCase):
    def plan(self, store):
        return make_plan(scan_tos(store, 'src', 'data', True, 'CONTENT'), store, 'dst', 'out')

    def test_conditional_copy_race(self):
        with tempfile.TemporaryDirectory() as directory:
            store = MemoryStore({('src', 'data/a'): b'abcdef', ('dst', 'out/a'): b'old'})
            plan = self.plan(store)
            store.before_complete = lambda: store.objects.__setitem__(('dst', 'out/a'), b'external')
            with self.assertRaisesRegex(SyncError, 'TARGET_CHANGED'):
                execute(plan, store, store, Path(directory), 'run', 'config', part_size=2)
            self.assertEqual(store.objects['dst', 'out/a'], b'external')
            self.assertEqual(store.writes, [])

    def test_pause_keeps_checkpoint_resume_only_missing_parts(self):
        with tempfile.TemporaryDirectory() as directory:
            store = MemoryStore({('src', 'data/a'): b'abcdef'})
            plan = self.plan(store)
            state = {'control': ''}
            store.after_part = lambda number: state.update(control='PAUSE') if number == 1 else None
            with self.assertRaises(StopRequested):
                execute(plan, store, store, Path(directory), 'run', 'config', part_size=2, control=lambda: state['control'])
            self.assertEqual(store.aborted, [])
            self.assertEqual(len(store.uploads), 1)
            store.after_part = None
            result = execute(plan, store, store, Path(directory), 'run', 'config', part_size=2)
            self.assertEqual(store.objects['dst', 'out/a'], b'abcdef')
            self.assertEqual(result['verifiedFiles'], 1)
            self.assertEqual(result['completedBytes'], 6)

    def test_cancel_aborts_only_owned_partial_not_completed_or_foreign(self):
        with tempfile.TemporaryDirectory() as directory:
            store = MemoryStore({('src', 'data/a'): b'abcdef', ('dst', 'out/completed'): b'keep'})
            foreign = store.create_upload('dst', 'foreign', None, {})
            state = {'control': ''}
            store.after_part = lambda number: state.update(control='CANCEL')
            with self.assertRaises(StopRequested):
                execute(self.plan(store), store, store, Path(directory), 'run', 'config', part_size=2, control=lambda: state['control'])
            self.assertIn(foreign, store.uploads)
            self.assertEqual(store.objects['dst', 'out/completed'], b'keep')
            self.assertEqual(len(store.aborted), 1)
            self.assertNotIn(foreign, store.aborted)

    def test_checkpoint_bound_to_source_and_config(self):
        with tempfile.TemporaryDirectory() as directory:
            store = MemoryStore({('src', 'data/a'): b'abcdef'})
            state = {'control': ''}
            store.after_part = lambda number: state.update(control='PAUSE')
            with self.assertRaises(StopRequested):
                execute(self.plan(store), store, store, Path(directory), 'run', 'config', part_size=2, control=lambda: state['control'])
            store.after_part = None
            with self.assertRaisesRegex(SyncError, 'CHECKPOINT_MISMATCH'):
                execute(self.plan(store), store, store, Path(directory), 'run', 'different-config', part_size=2)

    def test_progress_monotonic_and_zero_work_success(self):
        with tempfile.TemporaryDirectory() as directory:
            store = MemoryStore({('src', 'data/a'): b''})
            reports = []
            result = execute(self.plan(store), store, store, Path(directory), 'run', 'config', report=reports.append)
            self.assertEqual(result['verifiedFiles'], 1)
            self.assertEqual(result['completedBytes'], 0)
            self.assertTrue(all(a['sequence'] < b['sequence'] for a, b in zip(reports, reports[1:])))
            self.assertTrue(all(r['completedBytes'] <= r['pendingBytes'] for r in reports))
            self.assertEqual(reports[-1]['status'], 'SUCCEEDED')

    def test_source_condition_blocks_scan_copy_race(self):
        with tempfile.TemporaryDirectory() as directory:
            store = MemoryStore({('src', 'data/a'): b'old'})
            plan = self.plan(store)
            store.objects['src', 'data/a'] = b'new'
            with self.assertRaisesRegex(SyncError, 'SOURCE_CHANGED'):
                execute(plan, store, store, Path(directory), 'run', 'config')
            self.assertEqual(store.writes, [])


if __name__ == '__main__':
    unittest.main()


class ContentVerificationTests(unittest.TestCase):
    class SameCRCStore(MemoryStore):
        def head(self, bucket, key):
            value = super().head(bucket, key)
            return replace(value, sha256='', crc64='7') if value else None

    def test_content_mode_compares_bytes_even_when_service_metadata_crc_matches(self):
        store = self.SameCRCStore({('src', 'data/a'): b'a', ('dst', 'out/a'): b'b'})
        entries = scan_tos(store, 'src', 'data', True, 'CONTENT')
        plan = make_plan(entries, store, 'dst', 'out', verification='CONTENT')
        self.assertEqual(plan.pending_bytes, 1)
        self.assertEqual(plan.reused_files, 0)

    def test_content_verification_rejects_corrupt_transfer_with_equal_crc(self):
        class CorruptCopy(self.SameCRCStore):
            def copy(self, source_bucket, source, bucket, key, expected):
                self.objects[bucket, key] = b'b'
                return self.head(bucket, key)
        with tempfile.TemporaryDirectory() as directory:
            store = CorruptCopy({('src', 'data/a'): b'a'})
            plan = make_plan(scan_tos(store, 'src', 'data', True, 'CONTENT'), store, 'dst', 'out', verification='CONTENT')
            with self.assertRaisesRegex(SyncError, 'CHECKSUM_MISMATCH'):
                execute(plan, store, store, Path(directory), 'run', 'config')
