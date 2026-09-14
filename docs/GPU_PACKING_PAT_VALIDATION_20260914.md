# GPU 装箱与 MLflow 永久令牌发布验证

日期：2026-09-14（北京时间）。用户明确授权发布后，后端和 Portal dev 已上线；8个真实训练任务全部验收成功。线上账户复选框点击尚未完成，边界见下文。

## 发布版本

| 对象 | 版本与证据 |
| --- | --- |
| 后端构建提交 | `6644567593f7e2b352d4c4d894e85a84e180b758`，构建时本地、GitHub main、内部 GitLab main、构建机正式目录一致 |
| 后端镜像 | `release-20260914-gpu-pat-01`，digest `sha256:868d69fcdfdf9564e461da00fc3efe717e2ed41651272afa604c8a96c9c4f439` |
| 后端线上 | Helm revision **235**；2/2 Ready，两个 Pod imageID 均匹配；healthz 200；schema **54** |
| Portal dev | `18aec4916063a8dabe3ec3bf15243c6aa68f979e` |
| Portal CI | [流水线 33955](https://gitlab.wellspiking.ai/wellspiking/frontend/wellspiking-frontend/-/pipelines/33955) 的 lint、docker-dev、helm-deploy-dev 全部通过 |
| Portal 镜像 | 实际 Deployment tag 为完整 `18aec491` 提交；Pod imageID `sha256:25b0b7c0e9a2103040c47ab055b74de48ec71152674d34f9b5e8a41e2b0f58f9`；rollout 成功，SPA 200 |

仅重建后端和 Portal；CLI 下载服务、训练运行时镜像保持原版本。发布前备份数据库并验证备份目录可读取，随后执行向后兼容的迁移。后续本记录的文档提交不重建镜像。

Portal 新入口 `index-boZxrMH7.js` 引用账户组件 `index-D2xzj4ad.js`，实际包含 `neverExpires` 请求和“永不过期”“已撤销”文案。通过 dev 真实菜单路径 `/raytrain/RayTrainAccountSecurity` 已进入账户页面；随后浏览器辅助功能视图停在侧栏菜单，且用户开始操作 Chrome，故停止干扰。**线上复选框点击未完成**；本轮候选验证为行为合同、完整 lint/build，没有将其表述为浏览器交互测试通过。未发布 master/common。

## GPU 调度变化

同一个 `gpu-4090-flavor` 首次增加 `topologyName=raytrain-hostname`，保留原 ClusterQueue、LocalQueue、资源池及提交方式。`KUEUE_TOPOLOGY_ENABLED=true`，抢占仍关闭，全部抢占策略为 Never。GPU 总配额保持 **32**；CPU `709364m`、内存 `2951216230144` 均按发布前 live 值保留。

新 RayJob Worker 使用 `podset-unconstrained-topology: "true"`，取消与装箱冲突的硬拓扑分散。TAS 按实际 GPU、CPU、内存和存储约束安排节点。现有任务不会自动搬迁，DevWorkspace 不在此训练调度改动范围。

首次增加 ResourceFlavor 的 topologyName 已通过实际 API server 更新 dry-run；已有该字段后禁止修改或移除。因此此次首次启用未使用可能尝试移除字段的 Helm 自动回滚；恢复流程见 [运维指南](OPERATIONS_GUIDE.md#63-gpu-装箱与-tas-切换)。只关闭 Worker 注解不能视作完全关闭 TAS。

### 用户训练连续性

发布前有三个活跃训练，滚动完成后立即核对其 Worker UID、节点及重启数全部不变，均为 Running、0 重启。后续验收期间：

| 原有任务 | 连续性 |
| --- | --- |
| `job-23bc9cc965d818c4dce876fb` | 原地运行后自然 SUCCEEDED；RayJob UID、Workload admission 保持不变 |
| `job-99ba40c6941e6a7eb779e107` | 原地运行后自然 SUCCEEDED；RayJob UID、Workload admission 保持不变 |
| `job-27a0c104491a994b0a7e885b` | 8 GPU 训练继续 RUNNING；RayJob/RayCluster/Pod UID、节点、重启数及 admission 不变 |

更早快照中的 `job-4ab6148554562ae5c6f82ee2` 在本次部署前已经结束，不计入发布时活跃集合。未停止或重启任何用户训练。

### spk-rayjob 真实验收

使用临时普通训练 PAT，代码由 CLI 打包提交。升级前已安装的 `release-20260911-05` 成功提交三个独立 `--engine ray-ddp --workers 1 --gpus-per-worker 1` 任务，无新增队列/TAS 参数，也不需要升级客户端。

三个任务 `job-c4c8527ec4061b4dd40c71f2`、`job-d079f6d9789905d82fd7a75d`、`job-a5c4a8dfbd61822494e06bb4` 的 Kueue topologyAssignment 与实际 GPU Worker 全部指向 `172.28.1.233`；此时 `172.28.1.229` 和 `172.28.1.232` 保持 GPU 空闲。三任务均已输出 CUDA RTX 4090、12 个训练 step、rank 0/world_size 1、产物及 `PACK_ACCEPTANCE_SUCCESS`，全部自然 **SUCCEEDED**。

| 规格 | 任务 | 结果 |
| --- | --- | --- |
| Ray DDP，1 Worker × 2 GPU | `job-ee7cc21af1f53599890f5373` | SUCCEEDED；落233；rank 0/1，world_size 2，NCCL sum 3 |
| Ray DDP，1 Worker × 4 GPU | `job-65e4d07e5591865458835d0f` | SUCCEEDED；落229；rank 0–3，world_size 4，NCCL sum 10 |
| Ray Train，2 Worker × 1 GPU | `job-5e0ad0c706c05eccde6fed92` | SUCCEEDED；两个 Worker 均落229的剩余空间；rank 0/1，world_size 2，NCCL sum 3 |
| Ray Train，2 Worker × 4 GPU | `job-f8470350489c638bf34f33d7` | SUCCEEDED；两个 Worker 同落232；world_size 8，NCCL sum 36；不是跨物理节点证据 |
| Ray Train，2 Worker × 5 GPU | `job-0949e800d34096cb75a126a4` | SUCCEEDED；两个 Worker 分别落233和232；rank 0–9，world_size 10，NCCL sum 55；真实跨物理节点通过 |

2 卡提交时233有足够剩余容量，继续填充该节点；随后4卡提交时233只剩3卡，因此选择另一节点属于正常容量约束。并发用户任务持续进入，不能把所有落在新节点的任务都判作装箱失效。前五个已完成任务均使用同一普通训练 PAT 通过 `jobs/:id/artifacts` 和预览接口回读，9份 rank JSON 的 world_size、GPU、12 steps、有限 loss 和 all-reduce 结果均匹配日志，证实终态后产物持久化。

2×4 提交时，229/233/232 分别剩4/5/8卡，两个 Worker 实际同落232，未拆到229+233。已核对 Kueue v0.19 官方源码：同一 PodSet 优先寻找能容纳整个 PodSet 的最紧单域，无单域可容纳才跨域。这符合默认算法，不能宣称所有多 Worker 任务都会先填跨节点碎片。补充2×5规格验收确认跨物理节点路径，其中233已有3卡用户任务，本次5卡正好填满剩余空间；2×8因当时没有两台完整空闲节点而不提交。

最后三个多 Worker 任务的2/8/10份 rank JSON 也已全量回读，与期望 rank 集合、world_size、NCCL 结果匹配。8个任务合计29份 rank 产物全部验证。验收任务GPU Pod最终剩 **0**，local配额恢复为总24 / 占用8 / 可用16；原8卡任务仍RUNNING，Worker UID、222节点及0重启保持不变。测试记录和产物保留作为证据，不删除用户历史任务。

临时训练 PAT 已撤销，同一令牌再次请求返回401，其配置文件和临时下载的CLI已删除；原用户CLI配置与旧版二进制保留。已删除本次两个干净的测试工作树及传输bundle，保留数据库备份、发布覆盖文件、源码提交、日志和验收产物。

## MLflow 永久令牌

仅个人“MLflow 全局读写”用途可选择 `neverExpires:true`，API 返回 `expiresAt:null`。普通训练用途不可永久有效；原默认 90 天、有限期 1–365 天、集成账号原有边界不变。永久令牌仍受撤销、用户状态、当前团队成员关系和 scope 校验约束。

生产 API 验收结果：

- 12 个既有 PAT 的 expiresAt、scope、所属用户/团队和撤销状态均未变化。
- 永久训练 scope、永久同时指定数值有效期、有限期 366 天均返回 400。
- 365 天 MLflow 令牌仍可创建且有明确 expiresAt；永久 MLflow 令牌返回 null。
- 永久 MLflow PAT 请求训练任务返回 403；普通训练 PAT 请求全局 MLflow 返回 403。
- 永久 PAT 成功创建专用实验、Run，写入并回读 metric=1，结束 Run。
- 撤销永久 PAT 后再次请求返回 401。验收专用实验已删除，两枚临时 MLflow PAT 均已撤销。

## 测试与证据

全部编译、测试、lint 和构建均在构建机隔离目录执行。后端全量 `go test -timeout=20m ./...` 通过，并使用真实隔离 PostgreSQL 验证全新迁移、重复执行、旧数据升级与 nullable expires_at 兼容。GPU 回归先验证旧策略失败，再验证新策略；两引擎及 1/2 Worker × 1/2/4/8 GPU 组合通过。Helm 配置有效/无效组合与最新合入的帮助文档相关增量测试通过。Portal 完整 Dockerfile.lint 和 build:dev 通过，线上 CI 再次通过。

构建机受限发布目录 `/root/raytrain-release-gpu-pat-20260914T102020Z` 保存数据库、values、manifest 备份、精确 dry-run、镜像、升级、存量训练基线及 MLflow 验收证据。CLI 提交与落点日志位于 `/root/raytrain-release-6644567-spk-*.log`。令牌不写入文档或日志。
