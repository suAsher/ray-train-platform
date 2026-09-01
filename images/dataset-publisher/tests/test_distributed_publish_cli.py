"""Unit tests for distributed publication identities and bounded work mapping."""

from __future__ import annotations

import os
import sys
import unittest
from pathlib import Path


PACKAGE_ROOT = Path(__file__).resolve().parents[1]
if str(PACKAGE_ROOT) not in sys.path:
    sys.path.insert(0, str(PACKAGE_ROOT))


class DistributedPublicationPlanningTest(unittest.TestCase):
    def test_token_partition_mapping_is_stable_and_bounded(self) -> None:
        from raytrain_publisher.distributed_publish import _sample_partition

        tokens = [f"token-{index}" for index in range(200)]
        first = [_sample_partition(token, 17) for token in tokens]
        self.assertEqual(first, [_sample_partition(token, 17) for token in tokens])
        self.assertTrue(all(0 <= value < 17 for value in first))

    def test_partition_ordinal_rejects_unsafe_values(self) -> None:
        from raytrain_publisher.distributed_publish import _partition_ordinal

        self.assertEqual(_partition_ordinal("0", 2), 0)
        self.assertEqual(_partition_ordinal("1", 2), 1)
        for value in ("", "-1", "2", "0;whoami", " 1"):
            with self.subTest(value=value):
                with self.assertRaises(ValueError):
                    _partition_ordinal(value, 2)

    def test_parser_requires_explicit_bounded_execution_shape(self) -> None:
        from raytrain_publisher.distributed_publish import build_argument_parser

        parser = build_argument_parser()
        with self.assertRaises(Exception):
            parser.parse_args(["--phase", "pack"])
        parsed = parser.parse_args([
            "--phase", "pack", "--partition-count", "8", "--run-id", "run-1",
            "--dataset-id", "dataset-1", "--dataset-version-id", "version-1", "--version", "v1",
            "--schema-version", "s1h-lidar-parquet-v1", "--source-bucket", "source-bucket",
            "--target-bucket", "target-bucket", "--tos-endpoint", "tos-cn-shanghai.ivolces.com",
            "--tos-region", "cn-shanghai", "--source-root", "ray-train/public/labeled",
            "--source-index", "index.pkl", "--internal-prefix", "ray-train/platform/datasets",
            "--output-dir", "/data/output",
        ])
        self.assertEqual(parsed.partition_count, 8)


if __name__ == "__main__":
    unittest.main()
