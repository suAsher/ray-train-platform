# 调试目录 FSX 兼容性修复与验收

## 问题与范围

2026-09-28 用 guofeng.su 的独立单卡 Base 调试环境复现：`/home/ray` 的 Git clone/commit/pull 正常，原 TOS `/workspace` 能 clone，但 commit/pull 在追加 `.git/logs/HEAD` 时返回 `Invalid argument`。原挂载的追加写与稀疏文件复制也存在语义限制。这不是统一的权限不足，也不证明 TOS 数据丢失或挂载掉线。

原 Base 训练镜像保持不变。调试镜像目录将 `job-87626bd437a0934ec35d72ca`（RayTrain Base 调试环境）设为全局默认；原调试镜像仍可选，团队专属默认优先，运行中工作区不替换。

## 实现选择

- 保留个人存储 home 和现有 TOS `workspace/` 前缀；不新增云盘，不复制或迁移用户文件。
- `WORKSPACE_FSX_COMPATIBLE_ENABLED` / Helm `dataSpaces.workspaceCompatibleEnabled` 默认为 false，依赖 Data Spaces。
- 启用后只在新工作区创建时为该个人前缀建立独立静态 FSX PV/PVC，增加 `compatible_mode=true`，保留 `no_writeback_cache` 与现有 UID/GID/权限选项。
- PV 使用 `Retain`，PVC 使用空 StorageClass 和显式预绑定，不触发动态云盘分配。身份由 namespace、存储位置和 `fsx-compat-v1` profile 决定，不跟随短期工作区 ID。
- `/workspace` 和 `/mnt/storage/me/workspace` 指向同一兼容挂载。其他个人目录、数据集、训练输出和运行中工作区继续原挂载。
- 挂载尚未就绪或发现资源属性不符时拒绝创建计算资源，不退回有问题的旧挂载。

## 实际验证

以下为独立 canary 的结果，不能代替平台候选发布后验收：

| 检查 | 结果 |
| --- | --- |
| FSX 版本 | 集群现有 1.3.6.1296，无组件升级 |
| Git clone/pull/commit/checkout/gc/fsck | 兼容模式通过；旧模式的 commit/pull 复现 errno 22 |
| append、随机写、truncate、fsync | 通过 |
| 普通 cp 复制稀疏文件 | 通过 |
| rename、chmod、symlink、独占创建 | 通过 |
| 从节点 172.28.1.81 卸载，再到 172.28.1.229 挂载 | 文件内容、Git HEAD 与 fsck 校验通过 |
| 原挂载读取兼容挂载写入的内容 | 文件及 Git fsck 校验通过 |
| Portal 从测试仓库创建代码快照 | `snapshot-cc369a369b39465500125781`，内容及 Git fsck 通过 |
| 本人 Base 工作区 | GPU、Jupyter、VS Code、内部源安装与环境 capture 通过；已停止回收 |
| 私有 Git 远端认证 | 未使用用户私密凭据验证；TCP 可达不等于认证成功 |

构建机受限证据（无密钥）：

- `/root/raytrain-base-debug-permissions-20260928.jsonl`
- `/root/raytrain-base-debug-network-dependencies-20260928.jsonl`
- `/root/raytrain-base-debug-git-fastforward-20260928.jsonl`
- `/root/rtp-fsx-compatible-tests-20260928.jsonl`
- `/root/rtp-workspace-fsx-red-20260928.log`
- `/root/rtp-workspace-fsx-wait-red-20260928.log`
- `/root/rtp-workspace-fsx-full-20260928.log`
- `/root/rtp-workspace-fsx-verification-20260928/coverage-functions.txt`
- `/root/rtp-workspace-fsx-adapter-integration-20260928.log`
- `/root/rtp-fsx-compatible-final-20260928.json`

## 能力边界与发布

兼容模式不是完整本地文件系统的替代。不要据此承诺硬链接、多客户端同时修改同一 Git 仓库、数据库或大量小文件编译的语义和性能。大规模编译可在临时本地目录进行，需保留的源码和结果保存到个人目录；远端 Git 的网络与凭据问题单独诊断。

官方资料：[FSX 兼容优先模式](https://www.volcengine.com/docs/6349/2363634?lang=zh)、[FSX 客户端与版本说明](https://www.volcengine.com/docs/6349/1404012?lang=en)。

构建机已通过完整 Go 回归、go vet、格式检查及真实隔离 PostgreSQL 验证；新增生产函数覆盖率为 86.4%–100%，全项目既有覆盖率为 74.2%。审阅提出的真实 Kubernetes API 默认化缺项已补齐：服务端 dry-run、实际静态 PV/PVC 创建/Bound/二次 Ensure 复用全部通过，测试对象已按 UID 清理。

本记录是发布前证据。下一步为授权范围内同步及最小后端发布、真实新建工作区/重建/代码快照验收与线上使用说明更新。无 schema 迁移，不需要重建训练镜像或 Portal。

回滚关闭新开关仅影响后续创建；已运行的兼容挂载不会自动变更。不要自动重启用户工作区，或删除静态 PV/PVC 所指向的数据。
