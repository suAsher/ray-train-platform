"""Prefix-scoped TOS storage for immutable dataset publications."""

from __future__ import annotations

import hashlib
import importlib
import os
import re
import tempfile
import urllib.parse
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any, BinaryIO

from .irsa import VKEIRSAProvider, create_federation_credentials


MAX_INDEX_BYTES = 64 * 1024 * 1024
MAX_TRANSFER_BYTES = 2 * 1024 * 1024 * 1024
DEFAULT_STREAM_CHUNK_BYTES = 1024 * 1024
MAX_STREAM_CHUNK_BYTES = 16 * 1024 * 1024
MAX_OBJECT_KEY_BYTES = 4096
MAX_LIST_MARKER_BYTES = 4096
MAX_LIST_KEYS = 1000

_BUCKET_PATTERN = re.compile(r"^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$")
_REGION_PATTERN = re.compile(r"^[a-z0-9]+(?:-[a-z0-9]+)+$")
_ENDPOINT_LABEL_PATTERN = re.compile(
    r"^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$"
)
_PRIVATE_TOS_SERVICE_PATTERN = re.compile(r"^tos[0-9]*-private$")
_URI_SCHEME_PATTERN = re.compile(r"^[A-Za-z][A-Za-z0-9+.-]*:")
_SHA256_PATTERN = re.compile(r"^[0-9a-f]{64}$")


class TOSStorageError(RuntimeError):
    """A sanitized object-storage failure."""


@dataclass(frozen=True)
class TOSObjectInfo:
    size: int
    sha256: str | None


@dataclass(frozen=True)
class TOSListedObject:
    key: str
    size: int
    etag: str


@dataclass(frozen=True)
class TOSListPage:
    objects: tuple[TOSListedObject, ...]
    next_marker: str | None


