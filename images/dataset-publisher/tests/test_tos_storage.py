"""Tests for the prefix-scoped dataset publisher TOS adapter."""

from __future__ import annotations

import hashlib
import importlib.util
import inspect
import io
import sys
import tempfile
import unittest
import warnings
from importlib import metadata
from pathlib import Path
from types import SimpleNamespace


PUBLISHER_ROOT = Path(__file__).resolve().parents[1]
if str(PUBLISHER_ROOT) not in sys.path:
    sys.path.insert(0, str(PUBLISHER_ROOT))

from raytrain_publisher.irsa import (  # noqa: E402
    TemporaryCredentials,
    create_federation_credentials,
)
from raytrain_publisher import tos_storage as tos_storage_module  # noqa: E402
from raytrain_publisher.tos_storage import (  # noqa: E402
    MAX_INDEX_BYTES,
    TOSStorage,
    TOSStorageError,
)


SOURCE_BUCKET = "publisher-source"
TARGET_BUCKET = "publisher-target"
ENDPOINT = "https://tos-cn-shanghai.ivolces.com"
REGION = "cn-shanghai"
SOURCE_PREFIX = "ray-train/public/labeled"
INTERNAL_PREFIX = "ray-train/platform/datasets/dataset-a"
PAYLOAD = b"immutable-payload"
DIGEST = hashlib.sha256(PAYLOAD).hexdigest()
PINNED_TOS_SDK_VERSION = "2.9.2"
MIB = 1024 * 1024
EXPECTED_MAX_TRANSFER_BYTES = 2 * 1024 * MIB


class _StreamingOutput:
    def __init__(self, payload: bytes, *, content_length: int | None = None) -> None:
        self._stream = io.BytesIO(payload)
        self.content_length = len(payload) if content_length is None else content_length
        self.meta: dict[str, str] = {}
        self.closed = False

    def read(self, amount: int | None = None) -> bytes:
        return self._stream.read(amount)

    def close(self) -> None:
        self.closed = True
        self._stream.close()


class _WriteOnlyDestination:
    def __init__(self) -> None:
        self.payload = bytearray()

    def write(self, chunk: bytes) -> int:
        self.payload.extend(chunk)
        return len(chunk)


class _FakeTOSClient:
    def __init__(self) -> None:
        self.calls: list[tuple[str, tuple[object, ...], dict[str, object]]] = []
        self.head_results: dict[tuple[str, str], object] = {}
        self.get_results: dict[tuple[str, str], _StreamingOutput] = {}
        self.list_result: object = SimpleNamespace(contents=[], next_marker=None)
        self.failure: Exception | None = None

    def _record(self, operation: str, *args: object, **kwargs: object) -> None:
        self.calls.append((operation, args, kwargs))
        if self.failure is not None:
            raise self.failure

    def head_object(self, bucket: str, key: str) -> object:
        self._record("head_object", bucket, key)
        return self.head_results[(bucket, key)]

    def get_object(self, bucket: str, key: str) -> _StreamingOutput:
        self._record("get_object", bucket, key)
        return self.get_results[(bucket, key)]

    def download_file(self, bucket: str, key: str, file_path: str) -> object:
        self._record("download_file", bucket, key, file_path)
        return self.head_results[(bucket, key)]

    def put_object(self, bucket: str, key: str, **kwargs: object) -> object:
        self._record("put_object", bucket, key, **kwargs)
        return SimpleNamespace(status_code=200)

    def list_objects(self, bucket: str, **kwargs: object) -> object:
        self._record("list_objects", bucket, **kwargs)
        return self.list_result


class _FakeTOSServiceError(RuntimeError):
    def __init__(self, status_code: int, message: str = "sensitive provider detail") -> None:
        super().__init__(message)
        self.status_code = status_code


class _FakeFederationToken:
    def __init__(
        self,
        access_key_id: str,
        access_key_secret: str,
        security_token: str,
        expiration: int,
    ) -> None:
        self.access_key_id = access_key_id
        self.access_key_secret = access_key_secret
        self.security_token = security_token
        self.expiration = expiration


class _FakeFederationCredentials:
    def __init__(self, refresh) -> None:
        self.refresh = refresh


class _FakeStaticCredentialsProvider:
    def __init__(self, access_key: str, secret_key: str) -> None:
        self.access_key = access_key
        self.secret_key = secret_key


class _FakeCredentialModule:
    FederationToken = _FakeFederationToken
    FederationCredentials = _FakeFederationCredentials
    StaticCredentialsProvider = _FakeStaticCredentialsProvider


