"""Builder-only real TOS acceptance in a newly generated, caller-approved subtree.

Example: PYTHONPATH=images/storage-sync python3 images/storage-sync/acceptance.py
  --config /restricted/tosutil-config --bucket <approved> --prefix <own-test-root>
Never prints credentials, SDK exception text, or signed URLs.
"""
import argparse
import io
import json
import os
from pathlib import Path
import tempfile
import uuid

from storage_sync.engine import execute, make_plan, scan_tos
from storage_sync.filesystem import scan_idc
from storage_sync.model import SyncError, StopRequested, plan_from_dict, safe_relative
from storage_sync.checkpoint import load_json
from storage_sync.tos_adapter import from_config


class Acceptance:
    def __init__(self, store, bucket, prefix):
        self.store, self.bucket = store, bucket
        self.root = safe_relative(prefix) + '/storage-sync-acceptance-' + uuid.uuid4().hex
        self.keys = set()
        self.uploads = set()
        self.results = []
        create = store.create_upload
        def track(bucket, key, expected, metadata):
            upload = create(bucket, key, expected, metadata)
            self.uploads.add((bucket, key, upload))
            return upload
        store.create_upload = track

    def seed(self, key, data):
        full = self.root + '/' + key
        self.keys.add(full)
        self.store.put(self.bucket, full, io.BytesIO(data), self.store.head(self.bucket, full))
        return full

    def plan(self, source, target, mode='INCREMENTAL', baseline=None):
        entries = scan_tos(self.store, self.bucket, self.root + '/' + source, True, 'CONTENT')
        plan = make_plan(entries, self.store, self.bucket, self.root + '/' + target, mode=mode, baseline=baseline, verification='CONTENT')
        self.keys.update(item.target_key for item in plan.entries)
        return plan

    def record(self, name):
        result = {'case': name, 'passed': True}
        self.results.append(result)
        print(json.dumps(result), flush=True)

    def cleanup(self):
        errors = 0
        for bucket, key, upload in self.uploads:
            try:
                self.store.abort_upload(bucket, key, upload)
            except SyncError:
                errors += 1
        for key in self.keys:
            try:
                self.store.client.delete_object(self.bucket, key)
            except Exception:
                errors += 1
        return errors

    def run(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            for index in range(1005):
                self.seed(f'source/中文 %/{index:04d}', f'entry-{index}'.encode())
                if (index + 1) % 200 == 0:
                    print(json.dumps({'stage': 'seed', 'createdFiles': index + 1}), flush=True)
            extra = self.seed('target/maps/cnwxijk.json', b'{"preserve":true}')
            original = self.store.head(self.bucket, extra)
            plan = self.plan('source', 'target', mode='FULL')
            assert len(plan.entries) == 1005
            execute(plan, self.store, self.store, work / 'full', 'accept-full', plan.digest)
            assert self.store.head(self.bucket, extra).fingerprint == original.fingerprint
            self.record('pagination_1005_and_full_preserves_target_only_map_json')
            baseline_plan = plan_from_dict(load_json(work / 'full' / 'baseline.json'))
            baseline = {item.target_key: item for item in baseline_plan.entries}
            incremental = self.plan('source', 'target', baseline=baseline)
            assert incremental.pending_bytes == 0 and incremental.reused_files == 1005
            execute(incremental, self.store, self.store, work / 'incremental', 'accept-incremental', incremental.digest)
            assert self.store.head(self.bucket, extra).fingerprint == original.fingerprint
            self.record('zero_change_and_incremental_preserves_target_only_map_json')
            self._idc(work)
            self._copy_race(work)
            self._multipart(work)

    def _idc(self, work):
        source = work / 'idc'; source.mkdir()
        (source / '数据 %.bin').write_bytes(b'idc-file')
        target = self.root + '/idc-target'
        plan = make_plan(scan_idc(source, '', 'CONTENT'), self.store, self.bucket, target, verification='CONTENT')
        self.keys.update(item.target_key for item in plan.entries)
        result = execute(plan, self.store, self.store, work / 'idc-state', 'idc', plan.digest)
        assert result['verifiedFiles'] == 1
        self.record('idc_guarded_put_and_content_readback')
        baseline_plan = plan_from_dict(load_json(work / 'idc-state' / 'baseline.json'))
        baseline = {item.target_key: item for item in baseline_plan.entries}
        path = source / '数据 %.bin'
        before = path.stat()
        path.write_bytes(b'new-file')
        os.utime(path, ns=(before.st_atime_ns, before.st_mtime_ns - 60 * 1_000_000_000))
        changed = make_plan(scan_idc(source, '', 'CONTENT'), self.store, self.bucket, target,
                            baseline=baseline, verification='CONTENT')
        assert changed.pending_bytes == 8 and changed.reused_files == 0
        result = execute(changed, self.store, self.store, work / 'idc-modified', 'idc-modified', changed.digest)
        assert result['verifiedFiles'] == 1
        with self.store.read(self.bucket, self.store.head(self.bucket, changed.entries[0].target_key)) as stream:
            assert stream.read() == b'new-file'
        self.record('idc_same_size_backdated_modification_is_copied')

    def _copy_race(self, work):
        self.seed('race-source/a', b'new-value')
        target = self.seed('race-target/a', b'old-value')
        plan = self.plan('race-source', 'race-target')
        self.seed('race-target/a', b'external-write')
        current = self.store.head(self.bucket, target)
        try:
            execute(plan, self.store, self.store, work / 'copy-race', 'copy-race', plan.digest)
            raise AssertionError('conditional copy unexpectedly succeeded')
        except SyncError as error:
            assert str(error) == 'OBJECT_CONDITION_FAILED'
        assert self.store.head(self.bucket, target).fingerprint == current.fingerprint
        self.record('conditional_copy_race')
        source_plan = self.plan('race-source', 'source-race-target')
        original_copy = self.store.copy
        def replace_source_before_copy(*args, **kwargs):
            self.seed('race-source/a', b'replaced-source')
            return original_copy(*args, **kwargs)
        self.store.copy = replace_source_before_copy
        try:
            execute(source_plan, self.store, self.store, work / 'source-race', 'source-race', source_plan.digest)
            raise AssertionError('source condition unexpectedly succeeded')
        except SyncError as error:
            expected = {'OBJECT_CONDITION_FAILED'}
            if source_plan.entries[0].source.version_id:
                expected.add('SOURCE_CHANGED')
            assert str(error) in expected
        finally:
            self.store.copy = original_copy
        copied = self.store.head(self.bucket, self.root + '/source-race-target/a')
        if copied is not None:
            assert source_plan.entries[0].source.version_id
            with self.store.read(self.bucket, copied) as stream:
                assert stream.read() == b'new-value'
        self.record('conditional_source_copy_race')

    def _multipart(self, work):
        self.seed('multipart-source/a', b'm' * (6 * 1024 * 1024))
        plan = self.plan('multipart-source', 'multipart-target')
        original_part = self.store.copy_part
        state = {'control': ''}
        def pause_after_part(*args, **kwargs):
            result = original_part(*args, **kwargs)
            state['control'] = 'PAUSE'
            return result
        self.store.copy_part = pause_after_part
        try:
            execute(plan, self.store, self.store, work / 'multipart', 'multipart', plan.digest,
                    part_size=5 * 1024 * 1024, control=lambda: state['control'])
            raise AssertionError('pause not observed')
        except StopRequested:
            pass
        finally:
            self.store.copy_part = original_part
        resumed_parts = []
        def record_resumed_part(*args, **kwargs):
            resumed_parts.append(args[5])
            return original_part(*args, **kwargs)
        self.store.copy_part = record_resumed_part
        try:
            result = execute(plan, self.store, self.store, work / 'multipart', 'multipart', plan.digest, part_size=5 * 1024 * 1024)
        finally:
            self.store.copy_part = original_part
        assert result['verifiedFiles'] == 1
        assert resumed_parts == [2]
        self.record('multipart_pause_resume_reuses_remote_parts')
        completed = self.store.head(self.bucket, self.root + '/multipart-target/a')
        foreign_key = self.root + '/foreign-upload'
        foreign_upload = self.store.create_upload(self.bucket, foreign_key, None, {})
        cancelled = self.plan('multipart-source', 'cancel-target')
        def cancel_after_part(*args, **kwargs):
            result = original_part(*args, **kwargs)
            state['control'] = 'CANCEL'
            return result
        state['control'] = ''
        self.store.copy_part = cancel_after_part
        try:
            execute(cancelled, self.store, self.store, work / 'cancel', 'cancel', cancelled.digest,
                    part_size=5 * 1024 * 1024, control=lambda: state['control'])
            raise AssertionError('cancel not observed')
        except StopRequested:
            pass
        finally:
            self.store.copy_part = original_part
        owned_uploads = [(bucket, key, upload) for bucket, key, upload in self.uploads
                         if key == cancelled.entries[0].target_key]
        assert len(owned_uploads) == 1
        try:
            self.store.list_parts(*owned_uploads[0])
            raise AssertionError('cancelled multipart upload still exists')
        except SyncError as error:
            assert str(error) == 'MULTIPART_NOT_FOUND'
        assert self.store.head(self.bucket, self.root + '/cancel-target/a') is None
        assert self.store.head(self.bucket, self.root + '/multipart-target/a').fingerprint == completed.fingerprint
        assert self.store.list_parts(self.bucket, foreign_key, foreign_upload) == {}
        self.record('cancel_only_owned_partial_preserves_completed_and_foreign')
        raced = self.plan('multipart-source', 'multipart-race-target')
        complete = self.store.complete_upload
        def change_before_complete(bucket, key, upload, parts, expected):
            self.seed('multipart-race-target/a', b'concurrent-target')
            return complete(bucket, key, upload, parts, expected)
        self.store.complete_upload = change_before_complete
        try:
            execute(raced, self.store, self.store, work / 'multipart-race', 'multipart-race', raced.digest, part_size=5 * 1024 * 1024)
            raise AssertionError('multipart overwrite race unexpectedly succeeded')
        except SyncError as error:
            assert str(error) == 'OBJECT_CONDITION_FAILED'
        finally:
            self.store.complete_upload = complete
        with self.store.read(self.bucket, self.store.head(self.bucket, self.root + '/multipart-race-target/a')) as stream:
            assert stream.read() == b'concurrent-target'
        self.record('conditional_multipart_completion_race')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--config', required=True)
    parser.add_argument('--bucket', required=True)
    parser.add_argument('--prefix', required=True)
    parser.add_argument('--endpoint', default='')
    parser.add_argument('--region', default='')
    args = parser.parse_args()
    test = Acceptance(from_config(args.config, args.endpoint, args.region), args.bucket, args.prefix)
    status = 0
    try:
        test.run()
    except Exception as error:
        status = 1
        code = str(error) if isinstance(error, SyncError) else type(error).__name__
        test.results.append({'case': 'acceptance', 'passed': False, 'errorCode': code})
    finally:
        cleanup_errors = test.cleanup()
        print(json.dumps({'results': test.results, 'cleanupErrors': cleanup_errors}, ensure_ascii=False))
    return status or bool(cleanup_errors)


if __name__ == '__main__':
    raise SystemExit(main())
