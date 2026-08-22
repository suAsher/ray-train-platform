import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import benchmark


class DatasetBenchmarkTest(unittest.TestCase):
    def test_discover_files_is_sorted_and_honors_limits(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            (base / "b.bin").write_bytes(b"b" * 8)
            (base / "a.bin").write_bytes(b"a" * 4)
            (base / "nested").mkdir()
            (base / "nested" / "c.bin").write_bytes(b"c" * 16)

            files, metadata = benchmark.discover_files(base, max_files=2)

            self.assertEqual([path.relative_to(base).as_posix() for path in files], ["a.bin", "b.bin"])
            self.assertEqual(metadata["selected_files"], 2)
            self.assertEqual(metadata["selected_bytes"], 12)
            self.assertTrue(metadata["file_limit_reached"])

    def test_partition_files_is_deterministic_and_disjoint(self):
        files = [Path(f"sample-{index}.bin") for index in range(7)]

        rank_zero = benchmark.partition_files(files, rank=0, world_size=2)
        rank_one = benchmark.partition_files(files, rank=1, world_size=2)

        self.assertEqual(rank_zero, files[0::2])
        self.assertEqual(rank_one, files[1::2])
        self.assertEqual(set(rank_zero) & set(rank_one), set())
        self.assertEqual(set(rank_zero) | set(rank_one), set(files))

    def test_limit_partition_bytes_never_exceeds_worker_limit(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            files = []
            for index, size in enumerate((8, 32, 8)):
                path = base / f"sample-{index}.bin"
                path.write_bytes(b"x" * size)
                files.append(path)

            selected = benchmark.limit_partition_bytes(files, max_bytes=16)

            self.assertEqual(selected, [files[0], files[2]])
            self.assertLessEqual(sum(path.stat().st_size for path in selected), 16)

    def test_require_nonempty_partition_rejects_zero_file_worker(self):
        with self.assertRaisesRegex(RuntimeError, "worker 1 selected no files"):
            benchmark.require_nonempty_partition([], rank=1, max_bytes=16)

    def test_discover_files_rejects_first_file_above_strict_byte_limit(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            (base / "too-large.bin").write_bytes(b"x" * 32)

            files, metadata = benchmark.discover_files(base, max_files=10, max_bytes=16)

            self.assertEqual(files, [])
            self.assertEqual(metadata["selected_bytes"], 0)
            self.assertTrue(metadata["byte_limit_reached"])

    def test_resolve_dataset_path_rejects_escape(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            dataset = base / "dataset"
            dataset.mkdir()

            with self.assertRaisesRegex(ValueError, "must stay inside"):
                benchmark.resolve_dataset_path(dataset, "../outside")

    def test_write_report_rejects_output_escape(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            output = base / "output"
            output.mkdir()
            outside = base / "outside.json"

            with mock.patch.dict(os.environ, {"PLATFORM_OUTPUT_PATH": str(output)}, clear=False):
                with self.assertRaisesRegex(ValueError, "must stay inside"):
                    benchmark.write_report({"ok": True}, str(outside))

    def test_read_pass_reports_bytes_rate_and_latency_percentiles(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            files = []
            for index, size in enumerate((4096, 8192, 12288)):
                path = base / f"sample-{index}.bin"
                path.write_bytes(os.urandom(size))
                files.append(path)

            result = benchmark.read_pass(files, chunk_size=4096)

            self.assertEqual(result["files"], 3)
            self.assertEqual(result["bytes"], 24576)
            self.assertGreater(result["seconds"], 0)
            self.assertGreater(result["mib_per_second"], 0)
            self.assertGreaterEqual(result["latency_ms_p95"], result["latency_ms_p50"])

    def test_aggregate_sums_throughput_and_hides_node_names(self):
        workers = [
            {"rank": 0, "metadata_seconds": 2.0, "passes": [{"files": 1, "bytes": 100 * 1024 * 1024, "seconds": 4.0}]},
            {"rank": 1, "metadata_seconds": 3.0, "passes": [{"files": 3, "bytes": 300 * 1024 * 1024, "seconds": 5.0}]},
        ]

        report = benchmark.aggregate_report(
            workers=workers,
            dataset_path="/mnt/data/input",
            selected_files=4,
            selected_bytes=400 * 1024 * 1024,
        )

        self.assertEqual(report["dataset_path"], "/mnt/data/input")
        self.assertEqual(report["workers"], 2)
        self.assertEqual(report["passes"][0]["bytes"], 400 * 1024 * 1024)
        self.assertEqual(report["passes"][0]["wall_seconds"], 5.0)
        self.assertAlmostEqual(report["passes"][0]["aggregate_mib_per_second"], 80.0)
        self.assertNotIn("hostname", json.dumps(report))


if __name__ == "__main__":
    unittest.main()