class _FakeTOSSDK:
    credential = _FakeCredentialModule

    def __init__(self, client: _FakeTOSClient) -> None:
        self.client = client
        self.client_options: dict[str, object] | None = None

    def TosClientV2(self, **kwargs: object) -> _FakeTOSClient:
        self.client_options = dict(kwargs)
        return self.client


class _ClientFactoryTOSSDK:
    credential = _FakeCredentialModule

    def __init__(self) -> None:
        self.clients: list[_FakeTOSClient] = []

    def TosClientV2(self, **_kwargs: object) -> _FakeTOSClient:
        client = _FakeTOSClient()
        self.clients.append(client)
        return client


class _RotatingIRSA:
    def __init__(self) -> None:
        self.calls = 0

    def fetch(self) -> TemporaryCredentials:
        self.calls += 1
        return TemporaryCredentials(
            access_key_id=f"temporary-ak-{self.calls}",
            secret_access_key=f"temporary-sk-{self.calls}",
            session_token=f"session-token-{self.calls}",
            expiration=4_102_444_800,
        )


def _new_storage(
    client: _FakeTOSClient | None = None,
    **overrides: object,
) -> tuple[TOSStorage, _FakeTOSClient]:
    effective_client = client or _FakeTOSClient()
    options = {
        "source_bucket": SOURCE_BUCKET,
        "target_bucket": TARGET_BUCKET,
        "endpoint": ENDPOINT,
        "region": REGION,
        "source_prefix": SOURCE_PREFIX,
        "internal_dataset_prefix": INTERNAL_PREFIX,
        "client": effective_client,
    }
    options.update(overrides)
    return TOSStorage(**options), effective_client


class TOSSDKContractTests(unittest.TestCase):
    def test_requirements_pin_the_official_tos_sdk(self) -> None:
        requirements = (PUBLISHER_ROOT / "requirements.txt").read_text(
            encoding="utf-8"
        )
        tos_requirements = [
            line.strip()
            for line in requirements.splitlines()
            if line.strip().lower().startswith("tos")
        ]

        self.assertEqual(
            tos_requirements,
            [f"tos=={PINNED_TOS_SDK_VERSION}"],
        )

    @unittest.skipUnless(
        importlib.util.find_spec("tos") is not None,
        "the pinned TOS SDK is not installed in this test environment",
    )
    def test_installed_sdk_matches_federation_and_immutable_put_contract(
        self,
    ) -> None:
        with warnings.catch_warnings():
            warnings.filterwarnings(
                "ignore",
                message="invalid escape sequence.*",
                category=DeprecationWarning,
            )
            warnings.filterwarnings(
                "ignore",
                message="urllib3 v2 only supports OpenSSL.*",
            )
            import tos

        self.assertEqual(metadata.version("tos"), PINNED_TOS_SDK_VERSION)
        self.assertEqual(
            tuple(inspect.signature(tos.credential.FederationToken).parameters)[:4],
            (
                "access_key_id",
                "access_key_secret",
                "security_token",
                "expiration",
            ),
        )
        self.assertIn(
            "get_credentials_func",
            inspect.signature(tos.credential.FederationCredentials).parameters,
        )
        client_parameters = inspect.signature(tos.TosClientV2).parameters
        for parameter in ("endpoint", "region", "credentials_provider"):
            self.assertIn(parameter, client_parameters)
        put_parameters = inspect.signature(tos.TosClientV2.put_object).parameters
        for parameter in (
            "content",
            "content_length",
            "content_sha256",
            "meta",
            "forbid_overwrite",
        ):
            self.assertIn(parameter, put_parameters)

        irsa = _RotatingIRSA()
        provider = create_federation_credentials(irsa, tos.credential)
        credentials = provider.get_credentials()

        self.assertEqual(credentials.get_ak(), "temporary-ak-1")
        self.assertEqual(credentials.get_sk(), "temporary-sk-1")
        self.assertEqual(credentials.get_security_token(), "session-token-1")
        self.assertEqual(irsa.calls, 1)


