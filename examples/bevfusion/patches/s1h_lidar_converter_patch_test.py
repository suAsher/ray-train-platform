"""Tests for the LiDAR-only S1H converter source patch."""

from __future__ import annotations

import ast
from pathlib import Path
import sys
import unittest


PATCH_DIR = Path(__file__).resolve().parent
if str(PATCH_DIR) not in sys.path:
    sys.path.insert(0, str(PATCH_DIR))


class S1HLidarConverterPatchTest(unittest.TestCase):
    def test_adds_scene_provenance_and_skips_camera_work_explicitly(self):
        try:
            from s1h_lidar_converter_patch import patch_converter_source
        except ModuleNotFoundError as error:
            raise AssertionError("LiDAR-only converter patch is not implemented") from error

        patched = patch_converter_source(self._baseline())

        self.assertIn("lidar_only=False", patched)
        self.assertIn("site_name=site_name, lidar_only=lidar_only", patched)
        self.assertIn('"scene_token": sample["scene_token"]', patched)
        self.assertIn("        if not lidar_only:\n            # obtain 6 image", patched)
        self.assertIn(
            'camera_types = _raytrain_camera_types(sample["data"], site_name)',
            patched,
        )
        self.assertIn("        # obtain sweeps for a single key-frame", patched)
        self.assertEqual(patch_converter_source(patched), patched)

    def test_rejects_unknown_converter_layout(self):
        from s1h_lidar_converter_patch import patch_converter_source

        with self.assertRaisesRegex(ValueError, "layout"):
            patch_converter_source("def unrelated():\n    pass\n")

    def test_selects_the_pinned_camera_family_from_each_samples_real_channels(self):
        from s1h_lidar_converter_patch import patch_converter_source

        patched = patch_converter_source(self._baseline())
        module = ast.parse(patched)
        helper = next(
            node
            for node in module.body
            if isinstance(node, ast.FunctionDef)
            and node.name == "_raytrain_camera_types"
        )
        namespace = {}
        exec(
            compile(ast.Module(body=[helper], type_ignores=[]), "<camera-helper>", "exec"),
            namespace,
        )
        select = namespace["_raytrain_camera_types"]

        self.assertEqual(
            select(
                {
                    "CAM_FRONT_MID": "front",
                    "CAM_FRONT_MID_RIGHT": "right",
                    "CAM_FRONT_MID_LEFT": "left",
                    "CAM_REAR_TOP_LEFT": "rear-left",
                    "CAM_REAR_TOP_RIGHT": "rear-right",
                }
            ),
            [
                "CAM_FRONT_MID",
                "CAM_FRONT_MID_RIGHT",
                "CAM_FRONT_MID_LEFT",
                "CAM_REAR_TOP_LEFT",
                "CAM_REAR_TOP_RIGHT",
            ],
        )
        self.assertEqual(
            select(
                {
                    "CAM_FRONT_MID": "front",
                    "CAM_FRONT_MID_RIGHT": "right",
                    "CAM_FRONT_LEFT_TOP": "left",
                    "CAM_REAR_LEFT": "rear-left",
                    "CAM_REAR_RIGHT": "rear-right",
                    "CAM_FRONT_LEFT": "extra-left",
                    "CAM_FRONT_RIGHT": "extra-right",
                }
            ),
            [
                "CAM_FRONT_MID",
                "CAM_FRONT_MID_RIGHT",
                "CAM_FRONT_LEFT_TOP",
                "CAM_REAR_LEFT",
                "CAM_REAR_RIGHT",
            ],
        )
        self.assertEqual(
            select(
                {
                    "CAM_FRONT_MID": "front",
                    "CAM_FRONT_RIGHT": "right",
                    "CAM_FRONT_LEFT": "left",
                    "CAM_REAR_LEFT": "rear-left",
                    "CAM_REAR_RIGHT": "rear-right",
                }
            ),
            [
                "CAM_FRONT_MID",
                "CAM_FRONT_RIGHT",
                "CAM_FRONT_LEFT",
                "CAM_REAR_LEFT",
                "CAM_REAR_RIGHT",
            ],
        )
        self.assertEqual(
            select(
                {
                    "CAM_MID FRONT": "front",
                    "CAM_FRONT RIGHT": "front-right",
                    "CAM_FRONT LEFT": "front-left",
                    "CAM_MID LEFT": "side-left",
                    "CAM_MID RIGHT": "side-right",
                }
            ),
            [
                "CAM_MID FRONT",
                "CAM_FRONT RIGHT",
                "CAM_FRONT LEFT",
                "CAM_MID LEFT",
                "CAM_MID RIGHT",
            ],
        )
        with self.assertRaisesRegex(KeyError, "unsupported camera layout"):
            select({"CAM_UNKNOWN": "token"})

    @staticmethod
    def _baseline() -> str:
        return '''def create_nuscenes_infos(
    root_path,
    out_dir,
    info_prefix,
    version="v1.0-trainval",
    max_sweeps=10,
    site_name=None,
    min_scene_samples=81,
):
    train_nusc_infos, val_nusc_infos = _fill_trainval_infos(
        nusc, train_scenes, val_scenes, test, max_sweeps=max_sweeps, site_name=site_name
    )

def _fill_trainval_infos(nusc, train_scenes, val_scenes, test=False, max_sweeps=10, site_name=None):
    for sample in nusc.sample:
        info = {
            "lidar_path": lidar_path,
            "location": location,
        }

        # obtain 6 image's information per frame
        camera_types = ["CAM_FRONT"]
        for cam in camera_types:
            cam_path = cam

        # obtain sweeps for a single key-frame
        sweeps = []
'''


if __name__ == "__main__":
    unittest.main(verbosity=2)
