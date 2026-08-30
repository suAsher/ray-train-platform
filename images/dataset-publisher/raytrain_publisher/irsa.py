"""Refreshable VKE IRSA credentials for the dataset publisher.

This module deliberately uses a small, injectable HTTP boundary.  It never logs
or includes the projected OIDC token, temporary credentials, or STS response body
in an exception.
"""

from __future__ import annotations

import json
import math
import os
import re
import time
import urllib.error
import urllib.parse
import urllib.request
from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


DEFAULT_STS_ENDPOINT = (
    "https://sts.volcengineapi.com/"
    "?Action=AssumeRoleWithOIDC&Version=2018-01-01"
)
DEFAULT_STS_TIMEOUT_SECONDS = 5.0
DEFAULT_STS_ATTEMPTS = 3
DEFAULT_STS_BACKOFF_SECONDS = 0.2
DEFAULT_SESSION_DURATION_SECONDS = 3600
MAX_STS_ATTEMPTS = 5
MAX_STS_TIMEOUT_SECONDS = 30.0
MAX_STS_BACKOFF_SECONDS = 2.0
MAX_OIDC_TOKEN_BYTES = 1024 * 1024
MAX_STS_RESPONSE_BYTES = 1024 * 1024

_ROLE_TRN_PATTERN = re.compile(r"^trn:iam::[0-9]+:role/[A-Za-z0-9+=,.@_/-]+$")
_SESSION_NAME_PATTERN = re.compile(r"^[A-Za-z0-9+=,.@_-]{2,64}$")


class _NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    """Keep the projected OIDC token on the configured STS origin."""

    def redirect_request(
        self,
        request: Any,
        file_pointer: Any,
        status_code: int,
        message: str,
        headers: Any,
        new_url: str,
    ) -> None:
        return None


class IRSAError(RuntimeError):
    """A deliberately sanitized VKE IRSA failure."""


@dataclass(frozen=True)
class STSResponse:
    """Bounded HTTP response returned by an injected STS transport."""

    status_code: int
    body: bytes = field(repr=False)


@dataclass(frozen=True)
class TemporaryCredentials:
    """Temporary credentials with secret fields omitted from representations."""

    access_key_id: str = field(repr=False)
    secret_access_key: str = field(repr=False)
    session_token: str = field(repr=False)
    expiration: int


HTTPPost = Callable[..., STSResponse]