class TOSClientConstructionTests(unittest.TestCase):
    def test_fork_builds_an_independent_sdk_client_without_exposing_credentials(
        self,
    ) -> None:
        sdk = _ClientFactoryTOSSDK()
        storage = TOSStorage(
            source_bucket=SOURCE_BUCKET,
            target_bucket=TARGET_BUCKET,
            endpoint=ENDPOINT,
            region=REGION,
            source_prefix=SOURCE_PREFIX,
            internal_dataset_prefix=INTERNAL_PREFIX,
            environment={"TOS_ACCESS_KEY": "static-ak", "TOS_SECRET_KEY": "static-sk"},
            tos_sdk=sdk,
        )

        first = storage.fork()
        second = storage.fork()

        self.assertIsInstance(first, TOSStorage)
        self.assertIsInstance(second, TOSStorage)
        self.assertEqual(len(sdk.clients), 3)
        self.assertEqual(len({id(client) for client in sdk.clients}), 3)
        self.assertFalse(hasattr(first, "client"))
        self.assertFalse(hasattr(second, "client"))

    def test_prefers_static_environment_credentials_without_calling_irsa(self) -> None:
        client = _FakeTOSClient()
        sdk = _FakeTOSSDK(client)
        irsa = _RotatingIRSA()

        TOSStorage(
            source_bucket=SOURCE_BUCKET,
            target_bucket=TARGET_BUCKET,
            endpoint=ENDPOINT,
            region=REGION,
            source_prefix=SOURCE_PREFIX,
            internal_dataset_prefix=INTERNAL_PREFIX,
            environment={"TOS_ACCESS_KEY": "static-ak", "TOS_SECRET_KEY": "static-sk"},
            irsa_provider=irsa,
            tos_sdk=sdk,
        )

        provider = sdk.client_options["credentials_provider"]
        self.assertIsInstance(provider, _FakeStaticCredentialsProvider)
        self.assertEqual(provider.access_key, "static-ak")
        self.assertEqual(provider.secret_key, "static-sk")
        self.assertEqual(irsa.calls, 0)

    def test_rejects_partial_static_credentials_without_falling_back_to_irsa(self) -> None:
        with self.assertRaisesRegex(TOSStorageError, "static credentials are invalid"):
            TOSStorage(
                source_bucket=SOURCE_BUCKET,
                target_bucket=TARGET_BUCKET,
                endpoint=ENDPOINT,
                region=REGION,
                source_prefix=SOURCE_PREFIX,
                internal_dataset_prefix=INTERNAL_PREFIX,
                environment={"TOS_ACCESS_KEY": "static-ak"},
                irsa_provider=_RotatingIRSA(),
                tos_sdk=_FakeTOSSDK(_FakeTOSClient()),
            )

    def test_builds_tos_client_with_refreshable_federation_provider(self) -> None:
        client = _FakeTOSClient()
        sdk = _FakeTOSSDK(client)
        irsa = _RotatingIRSA()

        storage = TOSStorage(
            source_bucket=SOURCE_BUCKET,
            target_bucket=TARGET_BUCKET,
            endpoint="tos-cn-shanghai.ivolces.com/",
            region=REGION,
            source_prefix=SOURCE_PREFIX + "/",
            internal_dataset_prefix=INTERNAL_PREFIX + "/",
            irsa_provider=irsa,
            tos_sdk=sdk,
        )

        self.assertFalse(hasattr(storage, "client"))
        self.assertEqual(storage.endpoint, ENDPOINT)
        self.assertEqual(storage.source_prefix, SOURCE_PREFIX)
        self.assertEqual(storage.internal_dataset_prefix, INTERNAL_PREFIX)
        self.assertIsNotNone(sdk.client_options)
        federation = sdk.client_options["credentials_provider"]
        self.assertIsInstance(federation, _FakeFederationCredentials)
        self.assertEqual(
            sdk.client_options,
            {
                "endpoint": ENDPOINT,
                "region": REGION,
                "credentials_provider": federation,
            },
        )

        first = federation.refresh()
        second = federation.refresh()
        self.assertIsInstance(first, _FakeFederationToken)
        self.assertEqual(first.access_key_id, "temporary-ak-1")
        self.assertEqual(second.access_key_id, "temporary-ak-2")
        self.assertEqual(irsa.calls, 2)

    def test_accepts_an_injected_client_without_importing_tos(self) -> None:
        client = _FakeTOSClient()
        storage, _ = _new_storage(client)
        full_key = SOURCE_PREFIX + "/index.json"
        client.head_results[(SOURCE_BUCKET, full_key)] = SimpleNamespace(
            content_length=1,
            meta={},
        )

        self.assertEqual(storage.head_source("index.json").size, 1)
        self.assertFalse(hasattr(storage, "client"))


