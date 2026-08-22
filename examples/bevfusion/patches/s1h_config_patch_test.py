import pathlib
import sys
import unittest


sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))

from s1h_config_patch import patch_default_config, patch_transfusion_config


class S1HConfigPatchTest(unittest.TestCase):
    def test_removes_machine_local_checkpoint(self):
        source = """resume_from: null
load_from: /storage/run_dir/lidar_0706/epoch_20.pth
"""
        expected = """resume_from: null
load_from: null
"""
        self.assertEqual(patch_default_config(source), expected)

    def test_defines_the_missing_post_center_range(self):
        source = """      bbox_coder:
        pc_range: ${point_cloud_range[:2]}
        post_center_range: ${post_center_range}
        score_threshold: 0.0
"""
        expected = """      bbox_coder:
        pc_range: ${point_cloud_range[:2]}
        post_center_range: [-61.2, -61.2, -10.0, 61.2, 61.2, 10.0]
        score_threshold: 0.0
"""
        self.assertEqual(patch_transfusion_config(source), expected)

    def test_both_transforms_are_idempotent(self):
        default = "load_from: /storage/run_dir/lidar_0706/epoch_20.pth\n"
        transfusion = "post_center_range: ${post_center_range}\n"
        once_default = patch_default_config(default)
        once_transfusion = patch_transfusion_config(transfusion)
        self.assertEqual(patch_default_config(once_default), once_default)
        self.assertEqual(patch_transfusion_config(once_transfusion), once_transfusion)


if __name__ == "__main__":
    unittest.main(verbosity=2)
