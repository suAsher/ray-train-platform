#!/usr/bin/env python3
"""Tests for the BEVFusion Portal acceptance client."""

from __future__ import annotations

import contextlib
from email.message import Message
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest
from unittest import mock
import urllib.error

import e2e_bevfusion_portal_submit as portal


DIGEST_IMAGE = "registry.example/bevfusion@sha256:" + "a" * 64


class RecordingHTTPClient:
    def __init__(self, responses=None, failure=None):
        self.responses = list(responses or [])
        self.failure = failure
        self.calls = []

    def request(
        self,
        method,
        url,
        *,
        headers=None,
        json_body=None,
        body=None,
        timeout=None,
    ):
        copied_headers = dict(headers or {})
        uploaded = body.read() if body is not None else None
        self.calls.append(
            {
                "method": method,
                "url": url,
                "headers": copied_headers,
                "json_body": json_body,
                "body": uploaded,
                "timeout": timeout,
            }
        )
        if self.failure is not None:
            raise self.failure(method, url, copied_headers)
        if not self.responses:
            return None
        return self.responses.pop(0)


class SourceDiscoveryTests(unittest.TestCase):
    def test_git_discovery_honors_gitignore_and_keeps_relative_directories(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            (root / ".gitignore").write_text("ignored/\n*.cache\n", encoding="utf-8")
            (root / "tracked").mkdir()
            (root / "tracked" / "train.py").write_text("train\n", encoding="utf-8")
            (root / "untracked").mkdir()
            (root / "untracked" / "config.py").write_text("config\n", encoding="utf-8")
            (root / "ignored").mkdir()
            (root / "ignored" / "secret.py").write_text("ignored\n", encoding="utf-8")
            (root / "local.cache").write_text("ignored\n", encoding="utf-8")
            subprocess.run(
                ["git", "-C", str(root), "add", ".gitignore", "tracked/train.py"],
                check=True,
            )

            discovered = portal.discover_source_files(root)

            self.assertEqual(
                [source.relative_path for source in discovered],
                [".gitignore", "tracked/train.py", "untracked/config.py"],
            )

    def test_recursive_discovery_rejects_symlinks(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "train.py").write_text("train\n", encoding="utf-8")
            os.symlink("train.py", root / "linked.py")

            with self.assertRaisesRegex(portal.AcceptanceError, "linked.py"):
                portal.discover_source_files(root)

    def test_recursive_discovery_excludes_dot_git_and_rejects_special_files(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / ".git").mkdir()
            (root / ".git" / "internal").write_text("hidden\n", encoding="utf-8")
            (root / "train.py").write_text("train\n", encoding="utf-8")

            discovered = portal.discover_source_files(root)

            self.assertEqual([source.relative_path for source in discovered], ["train.py"])
            self.assertTrue(stat.S_ISREG(discovered[0].mode))

    def test_recursive_discovery_excludes_dot_git_marker_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / ".git").write_text("gitdir: elsewhere\n", encoding="utf-8")
            (root / "train.py").write_text("train\n", encoding="utf-8")

            discovered = portal.discover_source_files(root)

            self.assertEqual([source.relative_path for source in discovered], ["train.py"])


class ConfigurationTests(unittest.TestCase):
    def test_help_requires_a_dedicated_interactive_session_config(self):
        help_text = portal.build_parser().format_help()

        self.assertIn("interactive session token", help_text)
        self.assertIn(
            "spk-rayjob login --username guofeng.su --password-stdin --config <dedicated-path>",
            help_text,
        )
        self.assertIn(
            "Short-lived PATs are only for spk-rayjob/native submission flows.",
            help_text,
        )

    def test_load_token_requires_owner_only_config(self):
        with tempfile.TemporaryDirectory() as temporary:
            config = Path(temporary) / "config.json"
            config.write_text(json.dumps({"token": "top-secret"}), encoding="utf-8")
            config.chmod(0o640)

            with self.assertRaisesRegex(portal.AcceptanceError, "owner-only") as caught:
                portal.load_token(config)

            self.assertNotIn("top-secret", str(caught.exception))

    def test_load_token_returns_token_without_formatting_it(self):
        with tempfile.TemporaryDirectory() as temporary:
            config = Path(temporary) / "config.json"
            config.write_text(json.dumps({"token": "top-secret"}), encoding="utf-8")
            config.chmod(0o600)

            self.assertEqual(portal.load_token(config), "top-secret")


