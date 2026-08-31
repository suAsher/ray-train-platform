"""Tests for full public/labeled S1H index generation orchestration."""

from __future__ import annotations

import json
from pathlib import Path
import pickle
import sys
import tempfile
import types
import unittest
from unittest import mock


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

    def test_quarantines_whole_package_when_tokens_overlap(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            package_a = output / "packages" / "site-a" / "package-a"
            package_b = output / "packages" / "site-b" / "package-b"
            self._write_pkl(package_a / "nuscenes_infos_train.pkl", [self._info("shared")])
            self._write_pkl(package_a / "nuscenes_infos_val.pkl", [])
            self._write_pkl(
                package_b / "nuscenes_infos_train.pkl",
                [self._info("shared"), self._info("unique-b")],
            )
            self._write_pkl(package_b / "nuscenes_infos_val.pkl", [])
            rejected = []

            summary = self.generator.merge_package_outputs(output, rejected)
            merged = self._read_pkl(output / "merged_nuscenes_infos_train.pkl")

        self.assertEqual([item["token"] for item in merged], ["shared"])
        self.assertEqual(summary, {"train_samples": 1, "val_samples": 0})
        self.assertEqual(rejected[0]["collection"], "site-b")
        self.assertEqual(rejected[0]["package"], "package-b")
        self.assertEqual(rejected[0]["error_type"], "DuplicateToken")

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

    def test_rejects_package_with_missing_payload_without_aborting_other_packages(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            package_path = self._package(root, "site-a", "package-a", samples=1)
            package = self.generator.discover_packages(root)[0]
            output = root / "output"

            tools = types.ModuleType("tools")
            data_converter = types.ModuleType("tools.data_converter")
            converter = types.ModuleType("tools.data_converter.nuscenes_converter")

            def fail_for_missing_payload(*_args, **_kwargs):
                raise FileNotFoundError(str(package_path / "samples/LIDAR_TOP/missing.bin"))

            converter.create_nuscenes_infos = fail_for_missing_payload
            modules = {
                "tools": tools,
                "tools.data_converter": data_converter,
                "tools.data_converter.nuscenes_converter": converter,
            }
            with mock.patch.dict(sys.modules, modules):
                result = self.generator._generate_package((package, output, 0, 81))

        self.assertEqual(result["status"], "rejected")
        self.assertEqual(result["collection"], "site-a")
        self.assertEqual(result["package"], "package-a")
        self.assertIn("missing.bin", result["reason"])

    def test_rejects_package_with_broken_metadata_references(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._package(root, "site-a", "package-a", samples=1)
            package = self.generator.discover_packages(root)[0]
            output = root / "output"

            tools = types.ModuleType("tools")
            data_converter = types.ModuleType("tools.data_converter")
            converter = types.ModuleType("tools.data_converter.nuscenes_converter")

            def fail_for_broken_reference(*_args, **_kwargs):
                raise KeyError("missing-category-token")

            converter.create_nuscenes_infos = fail_for_broken_reference
            modules = {
                "tools": tools,
                "tools.data_converter": data_converter,
                "tools.data_converter.nuscenes_converter": converter,
            }
            with mock.patch.dict(sys.modules, modules):
                result = self.generator._generate_package((package, output, 0, 81))

        self.assertEqual(result["status"], "rejected")
        self.assertEqual(result["error_type"], "KeyError")
        self.assertIn("missing-category-token", result["reason"])

    def test_does_not_quarantine_infrastructure_errors(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._package(root, "site-a", "package-a", samples=1)
            package = self.generator.discover_packages(root)[0]
            output = root / "output"

            tools = types.ModuleType("tools")
            data_converter = types.ModuleType("tools.data_converter")
            converter = types.ModuleType("tools.data_converter.nuscenes_converter")

            def fail_for_storage_error(*_args, **_kwargs):
                raise OSError(22, "invalid storage operation")

            converter.create_nuscenes_infos = fail_for_storage_error
            modules = {
                "tools": tools,
                "tools.data_converter": data_converter,
                "tools.data_converter.nuscenes_converter": converter,
            }
            with mock.patch.dict(sys.modules, modules):
                with self.assertRaisesRegex(OSError, "invalid storage operation"):
                    self.generator._generate_package((package, output, 0, 81))

    def test_does_not_append_converter_logs_to_shared_storage(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._package(root, "site-a", "package-a", samples=1)
            package = self.generator.discover_packages(root)[0]
            output = root / "output"

            tools = types.ModuleType("tools")
            data_converter = types.ModuleType("tools.data_converter")
            converter = types.ModuleType("tools.data_converter.nuscenes_converter")
            converter.create_nuscenes_infos = lambda *_args, **_kwargs: None
            modules = {
                "tools": tools,
                "tools.data_converter": data_converter,
                "tools.data_converter.nuscenes_converter": converter,
            }
            original_open = Path.open

            def reject_shared_converter_log(path, *args, **kwargs):
                if path.name == "converter.log":
                    raise OSError(22, "append is unsupported on shared storage")
                return original_open(path, *args, **kwargs)

            with mock.patch.dict(sys.modules, modules):
                with mock.patch.object(Path, "open", reject_shared_converter_log):
                    result = self.generator._generate_package((package, output, 0, 81))

        self.assertEqual(result["status"], "accepted")

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
