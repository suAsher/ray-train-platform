"""Validate and execute the narrow Python entrypoint managed by Ray Train."""

from __future__ import annotations

import dataclasses
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


def execute(entrypoint: PythonEntrypoint) -> None:
    """Execute user Python in the current Ray Train worker process."""

    previous = tuple(sys.argv)
    try:
        sys.argv = list(entrypoint.argv)
        if entrypoint.kind == "path":
            runpy.run_path(entrypoint.target, run_name="__main__")
        elif entrypoint.kind == "module":
            runpy.run_module(entrypoint.target, run_name="__main__", alter_sys=True)
        else:
            raise ValueError(f"unsupported Python entrypoint kind {entrypoint.kind!r}")
    finally:
        sys.argv = list(previous)
