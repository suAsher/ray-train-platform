# 权限与团队调度核对

日期：2026-09-14。权限修复已发布，后端接口与临时0卡工作区真实验收通过；Portal dev 目标镜像已上线。浏览器交互验收因自动化工具连续超时未完成。用户随后授权启用 GPU 团队软集中，后端与 Kueue 两项开关已开启并持久化；本轮 spk-rayjob 已验证同团队 1+1+2+4 卡集中同节点及容量不足回退，五个任务全部成功，测试资源已回收，现有训练未发生迁移或重启。

## 三层权限

Portal Java 服务的角色和菜单关联决定入口是否显示，RayTrain `/api/v1/me` 的角色决定业务操作，原生 MLflow 共享服务有独立的数据访问范围。Portal 菜单角色不会自动成为 RayTrain SuperAdmin。新统一认证用户在 RayTrain 默认为 Engineer，进入零 GPU 配额的“待分配用户”团队；已有用户的团队不因此改变。

| 能力 | Engineer | TenantAdmin | SuperAdmin |
| --- | --- | --- | --- |
| 平台训练任务列表/详情 | 本人 | 当前团队 | 全平台；列表需使用管理范围 |
| 停止平台训练 | 本人 | 当前团队 | 全平台 |
| 平台实验中心训练记录 | 本人 | 当前团队 | 全部团队的最近记录 |
| 自己的交互式环境 | 可创建、停止、访问 | 可创建、停止、访问 | 可创建、停止、访问 |
| 管理停止他人交互式环境 | 不允许 | 本团队 | 全平台 |
| 进入他人 Jupyter/VS Code | 不允许 | 不允许 | 不因管理停止权限而允许 |
| 跨团队分配成员 | 不允许 | 不允许 | 允许；前端已补上本人及其他超级管理员的团队按钮 |
| 分配全局 SuperAdmin | 不允许 | 不允许 | 团队成员接口也不能授予该全局角色 |

权限来源：`backend/auth/claims.go`、`backend/api/jobs.go`、`backend/api/memberships.go`、`backend/api/workspaces.go`、`backend/api/gpu_allocations.go`。后台仍校验角色和目标团队，隐藏按钮不替代 API 鉴权。

## MLflow 可见范围

平台“实验中心 → 训练记录”不是原生 MLflow 全量目录：它只显示有平台任务归属及签名的记录，仍用数据库 Job 校验团队和 owner。超级管理员的全局列表从数据库枚举团队，合并排序取最多100条；这是最近N候选的安全过滤接口，不是完整分页导出。

原生 MLflow 网页及个人 `mlflow:full` 当前是全局共享访问，包含修改、删除、Artifact 和 Registry；不能向普通用户承诺“仅本人可见”。`/api/v1/mlflow-native` 不做 owner/tenant 过滤。受治理的 `/api/v1/mlflow-tracking` 和指定 Job Run 接口另有归属检查，不能与 native 接口混淆。

用户已明确选择：保留原生 MLflow 全局共享，平台训练记录继续按角色隔离。因此本次不收紧原生 MLflow、Artifact、Registry 或已有 `mlflow:full` PAT 的权限；实验中心列表修复不能改变这一共享边界。

## 新前端菜单配置建议

普通用户配置以下8个菜单：我的训练任务（`RayTrainJob`）、实验中心（`RayTrainExperiments`）、交互式调试（`RayTrainDevcenter`）、数据与存储（`RayTrainDataCache`）、版本化数据集（`RayTrainDatasets`）、外部提交（`RayTrainExternalSubmit`）、账户与安全（`RayTrainAccountSecurity`）、使用说明（`RayTrainHelp`）。

团队管理员在此基础上增加 GPU资源池（`RayTrainDevicesManagement`）、GPU占用明细（`RayTrainControlCenter`）、平台管理（`RayTrainQuotaManage`）；超级管理员配置全部菜单。管理操作仍按 RayTrain 角色区分，团队管理员不能借全菜单跨团队管理。资源池可能显示共享物理设备情况，但其他团队占用对象名称会按后端规则隐藏。

这些是菜单建议及组件名，不是已写入 Java 服务的角色配置。实际 `roleId/menuId/permission` 由 Portal `/system/api/role/menu` 和菜单数据维护，未在源码中固定映射，不能把组件名伪装成 permission key。本次没有替任何真实用户修改 Portal 角色。

## guofeng.su 团队分配

现网只读确认：身份为 `oauth2-proxy`，当前团队 local，`global_roles=["SuperAdmin"]`。全局超级管理员身份独立于活动团队，团队决定本人新建任务使用的配额和存储授权上下文，管理全平台不需要加入所有团队。

前端原 `canManageUser` 同时禁止操作本人和超级管理员，被“团队”按钮复用，导致团队分配也隐藏。修复拆分成员关系与账号停用/删除权限；团队角色只提交 Engineer/TenantAdmin，全局 SuperAdmin 保持。本人变更后刷新 `/me` 及团队数据，继续禁止自停用、自删除等操作。未实际迁移 guofeng.su。

## 交互式任务管理停止