class TOSStorage:
    """Expose only the TOS capabilities required by the dataset publisher.

    All public object keys are relative.  Source operations always join them to
    ``source_prefix`` and immutable operations always join them to
    ``internal_dataset_prefix``.
    """

    def __init__(
        self,
        *,
        source_bucket: str,
        target_bucket: str,
        endpoint: str,
        region: str,
        source_prefix: str,
        internal_dataset_prefix: str,
        irsa_provider: VKEIRSAProvider | None = None,
        credentials_provider: Any = None,
        tos_sdk: Any = None,
        client: Any = None,
    ) -> None:
        self._source_bucket = _normalize_bucket(source_bucket)
        self._target_bucket = _normalize_bucket(target_bucket)
        self._region = _normalize_region(region)
        self._endpoint = _normalize_endpoint(endpoint, region=self._region)
        self._source_prefix = _normalize_prefix(source_prefix)
        self._internal_dataset_prefix = _normalize_prefix(
            internal_dataset_prefix
        )
        if (
            self._source_bucket == self._target_bucket
            and _prefixes_overlap(
                self._source_prefix, self._internal_dataset_prefix
            )
        ):
            raise ValueError("source and internal dataset prefixes must not overlap")

        if client is not None:
            self._client = client
            self._credentials_provider = credentials_provider
            return

        sdk = tos_sdk
        if sdk is None:
            import_failed = False
            try:
                sdk = importlib.import_module("tos")
            except Exception:
                import_failed = True
            if import_failed:
                raise TOSStorageError("TOS SDK is unavailable")

        effective_credentials = credentials_provider
        if effective_credentials is None:
            effective_irsa = irsa_provider or VKEIRSAProvider()
            credential_module = getattr(sdk, "credential", None)
            if credential_module is None:
                raise TOSStorageError("TOS federation credentials are unavailable")
            provider_failed = False
            try:
                effective_credentials = create_federation_credentials(
                    effective_irsa, credential_module
                )
            except Exception:
                provider_failed = True
            if provider_failed:
                raise TOSStorageError("TOS federation credentials are unavailable")

        client_failed = False
        sdk_client = None
        try:
            sdk_client = sdk.TosClientV2(
                endpoint=self._endpoint,
                region=self._region,
                credentials_provider=effective_credentials,
            )
        except Exception:
            client_failed = True
        if client_failed or sdk_client is None:
            raise TOSStorageError("TOS client initialization failed")
        self._credentials_provider = effective_credentials
        self._client = sdk_client

    @property
    def source_bucket(self) -> str:
        return self._source_bucket

    @property
    def target_bucket(self) -> str:
        return self._target_bucket

    @property
    def endpoint(self) -> str:
        return self._endpoint

    @property
    def region(self) -> str:
        return self._region

    @property
    def source_prefix(self) -> str:
        return self._source_prefix

    @property
    def internal_dataset_prefix(self) -> str:
        return self._internal_dataset_prefix

    def head_source(self, key: str) -> TOSObjectInfo:
        """HEAD one object below the configured source prefix."""

        full_key = self._source_key(key)
        return self._head(
            self._source_bucket,
            full_key,
            failure_message="TOS source HEAD failed",
        )

    def get_index(
        self,
        key: str,
        *,
        maximum_bytes: int,
    ) -> bytes:
        """Read a bounded source index, never exceeding 64 MiB."""

        _validate_positive_bound(
            "maximum_bytes", maximum_bytes, maximum=MAX_INDEX_BYTES
        )
        full_key = self._source_key(key)
        info = self._head(
            self._source_bucket,
            full_key,
            failure_message="TOS source HEAD failed",
        )
        if info.size > maximum_bytes:
            raise TOSStorageError("TOS source index exceeds the configured bound")
        output = self._get_source_output(full_key)
        payload, read_failed = _read_bounded_output(output, maximum_bytes)
        if read_failed:
            raise TOSStorageError("TOS source index download failed")
        if len(payload) != info.size:
            raise TOSStorageError("TOS source index size verification failed")
        return payload

    def download_stream(
        self,
        key: str,
        destination: BinaryIO,
        *,
        maximum_bytes: int,
        chunk_size: int = DEFAULT_STREAM_CHUNK_BYTES,
    ) -> int:
        """Stream one source object to a caller-owned binary destination."""

        full_key = self._source_key(key)
        byte_budget = _validate_positive_bound(
            "maximum_bytes", maximum_bytes, maximum=MAX_TRANSFER_BYTES
        )
        _validate_positive_bound(
            "chunk_size", chunk_size, maximum=MAX_STREAM_CHUNK_BYTES
        )
        write = getattr(destination, "write", None)
        if not callable(write):
            raise ValueError("destination must be a writable binary stream")
        checkpoint = _destination_checkpoint(destination)
        if checkpoint is None:
            raise ValueError("destination must support download rollback")
        info = self._head(
            self._source_bucket,
            full_key,
            failure_message="TOS source HEAD failed",
        )
        if info.size > byte_budget:
            raise TOSStorageError("TOS source stream exceeds the configured bound")
        output = self._get_source_output(full_key)
        total, stream_failed = _copy_download_output(
            output,
            destination,
            chunk_size,
            byte_budget,
        )
        if stream_failed or total != info.size:
            _rollback_destination(destination, checkpoint)
            raise TOSStorageError("TOS source stream download failed")
        return total

    def download_file(
        self,
        key: str,
        destination: str | os.PathLike[str],
        *,
        maximum_bytes: int,
    ) -> TOSObjectInfo:
        """Download one source object to a temporary file, then atomically replace."""

        full_key = self._source_key(key)
        byte_budget = _validate_positive_bound(
            "maximum_bytes", maximum_bytes, maximum=MAX_TRANSFER_BYTES
        )
        try:
            destination_path = os.fspath(destination)
        except TypeError:
            raise ValueError("destination path is invalid")
        if (
            not isinstance(destination_path, str)
            or not destination_path
            or "\x00" in destination_path
        ):
            raise ValueError("destination path is invalid")
        info = self._head(
            self._source_bucket,
            full_key,
            failure_message="TOS source HEAD failed",
        )
        if info.size > byte_budget:
            raise TOSStorageError("TOS source file exceeds the configured bound")

        temporary_path = None
        try:
            destination_directory = os.path.dirname(os.path.abspath(destination_path))
            with tempfile.NamedTemporaryFile(
                mode="wb",
                prefix=".raytrain-publisher-",
                dir=destination_directory,
                delete=False,
            ) as temporary:
                temporary_path = temporary.name
                output = self._get_source_output(full_key)
                total, stream_failed = _copy_download_output(
                    output,
                    temporary,
                    DEFAULT_STREAM_CHUNK_BYTES,
                    byte_budget,
                )
            if stream_failed or total != info.size:
                raise TOSStorageError("TOS source file download failed")
            os.replace(temporary_path, destination_path)
            temporary_path = None
        except Exception:
            raise TOSStorageError("TOS source file download failed") from None
        finally:
            _remove_temporary_file(temporary_path)
        return info

    def list_source(
        self,
        relative_prefix: str = "",
        *,
        marker: str | None = None,
        max_keys: int = MAX_LIST_KEYS,
    ) -> TOSListPage:
        """List only objects below the configured source prefix."""

        requested_prefix = self._source_list_prefix(relative_prefix)
        _validate_list_options(marker=marker, max_keys=max_keys)
        request = {"prefix": requested_prefix, "max_keys": max_keys}
        if marker is not None:
            request["marker"] = marker
        output = _safe_client_call(
            "TOS source list failed",
            self._client.list_objects,
            self._source_bucket,
            **request,
        )
        page, parse_failed = self._parse_list_page(
            output,
            requested_prefix=requested_prefix,
            marker=marker,
            max_keys=max_keys,
        )
        if parse_failed or page is None:
            raise TOSStorageError("TOS source list returned invalid data")
        return page

    def put_immutable(
        self,
        key: str,
        content: bytes | bytearray | memoryview | BinaryIO,
        *,
        sha256: str,
        maximum_bytes: int,
        size: int | None = None,
        content_type: str = "application/octet-stream",
    ) -> Any:
        """Write one immutable object below the internal dataset prefix."""

        full_key = self._target_key(key)
        digest = _validate_sha256(sha256)
        byte_budget = _validate_positive_bound(
            "maximum_bytes", maximum_bytes, maximum=MAX_TRANSFER_BYTES
        )
        if (
            not isinstance(content_type, str)
            or not content_type
            or "\r" in content_type
            or "\n" in content_type
        ):
            raise ValueError("content type is invalid")
        prepared_content, content_size, actual_digest = _inspect_upload_content(
            content,
            size=size,
            maximum_bytes=byte_budget,
        )
        if actual_digest != digest:
            raise ValueError("content does not match the declared SHA-256")
        return _safe_client_call(
            "TOS immutable put failed",
            self._client.put_object,
            self._target_bucket,
            full_key,
            content=prepared_content,
            content_length=content_size,
            content_sha256=digest,
            content_type=content_type,
            meta={"sha256": digest},
            forbid_overwrite=True,
        )

    def verify_immutable(
        self,
        key: str,
        *,
        expected_size: int,
        expected_sha256: str,
    ) -> TOSObjectInfo:
        """Verify exact size and SHA-256 metadata for one internal object."""

        full_key = self._target_key(key)
        _validate_nonnegative_size(expected_size)
        digest = _validate_sha256(expected_sha256)
        info = self._head(
            self._target_bucket,
            full_key,
            failure_message="TOS immutable HEAD failed",
        )
        if info.size != expected_size or info.sha256 != digest:
            raise TOSStorageError("TOS immutable object verification failed")
        return info

    def _head(
        self,
        bucket: str,
        full_key: str,
        *,
        failure_message: str,
    ) -> TOSObjectInfo:
        output = _safe_client_call(
            failure_message,
            self._client.head_object,
            bucket,
            full_key,
        )
        return _object_info_from_head(output, failure_message=failure_message)

    def _get_source_output(self, full_key: str) -> Any:
        output = _safe_client_call(
            "TOS source download failed",
            self._client.get_object,
            self._source_bucket,
            full_key,
        )
        if not callable(getattr(output, "read", None)):
            _close_download_output(output)
            raise TOSStorageError("TOS source download returned invalid data")
        return output

    def _parse_list_page(
        self,
        output: Any,
        *,
        requested_prefix: str,
        marker: str | None,
        max_keys: int,
    ) -> tuple[TOSListPage | None, bool]:
        try:
            raw_objects = getattr(output, "contents")
            listed_objects = []
            for raw_object in raw_objects:
                if len(listed_objects) >= max_keys:
                    return None, True
                full_key = raw_object.key
                if (
                    not isinstance(full_key, str)
                    or not full_key.startswith(requested_prefix)
                    or not full_key.startswith(self._source_prefix + "/")
                ):
                    return None, True
                relative_key = full_key[len(self._source_prefix) + 1 :]
                normalized_key = _normalize_relative_key(relative_key)
                size = raw_object.size
                etag = raw_object.etag
                if (
                    isinstance(size, bool)
                    or not isinstance(size, int)
                    or size < 0
                    or not isinstance(etag, str)
                    or not etag
                ):
                    return None, True
                listed_objects.append(
                    TOSListedObject(key=normalized_key, size=size, etag=etag)
                )
            next_marker = getattr(output, "next_marker", None)
            if next_marker == "":
                next_marker = None
            if next_marker is not None and not _is_valid_marker(next_marker):
                return None, True
            if next_marker is not None and next_marker == marker:
                return None, True
        except Exception:
            return None, True
        return TOSListPage(tuple(listed_objects), next_marker), False

    def _source_key(self, relative_key: str) -> str:
        return self._source_prefix + "/" + _normalize_relative_key(relative_key)

    def _target_key(self, relative_key: str) -> str:
        return self._internal_dataset_prefix + "/" + _normalize_relative_key(
            relative_key
        )

    def _source_list_prefix(self, relative_prefix: str) -> str:
        if relative_prefix == "":
            return self._source_prefix + "/"
        normalized = _normalize_prefix(relative_prefix)
        return self._source_prefix + "/" + normalized + "/"


