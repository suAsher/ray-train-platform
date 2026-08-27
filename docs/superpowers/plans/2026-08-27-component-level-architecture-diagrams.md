# Component-Level Architecture Diagrams Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver one production overview（生产总架构图）and three component-level sub-diagrams as maintainable SVG plus 3840×2160 PNG, then integrate them into the active documentation.

**Architecture:** A small data-driven Python renderer will generate the four SVG files from explicit component, lane, and arrow definitions. A portable shell wrapper will render SVG to PNG with an installed Chromium-compatible browser. Documentation contract tests will verify assets, required component labels, privacy constraints, image dimensions, and active-document references.

**Tech Stack:** Python 3 standard library, SVG 1.1, Bash, Chromium headless rendering, `unittest`, Markdown.

**交付图清单：** 生产总架构图、控制面与多租户、训练任务生命周期、存储与可观测性。总图表达当前生产系统全貌，三张子图分别解释权限与调度、任务执行链路、数据与观测链路。

---

## File map

**Create:**

- `scripts/generate_architecture_diagrams.py` — reusable SVG primitives and the four explicit diagram definitions.
- `scripts/render-architecture-diagrams.sh` — regenerate SVG and render all PNG assets at 2× density.
- `docs/architecture/ray-training-platform-production-architecture-v4.svg`
- `docs/architecture/ray-training-platform-production-architecture-v4.png`
- `docs/architecture/ray-training-platform-control-plane-tenancy-v1.svg`
- `docs/architecture/ray-training-platform-control-plane-tenancy-v1.png`
- `docs/architecture/ray-training-platform-job-lifecycle-v1.svg`
- `docs/architecture/ray-training-platform-job-lifecycle-v1.png`
- `docs/architecture/ray-training-platform-storage-observability-v1.svg`
- `docs/architecture/ray-training-platform-storage-observability-v1.png`
- `docs/archive/architecture/README.md` — explains why v3 was archived and which file is current.

**Modify:**

- `scripts/test_docs.py` — architecture asset, content, privacy, link, and PNG-size contracts.
- `README.md` — replace v3 overview with v4.
- `docs/README.md` — link the overview and three sub-diagrams.
- `docs/ARCHITECTURE.md` — embed all four diagrams in their matching sections.
- `docs/HANDOVER_GUIDE.md` — use v4 and link component sub-diagrams from operations sections.

**Move after v4 acceptance:**

- `docs/architecture/ray-training-platform-production-architecture-v3.svg` → `docs/archive/architecture/ray-training-platform-production-architecture-v3.svg`
- `docs/architecture/ray-training-platform-production-architecture-v3.png` → `docs/archive/architecture/ray-training-platform-production-architecture-v3.png`

## Task 1: Lock the diagram contract in tests

**Files:**

- Modify: `scripts/test_docs.py`
- Test: `scripts/test_docs.py`

- [ ] **Step 1: Add exact architecture asset constants**

Add these constants after `BUILD_AND_DEPLOY`:

```python
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
```

- [ ] **Step 2: Add a PNG dimension helper**

Add the standard-library helper near `markdown_section`:

```python
def png_dimensions(path: Path) -> tuple[int, int]:
    data = path.read_bytes()[:24]
    if len(data) != 24 or data[:8] != b"\x89PNG\r\n\x1a\n":
        raise AssertionError(f"not a PNG file: {path}")
    return int.from_bytes(data[16:20], "big"), int.from_bytes(data[20:24], "big")
```

- [ ] **Step 3: Add failing asset and component tests**

Add these tests to `DocumentationContractTest`:

```python
def test_component_architecture_assets_exist_and_are_full_hd_2x(self) -> None:
    for name, base in ARCHITECTURE_ASSETS.items():
        with self.subTest(diagram=name):
            svg = base.with_suffix(".svg")
            png = base.with_suffix(".png")
            self.assertTrue(svg.is_file(), svg)
            self.assertTrue(png.is_file(), png)
            self.assertEqual(png_dimensions(png), (3840, 2160))

def test_component_architecture_diagrams_contain_approved_components(self) -> None:
    for name, markers in ARCHITECTURE_COMPONENTS.items():
        svg = ARCHITECTURE_ASSETS[name].with_suffix(".svg").read_text(encoding="utf-8")
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
```