修复前只有 `DELETE /api/v1/dev-workspaces/me`，固定查询登录人自己的工作区；管理页虽然能列出其他用户的资源，却没有停止入口。已新增 `DELETE /api/v1/admin/dev-workspaces/:id`，从已鉴权的数据库记录取得 Kubernetes 目标，按 workspace ID 校验标签并更新状态，记录 `workspace.stopped` 审计。SuperAdmin 跨团队，TenantAdmin 仅本团队，普通用户继续用原 `/me` 路径。

前端在 GPU 占用明细及平台管理的交互式环境列表增加停止入口，支持0GPU工作区。返回202表示停止请求已处理，Pod回收仍可能异步，页面不提前承诺资源已经释放。本次专建0GPU工作区 `ws-89e031f6e36bb8ea39837867` 达到 RUNNING 后，通过新管理接口停止成功，RayCluster、Service、Pod 均已回收，`workspace.stopped` 审计成功。已有用户工作区未被停止。

## 团队8卡与物理节点

初次只读排查时 algorithm 配额8、占用7：三个1卡训练在233，一个4卡训练在229。四个任务均已启用 TAS unconstrained，不能归因为开关关闭。4卡任务于 `2026-09-14T10:57:20Z` 准入，额外请求48 CPU和200Gi内存；缺少该时刻完整资源账本，不能只用当前剩余量倒推其历史落点。

原 TAS 优化的是共享池任务装箱，不包含“同团队必须集中同节点”的规则，也不会搬迁已运行任务。团队8卡是配额上限，不代表独占一台8卡机器。Kueue确定 topologyAssignment 后，仅在 Pod 调度阶段增加 soft affinity 不能可靠改变其节点决定，硬亲和还可能造成准入后 Pending。

用户已选择“团队优先集中：尽量同节点，允许共享空闲卡”，不为团队独占节点，也不迁移运行任务。实现是在新任务 worker 上添加基于同团队现有 GPU worker 所在节点的 preferred node affinity，保留 TAS unconstrained 资源账本和容量不足时的其他节点回退。Kueue v0.19 必须同时启用默认关闭的 alpha gate `TASRespectNodeAffinityPreferred`，才能在准入阶段考虑这种偏好；只改 PodSet preferred topology 注解不具备团队语义。

用户要求“生效测试”后，两项开关已启用：后端 `KUEUE_TEAM_NODE_AFFINITY_ENABLED=true`，Helm `ray-platform` revision 237；Kueue controller 参数含 `--feature-gates=TASRespectNodeAffinityPreferred=true`。两次 server dry-run 分别只改变该参数和后端团队开关，镜像保持原摘要；两套控制面均2副本健康，抢占仍为 Never，队列与配额未改变。

Kueue 开关已保存到其自身 Helm release `kueue` revision 3，使用现有 `kueue-0.19.0.tgz`，`--reuse-values` 的最小覆盖为 `controllerManager.featureGates: [{name: TASRespectNodeAffinityPreferred, enabled: true}]`。此字段是对象数组，不能写成字符串数组，也不能放进 `ray-platform` values 代替 Kueue 配置。持久化 dry-run 与原 Kueue Helm manifest 逐行比较仅新增 gate 参数；证书及其余资源内容一致。持久化后的 Deployment generation 未再次变化，2副本均 Ready。

启用前现场 algorithm 的两个4卡 GPU Worker 已全部位于 `172.28.1.229`，该团队占用8/8卡。另一个节点上的 Head 不占 GPU，不能据此认定 GPU 碎片。验收使用 guofeng.su 的 local 团队、一天有效最小权限临时 PAT 和当前官方下载的 `spk-rayjob release-20260911-11`，没有迁移真实用户或调整团队配额。

| 用例 | Job ID | GPU Worker 实际节点 |
| --- | --- | --- |
| 空团队首个1卡任务 | `job-41c7b3909ab31a7a599f675f` | `172.28.1.222` |
| 后续1卡任务 | `job-56463cbfcc6fefd4aa6bd2a8` | `172.28.1.222` |
| 后续2卡任务 | `job-fea61f1611d4e0bf01b6be1b` | `172.28.1.222` |
| 后续4卡任务 | `job-5107d67808e9672f61db683d` | `172.28.1.222` |
| 上述8卡仍运行时追加1卡 | `job-3e4f2dcb6087ccb3125548aa` | `172.28.1.232` |

后三个集中任务及回退任务的 RayJob worker 均携带 `matchFields: metadata.name In [172.28.1.222]`、weight 100，且保留 unconstrained topology；对应 Workload 的 topologyAssignment 与实际 Worker 落点一致。首任务没有既存团队 GPU Worker，因此没有团队偏好，正常由 TAS 选点。测试不依赖用户增加 queue、节点或亲和参数。已回读9份 rank 产物，world_size 分别为1、1、2、4、1，均完成12步 CUDA 训练，all-reduce 结果正确。CLI connect 进入首任务 Worker 后退出成功，退出后训练仍为 RUNNING。

