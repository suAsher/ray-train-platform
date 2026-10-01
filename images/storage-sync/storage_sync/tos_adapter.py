"""Pinned TOS SDK adapter; no high-level resumable APIs with missing guards."""
import io
from dataclasses import replace
from pathlib import Path
from .model import ObjectInfo, SyncError, canonical_timestamp


class TOSStore:
    atomic_multipart_replace = False
    max_single_size = 5 * 1024 ** 3

    def __init__(self, client, bandwidth=0):
        self.client = client
        self.bandwidth = bandwidth
        if bandwidth and bandwidth < 102400:
            raise SyncError('INVALID_BANDWIDTH_LIMIT')
        self.copy_traffic_limit = min(bandwidth, 100 * 1024 * 1024) * 8 if bandwidth else None
        self.uncertain_write = False
        self.limiter = None
        if bandwidth:
            from tos.utils import RateLimiter
            self.limiter = RateLimiter(bandwidth, max(bandwidth, 8 * 1024 * 1024))

    def _call(self, method, *args, write=False, **kwargs):
        try:
            return getattr(self.client, method)(*args, **kwargs)
        except Exception as error:
            status = getattr(error, 'status_code', None)
            if write and (status is None or status >= 500):
                self.uncertain_write = True
                raise SyncError('UNKNOWN_WRITE_OUTCOME') from None
            if status == 412 or status == 409:
                raise SyncError('OBJECT_CONDITION_FAILED') from None
            if status == 404:
                raise SyncError('OBJECT_NOT_FOUND') from None
            raise SyncError('TOS_REQUEST_FAILED') from None

    @staticmethod
    def _info(key, output):
        if getattr(output, 'object_type', 'Normal') not in ('Normal', None):
            raise SyncError('UNSUPPORTED_OBJECT_TYPE')
        values = {field: getattr(output, field, '') or '' for field in
                  ('etag', 'version_id', 'content_type', 'content_encoding', 'content_disposition', 'content_language', 'cache_control')}
        # Custom user metadata is never accepted as proof of content integrity.
        crc = getattr(output, 'hash_crc64_ecma', None)
        modified = getattr(output, 'last_modified', '')
        return ObjectInfo(key=key, size=int(getattr(output, 'content_length', getattr(output, 'size', 0)) or 0),
                          crc64=str(crc) if crc is not None else '', last_modified=canonical_timestamp(modified), **values)

    def head(self, bucket, key):
        try:
            return self._info(key, self._call('head_object', bucket, key))
        except SyncError as error:
            if str(error) == 'OBJECT_NOT_FOUND':
                return None
            raise

    def list_page(self, bucket, prefix, token='', limit=1000):
        result = self._call('list_objects_type2', bucket, prefix=prefix, continuation_token=token or None,
                            max_keys=min(limit, 1000), list_only_once=True)
        # Listing CRC presence is not portable across the Go/Python SDKs.
        # Source scans always HEAD each object before trusting its checksum.
        entries = [replace(self._info(obj.key, obj), crc64='') for obj in result.contents]
        following = result.next_continuation_token if result.is_truncated else ''
        if result.is_truncated and not following:
            raise SyncError('LIST_PAGINATION_STALLED')
        return entries, following

    def read(self, bucket, obj):
        result = self._call('get_object', bucket, obj.key, if_match=obj.etag or None,
                            version_id=obj.version_id or None, rate_limiter=self.limiter)
        return SDKReadStream(result)

    @staticmethod
    def _guard(expected):
        if expected is not None and not expected.etag:
            raise SyncError('TARGET_GUARD_UNAVAILABLE')
        return {'if_match': expected.etag if expected else None, 'forbid_overwrite': expected is None}

    @staticmethod
    def _metadata(source):
        return {field: getattr(source, field) or None for field in
                ('content_type', 'content_encoding', 'content_disposition', 'content_language', 'cache_control')}

    def put(self, bucket, key, stream, expected, sha256=''):
        from tos.enum import ACLType
        self._call('put_object', bucket, key, content=stream, acl=ACLType.ACL_Private,
                   content_sha256=sha256 or None, rate_limiter=self.limiter,
                   **self._guard(expected), write=True)
        return self.head(bucket, key)

    def copy(self, source_bucket, source, bucket, key, expected):
        from tos.enum import ACLType, MetadataDirectiveType
        self._call('copy_object', bucket, key, source_bucket, source.key,
                   src_version_id=source.version_id or None, copy_source_if_match=source.etag or None,
                   acl=ACLType.ACL_Private, metadata_directive=MetadataDirectiveType.Metadata_Directive_Replace,
                   meta={}, traffic_limit=self.copy_traffic_limit,
                   **self._metadata(source), **self._guard(expected), write=True)
        return self.head(bucket, key)

    def create_upload(self, bucket, key, expected, metadata):
        from tos.enum import ACLType
        if expected is not None:
            raise SyncError('UNSUPPORTED_CONDITIONAL_MULTIPART_UPDATE')
        source = metadata.get('source')
        result = self._call('create_multipart_upload', bucket, key, acl=ACLType.ACL_Private,
                            forbid_overwrite=True, **(self._metadata(source) if source else {}), write=True)
        return result.upload_id

    def list_parts(self, bucket, key, upload_id):
        marker = None
        parts = {}
        while True:
            try:
                result = self._call('list_parts', bucket, key, upload_id, part_number_marker=marker, max_parts=1000)
            except SyncError as error:
                if str(error) == 'OBJECT_NOT_FOUND':
                    raise SyncError('MULTIPART_NOT_FOUND') from None
                raise
            parts = {**parts, **{part.part_number: {'etag': part.etag, 'size': part.size} for part in result.parts}}
            if not result.is_truncated:
                return parts
            if result.next_part_number_marker == marker:
                raise SyncError('LIST_PAGINATION_STALLED')
            marker = result.next_part_number_marker

    def upload_part(self, bucket, key, upload_id, number, stream, size):
        result = self._call('upload_part', bucket, key, upload_id, number, content=LimitedStream(stream, size),
                            content_length=size, rate_limiter=self.limiter, write=True)
        return {'etag': result.etag, 'size': size}

    def copy_part(self, source_bucket, source, bucket, key, upload_id, number, offset, size):
        result = self._call('upload_part_copy', bucket, key, upload_id, number, source_bucket, source.key,
                            src_version_id=source.version_id or None, copy_source_if_match=source.etag or None,
                            copy_source_range_start=offset, copy_source_range_end=offset + size - 1,
                            traffic_limit=self.copy_traffic_limit, write=True)
        return {'etag': result.etag, 'size': size}

    def complete_upload(self, bucket, key, upload_id, parts, expected):
        from tos.models2 import UploadedPart
        if expected is not None:
            raise SyncError('UNSUPPORTED_CONDITIONAL_MULTIPART_UPDATE')
        self._call('complete_multipart_upload', bucket, key, upload_id,
                   parts=[UploadedPart(part_number=number, etag=part['etag']) for number, part in sorted(parts.items())],
                   forbid_overwrite=True, write=True)
        return self.head(bucket, key)

    def abort_upload(self, bucket, key, upload_id):
        try:
            self._call('abort_multipart_upload', bucket, key, upload_id, write=True)
        except SyncError as error:
            if str(error) != 'OBJECT_NOT_FOUND':
                raise


