#!/usr/bin/env python3
"""Generate deterministic, component-level RayTrain architecture diagrams."""

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


TONES = {
    "blue": ("#F4F8FF", "#8AB4F8", "#2166C2"),
    "green": ("#F1FBF6", "#7BC7A3", "#16805D"),
    "orange": ("#FFF7EC", "#E6A15B", "#C96D20"),
    "purple": ("#F8F4FF", "#A995E8", "#7353BE"),
    "gray": ("#F5F7FA", "#AAB4C3", "#667085"),
    "red": ("#FFF4F4", "#E79A9A", "#B54747"),
}

ARROW_CLASSES = {
    "request": "request",
    "control": "control",
    "data": "data",
    "observe": "observe",
    "failure": "failure",
    "planned": "planned",
}


def svg_text(value: str) -> str:
    return escape(value, quote=True)


def render_lane(lane: Lane) -> str:
    fill, stroke, accent = TONES[lane.tone]
    badge_width = max(152, min(248, 58 + len(lane.title) * 17))
    return (
        f'<g class="lane"><rect x="{lane.x}" y="{lane.y}" width="{lane.width}" '
        f'height="{lane.height}" rx="18" fill="{fill}" stroke="{stroke}" stroke-width="1.5"/>'
        f'<rect x="{lane.x + 14}" y="{lane.y + 14}" width="{badge_width}" height="34" '
        f'rx="9" fill="{accent}"/>'
        f'<text x="{lane.x + 28}" y="{lane.y + 37}" class="lane-title">'
        f'{svg_text(lane.title)}</text></g>'
    )


def render_box(box: Box) -> str:
    fill, stroke, accent = TONES[box.tone]
    dash = ' stroke-dasharray="8 6"' if box.dashed else ""
    title_y = box.y + (29 if box.lines else box.height / 2 + 6)
    line_nodes = "".join(
        f'<text x="{box.x + box.width / 2:g}" y="{box.y + 52 + index * 18}" '
        f'class="box-line" text-anchor="middle">{svg_text(line)}</text>'
        for index, line in enumerate(box.lines[:3])
    )
    return (
        f'<g class="component"><rect x="{box.x}" y="{box.y}" width="{box.width}" '
        f'height="{box.height}" rx="12" fill="#FFFFFF" stroke="{stroke}" '
        f'stroke-width="1.5"{dash}/>'
        f'<rect x="{box.x}" y="{box.y}" width="5" height="{box.height}" rx="3" fill="{accent}"/>'
        f'<text x="{box.x + box.width / 2:g}" y="{title_y:g}" '
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
            f'<rect x="{middle_x - max(34, len(arrow.label) * 6)}" y="{middle_y - 20}" '
            f'width="{max(68, len(arrow.label) * 12)}" height="17" rx="5" fill="#F8FBFF"/>'
            f'<text x="{middle_x}" y="{middle_y - 8}" class="arrow-label" '
            f'text-anchor="middle">{svg_text(arrow.label)}</text>'
        )
    return f'<g>{label}<polyline points="{points}" class="arrow {css_class}"/></g>'


def render_legend(x: int, y: int) -> str:
    entries = (
        ("request", "请求"),
        ("control", "控制"),
        ("data", "数据"),
        ("observe", "观测"),
        ("failure", "失败"),
        ("planned", "规划"),
    )
    nodes = []
    for index, (kind, label) in enumerate(entries):
        offset = index * 112
        nodes.append(
            f'<line x1="{x + offset}" y1="{y}" x2="{x + offset + 36}" y2="{y}" '
            f'class="arrow {kind}"/><text x="{x + offset + 44}" y="{y + 4}" '
            f'class="legend-text">{svg_text(label)}</text>'
        )
    return '<g class="legend">' + "".join(nodes) + "</g>"


