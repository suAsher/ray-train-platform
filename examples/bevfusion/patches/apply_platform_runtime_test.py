"""Behavior tests for the ephemeral BEVFusion runtime patch command."""

from contextlib import redirect_stderr
import io
import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock


PATCH_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(PATCH_DIR))
spec = importlib.util.spec_from_file_location(
    "apply_platform_runtime",
    PATCH_DIR / "apply-platform-runtime.py",
)
assert spec is not None and spec.loader is not None
apply_platform_runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(apply_platform_runtime)


class ApplyPlatformRuntimeTest(unittest.TestCase):
    def test_missing_checkout_error_does_not_disclose_runtime_path(self):
        with tempfile.TemporaryDirectory(prefix="private-ray-working-dir-") as root:
            stderr = io.StringIO()
            with mock.patch.object(sys, "argv", ["apply-platform-runtime.py", root]):
                with redirect_stderr(stderr):
                    code = apply_platform_runtime.main()

            self.assertEqual(code, 2)
            message = stderr.getvalue()
            self.assertIn("missing tools/westwell_train.py", message)
            self.assertNotIn(root, message)
            self.assertNotIn("private-ray-working-dir", message)


if __name__ == "__main__":
    unittest.main(verbosity=2)