class SDKReadStream:
    def __init__(self, result):
        self.result = result

    def read(self, size=-1):
        return self.result.read(None if size is None or size < 0 else size)

    def __enter__(self):
        return self

    def __exit__(self, *_):
        # SDK 2.9.3 has no GetObjectOutput.close; unwrap its CRC/rate adapters.
        stream = self.result.content
        while hasattr(stream, 'data'):
            stream = stream.data
        response = getattr(stream, 'resp', None)
        if response is not None:
            response.close()


class LimitedStream:
    def __init__(self, stream, size):
        self.stream, self.remaining = stream, size

    def read(self, size=-1):
        count = self.remaining if size is None or size < 0 else min(size, self.remaining)
        value = self.stream.read(count)
        self.remaining -= len(value)
        return value

    def __len__(self):
        return self.remaining


def from_config(path, endpoint='', region='', bandwidth=0):
    import tos
    values = {}
    for line in Path(path).read_text().splitlines():
        line = line.strip()
        if not line or line.startswith(('#', ';', '[')):
            continue
        name, separator, value = line.partition('=')
        if separator:
            values[name.strip().lower()] = value.strip()
    access = values.get('accesskeyid', '')
    secret = values.get('secretaccesskey', '')
    endpoint = endpoint or values.get('endpoint', '')
    region = region or values.get('region', '')
    if not all((access, secret, endpoint, region)):
        raise SyncError('TOS_CONFIGURATION_INCOMPLETE')
    client = tos.TosClientV2(access, secret, endpoint, region, security_token=values.get('securitytoken') or None,
                             max_retry_count=0, max_connections=4, enable_crc=True, connection_time=10, socket_timeout=60,
                             high_latency_log_threshold=0)
    return TOSStore(client, bandwidth)
