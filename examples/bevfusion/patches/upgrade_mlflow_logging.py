#!/usr/bin/env python3
"""Upgrade BEVFusion's platform MLflow bridge to avoid duplicate MMCV logs."""

from __future__ import annotations

import argparse
import ast
import os
from pathlib import Path
import stat
import sys
import tempfile
from typing import List, NamedTuple, Optional, Sequence, Tuple


TARGET_RELATIVE_PATH = Path("mmdet3d") / "utils" / "platform_mlflow.py"

MLFLOW_LOGGING_BLOCK = """    # MMCV owns these handlers; MLflow may add a root handler later.
    import logging

    training_logger = logging.getLogger("mmdet3d")
    if any(not isinstance(handler, logging.NullHandler) for handler in training_logger.handlers):
        training_logger.propagate = False

"""

ANCHOR_BEFORE = """    tracking_uri = os.environ.get("MLFLOW_TRACKING_URI", "").strip()
    if not tracking_uri or rank != 0:
        return None

    import mlflow
"""

PARTIAL_MARKERS = (
    "MMCV owns these handlers",
    'logging.getLogger("mmdet3d")',
    "training_logger.propagate = False",
)


class UpgradeError(RuntimeError):
    """Raised when a checkout cannot be upgraded safely."""


class UpgradeResult(NamedTuple):
    changed: bool
    applied: bool
    target: Path


def _line_offsets(lines: List[str]) -> List[int]:
    offsets: List[int] = []
    position = 0
    for line in lines:
        offsets.append(position)
        position += len(line)
    return offsets


def _find_start_platform_mlflow(source: str, target: Path) -> Tuple[int, int]:
    try:
        tree = ast.parse(source, filename=str(target))
    except SyntaxError as exc:
        raise UpgradeError("target has invalid Python syntax") from exc

    matches = [
        node
        for node in tree.body
        if isinstance(node, ast.FunctionDef) and node.name == "start_platform_mlflow"
    ]
    if len(matches) != 1:
        raise UpgradeError("expected exactly one start_platform_mlflow function")
    function = matches[0]
    if function.end_lineno is None:
        raise UpgradeError("cannot determine start_platform_mlflow bounds")

    lines = source.splitlines(keepends=True)
    offsets = _line_offsets(lines)
    start = offsets[function.lineno - 1]
    if function.end_lineno >= len(lines):
        end = len(source)
    else:
        end = offsets[function.end_lineno]
    return start, end


def _newline_for(source: str) -> str:
    return "\r\n" if "\r\n" in source else "\n"


def _with_newline(text: str, newline: str) -> str:
    return text if newline == "\n" else text.replace("\n", newline)


def _anchor_after(block: str, newline: str) -> str:
    return _with_newline(
        """    tracking_uri = os.environ.get("MLFLOW_TRACKING_URI", "").strip()
    if not tracking_uri or rank != 0:
        return None

""",
        newline,
    ) + block + _with_newline("""    import mlflow
""", newline)


def upgrade_source(source: str, target: Path = TARGET_RELATIVE_PATH) -> Tuple[str, bool]:
    start, end = _find_start_platform_mlflow(source, target)
    function_source = source[start:end]
    newline = _newline_for(source)
    block = _with_newline(MLFLOW_LOGGING_BLOCK, newline)
    anchor_before = _with_newline(ANCHOR_BEFORE, newline)
    anchor_after = _anchor_after(block, newline)

    fixed_count = function_source.count(block)
    if fixed_count == 1:
        if function_source.count(anchor_after) != 1:
            raise UpgradeError("start_platform_mlflow contains misplaced logging guard")
        remainder = function_source.replace(block, "", 1)
        if any(marker in remainder for marker in PARTIAL_MARKERS):
            raise UpgradeError("start_platform_mlflow contains repeated logging guard")
        return source, False
    if fixed_count > 1:
        raise UpgradeError("start_platform_mlflow contains repeated logging guard")

    if any(marker in function_source for marker in PARTIAL_MARKERS):
        raise UpgradeError("start_platform_mlflow contains an unknown partial logging guard")

    if function_source.count(anchor_before) != 1:
        raise UpgradeError("start_platform_mlflow layout is not a supported version")

    upgraded_function = function_source.replace(anchor_before, anchor_after, 1)
    upgraded = source[:start] + upgraded_function + source[end:]
    try:
        compile(upgraded, str(target), "exec")
    except SyntaxError as exc:
        raise UpgradeError("upgraded target would not compile") from exc
    return upgraded, True


