#!/usr/bin/env python3
"""Release-contract checks for the public RayTrain documentation."""

from __future__ import annotations

import re
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
PUBLIC_DOCS = (
    ROOT / "README.md",
    ROOT / "docs" / "README.md",
    ROOT / "docs" / "USER_GUIDE.md",
    ROOT / "docs" / "SUBMIT_GUIDE.md",
    ROOT / "docs" / "BEVFUSION_END_TO_END_GUIDE.md",
    ROOT / "docs" / "BEVFUSION_CODE_CHANGES.md",
    ROOT / "docs" / "ARCHITECTURE.md",
)
LINK = re.compile(r"(?<!!)\[[^]]+\]\(([^)]+)\)")


class DocumentationContractTest(unittest.TestCase):
    def test_relative_markdown_links_resolve(self) -> None:
        missing = []
        for document in PUBLIC_DOCS:
            for target in LINK.findall(document.read_text(encoding="utf-8")):
                path = target.split("#", 1)[0].strip()
                if not path or "://" in path or path.startswith("mailto:"):
                    continue
                resolved = (document.parent / path).resolve()
                if not resolved.exists():
                    missing.append(f"{document.relative_to(ROOT)} -> {path}")
        self.assertEqual(missing, [])

    def test_public_storage_contract_is_canonical(self) -> None:
        combined = "\n".join(path.read_text(encoding="utf-8") for path in PUBLIC_DOCS)
        self.assertIn("tos://shanghai-data-transfer/ray-train/public/", combined)
        self.assertIn("/mnt/storage/public", combined)
        self.assertIn("public/labeled", combined)
        self.assertNotIn("ray-train/tenants/local/datasets/public", combined)
        self.assertNotIn("raytrain/public", combined)
        self.assertNotIn("ray-train/tray-train/public", combined)

    def test_selected_directory_rule_is_explicit(self) -> None:
        submit = (ROOT / "docs" / "SUBMIT_GUIDE.md").read_text(encoding="utf-8")
        guide = (ROOT / "docs" / "BEVFUSION_END_TO_END_GUIDE.md").read_text(
            encoding="utf-8"
        )
        self.assertIn("不要把所选子目录重复拼到环境变量后面", submit)
        self.assertIn('path: ""', guide)
        self.assertIn("path: bevfusion/fz-3dod-v1", guide)
        self.assertIn("$PLATFORM_DATASET_PATH/platform-validation/", guide)

    def test_end_to_end_guide_contains_all_required_code(self) -> None:
        guide = (ROOT / "docs" / "BEVFUSION_END_TO_END_GUIDE.md").read_text(
            encoding="utf-8"
        )
        required = (
            "class DatasetPathResolver",
            "def configure_platform_output(cfg)",
            "def start_platform_mlflow",
            "tools/platform_data_preflight.py",
            "spk-rayjob submit",
            "ray job submit",
            "PLATFORM_CHECKPOINT_PATH",
            "mmdet3d/runner/__init__.py",
        )
        for marker in required:
            with self.subTest(marker=marker):
                self.assertIn(marker, guide)
        self.assertIn("5.1～5.6 和 5.8", guide)
        self.assertLess(
            guide.index('logger.info(f"Model:\\n{model}")'),
            guide.index("platform_mlflow = start_platform_mlflow"),
        )

    def test_native_submission_boundary_is_not_overstated(self) -> None:
        readme = (ROOT / "README.md").read_text(encoding="utf-8")
        submit = (ROOT / "docs" / "SUBMIT_GUIDE.md").read_text(encoding="utf-8")
        self.assertIn("不自动获得任务产物浏览和一键续训", readme)
        self.assertIn("不会自动绑定到平台“训练产物”浏览根", submit)
        self.assertIn("command -v jq", submit)

    def test_user_docs_do_not_depend_on_maintainer_machine(self) -> None:
        user_docs = PUBLIC_DOCS[2:6]
        combined = "\n".join(path.read_text(encoding="utf-8") for path in user_docs)
        self.assertNotIn("/opt/guofeng/", combined)
        self.assertNotIn("qomolo-desktop", combined)
        self.assertNotRegex(combined, r"/root/raytrain-acceptance")
        self.assertNotRegex(combined, r"glpat-[A-Za-z0-9_-]{12,}")
        self.assertNotRegex(combined, r"rpt_[A-Za-z0-9_-]{20,}")

    def test_code_fences_are_balanced(self) -> None:
        unbalanced = []
        for document in PUBLIC_DOCS:
            count = sum(1 for line in document.read_text(encoding="utf-8").splitlines()
                        if line.startswith("```"))
            if count % 2:
                unbalanced.append(str(document.relative_to(ROOT)))
        self.assertEqual(unbalanced, [])


if __name__ == "__main__":
    unittest.main(verbosity=2)