- [ ] **Step 4: Add failing documentation reference tests**

```python
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
```

- [ ] **Step 5: Run the tests and confirm RED**

Run:

```bash
python3 scripts/test_docs.py
```

Expected: failures report the four missing v4/v1 assets and active documents still referencing v3.

- [ ] **Step 6: Commit the contract tests**

```bash
git add scripts/test_docs.py
git commit -m "test: define component architecture diagram contract"
```

## Task 2: Implement reusable SVG primitives

**Files:**

- Create: `scripts/generate_architecture_diagrams.py`
- Test: `scripts/test_docs.py`

- [ ] **Step 1: Create immutable diagram models**

Use frozen dataclasses so diagram definitions cannot mutate shared state:

```python
from __future__ import annotations

from dataclasses import dataclass
from html import escape
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
OUT = ROOT / "docs" / "architecture"
WIDTH = 1920
HEIGHT = 1080

@dataclass(frozen=True)
class Box:
    x: int
    y: int
    width: int
    height: int
    title: str
    lines: tuple[str, ...] = ()
    tone: str = "blue"
    dashed: bool = False

@dataclass(frozen=True)
class Lane:
    x: int
    y: int
    width: int
    height: int
    title: str
    tone: str = "blue"

@dataclass(frozen=True)
class Arrow:
    points: tuple[tuple[int, int], ...]
    kind: str
    label: str = ""
```

- [ ] **Step 2: Implement escaping and SVG primitives**

Implement these exact primitives:

