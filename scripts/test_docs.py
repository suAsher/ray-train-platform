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
CONTRACT_DOCS = PUBLIC_DOCS + (
    ROOT / "docs" / "BUILD_AND_DEPLOY.md",
    ROOT / "docs" / "BEVFUSION_RUNBOOK.md",
    ROOT / "ops" / "mlflow" / "README.md",
)
USER_FACING_DOCS = (
    ROOT / "README.md",
    ROOT / "docs" / "USER_GUIDE.md",
    ROOT / "docs" / "SUBMIT_GUIDE.md",
    ROOT / "docs" / "BEVFUSION_END_TO_END_GUIDE.md",
    ROOT / "docs" / "BEVFUSION_RUNBOOK.md",
)
BUILD_AND_DEPLOY = ROOT / "docs" / "BUILD_AND_DEPLOY.md"
MLFLOW_PUBLIC_DOCS = (
    ROOT / "README.md",
    ROOT / "docs" / "ARCHITECTURE.md",
    BUILD_AND_DEPLOY,
    ROOT / "docs" / "USER_GUIDE.md",
)
LINK = re.compile(r"(?<!!)\[[^]]+\]\(([^)]+)\)")


class DocumentationContractTest(unittest.TestCase):
    def test_relative_markdown_links_resolve(self) -> None:
        missing = []
        for document in CONTRACT_DOCS:
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

    def test_image_catalog_docs_allow_tags_and_document_scope_changes(self) -> None:
        admin = (ROOT / "docs" / "ADMIN_GUIDE.md").read_text(encoding="utf-8")
        self.assertIn("支持显式 tag", admin)
        self.assertIn("设为全平台", admin)
        self.assertIn("改为本团队", admin)
        self.assertNotIn("必须带 `@sha256:` digest", admin)

    def test_bevfusion_guide_documents_reported_operational_gaps(self) -> None:
        guide = (ROOT / "docs" / "BEVFUSION_END_TO_END_GUIDE.md").read_text(
            encoding="utf-8"
        )
        runbook = (ROOT / "docs" / "BEVFUSION_RUNBOOK.md").read_text(
            encoding="utf-8"
        )
        for marker in (
            'export PATH="$HOME/.local/bin:$PATH"',
            "用户名输入 `oauth2`",
            "不要把 PAT 放进 URL",
            'jq -r ".observedState"',
            "SUCCEEDED / FAILED / CANCELED / TIMED_OUT",
            "按时间正序",
            "当前 CLI 还没有产物列表子命令",
            "`bev_3dod_s1h` 已验证范围是 smoke-128",
            "`/healthz`",
        ):
            with self.subTest(marker=marker):
                self.assertIn(marker, guide)
        self.assertIn("S1H 全量训练不能直接复用", runbook)
        submit = (ROOT / "docs" / "SUBMIT_GUIDE.md").read_text(encoding="utf-8")
        changes = (ROOT / "docs" / "BEVFUSION_CODE_CHANGES.md").read_text(
            encoding="utf-8"
        )
        self.assertIn("S1H 的全量训练不能直接套用", submit)
        self.assertIn("历史 S1H Head 只有 9 类", changes)
        for document in (runbook, changes):
            succeeded_s1h_rows = (
                line
                for line in document.splitlines()
                if "bev_3dod_s1h" in line and "SUCCEEDED" in line
            )
            for row in succeeded_s1h_rows:
                with self.subTest(row=row):
                    self.assertIn("smoke-128", row)
        self.assertNotRegex(guide, r"https://oauth2:[^@\s]+@gitlab")

    def test_bevfusion_guide_captures_first_run_failure_lessons(self) -> None:
        guide = (ROOT / "docs" / "BEVFUSION_END_TO_END_GUIDE.md").read_text(
            encoding="utf-8"
        )
        required = (
            "final_merged_nuscenes_infos_train.pkl",
            "当前 CLI 不支持 `--job-id`",
            "spk-rayjob jobs --output json",
            "`--config` 是登录认证 JSON",
            "`.spk-rayjob.yaml` 是任务默认值",
            "`statusMessage`",
            "不保证包含完整 traceback",
            "1 卡探针调试",
            "platform_directory_probe.py",
            "platform_mlflow_probe.py",
            "platform_result_probe.py",
            "7～9 分钟",
            "working directory archive",
            "与 Git commit 状态无关",
            "dynamic loss scale",
            "一个 2×8",
            "未发布的本地 commit 不能作为交付链接",
        )
        for marker in required:
            with self.subTest(marker=marker):
                self.assertIn(marker, guide)

        self.assertRegex(
            guide,
            re.escape("0429_pkl/fz/merged_nuscenes_infos_*.pkl")
            + r".*?根目录.*?留空",
        )
        self.assertRegex(
            guide,
            r"platform-validation/annotations/fz-0429-platform-smoke-128/.*?bevfusion/fz-3dod-v1",
        )
        for script_name in (
            "platform_directory_probe.py",
            "platform_mlflow_probe.py",
            "platform_result_probe.py",
        ):
            marker = f"`tools/{script_name}`"
            fence = guide.index("```python", guide.index(marker)) + len("```python")
            end = guide.index("```", fence)
            compile(guide[fence:end], script_name, "exec")

    def test_mlflow_probe_runbook_explains_failure_semantics_and_placement(self) -> None:
        runbook = (ROOT / "ops" / "mlflow" / "README.md").read_text(
            encoding="utf-8"
        )
        for marker in (
            "`0/1 Running`",
            "`Pending` 不等于 DNS 或 FSX 检查失败",
            "MLflow 不申请 `nvidia.com/gpu`",
            "PostgreSQL 和轻量 ingest 仍放在 CPU/control-plane 节点",
            "所有 MLflow serving 节点 Ready",
        ):
            with self.subTest(marker=marker):
                self.assertIn(marker, runbook)

    def test_native_submission_boundary_is_not_overstated(self) -> None:
        readme = (ROOT / "README.md").read_text(encoding="utf-8")
        submit = (ROOT / "docs" / "SUBMIT_GUIDE.md").read_text(encoding="utf-8")
        self.assertIn("不自动获得任务产物浏览和一键续训", readme)
        self.assertIn("不会自动绑定到平台“训练产物”浏览根", submit)
        self.assertIn("command -v jq", submit)

    def test_user_docs_do_not_depend_on_maintainer_machine(self) -> None:
        combined = "\n".join(
            path.read_text(encoding="utf-8") for path in USER_FACING_DOCS
        )
        self.assertNotIn("/opt/guofeng/", combined)
        self.assertNotIn("qomolo-desktop", combined)
        self.assertNotRegex(combined, r"/root/raytrain-acceptance")
        self.assertNotRegex(combined, r"glpat-[A-Za-z0-9_-]{12,}")
        self.assertNotRegex(combined, r"rpt_[A-Za-z0-9_-]{20,}")
        self.assertNotIn("同版本接入工具包", combined)
        self.assertNotIn("内部工具包", combined)

    def test_deployment_guide_is_portable_and_contains_no_private_operator_details(
        self,
    ) -> None:
        deployment = BUILD_AND_DEPLOY.read_text(encoding="utf-8")

        self.assertNotIn("/opt/guofeng/", deployment)
        self.assertNotIn("qomolo-desktop", deployment)
        self.assertNotIn("guofeng.su", deployment)
        self.assertNotIn("/root/", deployment)
        self.assertNotRegex(deployment, r"\broot@")
        self.assertNotRegex(deployment, r"/(?:Users|home)/[A-Za-z0-9._-]+/")
        self.assertNotRegex(deployment, r"(?:~|/[^\s]+)?/\.ssh/[^\s]+")
        self.assertNotRegex(deployment, r"\b(?:\d{1,3}\.){3}\d{1,3}\b")
        self.assertNotRegex(deployment, r"\bcert-[a-f0-9]{16,}\b")
        self.assertNotRegex(deployment, r"glpat-[A-Za-z0-9_-]{12,}")
        self.assertNotRegex(deployment, r"rpt_[A-Za-z0-9_-]{20,}")
        self.assertNotRegex(deployment, r"AKLT[A-Za-z0-9+/=]{12,}")
        self.assertNotIn("BEGIN PRIVATE KEY", deployment)

        for portable_setting in (
            "BUILD_USER='<ssh-user>'",
            "BUILD_HOST='<build-host>'",
            "SSH_KEY='<path-to-private-key>'",
            "PLATFORM_REPO_ROOT='<absolute-build-directory>'",
            "REGISTRY_PROJECT='<registry-project>'",
            "CORPORATE_DNS_A='<dns-server-a>'",
            "CORPORATE_DNS_B='<dns-server-b>'",
            "CERTIFICATE_ID='<certificate-id>'",
        ):
            with self.subTest(portable_setting=portable_setting):
                self.assertIn(portable_setting, deployment)

    def test_mlflow_native_dashboard_policy_is_explicit(self) -> None:
        readme = (ROOT / "README.md").read_text(encoding="utf-8")
        architecture = (ROOT / "docs" / "ARCHITECTURE.md").read_text(
            encoding="utf-8"
        )
        user_guide = (ROOT / "docs" / "USER_GUIDE.md").read_text(encoding="utf-8")
        combined = "\n".join((readme, architecture, user_guide))

        for document in (readme, architecture):
            self.assertIn("原生 MLflow", document)
            self.assertIn("完整管理界面", document)

        for marker in (
            "实验中心是平台筛选视图",
            "原生 MLflow",
            "完整管理界面",
            "所有平台认证用户",
            "创建、修改、删除实验、Run 和模型注册条目",
            "上传、下载 MLflow Artifact",
            "当前明确策略",
        ):
            with self.subTest(marker=marker):
                self.assertIn(marker, combined)

    def test_user_guide_names_the_exact_mlflow_dashboard_button(self) -> None:
        user_guide = (ROOT / "docs" / "USER_GUIDE.md").read_text(encoding="utf-8")
        self.assertIn("**打开 MLflow 管理界面**", user_guide)

    def test_mlflow_artifacts_use_secretless_fsx_irsa(self) -> None:
        contents = {
            path: path.read_text(encoding="utf-8") for path in MLFLOW_PUBLIC_DOCS
        }
        combined = "\n".join(contents.values())

        for path, document in contents.items():
            with self.subTest(document=path.relative_to(ROOT)):
                self.assertIn(
                    "vke-cluster/ray-train/platform/mlflow-artifacts/", document
                )
                self.assertIn("FSX CSI", document)
                self.assertIn("`/mlflow-artifacts`", document)
                self.assertIn("`/mnt/storage/public`", document)

        for document in (
            contents[ROOT / "docs" / "ARCHITECTURE.md"],
            contents[BUILD_AND_DEPLOY],
        ):
            self.assertIn("静态 PV/PVC", document)
            self.assertIn("`fsx-agent`", document)
            self.assertIn("`CREDENTIALS_TYPE=IRSA`", document)
            self.assertIn("`ROLE_NAME_FOR_IRSA` 非空", document)
            self.assertIn("CSIDriver", document)
            self.assertIn("DaemonSet 全部可用", document)

        for marker in (
            "MLflow Pod 只看到 `/mlflow-artifacts` 挂载根",
            "不注入 TOS/AWS AK/SK",
            "MLflow Pod、PV 和 PVC 都不包含 AK/SK 或 Secret 引用",
            "MLflow Artifact 与 `/mnt/storage/public` 治理数据隔离",
        ):
            with self.subTest(marker=marker):
                self.assertIn(marker, combined)

        self.assertNotIn("ray-train-platform/tos-fsx-credentials", combined)
        self.assertIn("`mlflow-artifacts-irsa`", contents[ROOT / "docs" / "BUILD_AND_DEPLOY.md"])

        for obsolete in (
            "对象存储 Artifact 根目录",
            "平台专用 TOS 前缀",
            "TOS Artifact",
        ):
            with self.subTest(obsolete=obsolete):
                self.assertNotIn(obsolete, combined)

    def test_mlflow_has_only_same_domain_clusterip_access(self) -> None:
        deployment = (ROOT / "docs" / "BUILD_AND_DEPLOY.md").read_text(
            encoding="utf-8"
        )
        mlflow_ops = (ROOT / "ops" / "mlflow" / "README.md").read_text(
            encoding="utf-8"
        )
        combined = "\n".join((deployment, mlflow_ops))
        for marker in (
            "https://raytrain.wellspiking.ai/mlflow/",
            "ClusterIP",
            "不创建 NodePort",
            "不创建独立 Ingress",
        ):
            with self.subTest(marker=marker):
                self.assertIn(marker, combined)

    def test_daily_shortest_path_is_documented_in_order(self) -> None:
        guide = (ROOT / "docs" / "USER_GUIDE.md").read_text(encoding="utf-8")
        markers = (
            "改代码",
            "spk-rayjob submit --watch",
            "spk-rayjob logs -f",
            "平台实验中心",
            "原生 MLflow",
            "断点续跑/重提",
        )
        positions = [guide.index(marker) for marker in markers]
        self.assertEqual(positions, sorted(positions))

    def test_bevfusion_prerequisites_appear_before_clone(self) -> None:
        guide = (ROOT / "docs" / "BEVFUSION_END_TO_END_GUIDE.md").read_text(
            encoding="utf-8"
        )
        clone_position = guide.index("git clone --single-branch")
        prerequisites = guide[:clone_position]
        for marker in (
            "平台账号",
            "GitLab 访问权",
            "已批准镜像",
            "团队 16 GPU 配额",
        ):
            with self.subTest(marker=marker):
                self.assertIn(marker, prerequisites)

    def test_cli_installation_is_cross_platform_and_portal_discoverable(self) -> None:
        submit = (ROOT / "docs" / "SUBMIT_GUIDE.md").read_text(encoding="utf-8")
        for marker in ("Linux", "macOS", "Windows", "Portal“外部提交”"):
            with self.subTest(marker=marker):
                self.assertIn(marker, submit)

    def test_bevfusion_resource_profiles_do_not_mix_smoke_and_production(self) -> None:
        guide = (ROOT / "docs" / "BEVFUSION_END_TO_END_GUIDE.md").read_text(
            encoding="utf-8"
        )
        runbook = (ROOT / "docs" / "BEVFUSION_RUNBOOK.md").read_text(
            encoding="utf-8"
        )
        combined = "\n".join((guide, runbook))
        self.assertIn("32 CPU / 128GiB 仅用于 smoke", combined)
        self.assertIn("每个 Worker 64 CPU / 256GiB", combined)
        self.assertIn('"ray-platform.cpu-per-worker":"64"', runbook)
        self.assertIn('"ray-platform.memory-per-worker":"256Gi"', runbook)
        submit = (ROOT / "docs" / "SUBMIT_GUIDE.md").read_text(encoding="utf-8")
        self.assertIn('"ray-platform.cpu-per-worker":"64"', submit)
        self.assertIn('"ray-platform.memory-per-worker":"256Gi"', submit)

    def test_rayignore_omits_verified_overbroad_data_rules(self) -> None:
        guide = (ROOT / "docs" / "BEVFUSION_END_TO_END_GUIDE.md").read_text(
            encoding="utf-8"
        )
        self.assertIn("带前导 `/` 的同名写法都可能误伤", guide)
        self.assertIn("mmdet3d/datasets", guide)
        self.assertNotRegex(guide, r"(?m)^/?(?:data|datasets|work_dirs)/$")

    def test_code_fences_are_balanced(self) -> None:
        unbalanced = []
        for document in CONTRACT_DOCS:
            count = sum(1 for line in document.read_text(encoding="utf-8").splitlines()
                        if line.startswith("```"))
            if count % 2:
                unbalanced.append(str(document.relative_to(ROOT)))
        self.assertEqual(unbalanced, [])


if __name__ == "__main__":
    unittest.main(verbosity=2)