class TOSStorageValidationTests(unittest.TestCase):
    def test_accepts_only_documented_tos_endpoint_host_forms(self) -> None:
        valid_endpoints = (
            (
                "tos-cn-shanghai.ivolces.com/",
                REGION,
                "https://tos-cn-shanghai.ivolces.com",
            ),
            (
                "https://tos-cn-shanghai.volces.com",
                REGION,
                "https://tos-cn-shanghai.volces.com",
            ),
            (
                "https://tos-s3-cn-shanghai.ivolces.com",
                REGION,
                "https://tos-cn-shanghai.ivolces.com",
            ),
            (
                "https://tos-s3-cn-shanghai.volces.com",
                REGION,
                "https://tos-cn-shanghai.volces.com",
            ),
            (
                "https://dataset-bucket.tos-cn-shanghai.ivolces.com",
                REGION,
                "https://tos-cn-shanghai.ivolces.com",
            ),
            (
                "https://dataset-bucket.tos-cn-shanghai.volces.com",
                REGION,
                "https://tos-cn-shanghai.volces.com",
            ),
            (
                "https://dataset-bucket.tos-s3-cn-shanghai.ivolces.com",
                REGION,
                "https://tos-cn-shanghai.ivolces.com",
            ),
            (
                "https://tos-ap-southeast-1.ivolces.com",
                "ap-southeast-1",
                "https://tos-ap-southeast-1.ivolces.com",
            ),
            (
                "https://tos2-private.cn-shanghai.tos.ivolces.com",
                REGION,
                "https://tos2-private.cn-shanghai.tos.ivolces.com",
            ),
        )

        for endpoint, region, expected in valid_endpoints:
            with self.subTest(endpoint=endpoint, region=region):
                storage, _ = _new_storage(endpoint=endpoint, region=region)
                self.assertEqual(storage.endpoint, expected)

    def test_rejects_unsafe_constructor_values(self) -> None:
        invalid_values = {
            "source_bucket": ["", "Source-Bucket", "ab", "source/bucket", "source%2fbucket"],
            "target_bucket": ["", "target_bucket", "https://bucket", "target\\bucket"],
            "endpoint": [
                "",
                "http://tos-cn-shanghai.ivolces.com",
                "https://user:password@tos-cn-shanghai.ivolces.com",
                "https://tos-cn-shanghai.ivolces.com/private",
                "https://tos-cn-shanghai.ivolces.com?secret=value",
                "https://tos-cn-shanghai.ivolces.com/%2e%2e",
                "https://tos-.ivolces.com",
                "https://" + "a" * 64 + ".ivolces.com",
                "tos-cn-shanghai.ivolces.com\\escape",
                "https://example.com",
                "https://tos-cn-shanghai.ivolces.com.attacker.example",
                "https://localhost",
                "https://metadata",
                "https://metadata.google.internal",
                "https://127.0.0.1",
                "https://[::1]",
                "https://169.254.169.254",
                "https://tos-cn-beijing.ivolces.com",
                "https://tos-cn-shanghai.ivolces.com:443",
            ],
            "region": ["", "CN-Shanghai", "cn_shanghai", "../cn-shanghai", "cn%2dshanghai"],
            "source_prefix": [
                "",
                "/absolute",
                "C:/absolute",
                "file:/absolute",
                "../escape",
                "safe/../escape",
                "safe\\object",
                "safe/%2e%2e/object",
                "safe/%252e%252e/object",
                "safe//object",
                "https://bucket/key",
                " safe/object",
                "safe/\x00/object",
            ],
            "internal_dataset_prefix": [
                "",
                "/absolute",
                "C:/absolute",
                "file:/absolute",
                "safe/./object",
                "safe/../object",
                "safe\\object",
                "safe/%2f/object",
                "safe//object",
            ],
        }
        base = {
            "source_bucket": SOURCE_BUCKET,
            "target_bucket": TARGET_BUCKET,
            "endpoint": ENDPOINT,
            "region": REGION,
            "source_prefix": SOURCE_PREFIX,
            "internal_dataset_prefix": INTERNAL_PREFIX,
            "client": _FakeTOSClient(),
        }

        for field, values in invalid_values.items():
            for value in values:
                with self.subTest(field=field, value=value):
                    options = {**base, field: value}
                    with self.assertRaises(ValueError):
                        TOSStorage(**options)

    def test_transfer_operations_require_an_explicit_byte_budget(self) -> None:
        methods = (
            TOSStorage.get_index,
            TOSStorage.download_stream,
            TOSStorage.download_file,
            TOSStorage.put_immutable,
        )

        for method in methods:
            with self.subTest(method=method.__name__):
                parameter = inspect.signature(method).parameters.get("maximum_bytes")
                self.assertIsNotNone(parameter)
                self.assertEqual(parameter.kind, inspect.Parameter.KEYWORD_ONLY)
                self.assertIs(parameter.default, inspect.Parameter.empty)

    def test_transfer_budget_has_a_two_gib_hard_limit(self) -> None:
        self.assertEqual(
            getattr(tos_storage_module, "MAX_TRANSFER_BYTES", None),
            EXPECTED_MAX_TRANSFER_BYTES,
        )
        storage, client = _new_storage()

        storage.put_immutable(
            "object",
            PAYLOAD,
            sha256=DIGEST,
            maximum_bytes=512 * MIB + 1,
        )
        self.assertEqual(len(client.calls), 1)

        for invalid_budget in (True, 0, EXPECTED_MAX_TRANSFER_BYTES + 1):
            with self.subTest(invalid_budget=invalid_budget):
                with self.assertRaises(ValueError):
                    storage.put_immutable(
                        "other-object",
                        PAYLOAD,
                        sha256=DIGEST,
                        maximum_bytes=invalid_budget,
                    )

    def test_rejects_overlapping_prefixes_in_the_same_bucket(self) -> None:
        for internal_prefix in (
            SOURCE_PREFIX,
            "ray-train/public",
            SOURCE_PREFIX + "/internal",
        ):
            with self.subTest(internal_prefix=internal_prefix):
                with self.assertRaises(ValueError):
                    TOSStorage(
                        source_bucket=SOURCE_BUCKET,
                        target_bucket=SOURCE_BUCKET,
                        endpoint=ENDPOINT,
                        region=REGION,
                        source_prefix=SOURCE_PREFIX,
                        internal_dataset_prefix=internal_prefix,
                        client=_FakeTOSClient(),
                    )

    def test_rejects_unsafe_and_empty_operation_keys_before_tos(self) -> None:
        storage, client = _new_storage()
        unsafe = (
            "",
            "/absolute",
            "C:/absolute",
            "file:/absolute",
            "../escape",
            "safe/../escape",
            "safe\\object",
            "safe/%2e%2e/object",
            "safe/%252e%252e/object",
            "safe//object",
            "https://bucket/key",
        )

        for key in unsafe:
            with self.subTest(operation="read", key=key):
                with self.assertRaises(ValueError):
                    storage.get_index(key, maximum_bytes=16)
            with self.subTest(operation="write", key=key):
                with self.assertRaises(ValueError):
                    storage.put_immutable(
                        key,
                        PAYLOAD,
                        sha256=DIGEST,
                        maximum_bytes=len(PAYLOAD),
                    )
            with self.subTest(operation="head", key=key):
                with self.assertRaises(ValueError):
                    storage.verify_immutable(
                        key,
                        expected_size=len(PAYLOAD),
                        expected_sha256=DIGEST,
                    )

        self.assertEqual(client.calls, [])