def _normalize_bucket(value: object) -> str:
    if (
        not isinstance(value, str)
        or not _BUCKET_PATTERN.fullmatch(value)
        or value.strip() != value
    ):
        raise ValueError("TOS bucket is invalid")
    return value


def _normalize_endpoint(value: object, *, region: str) -> str:
    if (
        not isinstance(value, str)
        or not value
        or value.strip() != value
        or "%" in value
        or "\\" in value
        or any(ord(character) < 32 or ord(character) == 127 for character in value)
    ):
        raise ValueError("TOS endpoint is invalid")
    candidate = value
    if "://" not in candidate:
        candidate = "https://" + candidate
    try:
        parsed = urllib.parse.urlsplit(candidate)
        port = parsed.port
    except ValueError:
        raise ValueError("TOS endpoint is invalid")
    hostname = parsed.hostname
    canonical_hostname = (
        _canonical_tos_endpoint_hostname(hostname, region=region)
        if hostname
        else None
    )
    if (
        parsed.scheme != "https"
        or not hostname
        or hostname != hostname.lower()
        or canonical_hostname is None
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or parsed.path not in {"", "/"}
    ):
        raise ValueError("TOS endpoint is invalid")
    if port is not None:
        raise ValueError("TOS endpoint is invalid")
    return "https://" + canonical_hostname


