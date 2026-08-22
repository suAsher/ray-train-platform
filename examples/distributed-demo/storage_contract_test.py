import tempfile
import unittest
from pathlib import Path

from storage_contract import expected_file


class ExpectedFileTest(unittest.TestCase):
    def test_resolves_one_explicit_file_below_dataset_root(self):
        with tempfile.TemporaryDirectory() as root:
            self.assertEqual(
                expected_file(root, "annotations/train.pkl"),
                Path(root) / "annotations" / "train.pkl",
            )

    def test_rejects_absolute_and_parent_paths(self):
        for value in ("/etc/passwd", "../outside", "annotations/../outside", ""):
            with self.subTest(value=value), self.assertRaises(ValueError):
                expected_file("/mnt/storage/public", value)

    def test_single_gpu_smoke_does_not_request_a_second_ray_gpu(self):
        script = Path(__file__).with_name("storage_gpu_smoke.py").read_text(encoding="utf-8")
        self.assertNotIn("ray.remote", script)
        self.assertNotIn("ray.get(", script)


if __name__ == "__main__":
    unittest.main()
