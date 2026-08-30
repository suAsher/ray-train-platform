"""Tests for the VKE OIDC-to-STS credential exchange."""

from __future__ import annotations

import io
import json
import sys
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path
from unittest.mock import patch
from urllib.error import HTTPError
from urllib.parse import parse_qs


PUBLISHER_ROOT = Path(__file__).resolve().parents[1]
if str(PUBLISHER_ROOT) not in sys.path:
    sys.path.insert(0, str(PUBLISHER_ROOT))

from raytrain_publisher.irsa import (  # noqa: E402
    MAX_STS_ATTEMPTS,
    IRSAError,
    STSResponse,
    VKEIRSAProvider,
    _urllib_post,
    create_federation_credentials,
)


TOKEN = ".".join(("header", "fixture", "signature"))
ROLE_TRN = "trn:iam::2100000000:role/dataset-publisher"
ACCESS_KEY = "temporary-access-key"
SECRET_KEY = "temporary-secret-key"
SESSION_TOKEN = "temporary-session-token"
EXPIRATION = "2099-08-30T13:00:00Z"


def _success_response(
    *,
    access_key: str = ACCESS_KEY,
    secret_key: str = SECRET_KEY,
    session_token: str = SESSION_TOKEN,
    expiration: str = EXPIRATION,
) -> STSResponse:
    body = {
        "ResponseMetadata": {"RequestId": "request-id"},
        "Result": {
            "Credentials": {
                "AccessKeyId": access_key,
                "SecretAccessKey": secret_key,
                "SessionToken": session_token,
                "Expiration": expiration,
            }
        },
    }
    return STSResponse(status_code=200, body=json.dumps(body).encode("utf-8"))


class _ScriptedPost:
    def __init__(self, *results: object) -> None:
        self._results = list(results)
        self.calls: list[dict[str, object]] = []

    def __call__(
        self,
        *,
        url: str,
        data: bytes,
        headers: dict[str, str],
        timeout: float,
    ) -> STSResponse:
        self.calls.append(
            {"url": url, "data": data, "headers": headers, "timeout": timeout}
        )
        result = self._results.pop(0)
        if isinstance(result, BaseException):
            raise result
        if not isinstance(result, STSResponse):
            raise AssertionError("script result must be an STSResponse or exception")
        return result


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


class _FakeCredentialModule:
    FederationToken = _FakeFederationToken
    FederationCredentials = _FakeFederationCredentials


