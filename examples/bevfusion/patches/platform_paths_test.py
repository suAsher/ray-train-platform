"""Tests for the dataset path resolver.

Run with: python3 examples/bevfusion/patches/platform_paths_test.py
"""

import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from platform_paths import DatasetPathResolver, discover_drop_count  # noqa: E402


def build_dataset(root):
    """Create a dataset tree shaped like the published BEVFusion data."""
    sample = os.path.join(root, "bevfusion", "fz-3dod-v1", "raw", "cnfzhjyg",
                          "cnfzhjyg", "uuid-1", "samples", "LIDAR_TOP")
    os.makedirs(sample, exist_ok=True)
    path = os.path.join(sample, "1755401392.199735.bin")
    with open(path, "wb") as handle:
        handle.write(b"\x00")
    return path


def build_renamed_namespace_dataset(root):
    """Create the real public layout used by the full FZ dataset."""
    sample = os.path.join(root, "cnfzhjyg", "uuid-1", "samples", "LIDAR_TOP")
    os.makedirs(sample, exist_ok=True)
    path = os.path.join(sample, "1755401392.199735.bin")
    with open(path, "wb") as handle:
        handle.write(b"\x00")
    return path


def build_namespace_sample(root, namespace, scene, filename):
    sample = os.path.join(root, namespace, scene, "samples", "LIDAR_TOP")
    os.makedirs(sample, exist_ok=True)
    path = os.path.join(sample, filename)
    with open(path, "wb") as handle:
        handle.write(b"\x00")
    return path


class DiscoverDropCountTest(unittest.TestCase):
    def test_finds_the_prefix_recorded_by_another_machine(self):
        with tempfile.TemporaryDirectory() as root:
            build_dataset(root)
            recorded = ("/mnt/storage/public/bevfusion/fz-3dod-v1/raw/cnfzhjyg/"
                        "cnfzhjyg/uuid-1/samples/LIDAR_TOP/1755401392.199735.bin")
            # "/mnt/storage/public" is three components that must be dropped.
            self.assertEqual(discover_drop_count(recorded, root), 3)

    def test_returns_none_when_nothing_resolves(self):
        with tempfile.TemporaryDirectory() as root:
            self.assertIsNone(discover_drop_count("/a/b/c/missing.bin", root))

    def test_prefers_the_longest_matching_suffix(self):
        # A short tail such as LIDAR_TOP/x.bin could also exist directly under
        # the root. Matching it would silently read the wrong file, so the
        # longest (most specific) suffix has to win.
        with tempfile.TemporaryDirectory() as root:
            build_dataset(root)
            decoy = os.path.join(root, "samples", "LIDAR_TOP")
            os.makedirs(decoy, exist_ok=True)
            with open(os.path.join(decoy, "1755401392.199735.bin"), "wb") as handle:
                handle.write(b"\xff")
            recorded = ("/mnt/storage/public/bevfusion/fz-3dod-v1/raw/cnfzhjyg/"
                        "cnfzhjyg/uuid-1/samples/LIDAR_TOP/1755401392.199735.bin")
            drop = discover_drop_count(recorded, root)
            resolved = os.path.join(root, *recorded.split("/")[drop + 1:])
            self.assertIn("uuid-1", resolved)