```python
def svg_text(value: str) -> str:
    return escape(value, quote=True)

TONES = {
    "blue": ("#f4f9ff", "#6fa5ea", "#1460ce"),
    "green": ("#f3fff8", "#64b987", "#13836f"),
    "orange": ("#fff9ef", "#df8b45", "#df7b2e"),
    "purple": ("#faf6ff", "#9c83e1", "#7350c7"),
    "gray": ("#f7f8fa", "#9aa6b8", "#64748b"),
}

ARROW_CLASSES = {
    "request": "request",
    "control": "control",
    "data": "data",
    "observe": "observe",
    "planned": "planned",
}

def render_lane(lane: Lane) -> str:
    fill, stroke, accent = TONES[lane.tone]
    badge_width = max(150, min(300, 68 + len(lane.title) * 19))
    return (
        f'<g class="lane"><rect x="{lane.x}" y="{lane.y}" width="{lane.width}" '
        f'height="{lane.height}" rx="18" fill="#ffffff" stroke="{stroke}" stroke-width="1.6"/>'
        f'<rect x="{lane.x + 18}" y="{lane.y + 14}" width="{badge_width}" height="38" '
        f'rx="10" fill="{accent}"/>'
        f'<text x="{lane.x + 34}" y="{lane.y + 40}" class="lane-title">'
        f'{svg_text(lane.title)}</text></g>'
    )

def render_box(box: Box) -> str:
    fill, stroke, _ = TONES[box.tone]
    dash = ' stroke-dasharray="8 6"' if box.dashed else ""
    line_nodes = "".join(
        f'<text x="{box.x + box.width / 2:g}" y="{box.y + 56 + index * 21}" '
        f'class="box-line" text-anchor="middle">{svg_text(line)}</text>'
        for index, line in enumerate(box.lines[:3])
    )
    return (
        f'<g class="component"><rect x="{box.x}" y="{box.y}" width="{box.width}" '
        f'height="{box.height}" rx="14" fill="{fill}" stroke="{stroke}" '
        f'stroke-width="1.5"{dash}/>'
        f'<text x="{box.x + box.width / 2:g}" y="{box.y + 31}" '
        f'class="box-title" text-anchor="middle">{svg_text(box.title)}</text>'
        f'{line_nodes}</g>'
    )

def render_arrow(arrow: Arrow) -> str:
    css_class = ARROW_CLASSES[arrow.kind]
    points = " ".join(f"{x},{y}" for x, y in arrow.points)
    middle_x, middle_y = arrow.points[len(arrow.points) // 2]
    label = ""
    if arrow.label:
        label = (
            f'<text x="{middle_x}" y="{middle_y - 8}" class="arrow-label" '
            f'text-anchor="middle">{svg_text(arrow.label)}</text>'
        )
    return f'<g>{label}<polyline points="{points}" class="arrow {css_class}"/></g>'

def render_legend(x: int, y: int) -> str:
    entries = (
        ("request", "请求流"),
        ("control", "控制流"),
        ("data", "数据流"),
        ("observe", "观测流"),
        ("planned", "演进方向"),
    )
    nodes = []
    for index, (kind, label) in enumerate(entries):
        offset = index * 145
        nodes.append(
            f'<line x1="{x + offset}" y1="{y}" x2="{x + offset + 48}" y2="{y}" '
            f'class="arrow {kind}"/><text x="{x + offset + 58}" y="{y + 5}" '
            f'class="legend-text">{svg_text(label)}</text>'
        )
    return '<g class="legend">' + "".join(nodes) + "</g>"

def render_svg(title: str, description: str, lanes: tuple[Lane, ...],
               boxes: tuple[Box, ...], arrows: tuple[Arrow, ...]) -> str:
    lane_nodes = "".join(render_lane(lane) for lane in lanes)
    box_nodes = "".join(render_box(box) for box in boxes)
    arrow_nodes = "".join(render_arrow(arrow) for arrow in arrows)
    return f'''<svg xmlns="http://www.w3.org/2000/svg" width="1920" height="1080"
 viewBox="0 0 1920 1080" role="img" aria-labelledby="title desc">
<title id="title">{svg_text(title)}</title>
<desc id="desc">{svg_text(description)}</desc>
<defs>
  <filter id="shadow" x="-15%" y="-20%" width="130%" height="145%">
    <feDropShadow dx="0" dy="3" stdDeviation="4" flood-color="#153a72" flood-opacity=".09"/>
  </filter>
  <marker id="arrow-blue" markerWidth="9" markerHeight="9" refX="8" refY="3" orient="auto">
    <path d="M0 0L0 6L8 3Z" fill="#2f6fd2"/>
  </marker>
  <marker id="arrow-green" markerWidth="9" markerHeight="9" refX="8" refY="3" orient="auto">
    <path d="M0 0L0 6L8 3Z" fill="#13836f"/>
  </marker>
  <marker id="arrow-orange" markerWidth="9" markerHeight="9" refX="8" refY="3" orient="auto">
    <path d="M0 0L0 6L8 3Z" fill="#df7b2e"/>
  </marker>
</defs>
<style>
text{{font-family:Inter,"PingFang SC","Microsoft YaHei","Noto Sans CJK SC",Arial,sans-serif;fill:#152445}}
.page-title{{font-size:38px;font-weight:800}} .lane-title{{font-size:18px;font-weight:800;fill:#fff}}
.box-title{{font-size:16px;font-weight:750}} .box-line{{font-size:12px;fill:#5a6881}}
.arrow{{fill:none;stroke-width:2.2}} .request,.control{{stroke:#2f6fd2;marker-end:url(#arrow-blue)}}
.data{{stroke:#13836f;stroke-width:3;marker-end:url(#arrow-green)}}
.observe{{stroke:#df7b2e;stroke-dasharray:7 5;marker-end:url(#arrow-orange)}}
.planned{{stroke:#7b879b;stroke-dasharray:9 6}} .arrow-label{{font-size:11px;fill:#526581}}
.legend-text{{font-size:12px;fill:#526581}} .component{{filter:url(#shadow)}}
</style>
<rect width="1920" height="1080" fill="#f8fbff"/>
<rect x="30" y="22" width="1860" height="70" rx="18" fill="#ffffff" stroke="#a8c7f2"/>
<text x="960" y="66" class="page-title" text-anchor="middle">{svg_text(title)}</text>
{lane_nodes}{arrow_nodes}{box_nodes}{render_legend(1130, 1015)}
</svg>'''
```

