from pathlib import Path
import unittest


class DatasetPublisherContainerContractTests(unittest.TestCase):
    def test_module_remains_importable_when_job_overrides_working_directory(self) -> None:
        dockerfile = Path(__file__).resolve().parents[1] / "Dockerfile"
        contents = dockerfile.read_text(encoding="utf-8")
        self.assertIn("PYTHONPATH=/app", contents)


if __name__ == "__main__":
    unittest.main()
