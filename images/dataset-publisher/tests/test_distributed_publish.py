from __future__ import annotations

import sys
import unittest
from pathlib import Path


PROJECT_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(PROJECT_ROOT))

from raytrain_publisher.distributed_publish import (  # noqa: E402
    build_partition_plan,
)


class DistributedPublicationPlanTest(unittest.TestCase):
    def test_plan_is_stable_and_reuses_only_matching_verified_receipts(self) -> None:
        samples = (
            {"token": "sample-a", "source_key": "a.bin", "size": 10, "sha256": "a" * 64},
            {"token": "sample-b", "source_key": "b.bin", "size": 20, "sha256": "b" * 64},
        )
        first = build_partition_plan(samples=samples, partition_count=2)
        receipts = {item.ordinal: item.input_fingerprint for item in first}

        second = build_partition_plan(
            samples=samples,
            partition_count=2,
            verified_base_receipts=receipts,
        )
        self.assertTrue(all(item.reused for item in second))

        changed = build_partition_plan(
            samples=(samples[0], {**samples[1], "size": 21}),
            partition_count=2,
            verified_base_receipts=receipts,
        )
        self.assertTrue(any(not item.reused for item in changed))


if __name__ == "__main__":
    unittest.main()