- [ ] **Step 3: Add a deterministic write helper**

```python
def write_diagram(filename: str, svg: str) -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    path = OUT / filename
    path.write_text(svg.rstrip() + "\n", encoding="utf-8")
```

- [ ] **Step 4: Add unit checks for escaping and determinism**

In `scripts/test_docs.py`, import the generator with `importlib.util` and assert:

```python
self.assertEqual(module.svg_text("tenant-<team> & PAT"), "tenant-&lt;team&gt; &amp; PAT")
box = module.Box(10, 20, 180, 90, "tenant-<team>", ("PAT & OIDC",), "purple")
self.assertEqual(module.render_box(box), module.render_box(box))
self.assertNotIn("<team>", module.render_box(box))
```

- [ ] **Step 5: Run tests**

Run:

```bash
python3 scripts/test_docs.py
```

Expected: primitive tests pass; asset tests still fail.

- [ ] **Step 6: Commit primitives**

```bash
git add scripts/generate_architecture_diagrams.py scripts/test_docs.py
git commit -m "feat: add maintainable SVG architecture renderer"
```

## Task 3: Generate the production overview

**Files:**

- Modify: `scripts/generate_architecture_diagrams.py`
- Create: `docs/architecture/ray-training-platform-production-architecture-v4.svg`

- [ ] **Step 1: Define overview lanes**

Create `overview_diagram()` with eight lanes using these titles and bounds:

```python
lanes = (
    Lane(30, 110, 1860, 100, "用户与入口", "blue"),
    Lane(30, 225, 1860, 105, "访问与认证", "purple"),
    Lane(30, 345, 900, 220, "平台控制面", "blue"),
    Lane(950, 345, 940, 220, "集群控制与调度", "blue"),
    Lane(30, 580, 1860, 165, "Ray 运行时", "green"),
    Lane(30, 760, 1130, 210, "数据与存储", "green"),
    Lane(1180, 760, 710, 210, "可观测性与实验", "orange"),
    Lane(30, 985, 1860, 65, "演进方向", "gray"),
)
```

- [ ] **Step 2: Add all approved overview components**

Place component cards in their lane in this order:

- User: Web Portal, `spk-rayjob`, Ray Jobs API, GPU 调试, 管理员控制台.
- Access: 企业 DNS, IDC NGINX Ingress, 私网 ALB, Kubernetes Ingress, Frontend, Backend API, 本地账号 / Keycloak OIDC.
- Platform: 用户/团队/角色, 镜像目录, Git 凭据, 数据治理, 任务 API, Reconciler, 审计, PostgreSQL.
- Scheduling: Kubernetes API, Kueue Controller, ClusterQueue, LocalQueue, ResourceFlavor, KubeRay Operator.
- Runtime: 调试 RayCluster, Submitter Pod, Ray Head, Ray Worker, Dashboard, PyTorch DDP / NCCL.
- Storage: 个人, 团队, 公共, Checkpoint/产物, TOS, FSX CSI, FSX Agent / IRSA, IDC 数据源, 双 NVMe.
- Observation: Alloy, Loki, Prometheus Operator, Prometheus, DCGM/Node Exporter/kube-state-metrics, Grafana, MLflow, MLflow PostgreSQL/Artifact.
- Planned dashed cards: 外部 HA PostgreSQL, 强制 OIDC, IDC↔TOS 审批迁移, 多团队成员关系.

- [ ] **Step 3: Add overview arrows**

Add labeled arrows for:

```text
入口 → Frontend/Backend                         request
Backend → PostgreSQL / Kueue / KubeRay          control
Kueue → Ray Worker                              control
KubeRay → Ray Head/Worker                       control
TOS → FSX CSI → Ray Worker                      data
Ray Worker → Checkpoint/产物                    data
Ray Pod → Alloy/Loki                            observe
Ray Worker → Prometheus/Grafana                 observe
训练代码 → MLflow                               observe
```

- [ ] **Step 4: Write overview SVG**

At the bottom of `main()` call:

```python
write_diagram("ray-training-platform-production-architecture-v4.svg", overview_diagram())
```

Run:

```bash
python3 scripts/generate_architecture_diagrams.py
```

Expected: v4 SVG exists and contains every `ARCHITECTURE_COMPONENTS["overview"]` marker.

- [ ] **Step 5: Commit overview SVG**

```bash
git add scripts/generate_architecture_diagrams.py docs/architecture/ray-training-platform-production-architecture-v4.svg
git commit -m "docs: add component-level production architecture overview"
```

## Task 4: Generate control-plane and tenancy sub-diagram

**Files:**

- Modify: `scripts/generate_architecture_diagrams.py`
- Create: `docs/architecture/ray-training-platform-control-plane-tenancy-v1.svg`

- [ ] **Step 1: Define control lanes and components**

Create `control_plane_diagram()` with lanes:

```text
身份与入口
Backend 领域服务
角色与授权
团队 namespace 与 Kueue
平台元数据边界
```

Use cards for local session, OIDC session, PAT, Git credential, identity service, tenant service, job service, image catalog, data governance, audit, Reconciler, SuperAdmin, TenantAdmin, Engineer, `tenant-&lt;team&gt;`, LocalQueue, ClusterQueue, ResourceFlavor, Kubernetes API, PostgreSQL.

- [ ] **Step 2: Draw authorization and control relationships**

Required labeled paths:

```text
本地/OIDC → 交互式会话
PAT → 受限任务 API
Git 凭据 → 获准 Git 主机
SuperAdmin → 全平台治理
TenantAdmin → 本团队成员/配额/数据
Engineer → 本人调试/任务/结果
团队 → tenant-<team> → LocalQueue → ClusterQueue → ResourceFlavor
Reconciler → Kubernetes API / KubeRay
领域服务 → PostgreSQL（仅元数据）
```

- [ ] **Step 3: Generate and test the SVG**

```bash
python3 scripts/generate_architecture_diagrams.py
python3 scripts/test_docs.py
```

Expected: control component markers pass; lifecycle/storage asset tests remain red.

- [ ] **Step 4: Commit**

```bash
git add scripts/generate_architecture_diagrams.py docs/architecture/ray-training-platform-control-plane-tenancy-v1.svg
git commit -m "docs: add control-plane and tenancy architecture"
```

## Task 5: Generate job-lifecycle sub-diagram

**Files:**

- Modify: `scripts/generate_architecture_diagrams.py`
- Create: `docs/architecture/ray-training-platform-job-lifecycle-v1.svg`

- [ ] **Step 1: Define lifecycle phases**

Create `job_lifecycle_diagram()` with the phases:

```text
代码打包 → 提交前检查 → 平台任务记录 → Kueue 排队/准入
→ KubeRay 建群 → Submitter → Ray Head/GCS/Dashboard
→ Ray Worker/PyTorch DDP/NCCL → 终态回写 → TTL 回收
```

Use a horizontal main flow with three lower branches for observability, persistent outputs, and failures.

- [ ] **Step 2: Add failure branches**

Add explicit cards and arrows:

```text
预检失败 → 不创建 RayCluster
未准入 → 保持排队，不创建 Worker
ImagePull/FailedMount → 基础设施错误
Python/CUDA/NCCL/NaN → 训练错误分类
用户取消 → 属主或授权管理员
```

- [ ] **Step 3: Add retention boundary**

Show that TTL removes RayCluster but not platform history, Loki logs, Prometheus time series, MLflow Run, Checkpoint, or persistent results.

- [ ] **Step 4: Generate, test, and commit**

```bash
python3 scripts/generate_architecture_diagrams.py
python3 scripts/test_docs.py
git add scripts/generate_architecture_diagrams.py docs/architecture/ray-training-platform-job-lifecycle-v1.svg
git commit -m "docs: add Ray training job lifecycle architecture"
```

Expected: lifecycle markers pass; storage/PNG/document-reference tests remain red.

## Task 6: Generate storage and observability sub-diagram

**Files:**