class PayloadTests(unittest.TestCase):
    def test_job_payload_uses_workspace_snapshot_and_default_2_by_8_ray_train(self):
        options = portal.JobOptions(
            name="BEVFusion Smoke 01",
            image=DIGEST_IMAGE,
            entrypoint='python3 tools/train.py --work-dir "run one"',
            input_path="bevfusion/fz-3dod-v1",
            output_path="bevfusion/smoke-01",
            timeout=7200,
        )

        payload = portal.build_job_payload(options, "snapshot-123")

        spec = payload["spec"]
        self.assertEqual(spec["name"], "bevfusion-smoke-01")
        self.assertEqual(spec["source"], {"type": "workspace", "snapshot": "snapshot-123"})
        self.assertEqual(spec["entrypoint"], {"command": ["python3"], "args": ["tools/train.py", "--work-dir", "run one"]})
        self.assertEqual(spec["execution"], {"mode": "ray_train"})
        self.assertEqual(
            spec["resources"],
            {
                "workerReplicas": 2,
                "gpusPerWorker": 8,
                "cpuPerWorker": 32,
                "memoryPerWorker": "128Gi",
            },
        )
        self.assertEqual(spec["input"], {"space": "public", "relativePath": "bevfusion/fz-3dod-v1"})
        self.assertEqual(spec["output"], {"space": "my-runs", "relativePath": "bevfusion/smoke-01"})
        self.assertEqual(spec["queue"], "")
        self.assertEqual(spec["timeoutSeconds"], 7200)
        self.assertEqual(spec["cleanupPolicy"]["successTtlSeconds"], 60)
        self.assertGreaterEqual(
            spec["cleanupPolicy"]["failureTtlSeconds"],
            spec["cleanupPolicy"]["successTtlSeconds"],
        )

    def test_entrypoint_rejects_shell_only_or_unbalanced_input(self):
        base = dict(
            name="bevfusion-smoke",
            image=DIGEST_IMAGE,
            input_path="bevfusion/data",
            output_path="bevfusion/output",
        )
        with self.assertRaises(portal.AcceptanceError):
            portal.build_job_payload(portal.JobOptions(entrypoint="", **base), "snapshot-1")
        with self.assertRaises(portal.AcceptanceError):
            portal.build_job_payload(portal.JobOptions(entrypoint="python 'unterminated", **base), "snapshot-1")


