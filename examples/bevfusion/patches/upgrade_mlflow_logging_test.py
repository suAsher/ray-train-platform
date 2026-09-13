#!/usr/bin/env python3
"""Tests for the BEVFusion platform MLflow logging upgrade command."""

from __future__ import annotations

from contextlib import redirect_stderr, redirect_stdout
import importlib.util
import io
import os
from pathlib import Path
import stat
import tempfile
import unittest
from unittest import mock


PATCH_DIR = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location(
    "upgrade_mlflow_logging",
    PATCH_DIR / "upgrade_mlflow_logging.py",
)
assert spec is not None and spec.loader is not None
upgrade_mlflow_logging = importlib.util.module_from_spec(spec)
spec.loader.exec_module(upgrade_mlflow_logging)


BASE_SOURCE = '''"""User-maintained platform MLflow bridge."""

import os


def start_platform_mlflow(cfg, rank, world_size):
    """Start the governed MLflow run and install MMCV's scalar logger."""
    tracking_uri = os.environ.get("MLFLOW_TRACKING_URI", "").strip()
    if not tracking_uri or rank != 0:
        return None

    import mlflow

    mlflow.set_tracking_uri(tracking_uri)
    mlflow.log_params(
        {
            "world_size": world_size,
            "custom_owner": cfg.owner,
        }
    )
    return mlflow


def finish_platform_mlflow(client, status):
    if client is not None:
        client.end_run(status=status)
'''