class TOSStorageReadTests(unittest.TestCase):
    def test_bounded_immutable_get_uses_only_the_internal_target_prefix(self) -> None:
        storage, client = _new_storage()
        full_key = INTERNAL_PREFIX + "/dataset-a/publication/version-a/partitions/00000.json"
        output = _StreamingOutput(b"receipt")
        client.head_results[(TARGET_BUCKET, full_key)] = SimpleNamespace(
            content_length=7,
            meta={"sha256": hashlib.sha256(b"receipt").hexdigest()},
        )
        client.get_results[(TARGET_BUCKET, full_key)] = output

        payload = storage.get_immutable("dataset-a/publication/version-a/partitions/00000.json", maximum_bytes=16)

        self.assertEqual(payload, b"receipt")
        self.assertTrue(output.closed)
        self.assertEqual(client.calls, [
            ("head_object", (TARGET_BUCKET, full_key), {}),
            ("get_object", (TARGET_BUCKET, full_key), {}),
        ])

    def test_bounded_index_get_uses_only_the_source_bucket_and_prefix(self) -> None:
        storage, client = _new_storage()
        full_key = SOURCE_PREFIX + "/indexes/current.json"
        output = _StreamingOutput(b"index")
        client.head_results[(SOURCE_BUCKET, full_key)] = SimpleNamespace(
            content_length=5,
            meta={"sha256": hashlib.sha256(b"index").hexdigest()},
        )
        client.get_results[(SOURCE_BUCKET, full_key)] = output

        payload = storage.get_index("indexes/current.json", maximum_bytes=16)

        self.assertEqual(payload, b"index")
        self.assertTrue(output.closed)
        self.assertEqual(
            client.calls,
            [
                ("head_object", (SOURCE_BUCKET, full_key), {}),
                ("get_object", (SOURCE_BUCKET, full_key), {}),
            ],
        )

    def test_source_exists_distinguishes_missing_from_storage_failure(self) -> None:
        storage, client = _new_storage()
        full_key = SOURCE_PREFIX + "/indexes/version.json"
        client.head_results[(SOURCE_BUCKET, full_key)] = SimpleNamespace(
            content_length=1,
            meta={},
        )

        self.assertTrue(storage.source_exists("indexes/version.json"))

        client.failure = _FakeTOSServiceError(404)
        self.assertFalse(storage.source_exists("indexes/version.json"))

        client.failure = _FakeTOSServiceError(503)
        with self.assertRaisesRegex(TOSStorageError, "source availability check failed") as raised:
            storage.source_exists("indexes/version.json")
        self.assertNotIn("sensitive provider detail", str(raised.exception))

    def test_index_get_rejects_head_or_stream_larger_than_bound(self) -> None:
        storage, client = _new_storage()
        full_key = SOURCE_PREFIX + "/index.json"
        client.head_results[(SOURCE_BUCKET, full_key)] = SimpleNamespace(
            content_length=17,
            meta={},
        )

        with self.assertRaises(TOSStorageError):
            storage.get_index("index.json", maximum_bytes=16)
        self.assertEqual(
            [operation for operation, _args, _kwargs in client.calls],
            ["head_object"],
        )

        client.calls.clear()
        output = _StreamingOutput(b"12345", content_length=4)
        client.head_results[(SOURCE_BUCKET, full_key)] = SimpleNamespace(
            content_length=4,
            meta={},
        )
        client.get_results[(SOURCE_BUCKET, full_key)] = output
        with self.assertRaises(TOSStorageError):
            storage.get_index("index.json", maximum_bytes=4)
        self.assertTrue(output.closed)

        with self.assertRaises(ValueError):
            storage.get_index("index.json", maximum_bytes=MAX_INDEX_BYTES + 1)

    def test_stream_and_file_download_cannot_leave_source_prefix(self) -> None:
        storage, client = _new_storage()
        full_key = SOURCE_PREFIX + "/scene-a/points.bin"
        stream = _StreamingOutput(b"abcdefgh")
        client.get_results[(SOURCE_BUCKET, full_key)] = stream
        client.head_results[(SOURCE_BUCKET, full_key)] = SimpleNamespace(
            content_length=8,
            meta={"sha256": hashlib.sha256(b"abcdefgh").hexdigest()},
        )
        destination = io.BytesIO()

        downloaded = storage.download_stream(
            "scene-a/points.bin",
            destination,
            chunk_size=3,
            maximum_bytes=16,
        )
        file_stream = _StreamingOutput(b"abcdefgh")
        client.get_results[(SOURCE_BUCKET, full_key)] = file_stream
        with tempfile.TemporaryDirectory() as temp_dir:
            destination_path = Path(temp_dir) / "points.bin"
            file_result = storage.download_file(
                "scene-a/points.bin",
                destination_path,
                maximum_bytes=16,
            )
            self.assertEqual(destination_path.read_bytes(), b"abcdefgh")

        self.assertEqual(downloaded, 8)
        self.assertEqual(destination.getvalue(), b"abcdefgh")
        self.assertTrue(stream.closed)
        self.assertTrue(file_stream.closed)
        self.assertEqual(file_result.size, 8)
        self.assertEqual(
            client.calls,
            [
                ("head_object", (SOURCE_BUCKET, full_key), {}),
                ("get_object", (SOURCE_BUCKET, full_key), {}),
                ("head_object", (SOURCE_BUCKET, full_key), {}),
                ("get_object", (SOURCE_BUCKET, full_key), {}),
            ],
        )

    def test_stream_download_aborts_over_budget_and_rolls_back_destination(
        self,
    ) -> None:
        storage, client = _new_storage()
        full_key = SOURCE_PREFIX + "/scene-a/oversized.bin"
        output = _StreamingOutput(b"abcde", content_length=4)
        client.head_results[(SOURCE_BUCKET, full_key)] = SimpleNamespace(
            content_length=4,
            meta={},
        )
        client.get_results[(SOURCE_BUCKET, full_key)] = output
        destination = io.BytesIO(b"preserved")
        destination.seek(0, io.SEEK_END)

        with self.assertRaises(TOSStorageError) as raised:
            storage.download_stream(
                "scene-a/oversized.bin",
                destination,
                chunk_size=2,
                maximum_bytes=4,
            )

        self.assertEqual(str(raised.exception), "TOS source stream download failed")
        self.assertEqual(destination.getvalue(), b"preserved")
        self.assertTrue(output.closed)

    def test_stream_download_requires_rollback_capable_destination_before_tos(
        self,
    ) -> None:
        storage, client = _new_storage()
        full_key = SOURCE_PREFIX + "/scene-a/points.bin"
        client.head_results[(SOURCE_BUCKET, full_key)] = SimpleNamespace(
            content_length=4,
            meta={},
        )
        client.get_results[(SOURCE_BUCKET, full_key)] = _StreamingOutput(b"data")

        with self.assertRaises(ValueError):
            storage.download_stream(
                "scene-a/points.bin",
                _WriteOnlyDestination(),
                maximum_bytes=4,
            )

        self.assertEqual(client.calls, [])

    def test_stream_download_requires_append_position_before_tos(self) -> None:
        storage, client = _new_storage()
        full_key = SOURCE_PREFIX + "/scene-a/points.bin"
        client.head_results[(SOURCE_BUCKET, full_key)] = SimpleNamespace(
            content_length=4,
            meta={},
        )
        client.get_results[(SOURCE_BUCKET, full_key)] = _StreamingOutput(b"data")
        destination = io.BytesIO(b"preserved")
        destination.seek(0)

        with self.assertRaises(ValueError):
            storage.download_stream(
                "scene-a/points.bin",
                destination,
                maximum_bytes=4,
            )

        self.assertEqual(destination.getvalue(), b"preserved")
        self.assertEqual(client.calls, [])

    def test_file_download_aborts_over_budget_without_leaving_partial_files(
        self,
    ) -> None:
        storage, client = _new_storage()
        full_key = SOURCE_PREFIX + "/scene-a/oversized.bin"
        output = _StreamingOutput(b"abcde", content_length=4)
        client.head_results[(SOURCE_BUCKET, full_key)] = SimpleNamespace(
            content_length=4,
            meta={},
        )
        client.get_results[(SOURCE_BUCKET, full_key)] = output

        with tempfile.TemporaryDirectory() as temp_dir:
            destination = Path(temp_dir) / "points.bin"
            destination.write_bytes(b"preserved")
            original_entries = tuple(Path(temp_dir).iterdir())

            with self.assertRaises(TOSStorageError) as raised:
                storage.download_file(
                    "scene-a/oversized.bin",
                    destination,
                    maximum_bytes=4,
                )

            self.assertEqual(destination.read_bytes(), b"preserved")
            self.assertEqual(tuple(Path(temp_dir).iterdir()), original_entries)

        self.assertEqual(str(raised.exception), "TOS source file download failed")
        self.assertTrue(output.closed)

    def test_list_is_rooted_and_rejects_out_of_prefix_responses(self) -> None:
        storage, client = _new_storage()
        client.list_result = SimpleNamespace(
            contents=[
                SimpleNamespace(
                    key=SOURCE_PREFIX + "/scene-a/one.bin",
                    size=3,
                    etag="etag-one",
                )
            ],
            next_marker="next-marker",
        )

        page = storage.list_source("scene-a", marker="marker", max_keys=100)

        self.assertEqual([item.key for item in page.objects], ["scene-a/one.bin"])
        self.assertEqual(page.next_marker, "next-marker")
        self.assertEqual(
            client.calls,
            [
                (
                    "list_objects",
                    (SOURCE_BUCKET,),
                    {
                        "prefix": SOURCE_PREFIX + "/scene-a/",
                        "marker": "marker",
                        "max_keys": 100,
                    },
                )
            ],
        )

        client.calls.clear()
        client.list_result = SimpleNamespace(
            contents=[SimpleNamespace(key=SOURCE_PREFIX + "-private/object", size=1, etag="e")],
            next_marker=None,
        )
        with self.assertRaises(TOSStorageError):
            storage.list_source()

        client.list_result = SimpleNamespace(
            contents=[
                SimpleNamespace(
                    key=SOURCE_PREFIX + f"/object-{index}",
                    size=1,
                    etag="e",
                )
                for index in range(2)
            ],
            next_marker=None,
        )
        with self.assertRaises(TOSStorageError):
            storage.list_source(max_keys=1)

    def test_list_skips_only_zero_byte_directory_markers(self) -> None:
        storage, client = _new_storage()
        client.list_result = SimpleNamespace(
            contents=[
                SimpleNamespace(key=SOURCE_PREFIX + "/", size=0, etag="folder"),
                SimpleNamespace(key=SOURCE_PREFIX + "/scene-a/", size=0, etag="folder"),
                SimpleNamespace(key=SOURCE_PREFIX + "/scene-a/points.bin", size=4, etag="data"),
            ],
            next_marker=None,
        )

        page = storage.list_source()

        self.assertEqual([item.key for item in page.objects], ["scene-a/points.bin"])


