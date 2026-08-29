#!/usr/bin/env python3
"""Release-contract checks for the public RayTrain documentation."""

from __future__ import annotations

import importlib.util
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
NVME_CACHE_DESIGN = (
    ROOT
    / "docs"
    / "superpowers"
    / "specs"
    / "2026-08-23-nvme-cache-and-gpu-fallback-placement-design.md"
)
NVME_CACHE_PLAN = (
    ROOT
    / "docs"
    / "superpowers"
    / "plans"
    / "2026-08-23-nvme-cache-and-gpu-fallback-placement.md"
)
PLATFORM_ROADMAP = ROOT / "docs" / "PLATFORM_ROADMAP.md"
RAY_TRAIN_MANAGED_GUIDE = ROOT / "docs" / "RAY_TRAIN_MANAGED_GUIDE.md"
CONTRACT_DOCS = PUBLIC_DOCS + (
    ROOT / "docs" / "HANDOVER_GUIDE.md",
    ROOT / "docs" / "BUILD_AND_DEPLOY.md",
    ROOT / "docs" / "OPERATIONS_GUIDE.md",
    ROOT / "docs" / "NVME_CACHE_GUIDE.md",
    RAY_TRAIN_MANAGED_GUIDE,
    ROOT / "docs" / "BEVFUSION_RUNBOOK.md",
    ROOT / "ops" / "mlflow" / "README.md",
    NVME_CACHE_DESIGN,
    NVME_CACHE_PLAN,
    PLATFORM_ROADMAP,
)
USER_FACING_DOCS = (
    ROOT / "README.md",
    ROOT / "docs" / "USER_GUIDE.md",
    ROOT / "docs" / "SUBMIT_GUIDE.md",
    ROOT / "docs" / "BEVFUSION_END_TO_END_GUIDE.md",
    ROOT / "docs" / "BEVFUSION_RUNBOOK.md",
)
BUILD_AND_DEPLOY = ROOT / "docs" / "BUILD_AND_DEPLOY.md"
ARCHITECTURE_ASSETS = {
    "overview": ROOT / "docs/architecture/ray-training-platform-production-architecture-v4",
    "control": ROOT / "docs/architecture/ray-training-platform-control-plane-tenancy-v1",
    "lifecycle": ROOT / "docs/architecture/ray-training-platform-job-lifecycle-v1",
    "storage": ROOT / "docs/architecture/ray-training-platform-storage-observability-v1",
}
ARCHITECTURE_COMPONENTS = {
    "overview": (
        "Web Portal", "spk-rayjob", "Ray Jobs API", "IDC NGINX Ingress",
        "私网 ALB", "Backend API", "Kueue", "KubeRay Operator",
        "Ray Head", "Ray Worker", "TOS", "FSX CSI", "双 NVMe",
        "Alloy", "Loki", "Prometheus Operator", "MLflow",
    ),
    "control": (
        "SuperAdmin", "TenantAdmin", "Engineer", "本地会话", "OIDC 会话",
        "PAT", "Git 凭据", "tenant-&lt;team&gt;", "LocalQueue",
        "ClusterQueue", "ResourceFlavor", "PostgreSQL",
    ),
    "lifecycle": (
        "提交前检查", "Kueue Workload", "RayJob", "Submitter",
        "GCS", "Dashboard", "PyTorch DDP", "NCCL", "TTL 回收",
        "任务历史", "Checkpoint",
    ),
    "storage": (
        "/mnt/storage/me", "/mnt/storage/team", "/mnt/storage/public",
        "PLATFORM_DATASET_PATH", "PLATFORM_OUTPUT_PATH",
        "PLATFORM_CHECKPOINT_PATH", "IRSA", "FSX Agent", "PV / PVC",
        "preload: input", "DCGM Exporter", "Node Exporter",
        "kube-state-metrics", "MLflow PostgreSQL", "Artifact 空间",
    ),
}
MLFLOW_PUBLIC_DOCS = (
    ROOT / "README.md",
    ROOT / "docs" / "ARCHITECTURE.md",
    BUILD_AND_DEPLOY,
    ROOT / "docs" / "USER_GUIDE.md",
)
TASK17_DOCS = (
    ROOT / "README.md",
    ROOT / "docs" / "ARCHITECTURE.md",
    ROOT / "docs" / "BUILD_AND_DEPLOY.md",
    ROOT / "docs" / "OPERATIONS_GUIDE.md",
    ROOT / "docs" / "USER_GUIDE.md",
    ROOT / "docs" / "SUBMIT_GUIDE.md",
    ROOT / "docs" / "NVME_CACHE_GUIDE.md",
    ROOT / "docs" / "BEVFUSION_END_TO_END_GUIDE.md",
    PLATFORM_ROADMAP,
    RAY_TRAIN_MANAGED_GUIDE,
)
LINK = re.compile(r"(?<!!)\[[^]]+\]\(([^)]+)\)")


