# 数据同步执行器隔离与回收

用户要求：同步执行器不要持续堆积在平台服务 namespace，补齐历史执行器回收，保持现有提交、浏览、增量和续传方式。

## 实施范围

1. 增加独立执行 namespace 配置，兼容已有单 namespace 安装；生产目标 `ray-train-sync`。Worker ServiceAccount、检查点 PVC 和最小 Role 在执行 namespace，后端仍在 `ray-train-platform`。跨 namespace 回调使用服务完整域名。
2. 新建 Job 不提前设置 TTL。控制器读取已持久化的执行证据，确认对应 UID 的 Pod 已终止、传输请求已排空、回执已处理，才给 Job 设置 TTL。正常成功、暂停、取消保留 1 小时，真实失败保留 24 小时，均可配置。
3. 同步请求 Secret 经身份和 UID 检查后绑定 Job ownerReference，随 Job 回收。数据库历史、文件结果、PVC 检查点及 baseline 不随 Job 删除。
4. 旧 namespace 仅保留显式允许的回收权限。忽略其他 Job、未知归属、缺失证据、活动执行器和仍需回执恢复的对象。回收失败幂等重试，不改业务状态。
5. 正常暂停/取消只有在写入持久回执并完成停止协议后返回成功退出码；真实异常仍非零，避免把正常控制操作显示为 Pod Error。

## 切换边界

不对活动写入进程做 namespace 热迁移。发布前核对活动 run、暂停 run、预检、锁和真实 Pod；在停止同步准入的窗口内复制旧检查点卷到新 namespace 的独立卷，并逐文件校验。保留旧卷及受限配置备份。只有无活动写入且检查点校验通过，才启用新执行 namespace。

检查点复制仅限平台同步功能工作卷，不迁移用户 TOS 数据。旧 namespace 中已结束的 Job 由相同证据门禁回收。数据库 schema 不变，旧私有快照仍可读取。

## 验证

- 先执行失败回归：无 TTL、namespace 固定、受控停止退出码。
- 构建机格式检查、go vet、完整 Go 回归、真实 PostgreSQL 的 attempt 证据读取与幂等回收测试；Worker 回归和非 root 只读容器验证。
- Helm 单 namespace/独立 namespace 合同测试、最小权限及完整 server-side dry-run 差异审阅。
- guofeng.su 个人空间新临时前缀验证实际复制、增量、暂停续传、取消、独立 namespace、清理后的页面历史与检查点可用性。
- 只将本次已验证暂停并自动取得 3600 秒 TTL 的专用测试 Job 缩短到 1 秒，验证真实 TTL 回收；全局保留期始终为 1 小时/24 小时，不缩短其他任务保留期。
- 发布前后核对存量训练 RayJob/RayCluster/Pod UID、节点、重启和状态，清理仅限本次验收资源。

当前状态：已完成实现、完整回归、真实 PostgreSQL 验证与发布（业务源码 f3b367e，Helm 269）。新 namespace 为 ray-train-sync，97 个检查点文件逐项校验后迁移，旧卷保留。guofeng.su 已验证 IDC/TOS 复制、增量、实际 TTL 删除后分片续传、取消正常退出与页面历史保留。本次 7 个测试对象及全部辅助 Pod 已清理。完整证据、版本和保留边界见 docs/STORAGE_SYNC_RUNBOOK.md 的“执行隔离与回收上线”。
