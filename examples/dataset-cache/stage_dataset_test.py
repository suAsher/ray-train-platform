import tempfile
import unittest
from pathlib import Path

import stage_dataset


class StageDatasetTest(unittest.TestCase):
    def test_rank_zero_atomically_copies_selected_dataset(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            source = base / "source"
            cache = base / "cache"
            source.mkdir()
            (source / "nested").mkdir()
            (source / "nested" / "sample.bin").write_bytes(b"sample")

            result = stage_dataset.stage_selected_dataset(
                source_root=source,
                cache_root=cache,
                local_rank=0,
                timeout_seconds=5,
            )

            self.assertEqual(result.path, cache.resolve() / "dataset")
            self.assertEqual((result.path / "nested" / "sample.bin").read_bytes(), b"sample")
            self.assertEqual(result.files, 1)
            self.assertEqual(result.bytes, 6)
            self.assertTrue((cache / ".dataset.ready").is_file())

    def test_nonzero_rank_uses_an_already_published_dataset(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            source = base / "source"
            cache = base / "cache"
            source.mkdir()
            (cache / "dataset").mkdir(parents=True)
            (cache / "dataset" / "sample.bin").write_bytes(b"cached")
            (cache / ".dataset.ready").write_text(
                '{"files": 1, "bytes": 6, "seconds": 0.1}\n',
                encoding="utf-8",
            )

            result = stage_dataset.stage_selected_dataset(
                source_root=source,
                cache_root=cache,
                local_rank=3,
                timeout_seconds=1,
            )

            self.assertEqual((result.path / "sample.bin").read_bytes(), b"cached")
            self.assertFalse(result.copied)

    def test_rejects_cache_root_inside_source_dataset(self):
        with tempfile.TemporaryDirectory() as root:
            source = Path(root) / "source"
            source.mkdir()

            with self.assertRaisesRegex(ValueError, "cache root must not be inside the source dataset"):
                stage_dataset.stage_selected_dataset(
                    source_root=source,
                    cache_root=source / "cache",
                    local_rank=0,
                    timeout_seconds=1,
                )


if __name__ == "__main__":
    unittest.main()
