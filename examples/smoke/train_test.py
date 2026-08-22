from pathlib import Path
import unittest


class SingleGPUSmokeContractTest(unittest.TestCase):
    def test_uses_assigned_gpu_without_nested_ray_request(self):
        source = Path(__file__).with_name("train.py").read_text(encoding="utf-8")
        self.assertNotIn("ray.remote", source)
        self.assertIn("nvidia-smi", source)


if __name__ == "__main__":
    unittest.main()
