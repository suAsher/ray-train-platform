#!/usr/bin/env python3
"""Unit checks for the disposable BEVFusion FZ mini DDP smoke helper."""

from __future__ import annotations

import pickle
import tempfile
import unittest
from pathlib import Path

from fz_mini_ddp_smoke import create_smoke_indices


class CreateSmokeIndicesTest(unittest.TestCase):
    def test_trims_dict_infos_and_creates_a_nonempty_validation_index(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            source = root / "nuscenes_infos_train.pkl"
            with source.open("wb") as handle:
                pickle.dump({"infos": [{"token": "a"}, {"token": "b"}, {"token": "c"}]}, handle)

            result = create_smoke_indices(source, root / "smoke", sample_limit=2)

            with result.train_index.open("rb") as handle:
                train = pickle.load(handle)
            with result.validation_index.open("rb") as handle:
                validation = pickle.load(handle)
            self.assertEqual([entry["token"] for entry in train["infos"]], ["a", "b"])
            self.assertEqual(train, validation)
            self.assertEqual(result.sample_count, 2)

    def test_rejects_an_empty_or_unknown_index_shape(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            source = root / "nuscenes_infos_train.pkl"
            with source.open("wb") as handle:
                pickle.dump({"metadata": {"version": "v1.0-mini"}}, handle)

            with self.assertRaisesRegex(ValueError, "infos"):
                create_smoke_indices(source, root / "smoke", sample_limit=2)


if __name__ == "__main__":
    unittest.main()