- Modify: `scripts/generate_architecture_diagrams.py`
- Create: `docs/architecture/ray-training-platform-storage-observability-v1.svg`

- [ ] **Step 1: Define stable logical paths**

Create cards for `/workspace`, `/mnt/storage/me`, `/mnt/storage/team`, `/mnt/storage/public`, `PLATFORM_DATASET_PATH`, `PLATFORM_OUTPUT_PATH`, `PLATFORM_CHECKPOINT_PATH`, and `PLATFORM_CACHE_PATH`.

- [ ] **Step 2: Define persistent storage chain**

Draw:

```text
TOS 前缀 → FSX Agent / IRSA → FSX CSI → PV / PVC → Ray Worker
个人/团队/公共逻辑目录 → 本任务 input/output/checkpoint subPath
Ray Worker → PLATFORM_OUTPUT_PATH → 个人持久结果
```

Place “用户 Pod 无 AK/SK” beside the IRSA boundary.

- [ ] **Step 3: Define dual-NVMe modes**

Draw separate off/runtime/preload cards:

```text
off → 直接读取持久输入
runtime → Ray temp-dir / object spilling 使用 /mnt/cache + /mnt/cache2
preload: input → Worker initContainer 预热 → PLATFORM_DATASET_PATH 切换缓存视图
任务结束 → 临时 PVC / 缓存回收
Checkpoint / 结果 → 始终写持久空间
```

- [ ] **Step 4: Define observability paths**

Draw:

```text
stdout/stderr → Alloy → Loki → Backend → Portal
GPU → DCGM Exporter → Prometheus → Grafana / Portal
CPU/内存/磁盘/网络/Kubernetes → Node Exporter + kube-state-metrics → Prometheus
参数/Loss/mAP/NDS/Artifact → MLflow → MLflow PostgreSQL + Artifact 空间
```

- [ ] **Step 5: Generate, test, and commit**

```bash
python3 scripts/generate_architecture_diagrams.py
python3 scripts/test_docs.py
git add scripts/generate_architecture_diagrams.py docs/architecture/ray-training-platform-storage-observability-v1.svg
git commit -m "docs: add storage and observability architecture"
```

Expected: all SVG content and privacy tests pass; PNG and documentation-reference tests remain red.

## Task 7: Add portable PNG rendering

**Files:**

- Create: `scripts/render-architecture-diagrams.sh`
- Create: four PNG files under `docs/architecture/`
- Test: `scripts/test_docs.py`

- [ ] **Step 1: Implement browser discovery**

Use this exact precedence:

```bash
#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly SVG_DIR="${ROOT_DIR}/docs/architecture"

find_browser() {
  local candidate
  for candidate in chromium chromium-browser google-chrome google-chrome-stable; do
    if command -v "$candidate" >/dev/null 2>&1; then
      command -v "$candidate"
      return 0
    fi
  done
  if [[ -x "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" ]]; then
    printf '%s\n' "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
    return 0
  fi
  return 1
}
```

- [ ] **Step 2: Render exactly four assets**

```bash
python3 "${ROOT_DIR}/scripts/generate_architecture_diagrams.py"
browser="$(find_browser)" || { echo 'Chromium-compatible browser is required' >&2; exit 1; }
profile_dir="$(mktemp -d)"
trap 'rm -rf "$profile_dir"' EXIT

diagrams=(
  ray-training-platform-production-architecture-v4
  ray-training-platform-control-plane-tenancy-v1
  ray-training-platform-job-lifecycle-v1
  ray-training-platform-storage-observability-v1
)

for name in "${diagrams[@]}"; do
  "$browser" --headless=new --no-first-run --disable-gpu --hide-scrollbars \
    --user-data-dir="${profile_dir}/${name}" \
    --force-device-scale-factor=2 --window-size=1920,1080 \
    --screenshot="${SVG_DIR}/${name}.png" \
    "file://${SVG_DIR}/${name}.svg"
done
```

- [ ] **Step 3: Render and verify dimensions**

```bash
bash scripts/render-architecture-diagrams.sh
python3 scripts/test_docs.py
```

