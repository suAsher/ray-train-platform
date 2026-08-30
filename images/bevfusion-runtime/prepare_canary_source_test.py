import importlib.util
import pathlib
import tempfile
import unittest


SCRIPT = pathlib.Path(__file__).with_name("prepare-canary-source.py")
SPEC = importlib.util.spec_from_file_location("prepare_canary_source", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


class PrepareCanarySourceTest(unittest.TestCase):
    def test_rewrites_compiler_contracts_and_removes_legacy_binary(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            (root / "setup.py").write_text(
                'extra_args=["-std=c++14"],\n' + MODULE.LEGACY_ARCHES + "\n",
                encoding="utf-8",
            )
            binary = root / "mmdet3d" / "ops" / "old.so"
            binary.parent.mkdir(parents=True)
            binary.write_bytes(b"legacy")

            MODULE.prepare(root)

            updated = (root / "setup.py").read_text(encoding="utf-8")
            self.assertIn('"-std=c++17"', updated)
            self.assertIn("compute_89,code=sm_89", updated)
            self.assertNotIn("compute_70", updated)
            self.assertFalse(binary.exists())

    def test_fails_closed_for_unknown_source_shape(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            (root / "setup.py").write_text("unknown", encoding="utf-8")
            with self.assertRaisesRegex(RuntimeError, "architecture block"):
                MODULE.prepare(root)


if __name__ == "__main__":
    unittest.main()