def render_svg(
    title: str,
    description: str,
    lanes: tuple[Lane, ...],
    boxes: tuple[Box, ...],
    arrows: tuple[Arrow, ...],
) -> str:
    lane_nodes = "".join(render_lane(lane) for lane in lanes)
    box_nodes = "".join(render_box(box) for box in boxes)
    arrow_nodes = "".join(render_arrow(arrow) for arrow in arrows)
    return f'''<svg xmlns="http://www.w3.org/2000/svg" width="1920" height="1080"
 viewBox="0 0 1920 1080" role="img" aria-labelledby="title desc">
<title id="title">{svg_text(title)}</title>
<desc id="desc">{svg_text(description)}</desc>
<defs>
  <filter id="shadow" x="-15%" y="-20%" width="130%" height="145%">
    <feDropShadow dx="0" dy="2" stdDeviation="3" flood-color="#163A66" flood-opacity=".09"/>
  </filter>
  <marker id="arrow-blue" markerWidth="9" markerHeight="9" refX="8" refY="3" orient="auto"><path d="M0 0L0 6L8 3Z" fill="#3272CF"/></marker>
  <marker id="arrow-green" markerWidth="9" markerHeight="9" refX="8" refY="3" orient="auto"><path d="M0 0L0 6L8 3Z" fill="#16805D"/></marker>
  <marker id="arrow-orange" markerWidth="9" markerHeight="9" refX="8" refY="3" orient="auto"><path d="M0 0L0 6L8 3Z" fill="#C96D20"/></marker>
  <marker id="arrow-red" markerWidth="9" markerHeight="9" refX="8" refY="3" orient="auto"><path d="M0 0L0 6L8 3Z" fill="#B54747"/></marker>
  <marker id="arrow-gray" markerWidth="9" markerHeight="9" refX="8" refY="3" orient="auto"><path d="M0 0L0 6L8 3Z" fill="#667085"/></marker>
</defs>
<style>
text{{font-family:Inter,"PingFang SC","Microsoft YaHei","Noto Sans CJK SC",Arial,sans-serif;fill:#152445}}
.page-title{{font-size:34px;font-weight:800}} .page-desc{{font-size:13px;fill:#5D6B82}}
.lane-title{{font-size:16px;font-weight:800;fill:#FFFFFF}} .box-title{{font-size:14px;font-weight:760}}
.box-line{{font-size:11px;fill:#5A6881}} .arrow{{fill:none;stroke-width:2;stroke-linejoin:round}}
.request,.control{{stroke:#3272CF;marker-end:url(#arrow-blue)}} .data{{stroke:#16805D;stroke-width:2.6;marker-end:url(#arrow-green)}}
.observe{{stroke:#C96D20;stroke-dasharray:7 5;marker-end:url(#arrow-orange)}} .failure{{stroke:#B54747;stroke-dasharray:5 4;marker-end:url(#arrow-red)}}
.planned{{stroke:#667085;stroke-dasharray:9 6;marker-end:url(#arrow-gray)}} .arrow-label{{font-size:10px;fill:#526581}}
.legend-text{{font-size:11px;fill:#526581}} .component{{filter:url(#shadow)}}
</style>
<rect width="1920" height="1080" fill="#F7FAFE"/>
<rect x="28" y="20" width="1864" height="70" rx="18" fill="#FFFFFF" stroke="#B6CBE8"/>
<text x="960" y="55" class="page-title" text-anchor="middle">{svg_text(title)}</text>
<text x="960" y="77" class="page-desc" text-anchor="middle">{svg_text(description)}</text>
{lane_nodes}{arrow_nodes}{box_nodes}{render_legend(1205, 1040)}
</svg>'''


def write_diagram(filename: str, svg: str) -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    (OUT / filename).write_text(svg.rstrip() + "\n", encoding="utf-8")


