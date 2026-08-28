# Ray 训练平台产品与工程路线图

更新日期：2026-08-28

本路线图用于统一产品、训练、数据和平台运维的实施顺序。每一期必须独立上线、独立验收、独立回滚；不会把尚未验证的能力一次性叠加到生产训练链路。

## 当前基线

- 用户可通过网页、`spk-rayjob` 和原生 `ray job submit --working-dir` 提交代码随任务上传的训练；
- KubeRay 为每个训练任务创建隔离 RayCluster，Kueue 负责团队 GPU 配额和排队；
- 公共数据统一位于 `tos://shanghai-data-transfer/ray-train/public/`，Pod 内入口为 `/mnt/storage/public`；
- 个人与团队数据由平台治理，用户不接触 TOS AK/SK；
- Loki、Prometheus/Grafana 和 MLflow 已接入训练日志、基础设施指标和实验记录；
- 两个 BEVFusion 分支的 2×8 GPU 训练已有可复现验收基线。

这组已验证基线属于兼容的 Ray 编排 DDP。Ray Train 托管代码已经按计划交付，但 `RAY_TRAIN_MANAGED_ENABLED` 默认关闭；Ray 2.56.1 生产运行时、Ray 2.58.0 canary 和 KubeRay 1.6.2 仍需在目标环境按零训练负载流程独立升级、canary 和验收，不在路线图中写成已上线事实。用户契约见 [Ray Train 托管指南](RAY_TRAIN_MANAGED_GUIDE.md)。

## 交付顺序

| 优先级 | 能力 | 用户价值 | 关键依赖 | 完成标准 |
|---|---|---|---|---|
| P0 | 生产可靠性基线 | 更新可回滚，数据库、挂载与 DNS 故障可发现、可恢复 | 现有运维体系 | PostgreSQL 备份恢复演练；FSX/DNS SLO 与告警；发布前后训练连续性验证；确定性 release tag |
| P0 | 提交前检查 | 在占用 16 卡前发现镜像、数据、挂载、MLflow、配额和输出路径问题 | 平台 API、存储目录、Kueue | 网页和 CLI 共用 preflight API；失败给出可操作原因；不创建 RayCluster |
| P1 | 数据集版本管理 | 用户选择数据集和版本，不再猜目录与 PKL | 公共数据根、对象清单 | 数据集/版本/状态/文件数/容量/摘要可见；训练记录固定 manifest digest |
| P1 | 数据读取性能面板 | 解释“GPU 为什么在等数据”，展示冷读与热读差异 | Prometheus、数据集版本 | 展示 MiB/s、files/s、P95、GPU 等待数据比例、数据加载耗时和基准对比 |
| P1 | 节点 NVMe 缓存 | 按任务选择临时 NVMe，默认关闭且不改变现有训练 | 可靠性基线、性能基线、平台提交 API | 第一阶段支持 off/runtime 和 100Gi / 200Gi / 500Gi 策略；dataset 模式仅在收益达到门槛后另行设计 |
| P1 | MLflow 训练对比与调参 | 在平台比较 loss、mAP、NDS、参数和资源利用率 | MLflow、训练指标规范 | 多 Run 对比、参数差异、资源曲线、重新提交入口可用 |
| P2 | Checkpoint 生命周期 | 用户可续训、标记最佳模型并避免误删 | 数据集版本、MLflow、产物目录 | 保留策略、最佳标记、续训入口、引用保护和定期清理可审计 |
| P2 | 多团队成员关系 | 一个用户可加入多个团队并切换工作上下文 | 统一授权模型、租户隔离测试 | 成员关系、角色、当前团队切换、跨团队审计与命名空间隔离完成 |

## 当前迭代

当前只实施以下范围：

1. GPU 节点 `/data1/ray-cache`、`/data2/ray-cache` 全局具备供应能力，但每个任务按任务选择 `off` 或 `runtime`，初始默认关闭；
2. 管理员容量 allowlist 为 100Gi / 200Gi / 500Gi，`runtime` 默认 200Gi、最大 500Gi；Web、`spk-rayjob` 和经平台网关解析 metadata 的原生 `ray job submit` 使用同一契约；
3. `runtime` 只为 Ray Head/Worker 挂载 `/mnt/cache` 并配置 `PLATFORM_CACHE_PATH`、Ray temp-dir 和 object spilling，不自动复制 `/mnt/storage/public` 或加速 DataLoader；
4. 本地卷容量门禁、回收、监控和告警；
5. 平台、MLflow、Loki、Prometheus/Grafana 等服务从“CPU 节点硬绑定”改为“CPU 优先、GPU 节点可兜底”；
6. 平台 Backend、Frontend 和 `spk-rayjob` 重建，训练镜像保持不变；未提供缓存参数的任务和既有任务保持不变；
7. 用持续运行训练、三种提交入口的 off/runtime smoke、2×8 回归和 I/O 基准证明不影响现有训练。

详细设计见 [GPU 节点 NVMe 缓存与平台服务弹性调度设计](superpowers/specs/2026-08-23-nvme-cache-and-gpu-fallback-placement-design.md)，实施步骤见 [NVMe 缓存与 GPU 节点兜底实施计划](superpowers/plans/2026-08-23-nvme-cache-and-gpu-fallback-placement.md)。

## 后续阶段门禁

- dataset 模式不会因“有 NVMe”自动上线；它不在当前范围，必须先具备不可变数据集 manifest 和可校验、可恢复的预热流程，再证明训练受数据读取限制并达到设计文档规定的基准门禁；
- 数据同步到 IDC、团队共享目录或公共目录仍需审批与审计，不会开放网页下载或向用户暴露 AK/SK；
- PostgreSQL 高可用、跨团队身份模型和自动清理属于高风险变更，必须单独设计和迁移；
- 任一期上线都不得修改运行中 RayJob/RayCluster 的 Pod 模板，不得重启 kubelet、containerd、CNI、FSX Agent 或 GPU 驱动。
- 双引擎长期并行：`ray-ddp` 保持既有任务与镜像兼容，`ray-train` 只通过显式租户 canary 开放；Portal、`spk-rayjob` 和原生 Ray API 必须分别持久化正确来源与不可变运行时信息。
- KubeRay Operator 只能在零 RayJob、RayCluster、debug workspace 和活动 Kueue Workload 的维护窗升级；备份、维护门禁、制品摘要、verify 和失败后人工恢复缺一不可。
