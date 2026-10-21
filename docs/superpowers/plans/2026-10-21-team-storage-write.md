# 团队目录容器写权限实施计划

> 按已确认的范围在隔离候选中执行；测试与编译仅在既有构建环境进行。

**Goal:** 当前有效团队的所有成员，新建训练和调试容器可读写 `/mnt/storage/team`。

**Architecture:** 保留现有只读 shared binding/PV/PVC，仅通过已就绪、可写的内部 tenant-root PVC 暴露团队子目录。新任务保存可写团队根，旧任务保存的只读快照继续合法并按原权限渲染。公共、IDC、训练输入别名及网页管理写权限不变。

**Tech Stack:** Go、Kubernetes PVC/subPath、FSX、PostgreSQL、独立 Portal。

## 约束

- 不重启或修改既有 RayJob、RayCluster、Pod，不修改用户文件或配额。
- 不改变 `IsWritableDataSpace`、`CanManageDataSpace` 或管理员数据同步的权限。
- 不迁移既有数据库 binding，不修改或重建 live PV/PVC。
- 新建训练遇到团队 binding 存在但可写 tenant-root 缺失、只读或不属于本团队时拒绝解析，不能将旧只读 adapter 假装为可写。
- 本次不实现同步申请、审批、版本发布或 Wellspiking 存储传输。

## Task 1：先写行为测试并在隔离构建目录观察失败

- [ ] 新增 `backend/domain/team_storage_write_test.go`：可写和历史只读团队快照均合法；public/IDC 可写非法；team 元数据可写而普通网页管理权限仍拒绝。
- [ ] 新增 `backend/api/team_storage_write_test.go`：三角色均解析到当前团队可写 subPath，排除其他团队 binding，旧 binding 不被修改；tenant-root 缺失/只读/异团队均拒绝。
- [ ] 新增 `backend/k8s/team_storage_write_test.go`：训练与调试 head/worker 尊重 team 快照权限；同 PVC 只 stage 一次，public 和 `/mnt/data/input` 仍只读。
- [ ] 将旧“拒绝可写共享根”的测试收窄为“拒绝可写 public”，保留旧只读快照兼容断言。元数据测试区分挂载和网页写权限。
- [ ] 候选测试提交通过 Git bundle 进入隔离 detached worktree，执行 `go test -count=1 ./domain ./api ./k8s -run 'TestTeamStorage|TestResolvedDataSpaceRootsRejectsWritablePublicRoot|TestDataSpacesListReportsPrincipalWriteCapabilityWithoutChangingMountSemantics'`；确认缺少新行为造成失败。

## Task 2：实现最小合同变更

- [ ] `backend/domain/resolved_data_roots.go`：团队允许历史 RO 和新 RW，其余根权限约束不放宽。
- [ ] `backend/domain/data_space.go`：团队逻辑目录说明和 `ReadOnly` 表示新容器可写；网页管理授权函数不变。
- [ ] `backend/api/workspaces.go`、`backend/api/submission_service.go`：新 team 根为 RW；确保使用本团队可写 tenant-root，拒绝不完整准备状态。
- [ ] `backend/k8s/data_mounts.go`：team 允许两种快照，挂载读取 `root.ReadOnly`；public/IDC 仍由校验强制只读。同 PVC 原 source RO 时，后续合法 RW alias 应将 source 变为 RW，但保留所有各自 mount 标志。
- [ ] 重新执行定向测试，修复有效回归；不删除仍适用的越权和历史兼容断言。

## Task 3：完整验证、审阅和交付

- [ ] 在构建环境 gofmt 本次 Go 文件，核对仅格式变化后回传；生成最终候选提交。
- [ ] 最终精确候选完成格式检查、`go vet ./...`、`go build ./...` 和隔离真实 PostgreSQL 下 `go test -p=1 -json -count=1 -timeout=20m ./...`。
- [ ] 独立代码/权限审阅：旧作业和工作区不被重渲染；团队隔离、public/IDC RO、普通网页上传拒绝、管理员同步限制都保留。
- [ ] 新增专题使用说明记录版本与生效边界；原目录未提交文档不覆盖或提交。
- [ ] 核对远端更新和有效发布授权后，才同步正式源码、只构建 backend、审阅最小发布差异。未能同步的端明确记录，不宣称四端一致。
- [ ] 生产验证只在本次独立测试子目录进行；先验证新对象 team RW/public RO 的挂载，再做文件创建、读回和清理。已存在只读工作区不自动停止或重建。
