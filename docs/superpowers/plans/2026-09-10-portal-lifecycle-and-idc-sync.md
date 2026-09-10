# Portal 生命周期与 IDC 增量同步实施计划

> 本计划替代 2026-09-09 计划中尚未落地的产品层描述。后端在独立分支开发，编译、测试、镜像构建只在构建机执行；Portal 在独立 GitLab 仓库开发并由 CI 发布。

## 范围与完成定义

本期必须形成一个可复现闭环：OAuth 身份首次访问自动成为普通平台成员，管理员只维护成员角色与团队；IDC 只读 NFS 通过可恢复的增量同步生成不可变 inventory；成功 SyncRun 可发布不可变 DatasetVersion；训练任务保存完整来源；Portal 能查看和操作这些真实后端能力。

评估审批、模型 Registry/外部模型仓发版不在没有后端实体时伪造 UI，本期只保留可追溯扩展点。

## 批次一：OAuth JIT 成员建档

1. 先补测试：未知 OAuth 用户、已有用户、已禁用用户、默认租户缺失、并发首次访问。
2. 增加外部身份成员标识和退役身份 tombstone，确保本地密码入口不能使用 JIT 记录，退役成员不会被自动复活。
3. OAuth middleware 在配置允许时原子 Resolve-or-Provision，默认租户为 `local`、默认角色固定为 `Engineer`。
4. 用户管理接口继续承担团队、角色、配额与禁用，不提供 OAuth 模式下的平台密码创建。
5. 返回明确认证能力：登录方式与成员管理能力分开，供 Portal 正确显示管理员功能。

验收：新身份首次请求成功且仅为 Engineer；SuperAdmin 只能显式授予；禁用/退役身份仍拒绝；并发请求仅创建一条成员记录。

## 批次二：IDC SyncRun 完整状态机

1. 先补 repository/manager/job renderer/worker 测试。
2. 连接器支持启用状态、可选同步间隔、上次/下次运行时间；同一连接器只有一个活动 Run。
3. worker 读取上一成功 inventory，以路径、大小、mtime 复用摘要，只对新增或变化对象计算 SHA-256 并固化；删除只记 tombstone。
4. added/changed/reused/tombstoned、文件数、字节数和 canonical inventory digest 持久化并通过 API 返回。
5. reconciler 将 Kubernetes Job 失败、超时或丢失收敛到终态；失败可重试但不覆盖旧 Run。
6. 工作检查点使用平台配置的持久卷；未配置时禁止启用生产全量同步。

验收：首次小样本同步成功；第二次无变化全部复用；修改/新增/删除分别产生正确统计；模拟 Job 失败不再永久 RUNNING；源 NFS 始终只读且无删除命令。

## 批次三：不可变数据版本与训练溯源

1. 先补 Dataset API、publisher manager、TOS storage、SubmissionService 测试。
2. 发布请求可选择一个成功 SyncRun；其他状态和跨数据源 Run 一律拒绝。
3. publisher 从 inventory 指向的不可变 raw objects 读取，不从 mutable mirror 或源 NFS 回退。
4. DatasetVersion 保存 `sourceSyncRunID`、`sourceInventoryDigest`，响应中完整返回。
5. TrainingJob 创建时复制来源字段以及 dataset/version/manifest/site filter/data mode/cache policy。
6. 历史版本允许来源为空，新 SyncRun 版本必须来源完整。

验收：同一 inventory 重复发布得到一致来源；修改 mutable mirror 不影响已发布版本；训练任务 API 能完整追溯至 SyncRun。

## 批次四：Portal UI

1. 在 Portal 的 RayTrain 管理区完善“数据接入”：连接器、立即同步、计划、进度、变更统计、历史、失败原因与重试。
2. 成功 Run 提供“发布数据集版本”，数据集治理展示 SyncRun/inventory/Parquet manifest。
3. 任务详情展示 DatasetVersion、筛选、NVMe、Ray Data、Ray Train、MLflow/artifacts 的证据链；未产生数据时明确责任边界。
4. OAuth 模式下隐藏本地密码创建，仅显示成员角色/团队/状态管理；现有 yihan.she 等成员的数据归属继续按稳定 username/storage key 与 tenant 解析。
5. 修复 Portal 路由和大小写约束，在 Linux `git archive` 干净目录执行 lint、EP 检查、类型检查与构建。

验收：SuperAdmin 可完成数据接入闭环；普通 Engineer 只读自身和授权团队数据；旧 API/CLI 路由不回归；Portal CI 通过。

## 批次五：部署与上线验证

1. 先部署数据库迁移和兼容后端；不改动正在运行的 RayJob/训练 Pod。
2. 灰度开启 OAuth JIT，验证现有 SuperAdmin、普通成员、禁用成员和新成员。
3. 用有界 fixture 目录完成两轮 IDC 同步、版本发布和无卡数据读取验证。
4. fixture 通过后才允许管理员显式启用全量 `labeled` 计划；不自动删除源或 TOS 历史对象。
5. Portal 推送 `dev` 触发 CI，确认 dev ingress 和后端 CORS/认证头；旧入口保留 API/CLI。
6. 核对后端本地、GitHub、内部 GitLab、构建机 SHA 一致，并更新 release skill 的分仓开发/构建/部署流程。

## 每批验证门槛

- 测试必须先失败再实现通过；构建机全量 Go/Python 测试通过。
- API 边界输入校验、租户隔离、SuperAdmin 授权与错误信息通过安全复核。
- Portal lint/check/type/build 全通过。
- 镜像使用 commit 唯一 tag 与 digest，Helm server dry-run 后再原子升级。
- 任一门槛失败即停止下一阶段，不以手工数据库修补替代产品逻辑。