def _canonical_tos_endpoint_hostname(value: str, *, region: str) -> str | None:
    if len(value) > 253 or not all(
        _ENDPOINT_LABEL_PATTERN.fullmatch(label) for label in value.split(".")
    ):
        return None
    for domain in ("ivolces.com", "volces.com"):
        native_service = f"tos-{region}.{domain}"
        for service in (native_service, f"tos-s3-{region}.{domain}"):
            if value == service:
                return native_service
            suffix = "." + service
            if value.endswith(suffix):
                bucket = value[: -len(suffix)]
                if _BUCKET_PATTERN.fullmatch(bucket):
                    return native_service
    private_suffix = f".{region}.tos.ivolces.com"
    if value.endswith(private_suffix):
        service = value[: -len(private_suffix)]
        if _PRIVATE_TOS_SERVICE_PATTERN.fullmatch(service):
            return value
    return None


def _normalize_region(value: object) -> str:
    if (
        not isinstance(value, str)
        or value.strip() != value
        or not _REGION_PATTERN.fullmatch(value)
    ):
        raise ValueError("TOS region is invalid")
    return value


def _normalize_prefix(value: object) -> str:
    if not isinstance(value, str) or not value or value.strip() != value:
        raise ValueError("TOS prefix is invalid")
    candidate = value[:-1] if value.endswith("/") else value
    return _validate_path(candidate, field_name="TOS prefix")


def _normalize_relative_key(value: object) -> str:
    if not isinstance(value, str) or not value:
        raise ValueError("TOS object key is invalid")
    return _validate_path(value, field_name="TOS object key")