Expected: all asset, component, privacy, and dimension tests pass; only active-document reference tests remain red.

- [ ] **Step 4: Visually inspect all four PNGs**

Open each PNG at full size and verify:

- no clipped titles;
- no overlapping cards;
- arrows do not cross component labels;
- legend matches arrow styles;
- planned cards are visibly dashed;
- the overview remains readable when scaled to documentation width.

- [ ] **Step 5: Commit rendered assets**

```bash
git add scripts/render-architecture-diagrams.sh docs/architecture/*.png
git commit -m "docs: render component architecture diagrams"
```

## Task 8: Integrate active documentation and archive v3

**Files:**

- Modify: `README.md`
- Modify: `docs/README.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/HANDOVER_GUIDE.md`
- Create: `docs/archive/architecture/README.md`
- Move: v3 SVG and PNG to `docs/archive/architecture/`

- [ ] **Step 1: Replace the root README image**

Use:

```markdown
![Ray 分布式训练平台组件级生产架构](docs/architecture/ray-training-platform-production-architecture-v4.svg)
```

- [ ] **Step 2: Add four architecture links to the docs index**

Add a “架构图” table to `docs/README.md` with rows for overview, control plane, job lifecycle, and storage/observability.

- [ ] **Step 3: Embed all diagrams in architecture sections**

Place:

- overview immediately after the `docs/ARCHITECTURE.md` title;
- control diagram after “控制面高可用与租户模型”;
- lifecycle diagram after “训练任务生命周期”;
- storage diagram before “存储与数据架构”.

Each image receives one paragraph stating what it explains and what it intentionally omits.

- [ ] **Step 4: Update handover references**

Use v4 at the top of `docs/HANDOVER_GUIDE.md`, and link:

- Pending/authorization incidents to the control diagram;
- task failures to the lifecycle diagram;
- FSX, logs, metrics, MLflow, and cache incidents to the storage diagram.

- [ ] **Step 5: Archive v3 without deleting history**

```bash
mkdir -p docs/archive/architecture
git mv docs/architecture/ray-training-platform-production-architecture-v3.svg docs/archive/architecture/
git mv docs/architecture/ray-training-platform-production-architecture-v3.png docs/archive/architecture/
```

Create `docs/archive/architecture/README.md` stating v3 was the simplified overview and v4 is the active component-level architecture.

- [ ] **Step 6: Run documentation tests**

```bash
python3 scripts/test_docs.py
git diff --check
```

Expected: all tests pass and active docs contain no v3 reference.

- [ ] **Step 7: Commit integration**

```bash
git add README.md docs/README.md docs/ARCHITECTURE.md docs/HANDOVER_GUIDE.md docs/archive/architecture scripts/test_docs.py
git commit -m "docs: publish component-level platform architecture"
```

## Task 9: Final review and release evidence

**Files:**

- Verify all changed files.

- [ ] **Step 1: Run the complete documentation contract**

```bash
python3 scripts/test_docs.py
git diff --check
```

Expected: 0 failures and no whitespace errors.

- [ ] **Step 2: Run repository delivery rendering on a Helm-capable machine**

```bash
bash scripts/test-delivery-render.sh
```

Expected: `delivery render contract verified` plus all cache, ingress, FSX, HA, and external-submit contracts.

- [ ] **Step 3: Scan changed content for credentials and private infrastructure**

```bash
git diff HEAD~8 -U0 | rg '^\+.*(glpat-|rpt_|AKLT|BEGIN .*PRIVATE KEY|(?:\d{1,3}\.){3}\d{1,3})'
```

Expected: no output. If legitimate version strings resemble an address, narrow the scan and inspect manually rather than suppressing the whole check.

- [ ] **Step 4: Inspect final repository state**

```bash
git status --short
git log --oneline -10
```

Expected: working tree contains no uncommitted architecture changes; commits are ordered contract → generator → diagrams → render → docs integration.

- [ ] **Step 5: Record the final handoff**

Report:

- the four SVG/PNG paths;
- test commands and results;
- the current commit;
- whether the branch has been pushed;
- any unrelated pre-existing dirty files left untouched.
