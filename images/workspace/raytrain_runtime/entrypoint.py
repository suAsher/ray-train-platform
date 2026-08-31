"""Validate and execute the narrow Python entrypoint managed by Ray Train."""

from __future__ import annotations

import dataclasses
import os
import pathlib
import re
import runpy
import shlex
import sys
from collections.abc import Sequence


_MODULE_NAME = re.compile(r"^[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*$")
_SHELL_PUNCTUATION = frozenset(("&", "&&", "(", ")", ";", "<", "<<", ">", ">>", "|", "||"))


@dataclasses.dataclass(frozen=True)
class PythonEntrypoint:
    """An immutable Python script or module invocation."""

    kind: str
    target: str
    argv: tuple[str, ...]


def _shell_words(command: str) -> tuple[str, ...]:
    if "\n" in command or "\r" in command or "`" in command or "$(" in command:
        raise ValueError("managed Python entrypoint must not contain shell operators")
    lexer = shlex.shlex(command, posix=True, punctuation_chars="();<>|&")
    lexer.whitespace_split = True
    lexer.commenters = ""
    try:
        words = tuple(lexer)
    except ValueError as exc:
        raise ValueError(f"invalid quoted Python entrypoint: {exc}") from exc
    if any(word in _SHELL_PUNCTUATION for word in words):
        raise ValueError("managed Python entrypoint must not contain shell operators")
    return words


def _unwrap_legacy_shell(argv: tuple[str, ...]) -> tuple[str, ...]:
    if len(argv) == 3 and argv[:2] == ("/bin/sh", "-lc"):
        return _shell_words(argv[2])
    return argv


def _validate_script_target(target: str) -> None:
    path = pathlib.PurePosixPath(target)
    if (
        not target.endswith(".py")
        or target.startswith("-")
        or path.is_absolute()
        or ".." in path.parts
        or not path.parts
    ):
        raise ValueError("Python script must be a relative .py path inside the working directory")


def parse_python_entrypoint(argv: Sequence[str]) -> PythonEntrypoint:
    """Parse only ``python file.py`` or ``python -m module`` invocations.

    Portal and API submissions historically preserve user text as
    ``/bin/sh -lc <text>``. That representation is decoded with ``shlex`` and
    then discarded; a shell is never executed by managed Ray Train workers.
    """

    words = _unwrap_legacy_shell(tuple(str(value) for value in argv))
    if not words:
        raise ValueError("managed Python entrypoint is required")
    if words[0] == "torchrun" or any(word == "torchrun" for word in words):
        raise ValueError("managed Python entrypoint must not contain torchrun")
    if any(word in _SHELL_PUNCTUATION for word in words):
        raise ValueError("managed Python entrypoint must not contain shell operators")
    if words[0] != "python":
        raise ValueError("managed entrypoint must start with python")

    if len(words) >= 3 and words[1] == "-m":
        target = words[2]
        if not _MODULE_NAME.fullmatch(target):
            raise ValueError("Python module must be a dotted module name")
        return PythonEntrypoint("module", target, (target, *words[3:]))

    if len(words) < 2 or words[1].startswith("-"):
        raise ValueError("managed entrypoint must use python file.py or python -m module")
    target = words[1]
    _validate_script_target(target)
    return PythonEntrypoint("path", target, (target, *words[2:]))


def _worker_script_location(target: str) -> tuple[str, pathlib.Path | None]:
    """Find a relative script in Ray's governed working-dir PYTHONPATH.

    Ray Train workers receive the Ray Job package as a runtime environment,
    but Ray 2.56 does not guarantee that their process cwd changes to that
    package. The working-dir plugin does prepend the extracted package to
    ``sys.path``, so use only those interpreter-controlled roots as fallback.
    """

    direct = pathlib.Path(target)
    if direct.is_file():
        return target, None
    for raw_root in tuple(sys.path):
        if not raw_root:
            continue
        root = pathlib.Path(raw_root)
        candidate = root.joinpath(target)
        if candidate.is_file():
            return str(candidate.resolve()), root.resolve()
    return target, None


def _worker_module_root(target: str) -> pathlib.Path | None:
    """Find the governed working-dir root that contains a Python module."""

    module_parts = target.split(".")
    for raw_root in tuple(sys.path):
        if not raw_root:
            continue
        root = pathlib.Path(raw_root)
        candidate = root.joinpath(*module_parts)
        if candidate.with_suffix(".py").is_file() or (
            candidate / "__init__.py"
        ).is_file():
            return root.resolve()
    return None


def execute(entrypoint: PythonEntrypoint) -> None:
    """Execute user Python in the current Ray Train worker process."""

    previous = tuple(sys.argv)
    previous_path = tuple(sys.path)
    previous_cwd = pathlib.Path.cwd()
    try:
        sys.argv = list(entrypoint.argv)
        if entrypoint.kind == "path":
            script_target, package_root = _worker_script_location(entrypoint.target)
            if package_root is not None:
                os.chdir(package_root)
            script_directory = str(pathlib.Path(script_target).resolve().parent)
            if sys.path:
                sys.path[0] = script_directory
            else:
                sys.path.insert(0, script_directory)
            runpy.run_path(script_target, run_name="__main__")
        elif entrypoint.kind == "module":
            package_root = _worker_module_root(entrypoint.target)
            if package_root is not None:
                os.chdir(package_root)
            runpy.run_module(entrypoint.target, run_name="__main__", alter_sys=True)
        else:
            raise ValueError(f"unsupported Python entrypoint kind {entrypoint.kind!r}")
    finally:
        os.chdir(previous_cwd)
        sys.argv = list(previous)
        sys.path[:] = previous_path