def overview_diagram() -> str:
    lanes = (
        Lane(28, 105, 1864, 80, "用户与入口", "blue"),
        Lane(28, 195, 1864, 105, "访问与认证", "purple"),
        Lane(28, 310, 915, 210, "平台控制面", "blue"),
        Lane(953, 310, 939, 210, "集群控制与调度", "blue"),
        Lane(28, 530, 1864, 130, "Ray 运行时", "green"),
        Lane(28, 670, 1128, 225, "数据与存储", "green"),
        Lane(1166, 670, 726, 225, "可观测性与实验", "orange"),
        Lane(28, 905, 1864, 110, "演进方向", "gray"),
    )
    boxes = (
        Box(220, 119, 210, 52, "Web Portal", ("任务 · 数据 · 调试",), "blue"),
        Box(455, 119, 210, 52, "spk-rayjob", ("外部代码快照提交",), "blue"),
        Box(690, 119, 210, 52, "Ray Jobs API", ("自动化 working-dir",), "blue"),
        Box(925, 119, 210, 52, "GPU 交互式调试", ("Terminal · IDE · Notebook",), "green"),
        Box(1160, 119, 210, 52, "管理员控制台", ("租户 · 配额 · 镜像",), "purple"),
        Box(1395, 119, 210, 52, "Ray Dashboard", ("运行期诊断",), "orange"),
        Box(180, 227, 190, 58, "企业 DNS", ("统一域名",), "purple"),
        Box(390, 227, 205, 58, "IDC NGINX Ingress", ("内网反向代理",), "purple"),
        Box(615, 227, 180, 58, "私网 ALB", ("TLS · 七层路由",), "purple"),
        Box(815, 227, 200, 58, "Kubernetes Ingress", ("同域路径分发",), "purple"),
        Box(1035, 227, 180, 58, "Frontend", ("Web Portal",), "blue"),
        Box(1235, 227, 190, 58, "Backend API", ("鉴权 · 编排",), "blue"),
        Box(1445, 227, 240, 58, "本地账号 / Keycloak OIDC", ("会话 · PAT · RBAC",), "purple"),
        Box(190, 370, 185, 62, "身份与租户", ("用户 · 团队 · 角色",), "blue"),
        Box(395, 370, 160, 62, "镜像目录", ("tag / digest",), "blue"),
        Box(575, 370, 160, 62, "Git 凭据", ("主机允许列表",), "blue"),
        Box(755, 370, 160, 62, "数据治理", ("逻辑目录授权",), "green"),
        Box(190, 445, 185, 58, "任务 API", ("提交 · 取消 · 历史",), "blue"),
        Box(395, 445, 160, 58, "Reconciler", ("Lease 选主",), "blue"),
        Box(575, 445, 160, 58, "审计", ("主体 · 动作 · 结果",), "purple"),
        Box(755, 445, 160, 58, "PostgreSQL", ("平台元数据",), "purple"),
        Box(1115, 365, 180, 62, "Kubernetes API", ("对象与状态真相",), "blue"),
        Box(1315, 365, 170, 62, "Kueue", ("准入与公平排队",), "blue"),
        Box(1505, 365, 180, 62, "KubeRay Operator", ("Ray 生命周期",), "green"),
        Box(1115, 445, 170, 58, "LocalQueue", ("团队 namespace",), "blue"),
        Box(1305, 445, 170, 58, "ClusterQueue", ("共享资源池",), "blue"),
        Box(1495, 445, 180, 58, "ResourceFlavor", ("节点标签与 GPU",), "blue"),
        Box(210, 565, 210, 68, "调试 RayCluster", ("Head + GPU Worker",), "green"),
        Box(445, 565, 180, 68, "Submitter Pod", ("上传 runtime env",), "green"),
        Box(650, 565, 180, 68, "Ray Head", ("GCS · Jobs · Serve",), "green"),
        Box(855, 565, 180, 68, "Ray Worker", ("GPU 执行单元",), "green"),
        Box(1060, 565, 210, 68, "PyTorch DDP / NCCL", ("单机 / 多机多卡",), "green"),
        Box(1295, 565, 180, 68, "Dashboard", ("任务运行期",), "orange"),
        Box(1500, 565, 190, 68, "训练结果", ("状态 · 日志 · 产物",), "orange"),
        Box(180, 720, 170, 62, "个人空间", ("me · 读写",), "green"),
        Box(370, 720, 170, 62, "团队空间", ("team · Pod 只读",), "green"),
        Box(560, 720, 170, 62, "公共空间", ("public · 只读",), "green"),
        Box(750, 720, 190, 62, "Checkpoint / 产物", ("持久写入",), "green"),
        Box(960, 720, 170, 62, "IDC 数据源", ("受控登记",), "gray"),
        Box(180, 800, 170, 62, "TOS", ("持久数据真相",), "green"),
        Box(370, 800, 170, 62, "FSX CSI", ("受控卷发布",), "green"),
        Box(560, 800, 190, 62, "FSX Agent / IRSA", ("Pod 无 AK/SK",), "green"),
        Box(770, 800, 170, 62, "PV / PVC", ("任务 subPath",), "green"),
        Box(960, 800, 170, 62, "双 NVMe", ("临时任务缓存",), "green"),
        Box(1205, 715, 140, 58, "Alloy", ("日志发现",), "orange"),
        Box(1360, 715, 140, 58, "Loki", ("历史日志",), "orange"),
        Box(1515, 715, 165, 58, "Prometheus Operator", ("监控控制器",), "orange"),
        Box(1695, 715, 160, 58, "Prometheus", ("时序指标",), "orange"),
        Box(1205, 795, 170, 66, "DCGM / Node Exporter", ("GPU · 主机",), "orange"),
        Box(1390, 795, 145, 66, "Grafana", ("仪表盘",), "orange"),
        Box(1550, 795, 140, 66, "MLflow", ("实验与参数",), "orange"),
        Box(1705, 795, 150, 66, "MLflow 存储", ("PostgreSQL · Artifact",), "orange"),
        Box(205, 935, 270, 56, "外部 HA PostgreSQL", ("控制面去单点",), "gray", True),
        Box(505, 935, 230, 56, "强制 OIDC", ("关闭本地账号",), "gray", True),
        Box(765, 935, 290, 56, "IDC ↔ TOS 审批迁移", ("审计 · 校验 · 重试",), "gray", True),
        Box(1085, 935, 250, 56, "多团队成员关系", ("当前团队切换",), "gray", True),
        Box(1365, 935, 300, 56, "数据集版本与训练对比", ("版本 · 血缘 · 指标",), "gray", True),
    )
    arrows = (
        Arrow(((370, 145), (390, 145), (390, 256)), "request", "统一入口"),
        Arrow(((595, 256), (615, 256)), "request"),
        Arrow(((795, 256), (815, 256)), "request"),
        Arrow(((1015, 256), (1035, 256)), "request"),
        Arrow(((1215, 256), (1235, 256)), "request"),
        Arrow(((1330, 285), (1330, 370), (375, 370)), "control", "认证主体"),
        Arrow(((555, 474), (1115, 474)), "control", "声明式对象"),
        Arrow(((1285, 474), (1305, 474)), "control"),
        Arrow(((1475, 474), (1495, 474)), "control"),
        Arrow(((1400, 427), (1400, 445)), "control", "准入"),
        Arrow(((1595, 427), (1595, 565), (830, 565)), "control", "建群"),
        Arrow(((625, 599), (650, 599)), "request"),
        Arrow(((830, 599), (855, 599)), "control"),
        Arrow(((1035, 599), (1060, 599)), "control"),
        Arrow(((350, 831), (370, 831)), "data"),
        Arrow(((540, 831), (560, 831)), "data"),
        Arrow(((750, 831), (770, 831)), "data"),
        Arrow(((855, 800), (855, 633)), "data", "输入挂载"),
        Arrow(((1165, 633), (1165, 751), (940, 751)), "data", "结果持久化"),
        Arrow(((945, 633), (945, 744), (1205, 744)), "observe", "stdout"),
        Arrow(((1375, 744), (1360, 744)), "observe"),
        Arrow(((945, 633), (945, 828), (1205, 828)), "observe", "GPU / Node"),
        Arrow(((1375, 828), (1390, 828)), "observe"),
        Arrow(((1165, 633), (1165, 828), (1550, 828)), "observe", "参数 · Loss"),
        Arrow(((1690, 828), (1705, 828)), "observe"),
    )
    return render_svg(
        "Ray 分布式训练平台 · 组件级生产架构",
        "当前生产组件使用实线；灰色虚线表示明确规划，不展示环境 IP、节点规格或副本数量",
        lanes,
        boxes,
        arrows,
    )