def _validate_path(value: str, *, field_name: str) -> str:
    encoded_length = len(value.encode("utf-8"))
    if (
        not value
        or encoded_length > MAX_OBJECT_KEY_BYTES
        or value.strip() != value
        or value.startswith("/")
        or value.endswith("/")
        or "\\" in value
        or "%" in value
        or "://" in value
        or _URI_SCHEME_PATTERN.match(value)
        or any(ord(character) < 32 or ord(character) == 127 for character in value)
    ):
        raise ValueError(f"{field_name} is invalid")
    segments = value.split("/")
    if any(segment in {"", ".", ".."} for segment in segments):
        raise ValueError(f"{field_name} is invalid")
    return value


def _prefixes_overlap(left: str, right: str) -> bool:
    return (
        left == right
        or left.startswith(right + "/")
        or right.startswith(left + "/")
    )


def _validate_positive_bound(field_name: str, value: object, *, maximum: int) -> int:
    if (
        isinstance(value, bool)
        or not isinstance(value, int)
        or value <= 0
        or value > maximum
    ):
        raise ValueError(f"{field_name} is outside its allowed bound")
    return value


def _validate_nonnegative_size(value: object) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise ValueError("object size is invalid")
    return value


def _validate_sha256(value: object) -> str:
    if not isinstance(value, str) or not _SHA256_PATTERN.fullmatch(value):
        raise ValueError("SHA-256 digest is invalid")
    return value


def _validate_list_options(*, marker: object, max_keys: object) -> None:
    if marker is not None and not _is_valid_marker(marker):
        raise ValueError("TOS list marker is invalid")
    _validate_positive_bound("max_keys", max_keys, maximum=MAX_LIST_KEYS)


def _is_valid_marker(value: object) -> bool:
    return (
        isinstance(value, str)
        and 0 < len(value.encode("utf-8")) <= MAX_LIST_MARKER_BYTES
        and "\x00" not in value
        and all(ord(character) >= 32 and ord(character) != 127 for character in value)
    )


def _safe_client_call(message: str, function: Any, *args: Any, **kwargs: Any) -> Any:
    failed = False
    result = None
    try:
        result = function(*args, **kwargs)
    except Exception:
        failed = True
    if failed:
        raise TOSStorageError(message)
    return result


def _object_info_from_head(output: Any, *, failure_message: str) -> TOSObjectInfo:
    parse_failed = False
    size = None
    digest = None
    try:
        size = output.content_length
        metadata = output.meta
        if isinstance(size, bool) or not isinstance(size, int) or size < 0:
            raise TypeError
        if metadata is None:
            metadata = {}
        if not isinstance(metadata, Mapping):
            raise TypeError
        digest = metadata.get("sha256")
        if digest is not None and (
            not isinstance(digest, str) or not _SHA256_PATTERN.fullmatch(digest)
        ):
            raise TypeError
    except Exception:
        parse_failed = True
    if parse_failed:
        raise TOSStorageError(failure_message)
    return TOSObjectInfo(size=size, sha256=digest)


def _read_bounded_output(output: Any, maximum_bytes: int) -> tuple[bytes, bool]:
    payload = bytearray()
    failed = False
    try:
        declared_size = getattr(output, "content_length", None)
        if declared_size is not None and (
            isinstance(declared_size, bool)
            or not isinstance(declared_size, int)
            or declared_size < 0
            or declared_size > maximum_bytes
        ):
            raise ValueError
        while len(payload) <= maximum_bytes:
            chunk = output.read(min(DEFAULT_STREAM_CHUNK_BYTES, maximum_bytes + 1 - len(payload)))
            if not chunk:
                break
            if not isinstance(chunk, bytes):
                raise TypeError
            payload.extend(chunk)
            if len(payload) > maximum_bytes:
                raise ValueError
    except Exception:
        failed = True
    finally:
        _close_download_output(output)
    return bytes(payload), failed


def _copy_download_output(
    output: Any,
    destination: BinaryIO,
    chunk_size: int,
    maximum_bytes: int,
) -> tuple[int, bool]:
    total = 0
    failed = False
    try:
        declared_size = getattr(output, "content_length", None)
        if declared_size is not None and (
            isinstance(declared_size, bool)
            or not isinstance(declared_size, int)
            or declared_size < 0
            or declared_size > maximum_bytes
        ):
            raise ValueError
        while True:
            read_size = min(chunk_size, maximum_bytes + 1 - total)
            chunk = output.read(read_size)
            if not chunk:
                break
            if not isinstance(chunk, bytes):
                raise TypeError
            if len(chunk) > maximum_bytes - total:
                raise ValueError
            written = destination.write(chunk)
            if written is not None and written != len(chunk):
                raise OSError
            total += len(chunk)
        if declared_size is not None and total != declared_size:
            raise ValueError
    except Exception:
        failed = True
    finally:
        _close_download_output(output)
    return total, failed


