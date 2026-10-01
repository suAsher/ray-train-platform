"""Pin the exact write guards, without requiring network credentials."""
import io
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from storage_sync.model import ObjectInfo, SyncError
from storage_sync.tos_adapter import TOSStore, SDKReadStream, LimitedStream


class FakeClient:
    def __init__(self):
        self.calls = []
        self.failure = None

    def __getattr__(self, name):
        def call(*args, **kwargs):
            self.calls.append((name, args, kwargs))
            if self.failure:
                raise self.failure
            if name == 'head_object':
                return SimpleNamespace(content_length=3, etag='result', hash_crc64_ecma=123)
            return SimpleNamespace(upload_id='owned-upload', etag='part')
        return call


class GuardTests(unittest.TestCase):
    def setUp(self):
        enum = SimpleNamespace(ACLType=SimpleNamespace(ACL_Private='private'),
                               MetadataDirectiveType=SimpleNamespace(Metadata_Directive_Replace='REPLACE'))
        models = SimpleNamespace(UploadedPart=lambda **value: value)
        self.mock_import = patch.dict(sys.modules, {'tos.enum': enum, 'tos.models2': models})
        self.mock_import.start()
        self.addCleanup(self.mock_import.stop)
        self.client = FakeClient()
        self.store = TOSStore(self.client)
        self.source = ObjectInfo('source/key', 3, etag='source-etag', version_id='v1')

    def test_small_copy_guards_source_and_existing_target(self):
        self.store.copy('src', self.source, 'dst', 'key', ObjectInfo('key', 3, etag='old'))
        call = self.client.calls[0]
        self.assertEqual(call[0], 'copy_object')
        self.assertEqual(call[2]['copy_source_if_match'], 'source-etag')
        self.assertEqual(call[2]['src_version_id'], 'v1')
        self.assertEqual(call[2]['if_match'], 'old')
        self.assertFalse(call[2]['forbid_overwrite'])
        self.assertEqual(call[2]['acl'], 'private')
        self.assertEqual(call[2]['metadata_directive'], 'REPLACE')

    def test_new_put_forbids_overwrite_and_checks_content_hash(self):
        self.store.put('dst', 'key', io.BytesIO(b'abc'), None, 'content-sha')
        self.assertTrue(self.client.calls[0][2]['forbid_overwrite'])
        self.assertEqual(self.client.calls[0][2]['content_sha256'], 'content-sha')

    def test_each_part_source_guard_and_inclusive_range(self):
        self.store.copy_part('src', self.source, 'dst', 'key', 'upload', 2, 5, 3)
        params = self.client.calls[0][2]
        self.assertEqual(params['copy_source_if_match'], 'source-etag')
        self.assertEqual(params['src_version_id'], 'v1')
        self.assertEqual((params['copy_source_range_start'], params['copy_source_range_end']), (5, 7))

    def test_complete_new_object_forbids_overwrite(self):
        self.store.complete_upload('dst', 'key', 'upload', {1: {'etag': 'e', 'size': 3}}, None)
        self.assertTrue(self.client.calls[0][2]['forbid_overwrite'])
        self.assertEqual(self.client.calls[0][2]['parts'], [{'part_number': 1, 'etag': 'e'}])

    def test_complete_existing_object_fails_before_request(self):
        with self.assertRaisesRegex(SyncError, 'UNSUPPORTED_CONDITIONAL_MULTIPART_UPDATE'):
            self.store.complete_upload('dst', 'key', 'upload', {}, ObjectInfo('key', 3, etag='old'))
        self.assertEqual(self.client.calls, [])

    def test_unknown_write_outcome_never_claims_drained(self):
        self.client.failure = TimeoutError('must not leak sensitive URL')
        with self.assertRaisesRegex(SyncError, '^UNKNOWN_WRITE_OUTCOME$'):
            self.store.put('dst', 'key', io.BytesIO(b'abc'), None)
        self.assertTrue(self.store.uncertain_write)

    def test_412_is_known_rejection(self):
        error = RuntimeError('secret SDK details')
        error.status_code = 412
        self.client.failure = error
        with self.assertRaisesRegex(SyncError, '^OBJECT_CONDITION_FAILED$'):
            self.store.copy('src', self.source, 'dst', 'key', None)
        self.assertFalse(self.store.uncertain_write)

    def test_etag_and_custom_sha_metadata_never_become_checksum(self):
        value = self.store._info('key', SimpleNamespace(content_length=3, etag='opaque-5', meta={'sha256': 'untrusted'}))
        self.assertEqual(value.sha256, '')
        self.assertEqual(value.crc64, '')

    def test_limited_part_stream_does_not_read_next_part(self):
        stream = io.BytesIO(b'abcdef')
        part = LimitedStream(stream, 3)
        self.assertEqual(part.read(2), b'ab')
        self.assertEqual(part.read(10), b'c')
        self.assertEqual(part.read(), b'')
        self.assertEqual(stream.read(), b'def')

    def test_sdk_stream_closes_underlying_response(self):
        response = SimpleNamespace(closed=False)
        response.close = lambda: setattr(response, 'closed', True)
        result = SimpleNamespace(content=SimpleNamespace(data=SimpleNamespace(resp=response)), read=lambda amount: b'')
        with SDKReadStream(result):
            pass
        self.assertTrue(response.closed)


if __name__ == '__main__':
    unittest.main()
