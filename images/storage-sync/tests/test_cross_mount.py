"""Separate worker mounts must share identities without weakening read guards."""
from contextlib import contextmanager
from dataclasses import asdict, replace
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch
import os
import tempfile
import unittest

from storage_sync.filesystem import browse_idc, open_verified, scan_idc
from storage_sync.checkpoint import load_json, save_json
from storage_sync.model import Plan, PlanEntry, SourceEntry, SyncError, canonical_digest, plan_from_dict
from storage_sync.worker import _preview, _transfer, manifest_summary
from test_engine import MemoryStore
from test_worker import RecordingReporter


def with_device(metadata, device):
    return SimpleNamespace(**{name: device if name == 'st_dev' else getattr(metadata, name)
                              for name in dir(metadata) if name.startswith('st_')})


@contextmanager
def remounted_device(offset):
    """Model another Pod's mount: all local observations still agree."""
    original_stat, original_fstat = os.stat, os.fstat

    def stat_result(*args, **kwargs):
        result = original_stat(*args, **kwargs)
        return with_device(result, result.st_dev + offset)

    def fstat_result(*args, **kwargs):
        result = original_fstat(*args, **kwargs)
        return with_device(result, result.st_dev + offset)

    with patch('storage_sync.filesystem.os.stat', side_effect=stat_result), \
            patch('storage_sync.filesystem.os.fstat', side_effect=fstat_result):
        yield


def plan_for(source):
    return Plan((PlanEntry(source, 'out/file', None),), 'destination', verification='CONTENT')


