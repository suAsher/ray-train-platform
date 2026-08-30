import importlib.util
import pathlib
import tempfile
import unittest


SCRIPT = pathlib.Path(__file__).with_name("prepare-mmcv-source.py")
SPEC = importlib.util.spec_from_file_location("prepare_mmcv_source", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


class PrepareMMCVSourceTest(unittest.TestCase):
    def test_backports_torch_types_and_cpp17_contracts(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            helper = root / "mmcv" / "ops" / "csrc" / "common" / "pytorch_cpp_helper.hpp"
            helper.parent.mkdir(parents=True)
            helper.write_text("#include <torch/extension.h>\n", encoding="utf-8")
            pixel_group = (
                root
                / "mmcv"
                / "ops"
                / "csrc"
                / "pytorch"
                / "cpu"
                / "pixel_group.cpp"
            )
            pixel_group.parent.mkdir(parents=True)
            pixel_group.write_text(
                '#include "pytorch_cpp_helper.hpp"\n'
                "assert(embedding_dim.dim() == 3);\n",
                encoding="utf-8",
            )
            psamask = (
                root
                / "mmcv"
                / "ops"
                / "csrc"
                / "pytorch"
                / "cuda"
                / "psamask_cuda.cu"
            )
            psamask.parent.mkdir(parents=True)
            psamask.write_text(
                "#include <THC/THC.h>\n"
                "#include <torch/serialize/tensor.h>\n\n"
                "#include <THC/THCDeviceUtils.cuh>\n"
                '#include "psamask_cuda_kernel.cuh"\n',
                encoding="utf-8",
            )
            pybind = root / "mmcv" / "ops" / "csrc" / "pytorch" / "pybind.cpp"
            pybind.write_text(
                '#include "pytorch_cpp_helper.hpp"\n\n'
                "PYBIND11_MODULE(TORCH_EXTENSION_NAME, m) {}\n",
                encoding="utf-8",
            )
            (root / "setup.py").write_text(
                "extra_compile_args['cxx'] = ['-std=c++14']\n"
                "extra_compile_args['nvcc'] += ['-std=c++14']\n",
                encoding="utf-8",
            )

            MODULE.prepare(root)

            self.assertEqual(
                helper.read_text(encoding="utf-8"),
                "#include <torch/types.h>\n",
            )
            setup = (root / "setup.py").read_text(encoding="utf-8")
            self.assertEqual(setup.count("'-std=c++17'"), 2)
            self.assertNotIn("'-std=c++14'", setup)
            pixel_source = pixel_group.read_text(encoding="utf-8")
            self.assertIn("#include <queue>", pixel_source)
            self.assertIn("assert(embedding.dim() == 3);", pixel_source)
            self.assertNotIn("embedding_dim.dim()", pixel_source)
            psamask_source = psamask.read_text(encoding="utf-8")
            self.assertNotIn("THC/THC.h", psamask_source)
            self.assertNotIn("THC/THCDeviceUtils.cuh", psamask_source)
            self.assertIn("#include <torch/serialize/tensor.h>", psamask_source)
            pybind_source = pybind.read_text(encoding="utf-8")
            self.assertTrue(pybind_source.startswith("#include <torch/extension.h>\n"))
            self.assertIn('#include "pytorch_cpp_helper.hpp"', pybind_source)

    def test_fails_closed_when_upstream_source_shape_changes(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            helper = root / "mmcv" / "ops" / "csrc" / "common" / "pytorch_cpp_helper.hpp"
            helper.parent.mkdir(parents=True)
            helper.write_text("#include <torch/types.h>\n", encoding="utf-8")
            pixel_group = (
                root
                / "mmcv"
                / "ops"
                / "csrc"
                / "pytorch"
                / "cpu"
                / "pixel_group.cpp"
            )
            pixel_group.parent.mkdir(parents=True)
            pixel_group.write_text("unknown\n", encoding="utf-8")
            (root / "setup.py").write_text("unknown\n", encoding="utf-8")

            with self.assertRaisesRegex(RuntimeError, "mmcv helper include"):
                MODULE.prepare(root)

    def test_fails_closed_when_psamask_source_is_already_different(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            helper = root / "mmcv" / "ops" / "csrc" / "common" / "pytorch_cpp_helper.hpp"
            helper.parent.mkdir(parents=True)
            helper.write_text("#include <torch/extension.h>\n", encoding="utf-8")
            pixel_group = (
                root
                / "mmcv"
                / "ops"
                / "csrc"
                / "pytorch"
                / "cpu"
                / "pixel_group.cpp"
            )
            pixel_group.parent.mkdir(parents=True)
            pixel_group.write_text(
                '#include "pytorch_cpp_helper.hpp"\n'
                "assert(embedding_dim.dim() == 3);\n",
                encoding="utf-8",
            )
            psamask = (
                root
                / "mmcv"
                / "ops"
                / "csrc"
                / "pytorch"
                / "cuda"
                / "psamask_cuda.cu"
            )
            psamask.parent.mkdir(parents=True)
            psamask.write_text(
                "#include <torch/serialize/tensor.h>\n",
                encoding="utf-8",
            )
            (root / "setup.py").write_text(
                "extra_compile_args['cxx'] = ['-std=c++14']\n"
                "extra_compile_args['nvcc'] += ['-std=c++14']\n",
                encoding="utf-8",
            )

            with self.assertRaisesRegex(RuntimeError, "legacy THC include"):
                MODULE.prepare(root)


if __name__ == "__main__":
    unittest.main()
