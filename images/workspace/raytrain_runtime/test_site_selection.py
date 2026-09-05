from __future__ import annotations

import pathlib
import tempfile
import sys
import types
import unittest
from unittest import mock

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from raytrain_runtime.site_selection import normalize_sites, parse_sites_json, selected_training_count


class SiteSelectionTest(unittest.TestCase):
    def test_real_parquet_counts_across_row_groups(self):
        try:
            import pyarrow as pa
            import pyarrow.parquet as pq
        except ImportError:
            self.skipTest("PyArrow is required for real Parquet validation")
        with tempfile.TemporaryDirectory() as directory:
            path = str(pathlib.Path(directory) / "manifest.parquet")
            pq.write_table(pa.table({"site_id": ["a", "b", "a", "b"], "split": ["train", "train", "val", "train"]}), path, row_group_size=1)
            self.assertEqual(selected_training_count(path, ("b",)), 2)
            self.assertEqual(selected_training_count(path, ("a", "b")), 3)
            with self.assertRaisesRegex(ValueError, "unknown"):
                selected_training_count(path, ("c",))

    def test_epoch_shuffle_is_bounded_and_resume_deterministic(self):
        from raytrain_runtime.ray_data import _epoch_shuffle_options
        self.assertEqual(_epoch_shuffle_options(None, 0, None, 2), {})
        first = _epoch_shuffle_options(7, 2, 128, 2)
        self.assertEqual(first, _epoch_shuffle_options(7, 2, 128, 2))
        self.assertNotEqual(first, _epoch_shuffle_options(7, 3, 128, 2))
        for arguments in ((7, -1, 128, 2), (7, 0, 1, 2), (7, 0, 65537, 2), (True, 0, 128, 2)):
            with self.assertRaises(ValueError):
                _epoch_shuffle_options(*arguments)

    def test_canonical_selection_and_invalid_inputs(self):
        self.assertEqual(parse_sites_json('["site_b","site_a","site_b"]'), ("site_a", "site_b"))
        for value in ('null', '"a"', '{}', '["../a"]', '[""]', '[1]', '["a/b"]'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                parse_sites_json(value)
        with self.assertRaises(ValueError):
            normalize_sites(["a"] * 257)

    def inspect(self, rows, sites=("site_a",), names=("site_id", "split")):
        manifest = mock.MagicMock()
        manifest.__enter__.return_value = manifest
        manifest.schema_arrow.names = names
        batch = mock.Mock()
        batch.to_pydict.return_value = {
            "site_id": [row[0] for row in rows], "split": [row[1] for row in rows]
        }
        manifest.iter_batches.return_value = [batch]
        module = types.SimpleNamespace(ParquetFile=mock.Mock(return_value=manifest))
        count = selected_training_count("verified.parquet", sites, parquet_module=module)
        manifest.iter_batches.assert_called_once_with(batch_size=8192, columns=["site_id", "split"])
        return count

    def test_counts_only_requested_training_rows(self):
        rows = [("site_a", "train"), ("site_b", "train"), ("site_a", "val"), ("site_a", "train")]
        self.assertEqual(self.inspect(rows), 2)
        self.assertEqual(self.inspect(rows, ("site_a", "site_b")), 3)

    def test_unknown_empty_or_partial_metadata_fails(self):
        for rows in ([], [("site_a", "val")], [("site_b", "train")], [("site_a", "train"), ("", "train")]):
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                self.inspect(rows)
        with self.assertRaisesRegex(ValueError, "lacks site metadata"):
            self.inspect([], names=("split",))


if __name__ == "__main__":
    unittest.main()