class TOSStorageWriteTests(unittest.TestCase):
    def test_put_is_immutable_and_sets_both_sha256_controls(self) -> None:
        storage, client = _new_storage()
        relative_key = "objects/sha256/00/" + DIGEST + ".parquet"
        full_key = INTERNAL_PREFIX + "/" + relative_key

        storage.put_immutable(
            relative_key,
            PAYLOAD,
            sha256=DIGEST,
            maximum_bytes=len(PAYLOAD),
        )

        self.assertEqual(len(client.calls), 1)
        operation, args, kwargs = client.calls[0]
        self.assertEqual(operation, "put_object")
        self.assertEqual(args, (TARGET_BUCKET, full_key))
        self.assertEqual(kwargs["content"], PAYLOAD)
        self.assertEqual(kwargs["content_length"], len(PAYLOAD))
        self.assertEqual(kwargs["content_sha256"], DIGEST)
        self.assertEqual(kwargs["meta"], {"sha256": DIGEST})
        self.assertIs(kwargs["forbid_overwrite"], True)

    def test_put_rejects_digest_or_size_mismatch_before_tos(self) -> None:
        storage, client = _new_storage()
        with self.assertRaises(ValueError):
            storage.put_immutable(
                "object",
                PAYLOAD,
                sha256="A" * 64,
                maximum_bytes=len(PAYLOAD),
            )
        with self.assertRaises(ValueError):
            storage.put_immutable(
                "object",
                PAYLOAD,
                sha256="0" * 64,
                maximum_bytes=len(PAYLOAD),
            )
        with self.assertRaises(ValueError):
            storage.put_immutable(
                "object",
                io.BytesIO(PAYLOAD),
                sha256=DIGEST,
                size=len(PAYLOAD) + 1,
                maximum_bytes=len(PAYLOAD) + 1,
            )
        self.assertEqual(client.calls, [])

    def test_put_aborts_over_budget_and_restores_upload_stream(self) -> None:
        storage, client = _new_storage()
        stream = io.BytesIO(b"abcde")

        with self.assertRaises(TOSStorageError) as raised:
            storage.put_immutable(
                "object",
                stream,
                sha256=hashlib.sha256(b"abcde").hexdigest(),
                maximum_bytes=4,
            )

        self.assertEqual(
            str(raised.exception),
            "TOS immutable upload exceeds the configured bound",
        )
        self.assertEqual(stream.tell(), 0)
        self.assertEqual(client.calls, [])

        with self.assertRaises(TOSStorageError):
            storage.put_immutable(
                "object",
                b"abcde",
                sha256=hashlib.sha256(b"abcde").hexdigest(),
                maximum_bytes=4,
            )
        self.assertEqual(client.calls, [])

    def test_head_verifies_exact_size_and_sha256_metadata(self) -> None:
        storage, client = _new_storage()
        relative_key = "manifests/version.json"
        full_key = INTERNAL_PREFIX + "/" + relative_key
        client.head_results[(TARGET_BUCKET, full_key)] = SimpleNamespace(
            content_length=len(PAYLOAD),
            meta={"sha256": DIGEST},
        )

        info = storage.verify_immutable(
            relative_key,
            expected_size=len(PAYLOAD),
            expected_sha256=DIGEST,
        )

        self.assertEqual(info.size, len(PAYLOAD))
        self.assertEqual(info.sha256, DIGEST)
        self.assertEqual(
            client.calls,
            [("head_object", (TARGET_BUCKET, full_key), {})],
        )

        for head in (
            SimpleNamespace(content_length=len(PAYLOAD) + 1, meta={"sha256": DIGEST}),
            SimpleNamespace(content_length=len(PAYLOAD), meta={"sha256": "0" * 64}),
            SimpleNamespace(content_length=len(PAYLOAD), meta={}),
        ):
            with self.subTest(head=head):
                client.head_results[(TARGET_BUCKET, full_key)] = head
                with self.assertRaises(TOSStorageError):
                    storage.verify_immutable(
                        relative_key,
                        expected_size=len(PAYLOAD),
                        expected_sha256=DIGEST,
                    )

    def test_sdk_failures_are_sanitized(self) -> None:
        client = _FakeTOSClient()
        client.failure = RuntimeError(
            "oidc=secret-token ak=secret-ak sk=secret-sk full-response-body"
        )
        storage, _ = _new_storage(client)

        with self.assertRaises(TOSStorageError) as raised:
            storage.put_immutable(
                "object",
                PAYLOAD,
                sha256=DIGEST,
                maximum_bytes=len(PAYLOAD),
            )

        message = str(raised.exception).lower()
        for forbidden in (
            "secret-token",
            "secret-ak",
            "secret-sk",
            "full-response-body",
            SOURCE_BUCKET,
            TARGET_BUCKET,
        ):
            self.assertNotIn(forbidden, message)


if __name__ == "__main__":
    unittest.main()
