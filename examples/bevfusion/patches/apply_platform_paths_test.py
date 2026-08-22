"""Integration tests for the idempotent dataset path patch installer."""

from pathlib import Path
import subprocess
import tempfile
import unittest


PATCH_DIR = Path(__file__).resolve().parent

SOURCE = '''from typing import Any, Dict

from mmdet.datasets import DATASETS

from .custom_3d import Custom3DDataset


@DATASETS.register_module()
class NuScenesDataset(Custom3DDataset):
    def get_data_info(self, index: int) -> Dict[str, Any]:
        info = self.data_infos[index]
        data = dict(
            lidar_path=info["lidar_path"],
            sweeps=info["sweeps"],
        )
        for _, camera_info in info["cams"].items():
            if camera_info:
                data["image_paths"].append(camera_info["data_path"])
        return data
'''


class ApplyPlatformPathsTest(unittest.TestCase):
    def apply(self, source: str):
        temporary = tempfile.TemporaryDirectory()
        checkout = Path(temporary.name)
        target = checkout / "mmdet3d/datasets/nuscenes_dataset.py"
        target.parent.mkdir(parents=True)
        target.write_text(source, encoding="utf-8")
        result = subprocess.run(
            ["bash", str(PATCH_DIR / "apply-platform-paths.sh"), str(checkout)],
            check=False,
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            self.fail(f"patch installer failed:\n{result.stdout}\n{result.stderr}")
        return temporary, target

    def test_keeps_the_import_before_the_registered_class_decorator(self):
        temporary, target = self.apply(SOURCE)
        self.addCleanup(temporary.cleanup)
        patched = target.read_text(encoding="utf-8")
        self.assertLess(
            patched.index("from .platform_paths import DatasetPathResolver"),
            patched.index("@DATASETS.register_module()"),
        )
        compile(patched, str(target), "exec")

    def test_repairs_the_previous_invalid_import_placement(self):
        broken = SOURCE.replace(
            "@DATASETS.register_module()\nclass NuScenesDataset",
            "@DATASETS.register_module()\n"
            "from .platform_paths import DatasetPathResolver\n\n\n"
            "class NuScenesDataset",
        )
        temporary, target = self.apply(broken)
        self.addCleanup(temporary.cleanup)
        patched = target.read_text(encoding="utf-8")
        compile(patched, str(target), "exec")
        self.assertEqual(patched.count("from .platform_paths import DatasetPathResolver"), 1)

    def test_second_application_is_a_no_op(self):
        temporary, target = self.apply(SOURCE)
        self.addCleanup(temporary.cleanup)
        first = target.read_bytes()
        subprocess.run(
            ["bash", str(PATCH_DIR / "apply-platform-paths.sh"), str(Path(temporary.name))],
            check=True,
            capture_output=True,
            text=True,
        )
        self.assertEqual(target.read_bytes(), first)


if __name__ == "__main__":
    unittest.main(verbosity=2)
