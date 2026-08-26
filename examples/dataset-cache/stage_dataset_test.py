import os
import tempfile
import unittest
from pathlib import Path

import stage_dataset


class StageDatasetTest(unittest.TestCase):
    def test_empty_dataset_is_rejected_before_publish(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "source"
            source.mkdir()

            with self.assertRaisesRegex(ValueError, "selected dataset is empty"):
                stage_dataset.stage_selected_dataset(
                    source_root=source,
                    cache_roots=[root / "cache-a", root / "cache-b"],
                    local_rank=0,
                    timeout_seconds=5,
                )

    def test_rank_zero_atomically_copies_selected_dataset(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            source = base / "source"
            cache1 = base / "cache1"
            cache2 = base / "cache2"
            source.mkdir()
            (source / "nested").mkdir()
            (source / "nested" / "sample.bin").write_bytes(b"sample")

            result = stage_dataset.stage_selected_dataset(
                source_root=source,
                cache_roots=[cache1, cache2],
                local_rank=0,
                timeout_seconds=5,
            )


            self.assertEqual(result.path, cache1.resolve() / "dataset-view")
            self.assertEqual((result.path / "nested" / "sample.bin").read_bytes(), b"sample")
            self.assertTrue((result.path / "nested" / "sample.bin").is_symlink())
            self.assertEqual(result.files, 1)
            self.assertEqual(result.bytes, 6)
            self.assertTrue((cache1 / ".dataset.ready").is_file())

    def test_nonzero_rank_uses_an_already_published_dataset(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            source = base / "source"
            cache = base / "cache"
            source.mkdir()
            (cache / "dataset-view").mkdir(parents=True)
            (cache / "dataset-view" / "sample.bin").write_bytes(b"cached")
            (cache / ".dataset.ready").write_text(
                '{"files": 1, "bytes": 6, "seconds": 0.1}\n',
                encoding="utf-8",
            )

            result = stage_dataset.stage_selected_dataset(
                source_root=source,
                cache_roots=[cache],
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
                    cache_roots=[source / "cache"],
                    local_rank=0,
                    timeout_seconds=1,
                )

    def test_hash_shards_files_across_two_roots_deterministically(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            source = base / "source"
            source.mkdir()
            for index in range(128):
                (source / f"sample-{index:03d}.bin").write_bytes(bytes([index % 251]))

            result = stage_dataset.stage_selected_dataset(
                source_root=source,
                cache_roots=[base / "cache1", base / "cache2"],
                local_rank=0,
                timeout_seconds=5,
            )

            root_counts = [entry.files for entry in result.roots]
            self.assertEqual(sum(root_counts), 128)
            self.assertTrue(all(count > 40 for count in root_counts), root_counts)
            for index in range(128):
                relative = Path(f"sample-{index:03d}.bin")
                expected = stage_dataset.cache_root_index(relative, 2)
                self.assertTrue((base / f"cache{expected + 1}" / "data" / relative).is_file())

    def test_rejects_a_dataset_that_exceeds_either_nvme_share(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            source = base / "source"
            source.mkdir()
            for index in range(32):
                (source / f"sample-{index:03d}.bin").write_bytes(b"x" * 1024)

            with self.assertRaisesRegex(ValueError, "exceeds cache capacity"):
                stage_dataset.stage_selected_dataset(
                    source_root=source,
                    cache_roots=[base / "cache1", base / "cache2"],
                    local_rank=0,
                    timeout_seconds=5,
                    max_bytes_per_root=1024,
                )

    def test_parallel_copy_reports_every_file_and_reuses_atomic_ready_marker(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            source = base / "source"
            source.mkdir()
            for index in range(64):
                (source / f"sample-{index:03d}.bin").write_bytes(bytes([index]))

            first = stage_dataset.stage_selected_dataset(
                source_root=source,
                cache_roots=[base / "cache1", base / "cache2"],
                local_rank=0,
                timeout_seconds=5,
                copy_workers=8,
            )
            second = stage_dataset.stage_selected_dataset(
                source_root=source,
                cache_roots=[base / "cache1", base / "cache2"],
                local_rank=1,
                timeout_seconds=5,
                copy_workers=8,
            )

            self.assertTrue(first.copied)
            self.assertFalse(second.copied)
            self.assertEqual(first.files, second.files)
            self.assertEqual(first.bytes, second.bytes)

    def test_rejects_source_symlinks_and_overlapping_cache_roots(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            source = base / "source"
            source.mkdir()
            outside = base / "outside.bin"
            outside.write_bytes(b"secret")
            os.symlink(outside, source / "escape.bin")
            with self.assertRaisesRegex(ValueError, "symbolic link"):
                stage_dataset.stage_selected_dataset(
                    source_root=source,
                    cache_roots=[base / "cache1", base / "cache2"],
                    local_rank=0,
                    timeout_seconds=5,
                )

            (source / "escape.bin").unlink()
            with self.assertRaisesRegex(ValueError, "overlap"):
                stage_dataset.stage_selected_dataset(
                    source_root=source,
                    cache_roots=[base / "cache", base / "cache" / "nested"],
                    local_rank=0,
                    timeout_seconds=5,
                )


if __name__ == "__main__":
    unittest.main()
