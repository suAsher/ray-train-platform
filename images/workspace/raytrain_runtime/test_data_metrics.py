from __future__ import annotations

import math
import pathlib
import sys
import unittest


RUNTIME_PARENT = pathlib.Path(__file__).resolve().parent.parent
if str(RUNTIME_PARENT) not in sys.path:
    sys.path.insert(0, str(RUNTIME_PARENT))


class RuntimeDataMetricsTest(unittest.TestCase):
    def setUp(self) -> None:
        from raytrain_runtime.data_metrics import reset_data_metrics_for_tests

        reset_data_metrics_for_tests()

    def tearDown(self) -> None:
        from raytrain_runtime.data_metrics import reset_data_metrics_for_tests

        reset_data_metrics_for_tests()

    def test_accumulates_only_bounded_known_non_negative_metrics(self) -> None:
        from raytrain_runtime.data_metrics import (
            observe_data_metric,
            snapshot_data_metrics,
        )

        observe_data_metric("dataset_batches_total", 1)
        observe_data_metric("dataset_batches_total", 2)
        observe_data_metric("dataset_source_read_seconds_total", 0.25)
        observe_data_metric("dataset_cache_stale_temp_reclaimed_total", 1)

        self.assertEqual(
            snapshot_data_metrics(),
            {
                "dataset_batches_total": 3.0,
                "dataset_cache_stale_temp_reclaimed_total": 1.0,
                "dataset_source_read_seconds_total": 0.25,
            },
        )
        for name, value in (
            ("private_path", 1),
            ("dataset_batches_total", -1),
            ("dataset_batches_total", math.inf),
            ("dataset_batches_total", True),
        ):
            with self.subTest(name=name, value=value):
                with self.assertRaises(ValueError):
                    observe_data_metric(name, value)

    def test_snapshot_is_an_independent_copy_and_zero_values_are_omitted(self) -> None:
        from raytrain_runtime.data_metrics import (
            observe_data_metric,
            snapshot_data_metrics,
        )

        observe_data_metric("dataset_cache_hits_total", 1)
        first = snapshot_data_metrics()
        first["dataset_cache_hits_total"] = 99

        self.assertEqual(
            snapshot_data_metrics(),
            {"dataset_cache_hits_total": 1.0},
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
