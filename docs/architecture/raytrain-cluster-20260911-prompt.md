# 当前架构图生成提示词

生成方式：内置 imagegen。无参考图片；按生产只读核查与当前代码构建新图。

Use case: infographic-diagram
Asset type: a complete detailed cluster architecture image for engineering handover and external presentation.
Primary request: Generate one exceptionally clear, professional Chinese technical architecture diagram of the current RayTrain distributed training platform, based only on facts below. Landscape high resolution approximately 3840×2160, readable Chinese sans-serif type. Clean white/off-white background, deep navy titles, blue control plane, teal data plane, violet observability, small orange dashed boxes for unfinished capabilities. Flat precise vector-like blocks and fine orthogonal arrows, coherent generous grid, not a 3D illustration. Fine typography large enough to read, no crowded crossing arrows. The diagram must be a genuine polished image, not code.
Title exact: "RayTrain 分布式训练平台 · 集群架构"
Subtitle exact: "身份与权限 · 任务编排 · 数据与缓存 · 训练与观测"
Do not include any IP, account name, password, internal hostname, repository address, job ID, git SHA or endpoint URL. No fake logos. Include an unobtrusive "2026-09-11 现状" note.

Composition: large structured diagram with six numbered zones. Upper zone is user access and platform control, middle is scheduling and GPU execution, lower is data flow; a full-height right sidebar is observability. An unobtrusive small footer lists not-yet-enabled or unfinished capabilities. Use one consistent visual legend "蓝色：控制流  绿色：数据流  虚线：待落地能力". Current connections solid (data green, control blue); future connections dashed. Label arrows only where needed. Preserve all key labels below, use short concise labels, never invent unsupported features.

ZONE 01 "用户入口与认证"
Three small entry cards: "新 Portal UI" with "统一认证"; "独立旧 UI" with "保留兼容"; "spk-rayjob / Ray CLI" with "团队绑定 PAT".
SSO card "Keycloak → OAuth2 Proxy". Gateway "Ingress / HTTPS". Portal uses SSO/gateway; old UI uses local session, CLI uses PAT. Both feed same backend. Small note "身份认证与平台授权分离". No implication bare request headers are trusted; auth sublabel "签名令牌验证".

ZONE 02 "平台控制面"
Main card "Backend API ×2" with clean small 2-row chip list: "用户 / 多团队 / 角色", "配额 / 镜像登记", "任务 / 调试 / 数据集", "上传下载 / 日志 / MLflow 代理".
Beside it cylinder "PostgreSQL" sublabel "身份 · 任务 · 版本 · 审计" and "持久卷 / 单实例".
Below backend a slim box "Reconciler · 状态协调 / 资源回收".
User roles small note "SuperAdmin / TenantAdmin / Engineer". Personal data note "个人数据绑定稳定身份，团队切换不迁移原始文件".
Small "Harbor 环境镜像" card feeds compute, and separate "版本化代码包" card feeds submitter; prominent principle "代码不进镜像，镜像只提供运行环境".

ZONE 03 "资源准入与任务编排"
Two controller cards "Kueue 0.19 ×2" with "租户 LocalQueue → 共享 GPU ClusterQueue" and "GPU / CPU / 内存准入"; "KubeRay 1.6.2 ×2" with "RayJob / RayCluster 生命周期". Backend submits Kubernetes resources; Kueue admits suspended jobs then KubeRay manages execution. Do not draw Kueue as physically running worker code.
Small note "团队决定卡型；当前为共享 4090 池". "Ray 运行时：兼容 2.35 / 托管数据链路 2.58". Do not claim all Ray jobs use 2.58.
ZONE 04 "训练与交互执行"
Large border "GPU 资源池：4 节点 × 8 RTX 4090 = 32 GPU".
Within border a representative task group (not exact running count) "每个任务独立 RayCluster":
"Submitter" → "Ray Head（不申请 GPU）" → two parallel boxes "GPU Worker · Rank 0…N" and "GPU Worker · Rank N…M". Connect workers with double-ended line "PyTorch DDP / NCCL".
Inside/along group clear tool roles:
"Ray Train：分布式 Worker · Checkpoint / 恢复"
"Ray Data：流式读取 · 批处理 · 预处理"
"NVMe：分片缓存 · 预取 · 对象溢写"
RayData feeds worker training; RayTrain orchestrates workers. Do not imply every job automatically uses all components.
Adjacent "交互式调试" card "JupyterLab / VS Code" "短期票据 + HttpOnly Cookie" and "spk-rayjob connect" "任务所有者 / 运行中 Worker" "无需暴露 SSH 端口".
Small note "镜像版本随任务固定，既有任务保持运行时兼容".
ZONE 05 "数据接入、不可变版本与存储"
Bottom wide horizontal data path with clear five stages:
"IDC NFS 原始数据" "只读挂载 / labeled"
→ "tosutil 增量同步" "单向 / 不映射源端删除" small badge "已启用，待登记数据源"
→ "TOS 原始镜像" "SHA-256 对象 / inventory"
→ "CPU 数据集发布器" "校验 · 索引 · 分区构建"
→ "不可变数据集版本" "Parquet / 数据分片 / Manifest" "全量或按场地选择"
The immutable dataset goes upward via green arrow to Ray Data then NVMe then training. A separate existing "公共 labeled" route goes from TOS storage into dataset publisher (do not imply every existing dataset already came from SyncRun).
Beside/below stages storage strip "TOS + FSX/CSI" with three partitions "个人空间（读写）" "团队数据（Pod 只读）" "公共数据（Pod 只读）". Training writes outputs through green arrow to "Checkpoint / 训练产物（持久保存）".
Important small notes: "发布生成独立产物，不改写 labeled" and "NVMe 是可淘汰缓存；TOS 是持久数据源". Avoid depicting all file bytes inside Parquet: use Parquet plus data shards as labels.

ZONE 06 right column "实验与可观测性"
Three stacked groups:
"MLflow 3.14 ×2" subtitle "Run / 参数 / Loss / 指标" linked from training via "训练代码上报"; nested "独立 PostgreSQL" and "Artifact 存储 · FSX". A note "Artifact / Model 需代码显式记录"; "平台 Job ↔ MLflow Run" note "关联映射，ID 不相同".
"Prometheus / DCGM → Grafana" subtitle "GPU · CPU · 内存 · Worker 性能".
"Alloy → Loki" subtitle "任务日志 · 查询 · 导出".
Observability feeds Backend/UI through restrained arrows; task metrics/loss are not automatic from Ray Data. Do not show MLflow being the same database as backend.

Small footer callout with orange dashed border headed "尚未落地 / 未启用".
Exact three concise items: "团队专属节点绑定" "TAS / 闲时抢占（当前关闭）" "独立评估 / 模型审批发布（待完善）".
This future box must be visually separate and must not falsely present these as operational.
Avoid any statements of measured speedup, guaranteed full functionality, elastic autoscaling, enabled preemption, GPU heterogeneity in current live pool, or a completed model-publishing lifecycle. Keep exceptionally clear hierarchy and readable accurate Chinese labels.