class VKEIRSAProvider:
    """Exchange a projected VKE OIDC token for temporary STS credentials."""

    def __init__(
        self,
        *,
        environment: Mapping[str, str] | None = None,
        sts_endpoint: str = DEFAULT_STS_ENDPOINT,
        role_session_name: str = "dataset-publisher",
        duration_seconds: int = DEFAULT_SESSION_DURATION_SECONDS,
        timeout_seconds: float = DEFAULT_STS_TIMEOUT_SECONDS,
        max_attempts: int = DEFAULT_STS_ATTEMPTS,
        retry_backoff_seconds: float = DEFAULT_STS_BACKOFF_SECONDS,
        http_post: HTTPPost | None = None,
        sleep: Callable[[float], None] = time.sleep,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self._validate_options(
            sts_endpoint=sts_endpoint,
            role_session_name=role_session_name,
            duration_seconds=duration_seconds,
            timeout_seconds=timeout_seconds,
            max_attempts=max_attempts,
            retry_backoff_seconds=retry_backoff_seconds,
        )
        self._environment = os.environ if environment is None else environment
        self._sts_endpoint = sts_endpoint
        self._role_session_name = role_session_name
        self._duration_seconds = duration_seconds
        self._timeout_seconds = float(timeout_seconds)
        self._max_attempts = max_attempts
        self._retry_backoff_seconds = float(retry_backoff_seconds)
        self._http_post = http_post or _urllib_post
        self._sleep = sleep
        self._clock = clock

    @staticmethod
    def _validate_options(
        *,
        sts_endpoint: object,
        role_session_name: object,
        duration_seconds: object,
        timeout_seconds: object,
        max_attempts: object,
        retry_backoff_seconds: object,
    ) -> None:
        if not _is_valid_sts_endpoint(sts_endpoint):
            raise ValueError("STS endpoint must be the HTTPS AssumeRoleWithOIDC API")
        if not isinstance(role_session_name, str) or not _SESSION_NAME_PATTERN.fullmatch(
            role_session_name
        ):
            raise ValueError("role session name is invalid")
        if (
            isinstance(duration_seconds, bool)
            or not isinstance(duration_seconds, int)
            or duration_seconds < 900
            or duration_seconds > 43200
        ):
            raise ValueError("session duration must be between 900 and 43200 seconds")
        if (
            isinstance(max_attempts, bool)
            or not isinstance(max_attempts, int)
            or max_attempts < 1
            or max_attempts > MAX_STS_ATTEMPTS
        ):
            raise ValueError(f"STS attempts must be between 1 and {MAX_STS_ATTEMPTS}")
        if (
            isinstance(timeout_seconds, bool)
            or not isinstance(timeout_seconds, (int, float))
            or not math.isfinite(timeout_seconds)
            or timeout_seconds <= 0
            or timeout_seconds > MAX_STS_TIMEOUT_SECONDS
        ):
            raise ValueError(
                f"STS timeout must be between 0 and {MAX_STS_TIMEOUT_SECONDS} seconds"
            )
        if (
            isinstance(retry_backoff_seconds, bool)
            or not isinstance(retry_backoff_seconds, (int, float))
            or not math.isfinite(retry_backoff_seconds)
            or retry_backoff_seconds < 0
            or retry_backoff_seconds > MAX_STS_BACKOFF_SECONDS
        ):
            raise ValueError("STS retry backoff is invalid")

    def fetch(self) -> TemporaryCredentials:
        """Fetch one fresh credential set from STS.

        The token file is read for every call so Kubernetes token rotation and
        the TOS SDK's federation refresh callback work together.
        """

        token, role_trn = self._read_projected_identity()
        request_body = urllib.parse.urlencode(
            {
                "RoleTrn": role_trn,
                "OIDCToken": token,
                "RoleSessionName": self._role_session_name,
                "DurationSeconds": str(self._duration_seconds),
            }
        ).encode("ascii")
        headers = {"Content-Type": "application/x-www-form-urlencoded"}

        for attempt in range(self._max_attempts):
            response, network_failed, transport_failed = self._post_once(
                request_body, headers
            )
            if transport_failed:
                raise IRSAError("STS AssumeRoleWithOIDC transport failed")
            if network_failed:
                if attempt + 1 == self._max_attempts:
                    raise IRSAError("STS AssumeRoleWithOIDC request failed")
                self._sleep_before_retry(attempt)
                continue

            if response is None:
                raise IRSAError("STS AssumeRoleWithOIDC transport failed")
            if response.status_code == 200:
                return self._parse_credentials(response.body)
            if _is_retryable_status(response.status_code):
                if attempt + 1 == self._max_attempts:
                    raise IRSAError("STS AssumeRoleWithOIDC request failed")
                self._sleep_before_retry(attempt)
                continue
            raise IRSAError(
                f"STS AssumeRoleWithOIDC rejected the request (HTTP {response.status_code})"
            )

        raise IRSAError("STS AssumeRoleWithOIDC request failed")

    def _post_once(
        self, request_body: bytes, headers: dict[str, str]
    ) -> tuple[STSResponse | None, bool, bool]:
        try:
            response = self._http_post(
                url=self._sts_endpoint,
                data=request_body,
                headers=headers,
                timeout=self._timeout_seconds,
            )
        except (OSError, TimeoutError):
            return None, True, False
        except Exception:
            return None, False, True
        if not isinstance(response, STSResponse):
            return None, False, True
        if (
            isinstance(response.status_code, bool)
            or not isinstance(response.status_code, int)
            or response.status_code < 100
            or response.status_code > 599
            or not isinstance(response.body, bytes)
            or len(response.body) > MAX_STS_RESPONSE_BYTES
        ):
            return None, False, True
        return response, False, False

    def _read_projected_identity(self) -> tuple[str, str]:
        token_file = self._environment.get("VOLCENGINE_OIDC_TOKEN_FILE", "")
        role_trn = self._environment.get("VOLCENGINE_OIDC_ROLE_TRN", "")
        if not isinstance(token_file, str) or not isinstance(role_trn, str):
            raise IRSAError("IRSA configuration is unavailable")
        if not token_file or not _ROLE_TRN_PATTERN.fullmatch(role_trn):
            raise IRSAError("IRSA configuration is unavailable")
        token_path = Path(token_file)
        if not token_path.is_absolute():
            raise IRSAError("IRSA configuration is unavailable")

        read_failed = False
        token_bytes = b""
        try:
            with token_path.open("rb") as token_stream:
                token_bytes = token_stream.read(MAX_OIDC_TOKEN_BYTES + 1)
        except OSError:
            read_failed = True
        if read_failed:
            raise IRSAError("IRSA token is unavailable")
        if not token_bytes or len(token_bytes) > MAX_OIDC_TOKEN_BYTES:
            raise IRSAError("IRSA token is unavailable")
        try:
            token = token_bytes.decode("utf-8").strip()
        except UnicodeDecodeError:
            token = ""
        if not token or any(character.isspace() for character in token):
            raise IRSAError("IRSA token is unavailable")
        return token, role_trn

    def _parse_credentials(self, body: bytes) -> TemporaryCredentials:
        if not body or len(body) > MAX_STS_RESPONSE_BYTES:
            raise IRSAError("STS AssumeRoleWithOIDC returned invalid credentials")
        parse_failed = False
        access_key_id = None
        secret_access_key = None
        session_token = None
        expiration_text = None
        try:
            payload = json.loads(body)
            values = payload["Result"]["Credentials"]
            access_key_id = values["AccessKeyId"]
            secret_access_key = values["SecretAccessKey"]
            session_token = values["SessionToken"]
            expiration_text = values["Expiration"]
        except Exception:
            parse_failed = True
        if parse_failed:
            raise IRSAError("STS AssumeRoleWithOIDC returned invalid credentials")

        credential_values = (
            access_key_id,
            secret_access_key,
            session_token,
            expiration_text,
        )
        if any(
            not isinstance(value, str)
            or not value
            or value.strip() != value
            or any(character.isspace() for character in value)
            for value in credential_values
        ):
            raise IRSAError("STS AssumeRoleWithOIDC returned invalid credentials")
        expiration = _parse_rfc3339(expiration_text)
        if expiration is None or expiration <= int(self._clock()):
            raise IRSAError("STS AssumeRoleWithOIDC returned invalid credentials")
        return TemporaryCredentials(
            access_key_id=access_key_id,
            secret_access_key=secret_access_key,
            session_token=session_token,
            expiration=expiration,
        )

    def _sleep_before_retry(self, attempt: int) -> None:
        delay = min(
            self._retry_backoff_seconds * (2**attempt),
            MAX_STS_BACKOFF_SECONDS,
        )
        self._sleep(delay)


def create_federation_credentials(
    irsa_provider: VKEIRSAProvider,
    credential_module: Any,
) -> Any:
    """Create the TOS SDK's refreshable FederationCredentials provider."""

    federation_token_factory = getattr(credential_module, "FederationToken", None)
    federation_credentials_factory = getattr(
        credential_module, "FederationCredentials", None
    )
    if not callable(federation_token_factory) or not callable(
        federation_credentials_factory
    ):
        raise IRSAError("TOS federation credential support is unavailable")

    def refresh() -> Any:
        credentials = irsa_provider.fetch()
        creation_failed = False
        token = None
        try:
            token = federation_token_factory(
                credentials.access_key_id,
                credentials.secret_access_key,
                credentials.session_token,
                credentials.expiration,
            )
        except Exception:
            creation_failed = True
        if creation_failed:
            raise IRSAError("TOS federation token creation failed")
        return token

    creation_failed = False
    provider = None
    try:
        provider = federation_credentials_factory(refresh)
    except Exception:
        creation_failed = True
    if creation_failed:
        raise IRSAError("TOS federation credential creation failed")
    return provider


def _is_valid_sts_endpoint(value: object) -> bool:
    if (
        not isinstance(value, str)
        or not value
        or value.strip() != value
        or any(ord(character) < 32 or ord(character) == 127 for character in value)
    ):
        return False
    try:
        parsed = urllib.parse.urlsplit(value)
        query = urllib.parse.parse_qs(
            parsed.query,
            keep_blank_values=True,
            strict_parsing=True,
        )
    except ValueError:
        return False
    return (
        parsed.scheme == "https"
        and parsed.netloc == "sts.volcengineapi.com"
        and parsed.hostname == "sts.volcengineapi.com"
        and parsed.username is None
        and parsed.password is None
        and parsed.fragment == ""
        and parsed.path in {"", "/"}
        and query
        == {"Action": ["AssumeRoleWithOIDC"], "Version": ["2018-01-01"]}
    )


def _is_retryable_status(status_code: int) -> bool:
    return status_code == 429 or 500 <= status_code <= 599


def _parse_rfc3339(value: str) -> int | None:
    try:
        normalized = value[:-1] + "+00:00" if value.endswith("Z") else value
        parsed = datetime.fromisoformat(normalized)
        if parsed.tzinfo is None:
            return None
        return int(parsed.astimezone(timezone.utc).timestamp())
    except (ValueError, OverflowError, OSError):
        return None


def _urllib_post(
    *,
    url: str,
    data: bytes,
    headers: dict[str, str],
    timeout: float,
) -> STSResponse:
    request = urllib.request.Request(
        url=url,
        data=data,
        headers=headers,
        method="POST",
    )
    opener = urllib.request.build_opener(_NoRedirectHandler())
    try:
        response = opener.open(request, timeout=timeout)
    except urllib.error.HTTPError as error:
        try:
            body = _read_bounded_response(error)
        finally:
            error.close()
        return STSResponse(status_code=error.code, body=body)
    except (urllib.error.URLError, OSError, TimeoutError):
        raise OSError("STS network request failed") from None

    try:
        status_code = response.getcode()
        body = _read_bounded_response(response)
    finally:
        response.close()
    return STSResponse(status_code=status_code, body=body)


def _read_bounded_response(response: Any) -> bytes:
    body = response.read(MAX_STS_RESPONSE_BYTES + 1)
    if not isinstance(body, bytes) or len(body) > MAX_STS_RESPONSE_BYTES:
        return b""
    return body


__all__ = [
    "DEFAULT_STS_ENDPOINT",
    "IRSAError",
    "MAX_STS_ATTEMPTS",
    "STSResponse",
    "TemporaryCredentials",
    "VKEIRSAProvider",
    "create_federation_credentials",
]