def _destination_checkpoint(destination: BinaryIO) -> int | None:
    tell = getattr(destination, "tell", None)
    seek = getattr(destination, "seek", None)
    truncate = getattr(destination, "truncate", None)
    if not callable(tell) or not callable(seek) or not callable(truncate):
        return None
    try:
        position = tell()
        seek(0, os.SEEK_END)
        end_position = tell()
        seek(position)
    except Exception:
        return None
    if (
        isinstance(position, bool)
        or not isinstance(position, int)
        or position < 0
        or isinstance(end_position, bool)
        or not isinstance(end_position, int)
        or end_position != position
    ):
        return None
    return position


def _rollback_destination(destination: BinaryIO, checkpoint: int | None) -> None:
    if checkpoint is None:
        return
    try:
        destination.seek(checkpoint)
        destination.truncate(checkpoint)
    except Exception:
        pass


def _remove_temporary_file(path: str | None) -> None:
    if path is None:
        return
    try:
        os.unlink(path)
    except OSError:
        pass


def _close_download_output(output: Any) -> None:
    candidates = (
        output,
        getattr(output, "content", None),
        getattr(output, "resp", None),
    )
    closed_ids = set()
    for candidate in candidates:
        if candidate is None or id(candidate) in closed_ids:
            continue
        closed_ids.add(id(candidate))
        close = getattr(candidate, "close", None)
        if callable(close):
            try:
                close()
            except Exception:
                pass


def _inspect_upload_content(
    content: bytes | bytearray | memoryview | BinaryIO,
    *,
    size: int | None,
    maximum_bytes: int,
) -> tuple[Any, int, str]:
    if size is not None:
        _validate_nonnegative_size(size)
        if size > maximum_bytes:
            raise TOSStorageError(
                "TOS immutable upload exceeds the configured bound"
            )
    if isinstance(content, (bytes, bytearray, memoryview)):
        actual_size = content.nbytes if isinstance(content, memoryview) else len(content)
        if actual_size > maximum_bytes:
            raise TOSStorageError(
                "TOS immutable upload exceeds the configured bound"
            )
        prepared = bytes(content)
        if size is not None and size != actual_size:
            raise ValueError("content size does not match the declared size")
        return prepared, actual_size, hashlib.sha256(prepared).hexdigest()

    read = getattr(content, "read", None)
    tell = getattr(content, "tell", None)
    seek = getattr(content, "seek", None)
    if not callable(read) or not callable(tell) or not callable(seek):
        raise ValueError("upload content must be bytes or a seekable binary stream")
    inspection_failed = False
    total = 0
    digest = hashlib.sha256()
    original_position = None
    over_budget = False
    try:
        original_position = tell()
        while True:
            read_size = min(
                DEFAULT_STREAM_CHUNK_BYTES,
                maximum_bytes + 1 - total,
            )
            chunk = read(read_size)
            if not chunk:
                break
            if not isinstance(chunk, bytes):
                raise TypeError
            if len(chunk) > maximum_bytes - total:
                over_budget = True
                break
            total += len(chunk)
            digest.update(chunk)
        seek(original_position)
    except Exception:
        inspection_failed = True
    if inspection_failed or original_position is None:
        raise ValueError("upload content could not be validated")
    if over_budget:
        raise TOSStorageError("TOS immutable upload exceeds the configured bound")
    if size is not None and size != total:
        raise ValueError("content size does not match the declared size")
    return content, total, digest.hexdigest()


TosStorage = TOSStorage
TOSStorageAdapter = TOSStorage

__all__ = [
    "MAX_INDEX_BYTES",
    "MAX_TRANSFER_BYTES",
    "TOSListPage",
    "TOSListedObject",
    "TOSObjectInfo",
    "TOSStorage",
    "TOSStorageAdapter",
    "TOSStorageError",
    "TosStorage",
]