def _resolved_checkout(checkout: Path) -> Path:
    try:
        return checkout.resolve(strict=True)
    except FileNotFoundError as exc:
        raise UpgradeError("checkout directory does not exist") from exc


def _target_path(checkout: Path) -> Path:
    root = _resolved_checkout(checkout)
    target = root / TARGET_RELATIVE_PATH
    for candidate in (root / "mmdet3d", root / "mmdet3d" / "utils", target):
        if candidate.is_symlink():
            raise UpgradeError("refusing to modify symlink target")
    if not target.is_file():
        raise UpgradeError("not a BEVFusion checkout, missing mmdet3d/utils/platform_mlflow.py")
    return target


def _read_source(target: Path) -> str:
    with target.open("r", encoding="utf-8", newline="") as handle:
        return handle.read()


def _write_replacing_same_directory(
    target: Path,
    original_source: str,
    upgraded: str,
    original_stat: os.stat_result,
) -> None:
    original_mode = stat.S_IMODE(original_stat.st_mode)
    temp_name: Optional[str] = None
    try:
        with tempfile.NamedTemporaryFile(
            "w",
            encoding="utf-8",
            newline="",
            dir=str(target.parent),
            prefix=".platform_mlflow.",
            suffix=".tmp",
            delete=False,
        ) as handle:
            temp_name = handle.name
            handle.write(upgraded)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temp_name, original_mode)
        latest_stat = target.stat()
        latest_source = _read_source(target)
        latest_fingerprint = (
            latest_stat.st_dev,
            latest_stat.st_ino,
            latest_stat.st_size,
            latest_stat.st_mtime_ns,
            stat.S_IMODE(latest_stat.st_mode),
        )
        original_fingerprint = (
            original_stat.st_dev,
            original_stat.st_ino,
            original_stat.st_size,
            original_stat.st_mtime_ns,
            original_mode,
        )
        if latest_source != original_source or latest_fingerprint != original_fingerprint:
            raise UpgradeError("target changed while preparing upgrade; rerun the command")
        os.replace(temp_name, target)
        temp_name = None
    finally:
        if temp_name is not None:
            try:
                os.unlink(temp_name)
            except FileNotFoundError:
                pass


def upgrade_checkout(checkout: Path, *, apply: bool) -> UpgradeResult:
    target = _target_path(checkout)
    source = _read_source(target)
    upgraded, changed = upgrade_source(source, target)
    if apply and changed:
        original_stat = target.stat()
        _write_replacing_same_directory(target, source, upgraded, original_stat)
    return UpgradeResult(changed=changed, applied=apply and changed, target=target)


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Check or apply the BEVFusion platform MLflow logging upgrade.",
    )
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="report status without editing")
    mode.add_argument("--apply", action="store_true", help="apply the upgrade in place")
    parser.add_argument("checkout", help="BEVFusion checkout root")
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = _parser().parse_args(argv)
    try:
        result = upgrade_checkout(Path(args.checkout), apply=args.apply)
    except (OSError, UnicodeError, UpgradeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    display_path = TARGET_RELATIVE_PATH.as_posix()
    if result.changed and result.applied:
        print(f"patched: {display_path}")
    elif result.changed:
        print(f"needs upgrade: {display_path} (rerun with --apply)")
    else:
        print(f"already upgraded: {display_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