def control_plane_diagram() -> str:
    lanes = (
        Lane(28, 110, 1864, 130, "身份与入口", "purple"),
        Lane(28, 255, 1864, 190, "Backend 领域服务", "blue"),
        Lane(28, 460, 1864, 150, "角色与授权", "purple"),
        Lane(28, 625, 1864, 190, "团队 namespace 与 Kueue", "blue"),
        Lane(28, 830, 1864, 170, "平台元数据边界", "green"),
    )
    boxes = (
        Box(210, 150, 190, 62, "本地会话", ("账号 + 密码",), "purple"),
        Box(425, 150, 190, 62, "OIDC 会话", ("Keycloak / LDAP",), "purple"),
        Box(640, 150, 160, 62, "PAT", ("CLI / API",), "purple"),
        Box(825, 150, 190, 62, "Git 凭据", ("只读仓库",), "purple"),
        Box(1040, 150, 210, 62, "Frontend / spk-rayjob", ("交互与外部提交",), "blue"),
        Box(1275, 150, 190, 62, "Backend API", ("认证边界",), "blue"),
        Box(170, 305, 180, 72, "Identity Service", ("主体 · 会话 · PAT",), "blue"),
        Box(370, 305, 180, 72, "Tenant Service", ("团队 · 成员 · 配额",), "blue"),
        Box(570, 305, 180, 72, "Job Service", ("预检 · 提交 · 取消",), "blue"),
        Box(770, 305, 180, 72, "Image Catalog", ("scope · tag · digest",), "blue"),
        Box(970, 305, 180, 72, "Data Governance", ("目录 · 发布 · 配额",), "green"),
        Box(1170, 305, 180, 72, "Audit Service", ("主体 · 动作 · 结果",), "purple"),
        Box(1370, 305, 180, 72, "Reconciler", ("Lease · 状态回写",), "blue"),
        Box(1570, 305, 180, 72, "Kubernetes API", ("集群对象",), "blue"),
        Box(235, 505, 250, 72, "SuperAdmin", ("全平台用户 · 团队 · 配额",), "purple"),
        Box(570, 505, 250, 72, "TenantAdmin", ("本团队成员 · 数据 · 任务",), "purple"),
        Box(905, 505, 250, 72, "Engineer", ("本人调试 · 任务 · 结果",), "purple"),
        Box(1240, 505, 300, 72, "所有权校验", ("普通用户不可取消他人任务",), "red"),
        Box(175, 680, 245, 80, "tenant-<team>", ("团队 Kubernetes namespace",), "blue"),
        Box(465, 680, 220, 80, "LocalQueue", ("namespace 内任务入口",), "blue"),
        Box(730, 680, 220, 80, "ClusterQueue", ("全局容量与团队预算",), "blue"),
        Box(995, 680, 220, 80, "ResourceFlavor", ("GPU/CPU 节点标签",), "blue"),
        Box(1260, 680, 230, 80, "Kueue Workload", ("排队 · 准入 · 释放",), "blue"),
        Box(1535, 680, 190, 80, "KubeRay", ("RayJob / RayCluster",), "green"),
        Box(200, 875, 270, 78, "PostgreSQL", ("用户 · 团队 · 任务 · 审计",), "green"),
        Box(510, 875, 260, 78, "Kubernetes API", ("RayJob · Workload · Pod",), "blue"),
        Box(810, 875, 260, 78, "TOS / FSX 元数据", ("仅目录映射与绑定",), "green"),
        Box(1110, 875, 260, 78, "Secret 引用", ("凭据不回显、不入数据库",), "purple"),
        Box(1410, 875, 290, 78, "审计边界", ("认证主体不可由客户端覆盖",), "red"),
    )
    arrows = (
        Arrow(((400, 181), (1275, 181)), "request", "交互式会话"),
        Arrow(((615, 181), (1275, 181)), "request"),
        Arrow(((800, 181), (1275, 181)), "request", "受限任务 API"),
        Arrow(((1015, 181), (1015, 285), (860, 285), (860, 305)), "control", "获准 Git 主机"),
        Arrow(((1370, 212), (1370, 285), (260, 285), (260, 305)), "control"),
        Arrow(((750, 377), (750, 505)), "control", "属主与角色"),
        Arrow(((485, 541), (570, 541)), "control"),
        Arrow(((820, 541), (905, 541)), "control"),
        Arrow(((1155, 541), (1240, 541)), "control", "动作授权"),
        Arrow(((550, 345), (550, 650), (297, 650), (297, 680)), "control", "团队映射"),
        Arrow(((420, 720), (465, 720)), "control"),
        Arrow(((685, 720), (730, 720)), "control"),
        Arrow(((950, 720), (995, 720)), "control"),
        Arrow(((1215, 720), (1260, 720)), "control"),
        Arrow(((1490, 720), (1535, 720)), "control"),
        Arrow(((1460, 377), (1460, 650), (1630, 650), (1630, 680)), "control", "声明式协调"),
        Arrow(((1260, 377), (1260, 850), (335, 850), (335, 875)), "data", "平台元数据"),
        Arrow(((1550, 345), (1700, 345), (1700, 850), (640, 850), (640, 875)), "control"),
    )
    return render_svg(
        "控制面、多租户与资源准入",
        "同一认证主体贯穿 Portal、CLI、任务所有权、团队 namespace 与 Kueue 准入",
        lanes,
        boxes,
        arrows,
    )