def markdown_section(markdown: str, heading: str) -> str:
    """Return one Markdown section, including nested subsections."""
    lines = markdown.splitlines()
    target_level = len(heading) - len(heading.lstrip("#"))
    start = None

    for index, line in enumerate(lines):
        if line == heading:
            start = index
            continue
        if start is None or not line.startswith("#"):
            continue
        level = len(line) - len(line.lstrip("#"))
        if level <= target_level:
            return "\n".join(lines[start:index])

    if start is None:
        raise AssertionError(f"Markdown heading not found: {heading}")
    return "\n".join(lines[start:])


def png_dimensions(path: Path) -> tuple[int, int]:
    data = path.read_bytes()[:24]
    if len(data) != 24 or data[:8] != b"\x89PNG\r\n\x1a\n":
        raise AssertionError(f"not a PNG file: {path}")
    return int.from_bytes(data[16:20], "big"), int.from_bytes(data[20:24], "big")


class DocumentationContractTest(unittest.TestCase):
    def assert_section_markers(
        self, label: str, section: str, markers: tuple[str, ...]
    ) -> None:
        for marker in markers:
            with self.subTest(section=label, marker=marker):
                self.assertIn(
                    marker,
                    section,
                    f"{label} is missing required marker: {marker}",
                )

    def test_managed_guide_names_exact_versions_engines_and_feature_gate(
        self,
    ) -> None:
        self.assertTrue(
            RAY_TRAIN_MANAGED_GUIDE.is_file(),
            "docs/RAY_TRAIN_MANAGED_GUIDE.md must be delivered",
        )
        guide = RAY_TRAIN_MANAGED_GUIDE.read_text(encoding="utf-8")
        for marker in (
            "Ray 2.56.1",
            "KubeRay 1.6.2",
            "--engine ray-train",
            "--engine ray-ddp",
            "RAY_TRAIN_MANAGED_ENABLED",
        ):
            with self.subTest(marker=marker):
                self.assertIn(marker, guide)

    def test_managed_guide_covers_complete_supported_workflows(self) -> None:
        guide = RAY_TRAIN_MANAGED_GUIDE.read_text(encoding="utf-8")
        for marker in (
            "Web Portal",
            "spk-rayjob submit",
            "ray job submit",
            "代码随任务上传",
            "python file.py",
            "python -m package.module",
            "configure_ray_train_managed_hook",
            "RayTrainManagedRestoreHook",
            "RayTrainManagedHook",
            "checkpoint",
            "resume",
            "mount",
            "cache",
            "ray-data",
            "Ray Dashboard",
            "MLflow",
            "Loki",
            "Prometheus",
            "step_time",
            "data_time",
            "NCCL",
            "失败含义",
            "修改 Python 或配置代码无需重建镜像",
            "submissionOrigin=portal",
            "submissionOrigin=ray-cli",
            "externalSubmissionId",
        ):
            with self.subTest(marker=marker):
                self.assertIn(marker, guide)

    def test_managed_examples_match_public_submission_contract(self) -> None:
        guide = RAY_TRAIN_MANAGED_GUIDE.read_text(encoding="utf-8")
        for marker in (
            "Portal 工作区快照不接受 PAT",
            "--max-failures 2",
            "--checkpoint-every-epochs 1",
            '"platform.training.engine":"ray-train"',
            '"platform.data.input-space":"public"',
            '"platform.data.input-path":"datasets/example/v1"',
            '"ray-platform.worker-replicas":"2"',
        ):
            with self.subTest(marker=marker):
                self.assertIn(marker, guide)
        for stale in (
            "--max-worker-failures",
            "--checkpoint-interval-seconds",
            '"platform.data.mode"',
            '"platform.training.workers"',
        ):
            with self.subTest(stale=stale):
                self.assertNotIn(stale, guide)

    def test_managed_rollout_matrix_matches_production_global_enablement(self) -> None:
        guide = RAY_TRAIN_MANAGED_GUIDE.read_text(encoding="utf-8")
        rollout = markdown_section(guide, "## 发布门禁与运行时版本")
        self.assert_section_markers(
            "managed rollout matrix",
            rollout,
            (
                "RAY_TRAIN_MANAGED_TENANTS",
                "全局开关关闭、租户在 managed allowlist",
                "全局开关关闭、租户不在 managed allowlist",
                "canary 还必须同时满足",
                "tenant 为空",
                "managed allowlist 为空",
                "所有非空团队",
                "runtime.availableEngines",
                "runtime.productionRayVersion",
                "Ray Jobs API 协议版本 `4`",
            ),
        )
        self.assertNotIn('backend.rayVersion: "2.35.0"', rollout)
        self.assertNotIn("Task 18", rollout)

    def test_native_managed_output_and_resume_are_server_bound(self) -> None:
        guide = RAY_TRAIN_MANAGED_GUIDE.read_text(encoding="utf-8")
        user_guide = (ROOT / "docs" / "USER_GUIDE.md").read_text(encoding="utf-8")
        bevfusion = (ROOT / "docs" / "BEVFUSION_END_TO_END_GUIDE.md").read_text(
            encoding="utf-8"
        )
        native = markdown_section(guide, "### 原生 Ray API")
        self.assert_section_markers(
            "native managed output",
            native,
            (
                "my-runs/native-ray/<job-id>",
                "PLATFORM_OUTPUT_PATH=/mnt/data/output",
                "不能通过 metadata 指定 output",
                "namespace",
                "Kubernetes selector",
            ),
        )
        resume = markdown_section(guide, "## checkpoint 与 resume")
        self.assert_section_markers(
            "checkpoint resume binding",
            resume,
            (
                "GET /api/v1/jobs/<old-job-id>/checkpoints",
                "第一个 complete checkpoint",
                ".platform/ray-train/<old-job-id>/checkpoints/<checkpoint-id>",
                "parentJobId",
                "resumeCheckpointId",
                "没有完整 checkpoint 时提交失败",
            ),
        )
        self.assertIn("--output-path managed-example", guide)
        self.assertNotIn("--output-path runs/managed-example", guide)
        self.assertNotIn("三者都会把**上一次运行自己的结果目录**", user_guide)
        self.assertNotIn("--checkpoint-path <", bevfusion)

    def test_kuberay_runbook_has_copyable_audited_parameter_template(self) -> None:
        operations = (ROOT / "docs" / "OPERATIONS_GUIDE.md").read_text(
            encoding="utf-8"
        )
        upgrade = markdown_section(
            operations, "### 5.10 Ray Train feature gates 与 KubeRay 1.6.2 升级"
        )
        self.assert_section_markers(
            "KubeRay guarded upgrade template",
            upgrade,
            (
                "RAY_TRAIN_MANAGED_TENANTS=",
                "KUBERAY_CONTEXT='<reviewed-context>'",
                'CONFIRM_KUBE_CONTEXT="$KUBERAY_CONTEXT"',
                "KUBERAY_BACKUP_PARENT='/secure/backup-parent'",
                "EXPECTED_KUBERAY_CRD_SHA256='<pre-audited-64-lowercase-hex>'",
                'CONFIRM_KUBERAY_CRD_SHA256="$EXPECTED_KUBERAY_CRD_SHA256"',
                "EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST='<pre-audited-64-lowercase-hex>'",
                'CONFIRM_KUBERAY_OPERATOR_IMAGE_DIGEST="$EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST"',
                "CONFIRM_KUBERAY_UPGRADE=1",
                "bash ops/kuberay/preflight-upgrade.sh",
                'bash ops/kuberay/backup.sh "$KUBERAY_BACKUP_PARENT"',
                "bash ops/kuberay/upgrade-1.6.2.sh",
                "bash ops/kuberay/verify.sh",
                "预先审计记录",
                "禁止在维护现场临时计算摘要后自行确认",
            ),
        )

    def test_build_guide_records_current_managed_runtime_without_conflating_protocol_version(self) -> None:
        build = BUILD_AND_DEPLOY.read_text(encoding="utf-8")
        self.assertIn("生产托管运行时为 Ray 2.56.1", build)
        self.assertIn('协议字段 `version: "4"`', build)
        self.assertIn("runtime.availableEngines", build)
        self.assertIn("runtime.productionRayVersion", build)
        self.assertNotIn('backend.rayVersion: "2.35.0"', build)
        self.assertNotIn("Task 18", build)

    def test_submission_origin_semantics_are_consistent(self) -> None:
        documents = tuple(
            path.read_text(encoding="utf-8")
            for path in (
                RAY_TRAIN_MANAGED_GUIDE,
                ROOT / "docs" / "SUBMIT_GUIDE.md",
                ROOT / "docs" / "BEVFUSION_END_TO_END_GUIDE.md",
            )
        )
        for document in documents:
            self.assertIn("submissionOrigin=portal", document)
            self.assertIn("submissionOrigin=ray-cli", document)
            self.assertNotIn("submissionOrigin=ray-native", document)
        guide = documents[0]
        self.assertIn("`spk-rayjob` 与原生 Ray API 均持久化为", guide)
        self.assertIn("externalSubmissionId", guide)

    def test_managed_operations_are_fail_closed_and_preserve_running_jobs(
        self,
    ) -> None:
        operations = (ROOT / "docs" / "OPERATIONS_GUIDE.md").read_text(
            encoding="utf-8"
        )
        for marker in (
            "不得修改运行中 RayJob",
            "RAY_TRAIN_MANAGED_ENABLED",
            "RAY_TRAIN_CANARY_ENABLED",
            "RAY_TRAIN_CANARY_TENANTS",
            "并行运行时镜像",
            "零训练负载",
            "ops/kuberay/preflight-upgrade.sh",
            "ops/kuberay/backup.sh",
            "ops/kuberay/upgrade-1.6.2.sh",
            "ops/kuberay/verify.sh",
            "KubeRay 1.6.2",
            "Ray 2.35",
            "History Server",
            "alpha",
            "Operator 绝不在训练期间升级",
            "回滚",
        ):
            with self.subTest(marker=marker):
                self.assertIn(marker, operations)

    def test_readme_and_architecture_are_managed_training_entrypoints(self) -> None:
        for path in (ROOT / "README.md", ROOT / "docs" / "ARCHITECTURE.md"):
            document = path.read_text(encoding="utf-8")
            for marker in (
                "Ray 编排 DDP",
                "Ray Train 托管",
                "Kueue",
                "多租户",
                "TOS",
                "FSX",
                "IDC",
                "NVMe",
                "Loki",
                "Prometheus",
                "Grafana",
                "MLflow",
                "Ray Dashboard",
                "RAY_TRAIN_MANAGED_GUIDE.md",
            ):
                with self.subTest(document=path.name, marker=marker):
                    self.assertIn(marker, document)

    def test_task17_docs_have_no_legacy_cli_private_paths_ips_or_acceptance_flags(
        self,
    ) -> None:
        combined = "\n".join(
            path.read_text(encoding="utf-8") for path in TASK17_DOCS if path.exists()
        )
        self.assertNotIn("rayctl", combined)
        self.assertNotIn("/opt/guofeng/", combined)
        self.assertNotRegex(combined, r"\b(?:\d{1,3}\.){3}\d{1,3}\b")
        self.assertNotRegex(combined, r"glpat-[A-Za-z0-9_-]{12,}")
        self.assertNotRegex(combined, r"rpt_[A-Za-z0-9_-]{20,}")
        self.assertNotRegex(combined, r"AKLT[A-Za-z0-9+/=]{12,}")
        self.assertNotIn("ALLOW_DESTRUCTIVE_FAULT_TESTS", combined)
        self.assertNotIn("ACCEPTANCE_PREFIX", combined)

    def test_user_submission_examples_use_the_managed_runtime_version(self) -> None:
        user_docs = "\n".join(
            path.read_text(encoding="utf-8")
            for path in (
                ROOT / "docs" / "USER_GUIDE.md",
                ROOT / "docs" / "SUBMIT_GUIDE.md",
                ROOT / "docs" / "BEVFUSION_END_TO_END_GUIDE.md",
                RAY_TRAIN_MANAGED_GUIDE,
            )
            if path.exists()
        )
        self.assertNotIn("ray[default]==2.35.0", user_docs)
        self.assertIn("ray[default]==2.56.1", user_docs)

    def test_nvme_cache_is_per_job_opt_in_without_training_image_rebuilds(
        self,
    ) -> None:
        design = NVME_CACHE_DESIGN.read_text(encoding="utf-8")
        plan = NVME_CACHE_PLAN.read_text(encoding="utf-8")
        roadmap = PLATFORM_ROADMAP.read_text(encoding="utf-8")

        policy = markdown_section(
            design, "### 5.1 基础设施可用性与管理员策略"
        )
        self.assert_section_markers(
            "design policy",
            policy,
            (
                "基础设施全局提供缓存能力",
                "每个训练任务按需选择 `off` 或 `runtime`",
                "初始默认 `off`",
                "管理员允许的容量为 `100Gi`、`200Gi`、`500Gi`",
                "runtime 默认申请 `200Gi`",
                "管理员上限为 `500Gi`",
                "未提供缓存参数的任务保持不变",
                "既有任务保持不变",
            ),
        )
        self.assertNotIn("enabled: true", policy)
        self.assertNotIn("自动获得任务级临时缓存", policy)

        submission = markdown_section(design, "### 5.2 提交接口")
        self.assert_section_markers(
            "design submission",
            submission,
            (
                "Web 创建/重提训练任务",
                "`spk-rayjob` 提供等价的 cache mode/size 参数",
                "`platform.cache.mode`",
                "`platform.cache.size`",
                "平台网关",
                "训练镜像保持不变",
            ),
        )

        runtime = markdown_section(design, "### 5.3 runtime 挂载语义")
        self.assert_section_markers(
            "design runtime",
            runtime,
            (
                "只给选择 `runtime` 的 Ray Head 和 Worker 添加 generic ephemeral PVC",
                "Submitter 不挂载本地缓存",
                "PLATFORM_CACHE_PATH=/mnt/cache",
                "Ray temp-dir=/mnt/cache/ray",
                "Ray object spilling=/mnt/cache/ray-spill/objects",
                "不会自动复制 `/mnt/storage/public`",
                "不会自动加速 PyTorch `DataLoader`",
                "监控组件不自动删除目录、不终止训练",
            ),
        )
        self.assertNotIn("Submitter 挂载本地缓存", runtime)
        self.assertNotIn("监控组件自动删除目录", runtime)
        self.assertNotIn("监控组件终止训练", runtime)

        storage_class = markdown_section(design, "### 4.2 StorageClass 契约")
        self.assert_section_markers(
            "design storage class",
            storage_class,
            (
                "`ray-cache-local`",
                "WaitForFirstConsumer",
                "reclaimPolicy: Delete",
                "只允许已登记的生产 GPU 节点进入 `nodePathMap`",
                "/data1/ray-cache",
                "/data2/ray-cache",
                "默认节点路径为空",
                "CPU 节点和未来未登记节点不能误供应缓存",
            ),
        )

        non_goals = markdown_section(design, "### 3.2 本期不做")
        self.assert_section_markers(
            "design non-goals",
            non_goals,
            (
                "不把 `/data1`、`/data2` 根目录直接暴露给用户",
                "不修改、重建或迁移运行中的 RayJob/RayCluster",
                "不重启 kubelet、containerd、FSX Agent、CNI 或 GPU 驱动",
            ),
        )
        self.assertNotIn("- 重启 kubelet", non_goals)
        self.assertNotIn("- 修改、重建或迁移运行中的 RayJob/RayCluster", non_goals)

        security = markdown_section(design, "## 11. 安全边界")
        self.assert_section_markers(
            "design security",
            security,
            (
                "`nodePathMap` 默认拒绝未知节点",
                "不启用不安全路径模板",
                "不接受用户提供宿主机路径",
                "看不到节点缓存根和其他任务目录",
            ),
        )
        self.assertNotIn("allowUnsafePathPattern: true", security)
        self.assertNotIn("允许用户提供宿主机路径", security)

        upgrade = markdown_section(design, "## 8. 在线升级与现有训练保护")
        self.assert_section_markers(
            "design upgrade",
            upgrade,
            (
                "只影响未来创建的 Pod 模板",
                "既有任务保持不变",
                "已经存在的 RayJob、RayCluster",
                "不 patch 现有 RayJob/RayCluster",
            ),
        )
        self.assertNotIn("会 patch 现有 RayJob/RayCluster", upgrade)

        dataset = markdown_section(design, "## 6. dataset 模式的后续门槛")
        self.assert_section_markers(
            "design dataset",
            dataset,
            (
                "dataset 模式不在本期范围",
                "不可变数据集 manifest",
                "预热",
                "基准门禁",
            ),
        )

        plan_policy = markdown_section(
            plan,
            "## Task 6: 平台 Chart 支持 CPU 优先、GPU 兜底和缓存 availability/policy",
        )
        self.assert_section_markers(
            "plan policy",
            plan_policy,
            (
                "`training.localCache.available`",
                "`training.localCache.policy.allowedSizes`",
                "`training.localCache.policy.defaultSize`",
                "`training.localCache.policy.maxSize`",
                "每任务默认 `off`",
            ),
        )
        self.assertNotIn("`training.localCache.enabled` 改为 `true`", plan_policy)

        plan_runtime = markdown_section(
            plan,
            "## Task 8: 实现每任务 API、平台网关 metadata 和 RayJob runtime 渲染",
        )
        self.assert_section_markers(
            "plan runtime",
            plan_runtime,
            (
                "每任务 API",
                "`platform.cache.mode`",
                "`platform.cache.size`",
                "平台网关",
                "只有 `runtime` 为 Head/Worker",
                "generic ephemeral PVC",
                "PLATFORM_CACHE_PATH=/mnt/cache",
                "Ray temp-dir `/mnt/cache/ray`",
                "object spilling `/mnt/cache/ray-spill/objects`",
                "Submitter 无挂载",
                "不得自动复制 `/mnt/storage/public`",
                "不得修改 DataLoader",
            ),
        )
        self.assertNotIn("必须修改 DataLoader", plan_runtime)

        plan_clients = markdown_section(
            plan, "## Task 9: Web、spk-rayjob、镜像构建与离线交付"
        )
        self.assert_section_markers(
            "plan clients",
            plan_clients,
            (
                "Web RED 测试",
                "`spk-rayjob` RED 测试",
                "Backend、Frontend 和 `spk-rayjob` 镜像",
                "不重建训练镜像",
            ),
        )

        current_iteration = markdown_section(roadmap, "## 当前迭代")
        self.assert_section_markers(
            "roadmap current iteration",
            current_iteration,
            (
                "按任务选择",
                "默认关闭",
                "200Gi / 500Gi / 1Ti / 2Ti / 4Ti / 5Ti",
                "最大 5Ti",
                "Ray Head/Worker 挂载 `/mnt/cache`、`/mnt/cache2`",
                "`PLATFORM_CACHE_PATH`",
                "Ray temp-dir",
                "object spilling",
                "`preload: input`",
                "`PLATFORM_DATASET_PATH`",
                "训练镜像保持不变",
            ),
        )

        future_gate = markdown_section(roadmap, "## 后续阶段门禁")
        self.assertIn("dataset 模式", future_gate)
        self.assertIn("不可变数据集 manifest", future_gate)
        self.assertIn("预热流程", future_gate)
        self.assertIn("基准门禁", future_gate)

    def test_nvme_cache_docs_are_in_relative_link_contract(self) -> None:
        required = {NVME_CACHE_DESIGN, NVME_CACHE_PLAN, PLATFORM_ROADMAP}
        self.assertTrue(required.issubset(CONTRACT_DOCS))

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

        for git_first_marker in (
            "git fetch --prune origin",
            "git pull --ff-only origin main",
            "EXPECTED_COMMIT='<full-commit-from-reviewed-main>'",
            'test "$(git rev-parse HEAD)" = "$EXPECTED_COMMIT"',
            'test -z "$(git status --short)"',
        ):
            self.assertIn(git_first_marker, deployment)

        for portable_setting in (
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

    def test_component_architecture_assets_exist_and_are_4k(self) -> None:
        for name, base in ARCHITECTURE_ASSETS.items():
            with self.subTest(diagram=name):
                svg = base.with_suffix(".svg")
                png = base.with_suffix(".png")
                self.assertTrue(svg.is_file(), svg)
                self.assertTrue(png.is_file(), png)
                self.assertEqual(png_dimensions(png), (3840, 2160))

    def test_component_architecture_diagrams_contain_approved_components(self) -> None:
        for name, markers in ARCHITECTURE_COMPONENTS.items():
            svg = ARCHITECTURE_ASSETS[name].with_suffix(".svg").read_text(
                encoding="utf-8"
            )
            for marker in markers:
                with self.subTest(diagram=name, marker=marker):
                    self.assertIn(marker, svg)

    def test_component_architecture_diagrams_do_not_leak_environment_details(self) -> None:
        for name, base in ARCHITECTURE_ASSETS.items():
            svg = base.with_suffix(".svg").read_text(encoding="utf-8")
            with self.subTest(diagram=name):
                self.assertNotRegex(svg, r"\b(?:\d{1,3}\.){3}\d{1,3}\b")
                self.assertNotIn("qomolo-desktop", svg)
                self.assertNotIn("welldriver", svg)
                self.assertNotRegex(svg, r"(?:×|x)\s*\d+\s*(?:副本|Pod|节点)")
                self.assertNotRegex(svg, r"(?:RTX|4090|180C|780G)")

    def test_component_architecture_renderer_escapes_and_is_deterministic(self) -> None:
        path = ROOT / "scripts/generate_architecture_diagrams.py"
        spec = importlib.util.spec_from_file_location("architecture_renderer", path)
        self.assertIsNotNone(spec)
        self.assertIsNotNone(spec.loader)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.assertEqual(
            module.svg_text("tenant-<team> & PAT"),
            "tenant-&lt;team&gt; &amp; PAT",
        )
        box = module.Box(10, 20, 180, 90, "tenant-<team>", ("PAT & OIDC",), "purple")
        self.assertEqual(module.render_box(box), module.render_box(box))
        self.assertNotIn("<team>", module.render_box(box))

    def test_active_docs_reference_v4_and_component_subdiagrams(self) -> None:
        readme = (ROOT / "README.md").read_text(encoding="utf-8")
        docs_index = (ROOT / "docs/README.md").read_text(encoding="utf-8")
        architecture = (ROOT / "docs/ARCHITECTURE.md").read_text(encoding="utf-8")
        handover = (ROOT / "docs/HANDOVER_GUIDE.md").read_text(encoding="utf-8")
        self.assertIn("ray-training-platform-production-architecture-v4.svg", readme)
        for base in ARCHITECTURE_ASSETS.values():
            filename = base.with_suffix(".svg").name
            self.assertIn(filename, docs_index)
            self.assertIn(filename, architecture)
        self.assertIn("ray-training-platform-production-architecture-v4.svg", handover)
        combined = "\n".join((readme, docs_index, architecture, handover))
        self.assertNotIn("ray-training-platform-production-architecture-v3.svg", combined)

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
