import unittest
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from raytrain_publisher.site_metadata import sample_site_id


class SiteMetadataTest(unittest.TestCase):
    def test_only_declared_scene_relative_layouts_identify_sites(self):
        for path in ("cnfzhjyg/scene-a/samples/LIDAR_TOP/a.bin",
                     "labeled/cnfzhjyg/scene-a/samples/LIDAR_TOP/a.bin"):
            with self.subTest(path=path):
                sample = {"scene": "scene-a", "lidar_path": path}
                self.assertEqual(sample_site_id(sample), "cnfzhjyg")
                self.assertEqual(sample_site_id({"scene": "scene-a", "payloads": {"lidar": path}}), "cnfzhjyg")

    def test_legacy_ambiguous_and_unsafe_paths_remain_unknown(self):
        for path in ("scene-a/a.bin", "site/wrong-scene/a.bin", "site//scene-a/a.bin",
                     "/site/scene-a/a.bin", "site/../scene-a/a.bin",
                     "public/labeled/site/scene-a/a.bin", "raw/scene-a/a.bin",
                     "bad.site/scene-a/a.bin", "site/scene-a/./a.bin",
                     "site/scene-a/a\x00.bin", "x" * 129 + "/scene-a/a.bin"):
            with self.subTest(path=path):
                self.assertEqual(sample_site_id({"scene": "scene-a", "token": "cnfzhjyg-frame", "lidar_path": path}), "")
