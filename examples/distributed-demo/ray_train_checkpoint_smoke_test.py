from __future__ import annotations

import importlib
import json
import pathlib
import sys
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(ROOT))
checkpoint_smoke = importlib.import_module("ray_train_checkpoint_smoke")


class RayTrainCheckpointSmokeTest(unittest.TestCase):
    def test_module_import_does_not_require_ray(self) -> None:
        self.assertNotIn("ray", checkpoint_smoke.__dict__)

    def test_atomic_checkpoint_has_complete_manifest_and_verified_digest(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary) / "checkpoint"
            manifest = checkpoint_smoke.write_checkpoint_atomic(
                root,
                step=7,
                world_size=16,
                rank=0,
                metrics={"loss": 0.25},
            )

            self.assertEqual(manifest["version"], 1)
            self.assertTrue(manifest["complete"])
            self.assertEqual(manifest["metadata"]["step"], 7)
            self.assertEqual(manifest["metadata"]["worldSize"], 16)
            self.assertFalse(any(root.glob("*.tmp")))
            loaded = checkpoint_smoke.load_complete_checkpoint(root)
            self.assertEqual(loaded.step, 7)
            self.assertEqual(loaded.metrics, {"loss": 0.25})

    def test_incomplete_or_tampered_checkpoint_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary) / "checkpoint"
            checkpoint_smoke.write_checkpoint_atomic(
                root, step=3, world_size=2, rank=0, metrics={"loss": 1.0}
            )
            state = root / "state.json"
            state.write_text('{"step":99}', encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "digest"):
                checkpoint_smoke.load_complete_checkpoint(root)

            manifest_path = root / "manifest.json"
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            manifest["complete"] = False
            manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "complete"):
                checkpoint_smoke.load_complete_checkpoint(root)

    def test_resume_starts_after_last_committed_step(self) -> None:
        self.assertEqual(checkpoint_smoke.resume_start_step(None), 0)
        state = checkpoint_smoke.CheckpointState(
            step=8, world_size=16, rank=0, metrics={"loss": 0.1}
        )
        self.assertEqual(checkpoint_smoke.resume_start_step(state), 9)

    def test_checkpoint_root_must_not_be_a_symlink(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            target = base / "target"
            target.mkdir()
            linked = base / "linked"
            linked.symlink_to(target, target_is_directory=True)
            with self.assertRaisesRegex(ValueError, "symlink"):
                checkpoint_smoke.write_checkpoint_atomic(
                    linked, step=1, world_size=1, rank=0, metrics={}
                )

    def test_checkpoint_identity_and_metrics_are_not_coerced(self) -> None:
        invalid = (
            {"step": 1.5, "world_size": 1, "rank": 0, "metrics": {}},
            {"step": 1, "world_size": "1", "rank": 0, "metrics": {}},
            {"step": 1, "world_size": 1, "rank": False, "metrics": {}},
            {"step": 1, "world_size": 1, "rank": 0, "metrics": {7: 1.0}},
            {"step": 1, "world_size": 1, "rank": 0, "metrics": {"loss": True}},
        )
        for index, values in enumerate(invalid):
            with self.subTest(index=index), tempfile.TemporaryDirectory() as temporary:
                with self.assertRaises((TypeError, ValueError)):
                    checkpoint_smoke.write_checkpoint_atomic(
                        pathlib.Path(temporary) / "checkpoint", **values
                    )

    def test_stale_temporary_file_does_not_block_atomic_publication(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary) / "checkpoint"
            root.mkdir()
            (root.parent / ".checkpoint-state.json.stale.tmp").write_text(
                "stale", encoding="utf-8"
            )
            checkpoint_smoke.write_checkpoint_atomic(
                root, step=1, world_size=1, rank=0, metrics={"loss": 1.0}
            )
            self.assertEqual(checkpoint_smoke.load_complete_checkpoint(root).step, 1)
            self.assertEqual(
                sorted(item.name for item in root.iterdir()),
                ["manifest.json", "state.json"],
            )


if __name__ == "__main__":
    unittest.main()