截至 `2026-09-14T13:53:08Z`，五个任务全部 SUCCEEDED，完整日志均含 `PACK_ACCEPTANCE_SUCCESS`，无测试存活 Pod，local GPU 配额恢复0/24。仅撤销本轮临时 PAT，并验证其 login-check 被拒绝后删除独立凭据文件；原会话未修改。启用前已有的2个训练 RayJob、4个 RayCluster、8个运行 Pod 均保持原 UID；8个 Pod 节点不变、重启数均保持0，两个训练仍 RUNNING。

配置备份、最小覆盖、调度及产物证据、`acceptance-final.json`、`continuity-final.json` 保存在构建机 `/root/raytrain-team-packing-20260914/`。正常 CLI 提交与完整训练日志为 `/root/raytrain-release-474abec-spk-team-*.log`。本轮仅配置启用及文档记录，未重建镜像或修改训练代码的提交接口。

该策略是软偏好：已有任务不迁移，CPU/内存/GPU不足可以跨节点，同团队第一批任务同时提交且尚无绑定 Worker 时也可能分散。本次现场没有构造“另一团队的可行节点更满”的对照实验，不能把同节点结果单独作为团队偏好优先于全局装箱的因果证明；同时保留了开关、模板偏好、准入及实际节点四层证据。

官方依据：[Kueue v0.19 TAS 节点亲和解析与排序](https://github.com/kubernetes-sigs/kueue/blob/v0.19.0/pkg/cache/scheduler/tas_flavor_snapshot.go#L958)、[feature gate 默认值](https://github.com/kubernetes-sigs/kueue/blob/v0.19.0/pkg/features/kube_features.go#L699)。

## 验证与发布记录

- 后端业务 SHA：`3d8e4973c5ba7a6822be199487969f5caf3b3bc5`；本地、GitHub main、内部 GitLab main、正式构建目录在发布时一致。
- 后端镜像：`release-20260914-permissions-01`，摘要 `sha256:3958a80be9c7eba1ab367b4e0a67a6d2ae53c790cf486f5bd9512c307e6a54e7`。权限镜像发布为 Helm revision 236，后续团队集中配置启用为 revision 237；两副本均新摘要且 Ready，healthz 200。
- Helm server dry-run 只改变后端镜像与新增的默认关闭团队偏好 env；TAS、配额、抢占、存储、运行时资源保持原配置。现有 RayJob/RayCluster/Pod 没有 UID 变化、丢失或重启数增加。
- Portal dev SHA：`10fb586a68a5a80d798c17ccda63fc23a7c232f4`；完整 Dockerfile.lint、全部合同、build:dev 通过，STOPPING 重试测试旧实现 RED、新实现 GREEN。
- Portal 实际镜像：`sha256:54330d2594dad7fb3d19c792014110338b8cc511c14a0df709983cab8e3d446a`，rollout 成功且 Pod Ready。私有 GitLab API 无匿名读取权限，部署标记不含 pipeline/job ID，因此未独立确认流水线每个 job 的状态。
- 后端最终候选完整 `go test -p 4 -timeout=20m ./...` 通过；管理停止、全局实验目录、分页、生命周期及断连测试均验证了旧实现 RED 和新实现 GREEN。最早实验 API fixture 缺 AuthType 的失败不计业务 RED，修正身份后的旧实现已确认因跨团队目录缺失而失败。
- 隔离 PostgreSQL 16 实测：跨连接 creator/stop 互斥、锁等待取消、事务回滚后 STOPPING 保留、重新启动等待锁后重查，3项均通过；测试容器已删除。不是以 SQLite 或 skipped PG 测试代替此证据。
- 本次变更涉及语句块的覆盖率为 272/310（87.7%，不等同于精确新增行覆盖率）；历史整个包覆盖率为 api 68.6%、repositories 71.3%、observability 85.1%、k8s 76.6%，没有宣称整库达到80%。
- 最终独立权限复审无阻塞项。启动/停止以已提交记录的数据库行锁协调；先持久化 STOPPING，失败保留配额并允许重试。GET 按ID和旧状态条件更新。启动已持久化后使用独立2分钟预算，浏览器断开不取消；异常另用5秒记录清理意图，补偿失败明确报错。
- 管理停止现网验收：上述临时工作区 RUNNING → DELETE管理接口202 → STOPPED → 资源全部回收 → 审计 success。没有停止真实用户训练，没有实际迁移 guofeng.su 团队或修改 Java 菜单角色。
- 实验目录现网接口200；最近100条可关联记录的56个任务均属于 local，因此现网样本只能验证接口可用和归属核对，不能冒充跨团队样本验收。跨团队排序、数据库归属和角色边界由包含105个团队的回归测试验证。
- 发布记录、Helm备份及验收结果位于构建机 `/root/raytrain-permissions-20260914/`；测试日志为 `/tmp/rtp-permissions-3d8e497-full.log`、`/tmp/rtp-permissions-pg-{red,green}.log`、`/tmp/portal-admin-permissions-10fb586a-{lint,build}.log`。

## 尚未完成

1. 浏览器实际页面交互验收：自动化创建页签与状态读取均超时，未以静态构建或 HTTP 200 替代页面验收。需补查团队分配按钮、普通用户菜单、实验详情、编辑器入口和业务错误展示。