class VKEIRSAProviderTests(unittest.TestCase):
    def _provider(
        self,
        token_path: Path,
        post: _ScriptedPost,
        **overrides: object,
    ) -> VKEIRSAProvider:
        options = {
            "environment": {
                "VOLCENGINE_OIDC_ROLE_TRN": ROLE_TRN,
                "VOLCENGINE_OIDC_TOKEN_FILE": str(token_path),
            },
            "http_post": post,
            "role_session_name": "dataset-publisher-test",
            "timeout_seconds": 2.5,
            "max_attempts": 4,
            "retry_backoff_seconds": 0.1,
        }
        options.update(overrides)
        return VKEIRSAProvider(**options)

    def test_reads_vke_environment_and_posts_form_with_timeout(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            token_path = Path(temp_dir) / "token"
            token_path.write_text(TOKEN + "\n", encoding="utf-8")
            post = _ScriptedPost(_success_response())

            credentials = self._provider(token_path, post).fetch()

        self.assertEqual(credentials.access_key_id, ACCESS_KEY)
        self.assertEqual(credentials.secret_access_key, SECRET_KEY)
        self.assertEqual(credentials.session_token, SESSION_TOKEN)
        self.assertEqual(
            credentials.expiration,
            int(datetime(2099, 8, 30, 13, tzinfo=timezone.utc).timestamp()),
        )
        self.assertEqual(len(post.calls), 1)
        request = post.calls[0]
        self.assertEqual(
            request["url"],
            "https://sts.volcengineapi.com/?Action=AssumeRoleWithOIDC&Version=2018-01-01",
        )
        self.assertEqual(request["timeout"], 2.5)
        self.assertEqual(
            request["headers"],
            {"Content-Type": "application/x-www-form-urlencoded"},
        )
        self.assertEqual(
            parse_qs(request["data"].decode("ascii"), strict_parsing=True),
            {
                "RoleTrn": [ROLE_TRN],
                "OIDCToken": [TOKEN],
                "RoleSessionName": ["dataset-publisher-test"],
                "DurationSeconds": ["3600"],
            },
        )
        rendered = repr(credentials)
        for secret in (ACCESS_KEY, SECRET_KEY, SESSION_TOKEN):
            self.assertNotIn(secret, rendered)

    def test_official_vke_webhook_role_environment_variable_is_accepted(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            token_path = Path(temp_dir) / "token"
            token_path.write_text(TOKEN, encoding="utf-8")
            post = _ScriptedPost(_success_response())
            environment = {
                "VOLCENGINE_OIDC_ROLE_TRN": ROLE_TRN,
                "VOLCENGINE_OIDC_TOKEN_FILE": str(token_path),
            }

            credentials = self._provider(
                token_path,
                post,
                environment=environment,
            ).fetch()

        self.assertEqual(credentials.access_key_id, ACCESS_KEY)
        self.assertEqual(len(post.calls), 1)
        request_form = parse_qs(
            post.calls[0]["data"].decode("ascii"),
            strict_parsing=True,
        )
        self.assertEqual(request_form["RoleTrn"], [ROLE_TRN])

    def test_legacy_role_trn_environment_variable_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            token_path = Path(temp_dir) / "token"
            token_path.write_text(TOKEN, encoding="utf-8")
            post = _ScriptedPost(_success_response())
            legacy_environment = {
                "ROLE_TRN": ROLE_TRN,
                "VOLCENGINE_OIDC_TOKEN_FILE": str(token_path),
            }

            with self.assertRaises(IRSAError):
                self._provider(
                    token_path,
                    post,
                    environment=legacy_environment,
                ).fetch()

        self.assertEqual(post.calls, [])

    def test_default_transport_does_not_follow_sts_redirects(self) -> None:
        redirect_body = io.BytesIO(b'{"redirect":"response-body"}')
        redirect_error = HTTPError(
            "https://sts.volcengineapi.com/",
            307,
            "Temporary Redirect",
            {},
            redirect_body,
        )
        captured_handlers: list[object] = []

        class _RedirectingOpener:
            def open(self, request, *, timeout: float):
                self.request = request
                self.timeout = timeout
                raise redirect_error

        opener = _RedirectingOpener()

        def build_opener(*handlers: object) -> _RedirectingOpener:
            captured_handlers.extend(handlers)
            return opener

        with patch(
            "raytrain_publisher.irsa.urllib.request.build_opener",
            side_effect=build_opener,
        ), patch(
            "raytrain_publisher.irsa.urllib.request.urlopen",
            side_effect=AssertionError("redirect-following transport was used"),
        ):
            response = _urllib_post(
                url=(
                    "https://sts.volcengineapi.com/"
                    "?Action=AssumeRoleWithOIDC&Version=2018-01-01"
                ),
                data=b"OIDCToken=secret",
                headers={"Content-Type": "application/x-www-form-urlencoded"},
                timeout=1.5,
            )

        self.assertEqual(response.status_code, 307)
        self.assertEqual(response.body, b'{"redirect":"response-body"}')
        self.assertEqual(opener.timeout, 1.5)
        self.assertEqual(len(captured_handlers), 1)
        redirect_handler = captured_handlers[0]
        self.assertIsNone(
            redirect_handler.redirect_request(
                opener.request,
                None,
                307,
                "Temporary Redirect",
                {},
                "https://attacker.example/steal",
            )
        )

    def test_retries_network_429_and_all_5xx_with_bounded_backoff(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            token_path = Path(temp_dir) / "token"
            token_path.write_text(TOKEN, encoding="utf-8")
            post = _ScriptedPost(
                OSError("network failed with " + TOKEN),
                STSResponse(429, b'{"secret":"rate-limit-body"}'),
                STSResponse(500, b'{"secret":"server-body-500"}'),
                STSResponse(599, b'{"secret":"server-body-599"}'),
                _success_response(),
            )
            sleeps: list[float] = []

            credentials = self._provider(
                token_path,
                post,
                sleep=sleeps.append,
                max_attempts=MAX_STS_ATTEMPTS,
            ).fetch()

        self.assertEqual(credentials.access_key_id, ACCESS_KEY)
        self.assertEqual(len(post.calls), MAX_STS_ATTEMPTS)
        self.assertEqual(sleeps, [0.1, 0.2, 0.4, 0.8])

    def test_does_not_retry_non_network_transport_errors(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            token_path = Path(temp_dir) / "token"
            token_path.write_text(TOKEN, encoding="utf-8")
            post = _ScriptedPost(
                RuntimeError(f"transport {TOKEN} {SECRET_KEY}"),
                _success_response(),
            )
            sleeps: list[float] = []

            with self.assertRaises(IRSAError) as raised:
                self._provider(
                    token_path,
                    post,
                    sleep=sleeps.append,
                ).fetch()

        self.assertEqual(len(post.calls), 1)
        self.assertEqual(sleeps, [])
        self.assertNotIn(TOKEN, str(raised.exception))
        self.assertNotIn(SECRET_KEY, str(raised.exception))

    def test_does_not_retry_terminal_http_error_or_expose_response(self) -> None:
        response_body = (
            f"oidc={TOKEN} ak={ACCESS_KEY} sk={SECRET_KEY} "
            "complete-response-body-sentinel"
        ).encode("utf-8")
        with tempfile.TemporaryDirectory() as temp_dir:
            token_path = Path(temp_dir) / "token"
            token_path.write_text(TOKEN, encoding="utf-8")
            post = _ScriptedPost(STSResponse(403, response_body))
            sleeps: list[float] = []

            with self.assertRaises(IRSAError) as raised:
                self._provider(
                    token_path,
                    post,
                    sleep=sleeps.append,
                ).fetch()

        self.assertEqual(len(post.calls), 1)
        self.assertEqual(sleeps, [])
        message = str(raised.exception)
        for forbidden in (
            TOKEN,
            ACCESS_KEY,
            SECRET_KEY,
            "complete-response-body-sentinel",
            response_body.decode("utf-8"),
        ):
            self.assertNotIn(forbidden, message)

    def test_malformed_success_is_sanitized_and_not_retried(self) -> None:
        response_body = json.dumps(
            {
                "Result": {
                    "Credentials": {
                        "AccessKeyId": ACCESS_KEY,
                        "SecretAccessKey": SECRET_KEY,
                        "unexpected": "full-response-body-sentinel",
                    }
                }
            }
        ).encode("utf-8")
        with tempfile.TemporaryDirectory() as temp_dir:
            token_path = Path(temp_dir) / "token"
            token_path.write_text(TOKEN, encoding="utf-8")
            post = _ScriptedPost(STSResponse(200, response_body))

            with self.assertRaises(IRSAError) as raised:
                self._provider(token_path, post).fetch()

        self.assertEqual(len(post.calls), 1)
        for forbidden in (ACCESS_KEY, SECRET_KEY, "full-response-body-sentinel"):
            self.assertNotIn(forbidden, str(raised.exception))

    def test_out_of_range_expiration_is_sanitized(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            token_path = Path(temp_dir) / "token"
            token_path.write_text(TOKEN, encoding="utf-8")
            post = _ScriptedPost(
                _success_response(expiration="0001-01-01T00:00:00+23:59")
            )

            with self.assertRaises(IRSAError) as raised:
                self._provider(token_path, post).fetch()

        self.assertIsNone(raised.exception.__context__)
        self.assertNotIn(ACCESS_KEY, str(raised.exception))
        self.assertNotIn(SECRET_KEY, str(raised.exception))

    def test_malformed_body_is_not_retained_as_exception_context(self) -> None:
        bodies = (
            b'{"secret":"full-response-body-sentinel"',
            b"\xfffull-response-body-sentinel",
            b"[" * 1100 + b"]" * 1100,
        )
        for body in bodies:
            with self.subTest(body=body[:1]):
                with tempfile.TemporaryDirectory() as temp_dir:
                    token_path = Path(temp_dir) / "token"
                    token_path.write_text(TOKEN, encoding="utf-8")
                    post = _ScriptedPost(STSResponse(200, body))

                    with self.assertRaises(IRSAError) as raised:
                        self._provider(token_path, post).fetch()

                self.assertIsNone(raised.exception.__context__)
                self.assertNotIn(
                    "full-response-body-sentinel", str(raised.exception)
                )

    def test_retry_limit_and_transport_error_are_sanitized(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            token_path = Path(temp_dir) / "token"
            token_path.write_text(TOKEN, encoding="utf-8")
            post = _ScriptedPost(
                *(OSError(f"network {TOKEN} {SECRET_KEY}") for _ in range(MAX_STS_ATTEMPTS))
            )

            with self.assertRaises(IRSAError) as raised:
                self._provider(
                    token_path,
                    post,
                    max_attempts=MAX_STS_ATTEMPTS,
                    sleep=lambda _delay: None,
                ).fetch()

        self.assertEqual(len(post.calls), MAX_STS_ATTEMPTS)
        self.assertNotIn(TOKEN, str(raised.exception))
        self.assertNotIn(SECRET_KEY, str(raised.exception))

    def test_configuration_and_missing_environment_fail_closed(self) -> None:
        with self.assertRaises(ValueError):
            VKEIRSAProvider(max_attempts=0)
        with self.assertRaises(ValueError):
            VKEIRSAProvider(max_attempts=MAX_STS_ATTEMPTS + 1)
        with self.assertRaises(ValueError):
            VKEIRSAProvider(timeout_seconds=0)
        with self.assertRaises(ValueError):
            VKEIRSAProvider(timeout_seconds=31)
        with self.assertRaises(ValueError):
            VKEIRSAProvider(timeout_seconds=float("nan"))
        with self.assertRaises(ValueError):
            VKEIRSAProvider(retry_backoff_seconds=float("nan"))
        with self.assertRaises(ValueError):
            VKEIRSAProvider(
                sts_endpoint=(
                    "https://attacker.example/"
                    "?Action=AssumeRoleWithOIDC&Version=2018-01-01"
                )
            )
        with self.assertRaises(ValueError):
            VKEIRSAProvider(
                sts_endpoint=(
                    "https://sts.volcengineapi.com/"
                    "?Action=AssumeRoleWithOIDC&Version=2018-01-01&Extra="
                )
            )
        with self.assertRaises(ValueError):
            VKEIRSAProvider(
                sts_endpoint=(
                    "https://sts.volcengineapi.com/\n"
                    "?Action=AssumeRoleWithOIDC&Version=2018-01-01"
                )
            )

        provider = VKEIRSAProvider(environment={}, http_post=_ScriptedPost())
        with self.assertRaises(IRSAError) as raised:
            provider.fetch()
        self.assertNotIn("OIDC", str(raised.exception).upper().replace("OIDC CONFIGURATION", ""))

    def test_empty_token_file_is_rejected_without_calling_sts(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            token_path = Path(temp_dir) / "token"
            token_path.write_text(" \n", encoding="utf-8")
            post = _ScriptedPost()
            provider = self._provider(token_path, post)

            with self.assertRaises(IRSAError):
                provider.fetch()

        self.assertEqual(post.calls, [])

    def test_builds_refreshable_tos_federation_credentials(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            token_path = Path(temp_dir) / "token"
            token_path.write_text(TOKEN, encoding="utf-8")
            post = _ScriptedPost(
                _success_response(access_key="first-ak"),
                _success_response(access_key="second-ak"),
            )
            irsa = self._provider(token_path, post)

            provider = create_federation_credentials(irsa, _FakeCredentialModule)
            first = provider.refresh()
            second = provider.refresh()

        self.assertIsInstance(provider, _FakeFederationCredentials)
        self.assertIsInstance(first, _FakeFederationToken)
        self.assertEqual(first.access_key_id, "first-ak")
        self.assertEqual(second.access_key_id, "second-ak")
        self.assertEqual(first.access_key_secret, SECRET_KEY)
        self.assertEqual(first.security_token, SESSION_TOKEN)
        self.assertEqual(len(post.calls), 2)


if __name__ == "__main__":
    unittest.main()
