import importlib
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock


PATCH_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(PATCH_DIR))


class RayTrainBEVFusionWorkerTests(unittest.TestCase):
    def setUp(self):
        self.module = importlib.import_module("raytrain_bevfusion_worker")

    def _checkout(self, root: Path) -> Path:
        (root / "tools").mkdir(parents=True)
        (root / "mmdet3d" / "apis").mkdir(parents=True)
        (root / "tools" / "westwell_train.py").write_text(
            "observed = True\n", encoding="utf-8"
        )
        (root / "mmdet3d" / "apis" / "train.py").write_text(
            "observed = True\n", encoding="utf-8"
        )
        return root

    def test_finds_checkout_only_below_runtime_search_roots(self):
        with tempfile.TemporaryDirectory() as temporary:
            checkout = self._checkout(Path(temporary) / "source")
            resolved = self.module.find_checkout(
                cwd=Path(temporary) / "empty",
                search_paths=(str(checkout),),
            )
        self.assertEqual(resolved, checkout.resolve())

    def test_prepare_uses_fixed_platform_commands_and_checkout(self):
        with tempfile.TemporaryDirectory() as temporary:
            checkout = self._checkout(Path(temporary) / "source").resolve()
            with mock.patch.object(self.module.subprocess, "run") as run:
                self.module.prepare_checkout(checkout, environ={"SAFE": "value"})

        arguments, keywords = run.call_args
        self.assertEqual(
            arguments[0],
            ["/usr/local/bin/raytrain-bevfusion-prepare", "/usr/bin/true"],
        )
        self.assertEqual(keywords["cwd"], checkout)
        self.assertTrue(keywords["check"])
        self.assertEqual(keywords["env"]["SAFE"], "value")
        self.assertEqual(keywords["env"]["RAYTRAIN_SOURCE_ROOT"], str(checkout))
        self.assertEqual(
            keywords["env"]["RAYTRAIN_BEVFUSION_PATCHER"],
            "/usr/local/bin/apply-raytrain-bevfusion-runtime",
        )

    def test_finder_never_selects_the_immutable_image_checkout(self):
        with tempfile.TemporaryDirectory() as temporary:
            image_checkout = self._checkout(Path(temporary) / "image").resolve()
            with mock.patch.object(
                self.module,
                "_IMMUTABLE_IMAGE_CHECKOUTS",
                (image_checkout,),
            ):
                with self.assertRaisesRegex(RuntimeError, "unavailable"):
                    self.module.find_checkout(
                        cwd=Path(temporary) / "empty",
                        search_paths=(str(image_checkout),),
                        environ={},
                    )

    def test_finder_never_selects_configured_image_source_checkout(self):
        with tempfile.TemporaryDirectory() as temporary:
            image_checkout = self._checkout(Path(temporary) / "image").resolve()
            with self.assertRaisesRegex(RuntimeError, "unavailable"):
                self.module.find_checkout(
                    cwd=Path(temporary) / "empty",
                    search_paths=(str(image_checkout),),
                    environ={
                        "RAYTRAIN_IMAGE_SOURCE_ROOT": str(image_checkout / "mmdet3d")
                    },
                )

    def test_trusted_hook_prepares_the_discovered_checkout(self):
        with tempfile.TemporaryDirectory() as temporary:
            checkout = self._checkout(Path(temporary) / "source").resolve()
            with (
                mock.patch.object(self.module, "find_checkout", return_value=checkout),
                mock.patch.object(self.module, "prepare_checkout") as prepare,
            ):
                self.module.prepare_current_worker()

        prepare.assert_called_once_with(checkout, environ=os.environ)


if __name__ == "__main__":
    unittest.main()