class UpgradeMLflowLoggingTest(unittest.TestCase):
    def write_checkout(self, source: str = BASE_SOURCE, *, mode: int = 0o640):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        checkout = Path(temp.name)
        target = checkout / "mmdet3d" / "utils" / "platform_mlflow.py"
        target.parent.mkdir(parents=True)
        with target.open("w", encoding="utf-8", newline="") as handle:
            handle.write(source)
        os.chmod(target, mode)
        return checkout, target

    def read_raw(self, target: Path) -> str:
        with target.open("r", encoding="utf-8", newline="") as handle:
            return handle.read()

    def test_template_logging_guard_matches_upgrade_script(self):
        template = (PATCH_DIR / "platform_mlflow.py").read_text(encoding="utf-8")
        start, end = upgrade_mlflow_logging._find_start_platform_mlflow(
            template,
            PATCH_DIR / "platform_mlflow.py",
        )
        function_source = template[start:end]

        self.assertEqual(
            function_source.count(upgrade_mlflow_logging.MLFLOW_LOGGING_BLOCK),
            1,
        )
        self.assertIn(
            upgrade_mlflow_logging._anchor_after(
                upgrade_mlflow_logging.MLFLOW_LOGGING_BLOCK,
                "\n",
            ),
            function_source,
        )

    def test_check_reports_pending_without_modifying_source(self):
        checkout, target = self.write_checkout()
        before = self.read_raw(target)
        stdout = io.StringIO()

        with redirect_stdout(stdout):
            code = upgrade_mlflow_logging.main([str(checkout)])

        self.assertEqual(code, 0)
        self.assertIn("needs upgrade: mmdet3d/utils/platform_mlflow.py", stdout.getvalue())
        self.assertEqual(self.read_raw(target), before)

    def test_apply_is_idempotent_and_preserves_user_provenance_and_mode(self):
        checkout, target = self.write_checkout(mode=0o600)

        first = upgrade_mlflow_logging.upgrade_checkout(checkout, apply=True)
        second = upgrade_mlflow_logging.upgrade_checkout(checkout, apply=True)

        upgraded = self.read_raw(target)
        self.assertTrue(first.changed)
        self.assertTrue(first.applied)
        self.assertFalse(second.changed)
        self.assertEqual(upgraded.count(upgrade_mlflow_logging.MLFLOW_LOGGING_BLOCK), 1)
        self.assertIn('"custom_owner": cfg.owner', upgraded)
        self.assertEqual(stat.S_IMODE(target.stat().st_mode), 0o600)
        compile(upgraded, str(target), "exec")

    def test_apply_preserves_crlf_newlines(self):
        checkout, target = self.write_checkout(BASE_SOURCE.replace("\n", "\r\n"))

        result = upgrade_mlflow_logging.upgrade_checkout(checkout, apply=True)

        upgraded = self.read_raw(target)
        self.assertTrue(result.changed)
        self.assertIn(
            upgrade_mlflow_logging.MLFLOW_LOGGING_BLOCK.replace("\n", "\r\n"),
            upgraded,
        )
        self.assertNotIn(upgrade_mlflow_logging.MLFLOW_LOGGING_BLOCK, upgraded)

    def test_unknown_partial_upgrade_layout_is_rejected(self):
        source = BASE_SOURCE.replace(
            "    import mlflow\n",
            '    training_logger = logging.getLogger("mmdet3d")\n'
            "    training_logger.propagate = False\n"
            "    import mlflow\n",
        )
        checkout, target = self.write_checkout(source)
        before = self.read_raw(target)

        stderr = io.StringIO()
        with redirect_stderr(stderr):
            code = upgrade_mlflow_logging.main(["--apply", str(checkout)])

        self.assertEqual(code, 2)
        self.assertIn("unknown partial logging guard", stderr.getvalue())
        self.assertEqual(self.read_raw(target), before)

    def test_misplaced_complete_guard_is_rejected(self):
        source = BASE_SOURCE.replace(
            "    import mlflow\n",
            "    import mlflow\n\n" + upgrade_mlflow_logging.MLFLOW_LOGGING_BLOCK,
        )
        checkout, target = self.write_checkout(source)
        before = self.read_raw(target)

        stderr = io.StringIO()
        with redirect_stderr(stderr):
            code = upgrade_mlflow_logging.main(["--apply", str(checkout)])

        self.assertEqual(code, 2)
        self.assertIn("misplaced logging guard", stderr.getvalue())
        self.assertEqual(self.read_raw(target), before)

    def test_symlink_target_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            checkout = Path(temp) / "checkout"
            target = checkout / "mmdet3d" / "utils" / "platform_mlflow.py"
            target.parent.mkdir(parents=True)
            real_target = Path(temp) / "real_platform_mlflow.py"
            real_target.write_text(BASE_SOURCE, encoding="utf-8")
            target.symlink_to(real_target)

            stderr = io.StringIO()
            with redirect_stderr(stderr):
                code = upgrade_mlflow_logging.main(["--apply", str(checkout)])

            self.assertEqual(code, 2)
            self.assertIn("refusing to modify symlink target", stderr.getvalue())
            self.assertEqual(real_target.read_text(encoding="utf-8"), BASE_SOURCE)

    def test_symlink_parent_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            checkout = Path(temp) / "checkout"
            real_tree = Path(temp) / "real_tree"
            real_target = real_tree / "utils" / "platform_mlflow.py"
            real_target.parent.mkdir(parents=True)
            real_target.write_text(BASE_SOURCE, encoding="utf-8")
            checkout.mkdir()
            (checkout / "mmdet3d").symlink_to(real_tree, target_is_directory=True)

            stderr = io.StringIO()
            with redirect_stderr(stderr):
                code = upgrade_mlflow_logging.main(["--apply", str(checkout)])

            self.assertEqual(code, 2)
            self.assertIn("refusing to modify symlink target", stderr.getvalue())
            self.assertEqual(real_target.read_text(encoding="utf-8"), BASE_SOURCE)

    def test_concurrent_target_change_is_rejected(self):
        checkout, target = self.write_checkout()
        original_reader = upgrade_mlflow_logging._read_source
        calls = 0

        def changing_reader(path):
            nonlocal calls
            calls += 1
            if calls == 2:
                with path.open("a", encoding="utf-8", newline="") as handle:
                    handle.write("\n# user edit while command was running\n")
            return original_reader(path)

        with mock.patch.object(
            upgrade_mlflow_logging,
            "_read_source",
            side_effect=changing_reader,
        ):
            with self.assertRaisesRegex(upgrade_mlflow_logging.UpgradeError, "target changed"):
                upgrade_mlflow_logging.upgrade_checkout(checkout, apply=True)

    def test_missing_supported_anchor_is_rejected(self):
        source = BASE_SOURCE.replace("    import mlflow\n", "    import mlflow.tracking\n")
        checkout, target = self.write_checkout(source)
        before = self.read_raw(target)

        stderr = io.StringIO()
        with redirect_stderr(stderr):
            code = upgrade_mlflow_logging.main(["--apply", str(checkout)])

        self.assertEqual(code, 2)
        self.assertIn("layout is not a supported version", stderr.getvalue())
        self.assertEqual(self.read_raw(target), before)


if __name__ == "__main__":
    unittest.main(verbosity=2)