class AuthenticationBoundaryTests(unittest.TestCase):
    def test_unauthorized_workspace_api_explains_interactive_login_without_token(self):
        token = "pat-or-expired-session-must-stay-secret"
        for status in (401, 403):
            with self.subTest(status=status):
                http = RecordingHTTPClient(
                    failure=lambda _method, _url, _headers: portal.HTTPStatusError(status)
                )
                client = portal.PortalAcceptanceClient(
                    "https://platform.example", token, http_client=http
                )

                with self.assertRaises(portal.AcceptanceError) as caught:
                    client.create_snapshot("acceptance/bevfusion-smoke")

                message = str(caught.exception)
                self.assertIn("interactive session token", message)
                self.assertIn(
                    "spk-rayjob login --username guofeng.su --password-stdin --config <dedicated-path>",
                    message,
                )
                self.assertNotIn(token, message)

    def test_bearer_is_sent_only_to_platform_api_not_presigned_url(self):
        token = "platform-token-must-stay-secret"
        signed_url = "https://objects.example/upload?X-Signature=private"
        http = RecordingHTTPClient(
            responses=[
                {
                    "success": True,
                    "data": {
                        "url": signed_url,
                        "requiredHeaders": {"Content-Type": "text/x-python"},
                    },
                },
                b"",
            ]
        )
        output = io.StringIO()
        client = portal.PortalAcceptanceClient(
            "https://platform.example", token, http_client=http, output=output
        )
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "nested" / "train.py"
            path.parent.mkdir()
            path.write_bytes(b"print('ok')\n")
            source = portal._source_file(Path(temporary), "nested/train.py")

            client.upload_sources([source], "acceptance/bevfusion-smoke")

        platform_call, upload_call = http.calls
        self.assertEqual(platform_call["headers"]["Authorization"], f"Bearer {token}")
        self.assertNotIn("Authorization", upload_call["headers"])
        self.assertEqual(upload_call["url"], signed_url)
        self.assertEqual(upload_call["body"], b"print('ok')\n")
        formatted = output.getvalue()
        self.assertIn("1/1", formatted)
        self.assertIn("nested/train.py", formatted)
        self.assertNotIn(token, formatted)
        self.assertNotIn(signed_url, formatted)

    def test_zero_byte_python_package_marker_is_uploaded_unchanged(self):
        http = RecordingHTTPClient(
            responses=[
                {
                    "success": True,
                    "data": {
                        "url": "https://objects.example/empty-upload?X-Signature=private",
                        "requiredHeaders": {"Content-Type": "text/x-python"},
                    },
                },
                b"",
            ]
        )
        client = portal.PortalAcceptanceClient(
            "https://platform.example",
            "interactive-session",
            http_client=http,
            output=io.StringIO(),
        )
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "mmdet3d" / "__init__.py"
            path.parent.mkdir()
            path.write_bytes(b"")
            source = portal._source_file(Path(temporary), "mmdet3d/__init__.py")

            client.upload_sources([source], "acceptance/bevfusion-smoke")

        self.assertEqual(http.calls[0]["json_body"]["sizeBytes"], 0)
        self.assertEqual(http.calls[1]["body"], b"")
        self.assertEqual(http.calls[1]["headers"]["Content-Length"], "0")

    def test_replaced_same_size_source_is_rejected_before_object_upload(self):
        signed_url = "https://objects.example/upload?X-Signature=private"
        http = RecordingHTTPClient(
            responses=[
                {
                    "success": True,
                    "data": {
                        "url": signed_url,
                        "requiredHeaders": {"Content-Type": "text/x-python"},
                    },
                }
            ]
        )
        client = portal.PortalAcceptanceClient(
            "https://platform.example",
            "interactive-session",
            http_client=http,
            output=io.StringIO(),
        )
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            path = root / "train.py"
            path.write_bytes(b"first")
            source = portal.discover_source_files(root)[0]
            path.unlink()
            path.write_bytes(b"other")

            with self.assertRaisesRegex(portal.AcceptanceError, "source changed"):
                client.upload_sources([source], "acceptance/bevfusion-smoke")

        self.assertEqual(len(http.calls), 1)
        self.assertEqual(http.calls[0]["method"], "POST")

    def test_transport_exceptions_and_terminal_output_never_expose_credentials(self):
        token = "exception-secret-token"
        signed_url = "https://objects.example/upload?credential=private"

        def failure(_method, url, headers):
            return RuntimeError(f"transport url={url} auth={headers.get('Authorization')} token={token}")

        output = io.StringIO()
        client = portal.PortalAcceptanceClient(
            "https://platform.example",
            token,
            http_client=RecordingHTTPClient(failure=failure),
            output=output,
        )

        with self.assertRaises(portal.AcceptanceError) as caught:
            client.create_snapshot("acceptance/bevfusion-smoke")

        rendered = str(caught.exception) + output.getvalue()
        self.assertNotIn(token, rendered)
        self.assertNotIn("Bearer", rendered)
        self.assertNotIn(signed_url, rendered)

    def test_upload_failure_names_relative_file_but_hides_signed_url(self):
        signed_url = "https://objects.example/upload?credential=private"

        class FailOnPut(RecordingHTTPClient):
            def request(self, method, url, **kwargs):
                if method == "PUT":
                    raise RuntimeError(f"failed at {url}")
                return {
                    "success": True,
                    "data": {
                        "url": signed_url,
                        "requiredHeaders": {"Content-Type": "text/plain"},
                    },
                }

        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "train.py"
            path.write_text("train\n", encoding="utf-8")
            source = portal._source_file(Path(temporary), "train.py")
            client = portal.PortalAcceptanceClient(
                "https://platform.example", "secret", http_client=FailOnPut()
            )

            with self.assertRaisesRegex(portal.AcceptanceError, "train.py") as caught:
                client.upload_sources([source], "acceptance/bevfusion-smoke")

        self.assertNotIn(signed_url, str(caught.exception))
        self.assertNotIn("secret", str(caught.exception))


