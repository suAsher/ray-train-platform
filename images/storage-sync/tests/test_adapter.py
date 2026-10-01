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

import tempfile
from pathlib import Path
from storage_sync.tos_adapter import from_config


class AdapterPaginationTests(unittest.TestCase):
    def test_list_requests_exactly_one_page(self):
        client = FakeClient()
        client.list_objects_type2 = lambda *args, **kwargs: SimpleNamespace(contents=[SimpleNamespace(key='x/a', size=3, etag='e', hash_crc64_ecma=123)],
                                                                          is_truncated=True, next_continuation_token='next')
        values, cursor = TOSStore(client).list_page('bucket', 'x/')
        self.assertEqual(values[0].key, 'x/a')
        self.assertEqual(values[0].crc64, '')
        self.assertEqual(cursor, 'next')

    def test_parts_paginate_and_missing_upload_is_identified(self):
        calls = []
        def listed(*args, **kwargs):
            calls.append(kwargs['part_number_marker'])
            number = 1 if kwargs['part_number_marker'] is None else 2
            return SimpleNamespace(parts=[SimpleNamespace(part_number=number, etag=f'e{number}', size=10)],
                                   is_truncated=number == 1, next_part_number_marker=1)
        client = FakeClient()
        client.list_parts = listed
        self.assertEqual(len(TOSStore(client).list_parts('b', 'k', 'u')), 2)
        self.assertEqual(calls, [None, 1])
        error = RuntimeError('private SDK details'); error.status_code = 404
        client = FakeClient(); client.failure = error
        with self.assertRaisesRegex(SyncError, 'MULTIPART_NOT_FOUND'):
            TOSStore(client).list_parts('b', 'k', 'u')
        self.assertIsNone(TOSStore(client).head('b', 'missing'))

    def test_tosutil_configuration_uses_zero_automatic_retries(self):
        calls = []
        fake_tos = SimpleNamespace(TosClientV2=lambda *args, **kwargs: calls.append((args, kwargs)) or FakeClient())
        with tempfile.TemporaryDirectory() as directory, patch.dict(sys.modules, {'tos': fake_tos}):
            config = Path(directory) / 'config'
            config.write_text('accessKeyID=test-key\nsecretAccessKey=test-secret\nendpoint=https://tos.example\nregion=test-region\n')
            from_config(config)
        self.assertEqual(calls[0][1]['max_retry_count'], 0)
        self.assertTrue(calls[0][1]['enable_crc'])
        self.assertEqual(calls[0][1]['high_latency_log_threshold'], 0)

    def test_incomplete_configuration_has_sanitized_error(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(sys.modules, {'tos': SimpleNamespace()}):
            config = Path(directory) / 'config'; config.write_text('accessKeyID=private-example\n')
            with self.assertRaisesRegex(SyncError, '^TOS_CONFIGURATION_INCOMPLETE$'):
                from_config(config)

    def test_current_tosutil_short_keys_and_optional_token(self):
        for token in ('', 'temporary-session-token'):
            with self.subTest(has_token=bool(token)):
                calls = []
                fake_tos = SimpleNamespace(TosClientV2=lambda *args, **kwargs: calls.append((args, kwargs)) or FakeClient())
                with tempfile.TemporaryDirectory() as directory, patch.dict(sys.modules, {'tos': fake_tos}):
                    config = Path(directory) / 'config'
                    config.write_text('[default]\nak=example-ak\nsk=example-sk\n'
                                      'endpoint=https://tos.example\nregion=test-region\ntoken=' + token + '\n')
                    from_config(config)
                self.assertEqual(calls[0][0], ('example-ak', 'example-sk', 'https://tos.example', 'test-region'))
                self.assertEqual(calls[0][1]['security_token'], token or None)
                self.assertEqual(calls[0][1]['max_retry_count'], 0)

    def test_long_credential_keys_preserve_precedence_and_endpoint_override(self):
        calls = []
        fake_tos = SimpleNamespace(TosClientV2=lambda *args, **kwargs: calls.append((args, kwargs)) or FakeClient())
        with tempfile.TemporaryDirectory() as directory, patch.dict(sys.modules, {'tos': fake_tos}):
            config = Path(directory) / 'config'
            config.write_text('accessKeyID=long-ak\nsecretAccessKey=long-sk\nsecurityToken=long-token\n'
                              'ak=short-ak\nsk=short-sk\ntoken=short-token\n'
                              'endpoint=https://old.example\nregion=old-region\n')
            from_config(config, 'https://override.example', 'override-region')
        self.assertEqual(calls[0][0], ('long-ak', 'long-sk', 'https://override.example', 'override-region'))
        self.assertEqual(calls[0][1]['security_token'], 'long-token')

from datetime import datetime, timezone
from storage_sync.transport import object_from_wire


class CrossAdapterIdentityTests(unittest.TestCase):
    def test_gateway_and_sdk_metadata_share_canonical_identity(self):
        sdk = TOSStore._info('key', SimpleNamespace(content_length=3, etag='opaque-5', version_id='v1',
                             hash_crc64_ecma=123, last_modified=datetime(2026, 10, 1, tzinfo=timezone.utc),
                             content_type='application/octet-stream', content_encoding='gzip', content_disposition='attachment; filename=a+b%20.json',
                             content_language='zh', cache_control='no-cache'))
        wire = object_from_wire({'key': 'key', 'size': 3, 'etag': 'opaque-5', 'versionId': 'v1', 'crc64': '123',
                                'lastModified': '2026-10-01T00:00:00Z', 'contentType': 'application/octet-stream',
                                'contentEncoding': 'gzip', 'contentDisposition': 'attachment; filename=a+b%20.json', 'contentLanguage': 'zh',
                                'cacheControl': 'no-cache'})
        self.assertEqual(sdk.fingerprint, wire.fingerprint)
        self.assertEqual(sdk.last_modified, '2026-10-01T00:00:00.000Z')


class AdditionalGuardTests(unittest.TestCase):
    setUp = GuardTests.setUp

    def test_source_read_uses_both_version_and_etag(self):
        self.store.read('src', self.source)
        self.assertEqual(self.client.calls[0][0], 'get_object')
        self.assertEqual(self.client.calls[0][2]['if_match'], 'source-etag')
        self.assertEqual(self.client.calls[0][2]['version_id'], 'v1')

    def test_create_upload_refuses_existing_target_and_sets_private_policy(self):
        with self.assertRaisesRegex(SyncError, 'UNSUPPORTED_CONDITIONAL_MULTIPART_UPDATE'):
            self.store.create_upload('b', 'key', ObjectInfo('key', 3, etag='old'), {})
        self.assertEqual(self.client.calls, [])
        self.store.create_upload('b', 'key', None, {'source': self.source})
        self.assertTrue(self.client.calls[0][2]['forbid_overwrite'])
        self.assertEqual(self.client.calls[0][2]['acl'], 'private')

    def test_missing_destination_etag_fails_before_write(self):
        with self.assertRaisesRegex(SyncError, 'TARGET_GUARD_UNAVAILABLE'):
            self.store.put('b', 'key', io.BytesIO(b'abc'), ObjectInfo('key', 3))
        self.assertEqual(self.client.calls, [])

    def test_abort_owned_upload_is_idempotent(self):
        self.store.abort_upload('b', 'key', 'own-upload')
        self.assertEqual(self.client.calls[0][0], 'abort_multipart_upload')
        error = RuntimeError('private'); error.status_code = 404
        self.client.failure = error
        self.store.abort_upload('b', 'key', 'own-upload')

    def test_symlink_objects_and_invalid_bandwidth_fail_closed(self):
        with self.assertRaisesRegex(SyncError, 'UNSUPPORTED_OBJECT_TYPE'):
            self.store._info('key', SimpleNamespace(object_type='Symlink'))
        with self.assertRaisesRegex(SyncError, 'INVALID_BANDWIDTH_LIMIT'):
            TOSStore(self.client, bandwidth=1)