class CrossMountIdentityTests(unittest.TestCase):
    def test_tos_plan_identity_matches_existing_serialization(self):
        source = SourceEntry('file', 'TOS', bucket='source', key='data/file', size=7,
                             etag='opaque-etag', sha256='verified-sha256')
        plan = plan_for(source)
        expected_source = {key: value for key, value in asdict(source).items()
                           if key not in ('sha256', 'crc64')}
        self.assertEqual(source.fingerprint, canonical_digest(expected_source))
        self.assertEqual(plan.digest, canonical_digest(asdict(plan)))
        self.assertEqual(plan.source_fingerprint, canonical_digest([asdict(source)]))

    def test_preview_file_can_be_read_from_another_worker_mount(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'file').write_bytes(b'payload')
            with remounted_device(9):
                entry = scan_idc(root, 'file', 'CONTENT')[0]
            for verify_content in (False, True):
                with self.subTest(verify_content=verify_content):
                    with open_verified(entry, verify_content=verify_content) as stream:
                        self.assertEqual(stream.read(), b'payload')

    def test_preview_recheck_and_incremental_identity_survive_remount(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'file').write_bytes(b'payload')
            first = scan_idc(root, 'file', 'CONTENT')[0]
            with remounted_device(9):
                second = scan_idc(root, 'file', 'CONTENT')[0]
            first_plan, second_plan = plan_for(first), plan_for(second)
            self.assertEqual(first.fingerprint, second.fingerprint)
            self.assertEqual(first_plan.digest, second_plan.digest)
            self.assertEqual(first_plan.source_fingerprint, second_plan.source_fingerprint)
            self.assertEqual(manifest_summary([first_plan]), manifest_summary([second_plan]))

    def test_legacy_manifest_device_is_parseable_but_not_portable_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'file').write_bytes(b'payload')
            current = scan_idc(root, 'file', 'CONTENT')[0]
            legacy_data = asdict(plan_for(current))
            legacy_data['entries'][0]['source']['device'] = os.stat(root / 'file').st_dev + 9
            legacy = plan_from_dict(legacy_data)
            with open_verified(legacy.entries[0].source) as stream:
                self.assertEqual(stream.read(), b'payload')
            self.assertEqual(legacy.entries[0].source.fingerprint, current.fingerprint)
            self.assertEqual(legacy.digest, plan_for(current).digest)
            self.assertEqual(legacy.source_fingerprint, plan_for(current).source_fingerprint)

    def test_legacy_manifest_without_device_remains_readable(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'file').write_bytes(b'payload')
            data = asdict(plan_for(scan_idc(root, 'file', 'CONTENT')[0]))
            del data['entries'][0]['source']['device']
            with open_verified(plan_from_dict(data).entries[0].source) as stream:
                self.assertEqual(stream.read(), b'payload')

    def test_changed_portable_metadata_or_content_is_still_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'file').write_bytes(b'payload')
            with remounted_device(9):
                entry = scan_idc(root, 'file', 'CONTENT')[0]
            for field in ('inode', 'size', 'mtime_ns', 'ctime_ns', 'sha256'):
                value = '0' * 64 if field == 'sha256' else getattr(entry, field) + 1
                with self.subTest(field=field), self.assertRaisesRegex(SyncError, 'SOURCE_CHANGED'):
                    with open_verified(replace(entry, **{field: value})):
                        self.fail('changed source must be rejected before reading')

    def test_stable_identity_remains_bound_to_root_path_and_metadata(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'file').write_bytes(b'payload')
            entry = scan_idc(root, 'file', 'CONTENT')[0]
            for field in ('local_root', 'local_path', 'inode', 'size', 'mtime_ns', 'ctime_ns'):
                value = getattr(entry, field)
                changed = replace(entry, **{field: value + 1 if isinstance(value, int) else value + '-other'})
                with self.subTest(field=field):
                    self.assertNotEqual(entry.fingerprint, changed.fingerprint)
                    self.assertNotEqual(plan_for(entry).digest, plan_for(changed).digest)
                    self.assertNotEqual(plan_for(entry).source_fingerprint, plan_for(changed).source_fingerprint)

    def test_mount_change_during_one_descriptor_operation_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'file').write_bytes(b'payload')
            entry = scan_idc(root, 'file', 'CONTENT')[0]
            original_fstat = os.fstat

            def different_device(descriptor):
                result = original_fstat(descriptor)
                return with_device(result, result.st_dev + 9)

            with patch('storage_sync.filesystem.os.fstat', side_effect=different_device), \
                    self.assertRaisesRegex(SyncError, 'SOURCE_CHANGED'):
                with open_verified(entry):
                    self.fail('inconsistent local stat/fstat observations must be rejected')

    def test_directory_cursor_can_continue_from_another_worker_mount(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ('a', 'b', 'c'):
                (root / name).write_bytes(name.encode())
            with remounted_device(9):
                first, token = browse_idc(root, '', limit=1)
            second, token = browse_idc(root, '', token=token, limit=2)
            self.assertEqual([entry['name'] for entry in first + second], ['a', 'b', 'c'])
            self.assertFalse(token)

    def test_changed_directory_snapshot_still_invalidates_cursor(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'a').write_bytes(b'a')
            (root / 'b').write_bytes(b'b')
            with remounted_device(9):
                _, token = browse_idc(root, '', limit=1)
            (root / 'c').write_bytes(b'c')
            with self.assertRaisesRegex(SyncError, 'INVALID_CURSOR'):
                browse_idc(root, '', token=token, limit=1)


class CrossMountWorkerTests(unittest.TestCase):
    def test_new_preview_rechecks_and_transfers_after_two_remounts(self):
        with tempfile.TemporaryDirectory() as directory:
            root, work = Path(directory) / 'source', Path(directory) / 'work'
            root.mkdir()
            (root / 'file').write_bytes(b'payload')
            spec = {'runId': 'run', 'config': {'verification': 'CONTENT'},
                    'mappings': [{'source': {'kind': 'IDC', 'spaceId': 'idc', 'relativePath': 'file'},
                                  'destination': {'bucket': 'destination', 'prefix': 'out'},
                                  'layout': 'CONTENTS'}]}
            store, preview = MemoryStore(), RecordingReporter()
            with patch('storage_sync.worker._source_root', return_value=root), remounted_device(9):
                _preview(spec, store, preview, work)
            recheck_spec = {**spec, **{name: preview.values[name] for name in
                                     ('manifestDigest', 'sourceFingerprint', 'targetFingerprint')}}
            recheck = RecordingReporter()
            with patch('storage_sync.worker._source_root', return_value=root), remounted_device(19):
                _preview(recheck_spec, store, recheck, work)
            with patch('storage_sync.worker.from_config', return_value=store):
                _transfer(recheck_spec, RecordingReporter(), work, '/unused-test-config')
            self.assertEqual(store.objects['destination', 'out/file'], b'payload')
            self.assertEqual(preview.values['manifestDigest'], recheck.values['manifestDigest'])

    def test_raw_legacy_manifest_transfer_preserves_hash_and_device_field(self):
        with tempfile.TemporaryDirectory() as directory:
            root, work = Path(directory) / 'source', Path(directory) / 'work'
            root.mkdir()
            (root / 'file').write_bytes(b'payload')
            source = scan_idc(root, 'file', 'CONTENT')[0]
            legacy = replace(source, device=os.stat(root / 'file').st_dev + 9)
            manifest = {'plans': [asdict(plan_for(legacy))]}
            save_json(work / 'manifest.json', manifest)
            original_bytes = (work / 'manifest.json').read_bytes()
            original_digest = canonical_digest(manifest)
            spec = {'runId': 'run', 'config': {}, 'manifestDigest': original_digest}
            store, reporter = MemoryStore(), RecordingReporter()
            with patch('storage_sync.worker.from_config', return_value=store):
                _transfer(spec, reporter, work, '/unused-test-config')
            self.assertEqual(store.objects['destination', 'out/file'], b'payload')
            self.assertEqual((work / 'manifest.json').read_bytes(), original_bytes)
            self.assertEqual(reporter.values['manifestDigest'], original_digest)
            self.assertEqual(load_json(work / 'mapping-0' / 'baseline.json')['entries'][0]['source']['device'], legacy.device)
