from __future__ import annotations

import dataclasses
import os
import pathlib
import sys
import tempfile
import unittest
from unittest import mock


RUNTIME_PARENT = pathlib.Path(__file__).resolve().parent.parent
if str(RUNTIME_PARENT) not in sys.path:
    sys.path.insert(0, str(RUNTIME_PARENT))

from raytrain_runtime.entrypoint import (  # noqa: E402
    PythonEntrypoint,
    execute,
    parse_python_entrypoint,
)


class EntrypointTest(unittest.TestCase):
    def test_parse_python_script_entrypoint(self):
        parsed = parse_python_entrypoint(["python", "tools/train.py", "--epochs", "2"])

        self.assertEqual(parsed.kind, "path")
        self.assertEqual(parsed.target, "tools/train.py")
        self.assertEqual(parsed.argv, ("tools/train.py", "--epochs", "2"))

    def test_parse_python_module_entrypoint(self):
        parsed = parse_python_entrypoint(["python", "-m", "package.train", "--epochs", "2"])

        self.assertEqual(parsed.kind, "module")
        self.assertEqual(parsed.target, "package.train")
        self.assertEqual(parsed.argv, ("package.train", "--epochs", "2"))

    def test_parse_legacy_shell_wrapper_without_executing_shell(self):
        parsed = parse_python_entrypoint(
            ["/bin/sh", "-lc", "python tools/train.py --name 'managed run'"]
        )

        self.assertEqual(parsed.argv, ("tools/train.py", "--name", "managed run"))

    def test_rejects_nested_torchrun(self):
        with self.assertRaisesRegex(ValueError, "must not contain torchrun"):
            parse_python_entrypoint(["torchrun", "train.py"])

    def test_rejects_torchrun_hidden_in_shell_wrapper(self):
        with self.assertRaisesRegex(ValueError, "must not contain torchrun"):
            parse_python_entrypoint(["/bin/sh", "-lc", "torchrun train.py"])

    def test_rejects_shell_operators_before_execution(self):
        unsafe = (
            "python train.py && echo pwned",
            "python train.py; echo pwned",
            "python train.py | tee output",
            "python train.py > output",
            "python train.py\npython second.py",
            "python train.py $(id)",
            "python train.py `id`",
        )
        for command in unsafe:
            with self.subTest(command=command):
                with self.assertRaisesRegex(ValueError, "shell operators"):
                    parse_python_entrypoint(["/bin/sh", "-lc", command])

    def test_rejects_arbitrary_executable_and_python_code_flag(self):
        for command in (
            ["bash", "train.sh"],
            ["python3", "train.py"],
            ["python", "-c", "print('unsafe')"],
        ):
            with self.subTest(command=command):
                with self.assertRaises(ValueError):
                    parse_python_entrypoint(command)

    def test_rejects_script_path_outside_working_directory(self):
        for target in ("/tmp/train.py", "../train.py", "tools/../../train.py"):
            with self.subTest(target=target):
                with self.assertRaisesRegex(ValueError, "relative.*working directory"):
                    parse_python_entrypoint(["python", target])

    def test_rejects_invalid_module_name(self):
        for module in ("-package", "package/train", "package..train"):
            with self.subTest(module=module):
                with self.assertRaisesRegex(ValueError, "module"):
                    parse_python_entrypoint(["python", "-m", module])

    def test_entrypoint_is_immutable(self):
        parsed = parse_python_entrypoint(["python", "train.py"])

        with self.assertRaises(dataclasses.FrozenInstanceError):
            parsed.target = "other.py"  # type: ignore[misc]

    def test_execute_path_uses_runpy_in_process_and_restores_argv(self):
        entrypoint = PythonEntrypoint("path", "tools/train.py", ("tools/train.py", "--x", "1"))
        original = list(sys.argv)

        with mock.patch("raytrain_runtime.entrypoint.runpy.run_path") as run_path:
            execute(entrypoint)

        run_path.assert_called_once_with("tools/train.py", run_name="__main__")
        self.assertEqual(sys.argv, original)

    def test_execute_module_uses_runpy_in_process_and_restores_argv(self):
        entrypoint = PythonEntrypoint("module", "package.train", ("package.train", "--x", "1"))
        original = list(sys.argv)

        with mock.patch("raytrain_runtime.entrypoint.runpy.run_module") as run_module:
            execute(entrypoint)

        run_module.assert_called_once_with("package.train", run_name="__main__", alter_sys=True)
        self.assertEqual(sys.argv, original)

    def test_execute_path_finds_ray_worker_working_dir_on_pythonpath(self):
        original_path = list(sys.path)
        with tempfile.TemporaryDirectory() as temporary_directory:
            package = pathlib.Path(temporary_directory)
            (package / "train.py").write_text("VALUE = 1\n", encoding="utf-8")
            sys.path.insert(0, str(package))
            try:
                with mock.patch("raytrain_runtime.entrypoint.runpy.run_path") as run_path:
                    execute(PythonEntrypoint("path", "train.py", ("train.py",)))
            finally:
                sys.path[:] = original_path

        run_path.assert_called_once_with(str((package / "train.py").resolve()), run_name="__main__")

    def test_execute_path_supports_sibling_import_and_restores_process_state_on_failure(self):
        module_name = "task9_sibling_helper"
        original_argv = list(sys.argv)
        original_path = list(sys.path)
        original_cwd = pathlib.Path.cwd()
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = pathlib.Path(temporary_directory)
            tools = root / "tools"
            tools.mkdir()
            (tools / f"{module_name}.py").write_text("VALUE = 'sibling-loaded'\n", encoding="utf-8")
            (tools / "train.py").write_text(
                f"from {module_name} import VALUE\nraise RuntimeError(VALUE)\n",
                encoding="utf-8",
            )
            os.chdir(root)
            try:
                with self.assertRaisesRegex(RuntimeError, "sibling-loaded"):
                    execute(PythonEntrypoint("path", "tools/train.py", ("tools/train.py",)))
            finally:
                os.chdir(original_cwd)
                sys.modules.pop(module_name, None)

        self.assertEqual(sys.argv, original_argv)
        self.assertEqual(sys.path, original_path)


if __name__ == "__main__":
    unittest.main()