class WorkflowTests(unittest.TestCase):
    def test_run_uploads_snapshots_submits_and_polls_without_leaking_credentials(self):
        token = "interactive-session-secret"
        signed_url = "https://objects.example/upload?credential=private"
        responses = [
            {
                "success": True,
                "data": {
                    "url": signed_url,
                    "requiredHeaders": {"Content-Type": "text/x-python"},
                },
            },
            b"",
            {"success": True, "data": {"id": "snapshot-abc"}},
            {"success": True, "data": {"id": "job-abc"}},
            {
                "success": True,
                "data": {
                    "id": "job-abc",
                    "observedState": "SUCCEEDED",
                    "createdAt": "2026-08-20T01:00:00Z",
                    "startedAt": "2026-08-20T01:01:00Z",
                    "finishedAt": "2026-08-20T01:02:00Z",
                },
            },
        ]
        http = RecordingHTTPClient(responses=responses)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source_dir = root / "source"
            source_dir.mkdir()
            (source_dir / "train.py").write_text("print('train')\n", encoding="utf-8")
            config = root / "session.json"
            config.write_text(json.dumps({"token": token}), encoding="utf-8")
            config.chmod(0o600)
            args = portal.build_parser().parse_args(
                [
                    "--source-dir",
                    str(source_dir),
                    "--config",
                    str(config),
                    "--image",
                    DIGEST_IMAGE,
                    "--name",
                    "BEVFusion Full Flow",
                    "--entrypoint",
                    "python3 train.py --epochs 1",
                    "--input-path",
                    "bevfusion/fz-3dod-v1",
                    "--output-path",
                    "bevfusion/full-flow",
                ]
            )
            output = io.StringIO()

            with contextlib.redirect_stdout(output):
                result = portal.run(args, http_client=http)

        self.assertEqual(result, 0)
        self.assertEqual(
            [(call["method"], call["url"]) for call in http.calls],
            [
                ("POST", "https://raytrain.wellspiking.ai/api/v1/data-spaces/workspace/uploads"),
                ("PUT", signed_url),
                ("POST", "https://raytrain.wellspiking.ai/api/v1/workspace-snapshots"),
                ("POST", "https://raytrain.wellspiking.ai/api/v1/jobs"),
                ("GET", "https://raytrain.wellspiking.ai/api/v1/jobs/job-abc"),
            ],
        )
        self.assertEqual(
            http.calls[2]["json_body"],
            {"sourcePath": "acceptance/bevfusion-full-flow"},
        )
        self.assertEqual(
            http.calls[3]["json_body"]["spec"]["resources"]["workerReplicas"], 2
        )
        rendered = output.getvalue()
        self.assertIn("JOB_ID=job-abc", rendered)
        self.assertIn("SNAPSHOT_ID=snapshot-abc", rendered)
        self.assertIn("STATE=SUCCEEDED", rendered)
        self.assertIn("FINISHED_AT=2026-08-20T01:02:00Z", rendered)
        self.assertNotIn(token, rendered)
        self.assertNotIn(signed_url, rendered)

    def test_urllib_transport_decodes_json_and_sanitizes_http_errors(self):
        response_headers = Message()
        response_headers.add_header("Content-Type", "application/json")

        class Response:
            headers = response_headers

            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return False

            def read(self, _limit):
                return b'{"success":true,"data":{"id":"job-abc"}}'

        transport = portal.UrllibHTTPClient()
        with mock.patch("urllib.request.urlopen", return_value=Response()) as opened:
            decoded = transport.request(
                "POST",
                "https://platform.example/api/v1/jobs",
                headers={"Authorization": "Bearer secret"},
                json_body={"spec": {}},
                timeout=15,
            )

        self.assertEqual(decoded["data"]["id"], "job-abc")
        request = opened.call_args.args[0]
        self.assertEqual(request.get_method(), "POST")
        self.assertEqual(json.loads(request.data), {"spec": {}})
        self.assertEqual(opened.call_args.kwargs["timeout"], 15)

        http_error = urllib.error.HTTPError(
            "https://objects.example/?credential=private", 403, "forbidden", {}, None
        )
        with mock.patch("urllib.request.urlopen", side_effect=http_error):
            with self.assertRaises(portal.HTTPStatusError) as caught:
                transport.request("GET", "https://platform.example/api/v1/jobs")

        self.assertEqual(caught.exception.status, 403)
        self.assertNotIn("credential", str(caught.exception))


if __name__ == "__main__":
    unittest.main()
