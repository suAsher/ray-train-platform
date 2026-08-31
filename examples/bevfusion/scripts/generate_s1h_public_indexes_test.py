"""Tests for full public/labeled S1H index generation orchestration."""

from __future__ import annotations

import json
from pathlib import Path
import pickle
import sys
import tempfile
import unittest


SCRIPT_DIR = Path(__file__).resolve().parent
if str(SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIR))


class GenerateS1HPublicIndexesTest(unittest.TestCase):
    def setUp(self) -> None:
        try:
            import generate_s1h_public_indexes as generator
        except ModuleNotFoundError as error:
            raise AssertionError("S1H public index generator is not implemented") from error
        self.generator = generator

    def test_discovers_only_complete_non_symlink_packages_in_stable_order(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "labeled"
            complete_b = self._package(root, "site-b", "package-b", samples=2)
            complete_a = self._package(root, "site-a", "package-a", samples=3)
            incomplete = root / "site-a" / "incomplete" / "v1.0-mini"
            incomplete.mkdir(parents=True)
            (incomplete / "sample.json").write_text("[]", encoding="utf-8")
            (root / "site-a" / "link").symlink_to(complete_a, target_is_directory=True)

            packages = self.generator.discover_packages(root)

        self.assertEqual(
            [(item.collection, item.name, item.sample_count) for item in packages],
            [("site-a", "package-a", 3), ("site-b", "package-b", 2)],
        )
        self.assertEqual(packages[0].path, complete_a.resolve())
        self.assertEqual(packages[1].path, complete_b.resolve())

    def test_merges_generated_pkls_and_requires_scene_provenance(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            package_a = output / "packages" / "site-a" / "package-a"
            package_b = output / "packages" / "site-b" / "package-b"
            self._write_pkl(package_a / "nuscenes_infos_train.pkl", [self._info("a")])
            self._write_pkl(package_a / "nuscenes_infos_val.pkl", [self._info("b")])
            self._write_pkl(package_b / "nuscenes_infos_train.pkl", [self._info("c")])
            self._write_pkl(package_b / "nuscenes_infos_val.pkl", [])

            summary = self.generator.merge_package_outputs(output)

            train = self._read_pkl(output / "merged_nuscenes_infos_train.pkl")
            val = self._read_pkl(output / "merged_nuscenes_infos_val.pkl")
        self.assertEqual([item["token"] for item in train], ["a", "c"])
        self.assertEqual([item["token"] for item in val], ["b"])
        self.assertEqual(summary, {"train_samples": 2, "val_samples": 1})

        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            package = output / "packages" / "site" / "package"
            invalid = self._info("missing-scene")
            del invalid["scene_token"]
            self._write_pkl(package / "nuscenes_infos_train.pkl", [invalid])
            self._write_pkl(package / "nuscenes_infos_val.pkl", [])
            with self.assertRaisesRegex(ValueError, "scene_token"):
                self.generator.merge_package_outputs(output)

    def test_metadata_fingerprint_changes_when_a_package_is_updated(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "labeled"
            package = self._package(root, "site-a", "package-a", samples=3)
            before = self.generator.discover_packages(root)

            metadata = package / "v1.0-mini" / "scene.json"
            metadata.write_text(json.dumps([{"token": "new-scene"}]), encoding="utf-8")
            after = self.generator.discover_packages(root)

        self.assertEqual(len(before), 1)
        self.assertEqual(len(after), 1)
        self.assertNotEqual(before[0].fingerprint, after[0].fingerprint)

    @staticmethod
    def _package(root: Path, collection: str, name: str, *, samples: int) -> Path:
        package = root / collection / name
        metadata = package / "v1.0-mini"
        metadata.mkdir(parents=True)
        required = {
            "sample.json": [{"token": str(index)} for index in range(samples)],
            "scene.json": [],
            "sample_data.json": [],
            "sample_annotation.json": [],
        }
        for filename, payload in required.items():
            (metadata / filename).write_text(json.dumps(payload), encoding="utf-8")
        return package

    @staticmethod
    def _info(token: str) -> dict[str, object]:
        return {"token": token, "scene_token": f"scene-{token}", "timestamp": 1}

    @staticmethod
    def _write_pkl(path: Path, infos: list[dict[str, object]]) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("wb") as stream:
            pickle.dump({"infos": infos, "metadata": {"version": "v1.0-mini"}}, stream)

    @staticmethod
    def _read_pkl(path: Path) -> list[dict[str, object]]:
        with path.open("rb") as stream:
            return pickle.load(stream)["infos"]


if __name__ == "__main__":
    unittest.main(verbosity=2)