def job_lifecycle_diagram() -> str:
    lanes = (
        Lane(28, 110, 1864, 165, "提交与准入", "blue"),
        Lane(28, 290, 1864, 200, "RayJob 与分布式执行", "green"),
        Lane(28, 505, 905, 235, "失败与取消分支", "red"),
        Lane(950, 505, 942, 235, "持久化结果与可观测性", "orange"),
        Lane(28, 755, 1864, 245, "终态与保留边界", "purple"),
    )
    boxes = (
        Box(160, 160, 190, 72, "代码打包", ("working-dir / 快照",), "blue"),
        Box(385, 160, 190, 72, "提交前检查", ("镜像 · 数据 · 配额",), "blue"),
        Box(610, 160, 190, 72, "任务记录", ("主体 · 参数 · 时间",), "blue"),
        Box(835, 160, 200, 72, "Kueue Workload", ("排队 · 准入",), "blue"),
        Box(1070, 160, 190, 72, "RayJob", ("期望拓扑",), "green"),
        Box(1295, 160, 200, 72, "KubeRay 建群", ("RayCluster 生命周期",), "green"),
        Box(1530, 160, 190, 72, "Pod 调度", ("镜像 · 卷 · 节点",), "green"),
        Box(160, 345, 190, 86, "Submitter", ("上传 runtime env", "提交 Ray Job"), "green"),
        Box(395, 345, 190, 86, "Ray Head", ("GCS", "Dashboard"), "green"),
        Box(630, 345, 210, 86, "Ray Worker", ("训练进程", "数据与结果挂载"), "green"),
        Box(885, 345, 220, 86, "PyTorch DDP", ("world size / rank", "梯度同步"), "green"),
        Box(1150, 345, 180, 86, "NCCL", ("跨卡 / 跨节点", "collective"), "green"),
        Box(1375, 345, 190, 86, "训练循环", ("forward · backward", "Checkpoint"), "green"),
        Box(1610, 345, 150, 86, "终态回写", ("return code", "结束时间"), "purple"),
        Box(135, 560, 210, 70, "预检失败", ("不创建 RayCluster",), "red"),
        Box(375, 560, 210, 70, "未准入", ("保持排队 · 无 Worker",), "red"),
        Box(615, 560, 260, 70, "ImagePull / FailedMount", ("基础设施错误",), "red"),
        Box(135, 650, 210, 62, "Python / CUDA", ("代码或运行环境",), "red"),
        Box(375, 650, 210, 62, "NCCL / OOM / NaN", ("训练错误分类",), "red"),
        Box(615, 650, 260, 62, "用户取消", ("属主或授权管理员",), "red"),
        Box(1000, 560, 190, 70, "Loki 日志", ("stdout / stderr",), "orange"),
        Box(1220, 560, 190, 70, "Prometheus", ("GPU · CPU · 网络",), "orange"),
        Box(1440, 560, 190, 70, "MLflow Run", ("参数 · Loss · 评估",), "orange"),
        Box(1660, 560, 190, 70, "任务历史", ("主体 · 状态 · 时间",), "purple"),
        Box(1050, 650, 210, 62, "Checkpoint", ("个人持久空间",), "green"),
        Box(1300, 650, 210, 62, "训练产物", ("模型 · 配置 · 摘要",), "green"),
        Box(1550, 650, 250, 62, "Ray Dashboard", ("仅任务运行期保留",), "orange"),
        Box(150, 815, 240, 84, "SUCCEEDED", ("训练与验证完成",), "green"),
        Box(430, 815, 240, 84, "FAILED", ("保留错误归因",), "red"),
        Box(710, 815, 240, 84, "STOPPED", ("用户或管理员取消",), "gray"),
        Box(990, 815, 240, 84, "TTL 回收", ("删除 RayCluster", "释放 GPU / PVC"), "purple"),
        Box(1270, 815, 500, 84, "仍然保留", ("任务历史 · Loki · Prometheus · MLflow", "Checkpoint · 持久结果"), "green"),
        Box(250, 925, 300, 48, "重新提交 / 断点续训", ("新任务读取历史 Checkpoint",), "blue"),
        Box(600, 925, 330, 48, "参数调整", ("用户确认后创建新任务",), "blue"),
        Box(1270, 925, 500, 48, "Ray Head / Worker / Dashboard 不保留", (), "gray"),
    )
    arrows = (
        Arrow(((350, 196), (385, 196)), "request"),
        Arrow(((575, 196), (610, 196)), "request"),
        Arrow(((800, 196), (835, 196)), "control"),
        Arrow(((1035, 196), (1070, 196)), "control"),
        Arrow(((1260, 196), (1295, 196)), "control"),
        Arrow(((1495, 196), (1530, 196)), "control"),
        Arrow(((1625, 232), (1625, 320), (255, 320), (255, 345)), "control", "Pod Ready"),
        Arrow(((350, 388), (395, 388)), "request"),
        Arrow(((585, 388), (630, 388)), "control"),
        Arrow(((840, 388), (885, 388)), "control"),
        Arrow(((1105, 388), (1150, 388)), "control"),
        Arrow(((1330, 388), (1375, 388)), "control"),
        Arrow(((1565, 388), (1610, 388)), "control"),
        Arrow(((480, 232), (480, 560)), "failure", "校验失败"),
        Arrow(((935, 232), (935, 535), (480, 535), (480, 560)), "failure", "等待配额"),
        Arrow(((1625, 232), (1625, 520), (745, 520), (745, 560)), "failure", "Pod 异常"),
        Arrow(((995, 431), (995, 595), (1000, 595)), "observe", "日志"),
        Arrow(((1240, 431), (1240, 560)), "observe", "资源"),
        Arrow(((1470, 431), (1470, 560)), "observe", "指标"),
        Arrow(((1685, 431), (1685, 560)), "control", "状态"),
        Arrow(((1470, 431), (1470, 681), (1260, 681)), "data", "checkpoint"),
        Arrow(((1685, 431), (1685, 790), (1110, 790), (1110, 815)), "control", "终态"),
        Arrow(((1230, 857), (1270, 857)), "data", "持久保留"),
        Arrow(((390, 857), (430, 857)), "failure"),
        Arrow(((670, 857), (710, 857)), "control"),
        Arrow(((950, 857), (990, 857)), "control"),
        Arrow(((550, 949), (600, 949)), "request"),
    )
    return render_svg(
        "训练任务生命周期与故障边界",
        "任务从代码快照、Kueue 准入、KubeRay 建群到 DDP 执行；TTL 只回收运行时，不删除历史证据",
        lanes,
        boxes,
        arrows,
    )


