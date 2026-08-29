import os
import io
import json
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

import stage_dataset


class StageDatasetTest(unittest.TestCase):
    def test_main_keeps_completed_stage_when_metric_publication_fails(self):
        staged = stage_dataset.StageResult(
            path=Path("/cache/dataset-view"), copied=True, files=1, bytes=4,
            seconds=0.2, roots=(stage_dataset.RootStageResult(path="/cache", files=1, bytes=4),),
        )
        environment = {
            "PLATFORM_DATASET_PATH": "/dataset",
            "PLATFORM_CACHE_PATH": "/cache",
            "PLATFORM_JOB_ID": "job-01",
            "PLATFORM_POD_NAMESPACE": "tenant-a",
            "PLATFORM_RAY_CLUSTER": "job-01-cluster",
            "PLATFORM_RAY_NODE_TYPE": "worker",
            "PLATFORM_POD_NAME": "job-01-cluster-worker-abc",
        }
        stdout, stderr = io.StringIO(), io.StringIO()
        with mock.patch.dict(os.environ, environment, clear=True), mock.patch.object(
            stage_dataset, "stage_selected_dataset", return_value=staged
        ), mock.patch.object(
            stage_dataset, "write_preload_metrics", side_effect=OSError("disk path and secret detail")
        ), redirect_stdout(stdout), redirect_stderr(stderr):
            result = stage_dataset.main()

        self.assertEqual(result, 0)
        self.assertIn("/cache/dataset-view", stdout.getvalue())
        self.assertIn("RAYTRAIN_CACHE_METRICS_WARNING=publication_failed", stderr.getvalue())
        self.assertNotIn("secret detail", stderr.getvalue())

    def test_writes_atomic_strict_job_scoped_preload_metrics(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            cache = base / "cache"
            cache.mkdir()
            result = stage_dataset.StageResult(
                path=cache / "dataset-view", copied=True, files=2, bytes=12,
                seconds=0.25, roots=(stage_dataset.RootStageResult(path=str(cache), files=2, bytes=12),),
            )
            identity = stage_dataset.MetricIdentity(
                job_id="job-01", namespace="tenant-a", ray_cluster="job-01-cluster",
                node_type="worker", pod="job-01-cluster-worker-abc",
            )

            stage_dataset.write_preload_metrics([cache], result, identity)

            metric_file = cache / ".ray-cache-metrics" / "preload.metrics"
            self.assertTrue(metric_file.is_file())
            text = metric_file.read_text(encoding="utf-8")
            self.assertIn("platform_job_id=job-01\n", text)
            self.assertIn("bytes=12\n", text)
            self.assertIn("copied=1\n", text)
            self.assertIn("hits=0\nmisses=1\n", text)
            self.assertFalse(any(metric_file.parent.glob("*.tmp")))

            reused = stage_dataset.StageResult(
                path=result.path, copied=False, files=2, bytes=12, seconds=0.01,
                roots=result.roots,
            )
            stage_dataset.write_preload_metrics([cache], reused, identity)
            updated = metric_file.read_text(encoding="utf-8")
            self.assertIn("copied=0\n", updated)
            self.assertIn("hits=1\nmisses=1\n", updated)

    def test_rejects_unsafe_metric_labels_and_metric_symlinks(self):
        with tempfile.TemporaryDirectory() as root:
            cache = Path(root) / "cache"
            cache.mkdir()
            result = stage_dataset.StageResult(
                path=cache / "dataset-view", copied=False, files=1, bytes=1,
                seconds=0.1, roots=(stage_dataset.RootStageResult(path=str(cache), files=1, bytes=1),),
            )
            unsafe = stage_dataset.MetricIdentity(
                job_id='job"} or vector(1)', namespace="tenant-a", ray_cluster="cluster-a",
                node_type="worker", pod="cluster-a-worker-abc",
            )
            with self.assertRaisesRegex(ValueError, "metric label"):
                stage_dataset.write_preload_metrics([cache], result, unsafe)

            metric_dir = cache / ".ray-cache-metrics"
            outside = Path(root) / "outside"
            outside.mkdir()
            os.symlink(outside, metric_dir)
            safe = stage_dataset.MetricIdentity("job-01", "tenant-a", "cluster-a", "worker", "cluster-a-worker-abc")
            with self.assertRaisesRegex(ValueError, "symbolic link"):
                stage_dataset.write_preload_metrics([cache], result, safe)

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