class DatasetPathResolverTest(unittest.TestCase):
    def test_reroots_when_the_published_namespace_was_renamed(self):
        # The full FZ pkl records /temp_data/fz/<scene>/..., while the public
        # dataset is published as /mnt/storage/public/cnfzhjyg/<scene>/....
        # A data-version directory rename must not require rewriting the pkl.
        with tempfile.TemporaryDirectory() as root:
            real = build_renamed_namespace_dataset(root)
            recorded = ("/temp_data/fz/uuid-1/samples/LIDAR_TOP/"
                        "1755401392.199735.bin")
            self.assertEqual(DatasetPathResolver(root).resolve(recorded), real)

    def test_reroots_every_path_after_learning_the_prefix_once(self):
        with tempfile.TemporaryDirectory() as root:
            real = build_dataset(root)
            resolver = DatasetPathResolver(root)
            recorded = ("/mnt/storage/public/bevfusion/fz-3dod-v1/raw/cnfzhjyg/"
                        "cnfzhjyg/uuid-1/samples/LIDAR_TOP/1755401392.199735.bin")
            self.assertEqual(resolver.resolve(recorded), real)
            # A second path with the same recorded prefix reuses the learned
            # drop count without probing again.
            camera = recorded.replace("LIDAR_TOP", "CAM_FRONT").replace(".bin", ".jpg")
            real_camera = real.replace("LIDAR_TOP", "CAM_FRONT").replace(".bin", ".jpg")
            os.makedirs(os.path.dirname(real_camera), exist_ok=True)
            with open(real_camera, "wb") as handle:
                handle.write(b"\x00")
            self.assertEqual(resolver.resolve(camera), real_camera)

    def test_leaves_an_already_correct_path_untouched(self):
        with tempfile.TemporaryDirectory() as root:
            real = build_dataset(root)
            self.assertEqual(DatasetPathResolver(root).resolve(real), real)

    def test_does_not_trust_an_existing_absolute_path_outside_the_selected_root(self):
        with tempfile.TemporaryDirectory() as parent:
            root = os.path.join(parent, "mounted")
            os.makedirs(root)
            outside = os.path.join(parent, "sample.bin")
            with open(outside, "wb") as handle:
                handle.write(b"outside")
            inside = os.path.join(root, "sample.bin")
            with open(inside, "wb") as handle:
                handle.write(b"inside")
            self.assertEqual(DatasetPathResolver(root).resolve(outside), inside)

    def test_rediscovers_a_rewrite_when_a_later_sample_uses_another_namespace(self):
        with tempfile.TemporaryDirectory() as root:
            first = build_namespace_sample(root, "namespace-a", "scene-1", "first.bin")
            second = build_namespace_sample(root, "namespace-b", "scene-2", "second.bin")
            resolver = DatasetPathResolver(root)
            self.assertEqual(
                resolver.resolve("/legacy/a/scene-1/samples/LIDAR_TOP/first.bin"),
                first,
            )
            self.assertEqual(
                resolver.resolve("/legacy/b/scene-2/samples/LIDAR_TOP/second.bin"),
                second,
            )

    def test_is_inert_without_the_platform_environment_variable(self):
        resolver = DatasetPathResolver(None)
        self.assertFalse(resolver.active)
        self.assertEqual(resolver.resolve("/original/path.bin"), "/original/path.bin")

    def test_preserves_the_original_path_when_nothing_resolves(self):
        # Rewriting to a wrong location would replace a clear FileNotFoundError
        # with a confusing one pointing at a path the user never configured.
        with tempfile.TemporaryDirectory() as root:
            resolver = DatasetPathResolver(root)
            self.assertEqual(resolver.resolve("/nope/missing.bin"), "/nope/missing.bin")

    def test_never_resolves_parent_segments_outside_the_mounted_root(self):
        with tempfile.TemporaryDirectory() as parent:
            root = os.path.join(parent, "mounted")
            os.makedirs(root)
            secret = os.path.join(parent, "secret.bin")
            with open(secret, "wb") as handle:
                handle.write(b"not dataset content")
            recorded = "/../secret.bin"
            self.assertEqual(DatasetPathResolver(root).resolve(recorded), recorded)

    def test_ignores_non_string_values(self):
        resolver = DatasetPathResolver("/tmp")
        for value in (None, 42, ["/a"], {"a": 1}):
            self.assertEqual(resolver.resolve(value), value)

    def test_reroots_sweep_paths_and_keeps_other_keys(self):
        with tempfile.TemporaryDirectory() as root:
            build_dataset(root)
            resolver = DatasetPathResolver(root)
            recorded = ("/mnt/storage/public/bevfusion/fz-3dod-v1/raw/cnfzhjyg/"
                        "cnfzhjyg/uuid-1/samples/LIDAR_TOP/1755401392.199735.bin")
            sweeps = [{"data_path": recorded, "timestamp": 123, "sensor2lidar_rotation": "keep"}]
            resolved = resolver.resolve_sweeps(sweeps)
            self.assertTrue(resolved[0]["data_path"].startswith(root))
            self.assertEqual(resolved[0]["timestamp"], 123)
            self.assertEqual(resolved[0]["sensor2lidar_rotation"], "keep")
            # The caller's list must not be mutated in place.
            self.assertEqual(sweeps[0]["data_path"], recorded)

    def test_handles_an_empty_sweep_list(self):
        self.assertEqual(DatasetPathResolver("/tmp").resolve_sweeps([]), [])
        self.assertEqual(DatasetPathResolver("/tmp").resolve_sweeps(None), [])


if __name__ == "__main__":
    unittest.main(verbosity=2)
