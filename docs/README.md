# RayTrain 文档中心

根目录 [README](../README.md) 介绍平台定位、能力和架构。本目录保存实际使用、管理、部署和开发手册；以下链接是当前有效入口。

![RayTrain 组件级生产架构](architecture/ray-training-platform-production-architecture-v4.svg)

## 架构图

| 视图 | 说明 |
| --- | --- |
| [组件级生产总图](architecture/ray-training-platform-production-architecture-v4.svg) | 从用户入口到控制面、Kueue/KubeRay、Ray 运行时、存储、缓存与可观测性的当前全貌。 |
| [控制面与多租户](architecture/ray-training-platform-control-plane-tenancy-v1.svg) | 身份、角色、团队 namespace、所有权校验和 Kueue 资源准入。 |
| [训练任务生命周期](architecture/ray-training-platform-job-lifecycle-v1.svg) | 代码打包、预检、排队、建群、DDP/NCCL、失败分支、终态与 TTL。 |
| [存储与可观测性](architecture/ray-training-platform-storage-observability-v1.svg) | TOS/FSX、稳定逻辑路径、双 NVMe 缓存、日志、资源指标和 MLflow。 |

## 新维护者从这里开始

先阅读[交接与入手手册](HANDOVER_GUIDE.md)。它提供三端源码一致性、线上组件清单、第一小时只读检查、日常维护节奏、标准发布链、故障决策树和交接验收清单。随后按职责进入构建部署、运维或管理员手册，不需要从历史设计记录中拼接现状。

## 用户文档

从[用户文档入口](user/README.md)开始。它按一次完整训练的顺序组织以下手册：

| 任务 | 文档 |
| --- | --- |
| 第一次使用、数据目录、调试、任务、日志、MLflow 实验和 Checkpoint | [用户使用手册](USER_GUIDE.md) |
| Portal、`spk-rayjob`、原生 Ray Jobs API | [多方式提交手册](SUBMIT_GUIDE.md) |
| 新 PyTorch/DDP 项目接入与多机多卡改造 | [新训练代码接入](NEW_TRAINING_CODE_GUIDE.md) |
| 从全新 clone 到 BEVFusion 2×8 卡训练 | [BEVFusion 端到端操作手册](BEVFUSION_END_TO_END_GUIDE.md) |
| BEVFusion 两个分支的逐文件补丁 | [BEVFusion 代码改造](BEVFUSION_CODE_CHANGES.md) |
| BEVFusion 实际验收流程与结果 | [BEVFusion 交付手册](BEVFUSION_RUNBOOK.md) |
| 输入路径关系、双节点读取性能与 FSX 排障 | [数据路径与读取性能基准](DATA_IO_BENCHMARK.md) |
| 单机/多机训练速度对比、瓶颈定位与标准 A/B | [训练性能诊断指南](TRAINING_PERFORMANCE_DIAGNOSIS.md) |

## 管理员与运维文档

| 任务 | 文档 |
| --- | --- |
| 新维护者接手、发布责任、日常维护和故障入口 | [交接与入手手册](HANDOVER_GUIDE.md) |
| 用户、租户、角色、配额、镜像、Git 和数据发布 | [管理员手册](ADMIN_GUIDE.md) |
| 网络、高可用、KubeRay/Kueue、存储、安全和可观测性 | [生产架构](ARCHITECTURE.md) |
| 安装、升级、回滚和迁移 | [构建与部署](BUILD_AND_DEPLOY.md) |
| 组件清单、巡检、备份恢复和故障处置 | [生产运维手册](OPERATIONS_GUIDE.md) |
| 代码结构、测试和贡献 | [开发指南](DEVELOPMENT.md) |

## 文档状态

- 当前部署基线中 Frontend、Backend、CLI 发布服务和 MLflow 均使用多副本与不可变镜像 digest；具体 revision 以目标集群的 Helm release 为准。
- 当前环境保留本地账号和内置单实例 PostgreSQL，因此不把“强制 OIDC”和“外部 HA PostgreSQL”描述为已完成。
- BEVFusion 的 2×8 smoke 已覆盖两个分支和三种提交入口；另以 15,228 条训练样本、1,620 条验证样本完成一次 16 rank、1 epoch 的全量长时执行验收。两者验证平台链路，不代表正式多 epoch 收敛已经由算法团队验收。
- GPU 节点双 NVMe 缓存基础设施和输入预热已交付，但每个任务默认关闭、按需启用；IDC 与 TOS 的自助双向迁移尚未开放。

`archive/` 只保存历史设计、计划和研发记录，不是运行手册。不要把文档复制到临时目录后长期使用；版本冲突时以当前仓库的根 README 和本页为准。