def storage_observability_diagram() -> str:
    lanes = (
        Lane(28, 110, 1864, 145, "用户稳定路径与代码契约", "blue"),
        Lane(28, 270, 1000, 255, "TOS / FSX 持久存储链", "green"),
        Lane(1045, 270, 847, 255, "GPU 节点双 NVMe 缓存", "green"),
        Lane(28, 540, 1864, 235, "训练 Pod 数据流", "blue"),
        Lane(28, 790, 1864, 210, "日志、指标与实验观测", "orange"),
    )
    boxes = (
        Box(170, 155, 180, 62, "/workspace", ("代码与环境",), "blue"),
        Box(370, 155, 190, 62, "/mnt/storage/me", ("本人读写",), "green"),
        Box(580, 155, 200, 62, "/mnt/storage/team", ("团队 Pod 只读",), "green"),
        Box(800, 155, 200, 62, "/mnt/storage/public", ("公共只读",), "green"),
        Box(1020, 155, 250, 62, "PLATFORM_DATASET_PATH", ("本任务输入视图",), "blue"),
        Box(1290, 155, 240, 62, "PLATFORM_OUTPUT_PATH", ("本任务持久输出",), "blue"),
        Box(1550, 155, 260, 62, "PLATFORM_CHECKPOINT_PATH", ("历史结果只读",), "blue"),
        Box(140, 325, 190, 72, "TOS", ("个人 · 团队 · 公共", "持久数据真相"), "green"),
        Box(365, 325, 210, 72, "FSX Agent", ("IRSA", "用户 Pod 无 AK/SK"), "green"),
        Box(610, 325, 180, 72, "FSX CSI", ("节点卷发布",), "green"),
        Box(825, 325, 170, 72, "PV / PVC", ("逻辑目录绑定",), "green"),
        Box(140, 430, 190, 62, "个人前缀", ("用户稳定键",), "green"),
        Box(365, 430, 210, 62, "团队前缀", ("管理员发布",), "green"),
        Box(610, 430, 180, 62, "公共前缀", ("平台发布",), "green"),
        Box(825, 430, 170, 62, "任务 subPath", ("input / output",), "green"),
        Box(1090, 320, 160, 72, "off", ("直接读取", "持久输入"), "gray"),
        Box(1280, 320, 180, 72, "runtime", ("Ray temp-dir", "object spilling"), "green"),
        Box(1490, 320, 200, 72, "preload: input", ("initContainer 预热", "切换输入视图"), "green"),
        Box(1720, 320, 140, 72, "故障回退", ("显式失败", "不损坏源数据"), "red"),
        Box(1090, 425, 180, 72, "/mnt/cache", ("第一块 NVMe", "临时卷"), "green"),
        Box(1300, 425, 180, 72, "/mnt/cache2", ("第二块 NVMe", "临时卷"), "green"),
        Box(1510, 425, 180, 72, "PLATFORM_CACHE_PATH", ("任务缓存根",), "green"),
        Box(1720, 425, 140, 72, "任务结束", ("PVC / 缓存回收",), "gray"),
        Box(135, 600, 190, 80, "选定数据集", ("目录 / 版本",), "blue"),
        Box(360, 600, 210, 80, "input subPath", ("只读挂载",), "green"),
        Box(605, 600, 240, 80, "Worker initContainer", ("仅 preload 模式复制",), "green"),
        Box(880, 600, 210, 80, "Ray Worker", ("DataLoader · 训练",), "blue"),
        Box(1125, 600, 210, 80, "output subPath", ("结果持续写入",), "green"),
        Box(1370, 600, 210, 80, "checkpoint subPath", ("续训只读输入",), "green"),
        Box(1615, 600, 190, 80, "持久结果", ("模型 · 配置 · 摘要",), "green"),
        Box(250, 700, 280, 48, "缓存不是数据真相", ("丢失后可重新预热",), "gray"),
        Box(570, 700, 300, 48, "Checkpoint 永远不写 NVMe", ("始终落个人持久空间",), "red"),
        Box(910, 700, 300, 48, "多机每个 Worker 独立缓存", ("不跨节点共享本地盘",), "gray"),
        Box(155, 845, 175, 70, "stdout / stderr", ("Pod 日志",), "orange"),
        Box(360, 845, 150, 70, "Alloy", ("发现与采集",), "orange"),
        Box(540, 845, 150, 70, "Loki", ("日志存储",), "orange"),
        Box(720, 845, 190, 70, "DCGM Exporter", ("GPU 指标",), "orange"),
        Box(940, 845, 170, 70, "Node Exporter", ("CPU · 内存 · IO",), "orange"),
        Box(1140, 845, 190, 70, "kube-state-metrics", ("Kubernetes 状态",), "orange"),
        Box(1360, 845, 170, 70, "Prometheus", ("时序查询",), "orange"),
        Box(1560, 845, 140, 70, "Grafana", ("趋势面板",), "orange"),
        Box(1730, 845, 140, 70, "Portal", ("任务指标",), "orange"),
        Box(720, 930, 190, 54, "训练参数 / Loss", ("mAP · NDS",), "purple"),
        Box(940, 930, 150, 54, "MLflow", ("实验中心",), "purple"),
        Box(1120, 930, 230, 54, "MLflow PostgreSQL", ("实验元数据",), "purple"),
        Box(1380, 930, 190, 54, "Artifact 空间", ("FSX 持久挂载",), "purple"),
        Box(1600, 930, 250, 54, "训练对比与断点续跑", ("跨任务保留",), "purple"),
    )
    arrows = (
        Arrow(((330, 361), (365, 361)), "data", "身份访问"),
        Arrow(((575, 361), (610, 361)), "data"),
        Arrow(((790, 361), (825, 361)), "data"),
        Arrow(((910, 397), (910, 430)), "data", "subPath"),
        Arrow(((1000, 186), (1020, 186)), "control", "选择输入"),
        Arrow(((1090, 640), (1125, 640)), "data", "结果"),
        Arrow(((1335, 640), (1615, 640)), "data", "持久写入"),
        Arrow(((570, 640), (605, 640)), "data"),
        Arrow(((845, 640), (880, 640)), "data", "缓存视图"),
        Arrow(((1460, 356), (1490, 356)), "control"),
        Arrow(((1590, 392), (1590, 425)), "data", "预热"),
        Arrow(((1270, 461), (1300, 461)), "data"),
        Arrow(((1480, 461), (1510, 461)), "data"),
        Arrow(((1690, 461), (1720, 461)), "control", "回收"),
        Arrow(((330, 880), (360, 880)), "observe"),
        Arrow(((510, 880), (540, 880)), "observe"),
        Arrow(((690, 880), (690, 812), (1730, 812), (1730, 880)), "observe", "平台日志"),
        Arrow(((910, 880), (1360, 880)), "observe", "GPU"),
        Arrow(((1110, 880), (1360, 880)), "observe", "主机"),
        Arrow(((1330, 880), (1360, 880)), "observe", "对象"),
        Arrow(((1530, 880), (1560, 880)), "observe"),
        Arrow(((1700, 880), (1730, 880)), "observe"),
        Arrow(((910, 957), (940, 957)), "observe"),
        Arrow(((1090, 957), (1120, 957)), "data"),
        Arrow(((1090, 957), (1380, 957)), "data", "Artifact"),
        Arrow(((1570, 957), (1600, 957)), "request"),
    )
    return render_svg(
        "存储、双 NVMe 缓存与可观测性",
        "持久数据经 TOS / FSX 发布；缓存按任务显式启用；日志、资源指标和实验指标分别持久化",
        lanes,
        boxes,
        arrows,
    )


def main() -> None:
    write_diagram(
        "ray-training-platform-production-architecture-v4.svg",
        overview_diagram(),
    )
    write_diagram(
        "ray-training-platform-control-plane-tenancy-v1.svg",
        control_plane_diagram(),
    )
    write_diagram(
        "ray-training-platform-job-lifecycle-v1.svg",
        job_lifecycle_diagram(),
    )
    write_diagram(
        "ray-training-platform-storage-observability-v1.svg",
        storage_observability_diagram(),
    )


if __name__ == "__main__":
    main()
