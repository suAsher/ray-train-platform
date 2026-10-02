# 管理员数据同步

状态（2026-10-02）：已发布并启用，最新 Helm revision 269。同步执行器已切换到 `ray-train-sync`，补齐有停止证据的 Job/Pod/请求 Secret 自动回收；guofeng.su 个人空间已验证回收后分片续传、增量和取消。首次发布的 revision 267 与 14 个测试对象清理记录保留在下方历史章节；最新验收的 7 个对象也已清理，未完成分片为 0。

## 使用范围

新 Portal「平台管理 → 数据同步」面向当前有效的 RayTrain 超级管理员。入口和后端 API 都校验权限，普通用户、团队管理员及 PAT 不能使用该管理接口。

支持 IDC → TOS、TOS → TOS。源和目标必须从平台已登记的存储空间中选择；IDC 只作为只读源。目标可选公共空间、指定团队空间或管理员本人空间。个人目录使用平台的稳定 storage home，不随团队迁移，也不以用户名拼接底层 TOS 路径。个人空间计划和记录仅本人可见；公共、团队同步由超级管理员管理。

该功能与现有 IDC 数据集发布流程独立，不替换训练挂载、训练提交或数据集版本管理。

## 创建与执行

1. 新建同步计划，选择源空间、目标空间和相对路径。团队空间必须明确选团队。可配置多个映射。
2. 选择「复制目录内容」或「保留源目录名」，核对预览中的最终用户路径。例如四个 `nusc` 可以分别映射到 `58/<任务ID>/nusc/`，避免同名目录合并。
3. 选择全量或增量、校验方式、冲突处理和可选的定时规则，保存计划。保存不会立即复制；定时计划需单独启用。
4. 点击「预检并同步」，等待只读扫描完成，核对文件数、待复制量、目标路径和冲突，再确认开始。
5. 在运行详情查看扫描、传输、校验、已完成文件和分目录进度。页面可关闭，任务由服务端继续执行。

全量会重新复制源文件，但保留目标端额外文件；增量复用经确认未变化的内容。两种模式都不是删除型镜像同步，取消也不会删除已完成的目标文件。

预览绑定用户、计划版本及源/目标快照。编辑计划、变更根配置或目录内容可能使预览失效，需要重新预检。开始后还会只读复核，避免按旧预览覆盖新数据。

## 进度与控制

| 状态 | 含义 |
| --- | --- |
| 排队 | 等待执行容量；尚未开始传输 |
| 扫描 / 规划 | 正在统计源与目标，总量未确定时不显示虚假的完成百分比 |
| 传输 / 校验 | 按清单复制并验证；逻辑复制量与实际网络流量分别记录 |
| 暂停中 / 取消中 | 正在停止请求并等待执行器退出，不能视为已经停止 |
| 已暂停 | 已确认停止，可在检查点有效期内续传 |
| 成功 | 已收到完整校验回执，并确认执行器退出 |
| 失败 | 查看原因和文件结果；满足恢复条件后才允许重试 |

暂停保留本次检查点和分片；续传重新核验源、目标及远端分片。取消只中止本次持有的未完成分片，不删除已有对象或其他任务的分片。没有停止证据、请求结果不明或旧 Worker 状态未知时，系统保留锁，不自动启动第二个写入进程。

定时规则支持手动、间隔、每日和每周，默认时区 `Asia/Shanghai`。上一次仍活动或路径被占用时，不叠加启动同一路径的写任务；管理页保留跳过记录。用户失去管理权限后不再准入其定时计划。

## 运维配置

`storageSync.enabled` 默认关闭。启用前须准备以下独立配置，并按 release skill 备份、审阅完整 Helm 差异后发布：

- 固定摘要的 `ray-storage-sync` 镜像；构建目标为 `storage-sync`，后端变更另构建 `backend`。
- 明确选择已核验 CPU 节点的 `nodeSelector`，同时核实该节点可读已登记 NFS 源、可达 TOS 和后端回调。不能依靠可能失真的 GPU 标签判断节点类型。
- 持久化检查点 PVC。`WaitForFirstConsumer` 的新 PVC 可以由只读预检绑定；写入阶段要求 PVC 已绑定。RWO 卷与节点选择必须一致。
- 平台既有 TOS 桶、区域、Endpoint，以及管理员配置的 Secret 引用。只读 Worker 不挂写入凭据；只有传输 Worker 挂载该 Secret。
- 文件、分片、带宽和活动任务上限。计划带宽为 0 表示继承平台上限；非零值至少为 100 KiB/s。当前 Worker 按文件和分片串行执行，并发参数只作为上限，不代表已实现并行吞吐。

Worker 使用独立 ServiceAccount，不挂 Kubernetes API token；只读 NFS、只读根文件系统、无 GPU 请求。控制器通过独立 Lease 协调，PostgreSQL 持久化计划、执行状态和路径锁。

### 执行 namespace 与回收

执行 namespace 可用 `storageSync.namespace` 指定，空值兼容平台 namespace。独立部署时，Worker ServiceAccount、Role、请求 Secret 和检查点 PVC 位于执行 namespace；后端仍在平台 namespace，回调使用 `http://ray-train-backend.<平台namespace>.svc.cluster.local:8080`。已有安装若显式配置了短回调地址，迁移时必须同步更新。

控制器只扫描执行 namespace 和显式配置的 `storageSync.gcNamespaces`。它先核对 Job/请求 Secret 的身份、实际所有容器终止状态和数据库中对应 attempt 的停止证据；传输还必须有请求排空回执。满足条件后，才为 Job 设置 Kubernetes TTL，并将本次不可变请求 Secret 绑定到该 Job。未知归属、缺失证据或仍活动的执行器不会自动回收。

正常成功、暂停、取消默认保留 3600 秒，真实失败默认 86400 秒，分别由 `gcSucceededTTLSeconds`、`gcFailedTTLSeconds` 配置。TTL 从 Kubernetes Job 结束时间计算；旧 Job 超过保留期后，一旦核验并设置 TTL，可能立即被回收。数据库计划、运行历史、文件结果、检查点和增量基线不随 Job 删除。Pod 日志随回收消失，需要长期保留原始日志时，应在保留期内接入日志归档。

正常暂停、取消在持久回执保存且停止回调确认后以退出码 0 结束，因此 Pod 可显示 `Completed`，业务页面仍显示「已暂停」或「已取消」。不能只凭 Pod 的 Completed 判定复制成功；停止协议或真实传输失败仍返回非零。

独立 namespace 用于区分平台服务与短期执行资源，不改变可信后端已有的集群级权限，也不是新的凭据或网络隔离边界。检查点文件的磁盘生命周期与 Job TTL 是两件事，本次不删除 PVC 内容。

迁移现有安装时，先关闭同步准入并等所有后端副本生效，确认没有活动或暂停运行、预检、路径锁以及任何仍挂载检查点卷的写入 Pod，再复制检查点到新 namespace 的独立 PVC并逐文件校验。保留旧卷和受限备份，核对 Secret、RBAC、回调、镜像后才重新启用。新卷发生写入后不能直接 Helm 回滚到旧卷；须再次停止准入并核对、迁移新检查点，防止使用过期状态续传。

数据库迁移为 `0058_storage_sync`。发布前必须有可恢复数据库备份，完整 Go 回归及真实 PostgreSQL 的全新、重复、升级验证。Helm 回滚不会撤销已经提交的数据库迁移。

## 上线顺序与回退

先备份数据库与现有 Helm values，审阅 `--reuse-values` 的 server-side dry-run 全部差异，再发布后端和独立 Worker。确认所有后端副本均更新且健康后，再发布 Portal 并创建首个验收计划，避免请求落到不具备新接口的旧副本。

0058 仅新增本功能的表、索引、函数和触发器，不改既有业务表；旧版 `50ea611` 迁移器只核对其内置的 1–57 版本，源码核对确认额外的 58 不会阻止旧迁移器启动。该结论不表示可以在传输中直接降级。

有活动同步任务时，先停止其后续定时触发，再通过新版控制面暂停或取消，并等待经认证的请求排空回执及对应 Job/Pod 终止证据。之后才可关闭功能或回退后端。单纯停用定时计划不会停止已开始的运行。保留 0058 表、路径锁、PVC 和执行证据，不做数据库逆向删除或强制解锁。

## 当前实现限制

- TOS SDK 固定为 `tos==2.9.3`。新对象支持条件分片完成；已有对象大于 5 GiB 时，当前 SDK 无法提供本功能要求的条件分片覆盖保证，预检会拒绝，不能静默改为无条件覆盖。
- IDC 扫描目前保守读取并计算文件校验值，增量零写入不代表零源端读取。内容校验模式需要读取对象内容，会增加耗时和流量。
- 清单当前保存在内存及检查点 JSON 中。默认 Worker 内存上限 2 GiB；尚未以原 135,151 文件、约 270 GB 数据集做本功能规模验收，不能据小样本声称该规模已经验证。
- 写入结果不明时会保留锁并等待运维核对；静态凭据模式不会因超时自动转移写入权。不要通过删除 Pod、修改数据库状态或重复提交来绕过该限制。只读预检的回执无法恢复时可结束并释放锁；传输阶段没有排空回执时继续保留锁。
- 检查点有效期控制是否允许恢复，不代表检查点文件会自动从磁盘删除。Job/Pod/请求 Secret 的自动回收遵循上面的停止证据和 TTL 门禁；PVC 检查点和数据库审计记录继续保留。

## 2026-10-02 执行隔离与回收上线

业务源码为 `f3b367e2344028aa443fddd81279dd23293c2e77`。构建机格式、`go vet`、完整 Go 回归和真实 PostgreSQL 验证通过；同步核心包覆盖率 81.1%，新增业务回收文件 86.7%、Kubernetes 回收文件 85.2%。最终非 root、只读 Worker 镜像内 96 项测试通过；实际 Helm 渲染合同通过；检查点迁移脚本的 27 项隔离测试通过。完整回归曾发现测试 builder 缺少既有 CLI 用例需要的 zsh，补齐测试镜像依赖后完整回归通过，没有跳过该用例。

| 组件 | 发布结果 |
| --- | --- |
| 后端 | `ray-train-backend@sha256:d706342f7465388314a8278f65ffd9f1eaaaced4494ea7fb0c929a2c8726e220`，2/2 Ready |
| Worker | `ray-storage-sync@sha256:0225628d545d7787cd16b22c0b1e3286a66779ff23931f0a3699c2ea9abc2cf3` |
| Helm | 268 临时关闭同步准入；269 启用新 namespace、镜像、跨 namespace 回调和回收配置 |
| 回收 | 正常成功、暂停、取消 3600 秒；真实失败 86400 秒；旧 namespace 为显式 GC allowlist |
| 新检查点 PVC | `ray-train-sync/ray-storage-sync-work`，20Gi `ebs-ssd` RWO；UID `ab1cb9dc-f9b0-4c72-91ce-7a7c6c6ac351` |
| 保留旧 PVC | `ray-train-platform/ray-storage-sync-work`；UID `a7d199cb-3389-40a5-b67d-2394fdc9df90` |

新 Portal 和 CLI 没有变更。后端、Worker 镜像前缀仍为 `harbor.wellspiking.ai/guofeng.su/`。本次不新增数据库迁移，schema 仍为 58。

迁移前确认所有后端副本禁用同步，活动/暂停运行、活动预检、路径锁均为 0，所有原执行 Pod 已真正终止。检查点共 97 文件、152 条文件/目录记录、111,500 字节；源端、受限备份和新卷逐项 SHA256 一致，清单摘要 `6823cf33afa3eae2613d97c490ae1c8b5a900a7278b9ed330955720fecd2f44e`。旧卷保留，两个迁移辅助 Pod 已按 UID 清理。完整 Helm values 导出曾被自动审批拒绝，因此采用最小脱敏快照和内存中的完整 manifest 对比，原配置仍在集群的 Helm revision 中；未将完整 values 或秘密写入构建机证据目录。

### guofeng.su 实际验收

测试用户路径为 `/mnt/storage/me/files/storage-sync-lifecycle-194a755053a8404382a26b294f13e3d2/`，所有写入仅在这个新建目录。

| 验收 | 结果 |
| --- | --- |
| 新 namespace 与回调 | BROWSE、PREVIEW、TRANSFER 全部在 `ray-train-sync`；仍使用 CPU 节点 `172.28.2.65`、无 GPU、非 root、只读根文件系统、无 ServiceAccount token |
| IDC → TOS / 增量 | `ssr-f557b8a6-59c8-4d05-8fb5-d22c90c477cc` 复制并验证 2 字节文件；`ssr-452bccf8-a047-4f9f-be27-dec098822636` 同计划重跑传输 0 字节、复用并验证 1 文件 |
| 真实回收后续传 | `ssr-583da214-a6c6-4401-a962-7f404c65368a` 在 attempt 2 暂停，远端第一片 64 MiB 与 PVC 检查点一致，排空/停止证据入库，Pod 正常退出。先观察控制器自动设置 TTL=3600 和 Secret ownerRef，再仅将该测试 Job 的 TTL 改成 1 秒；实际 Job、Pod、Secret 均被 Kubernetes 回收。随后同一运行 attempt 3 续传成功 |
| 内容回读 | 65 MiB 源/目标 SHA256 均为 `ca5239937bca47ea8b6780e1fb671e45731772a9e915ad16371cde002d2c893e`；小文件也逐一匹配 |
| 取消正常结束 | `ssr-f23173bb-81a4-48e6-9373-a910516a2ae0` 在真实分片传输中取消，API/数据库为 CANCELLED，Pod Succeeded/退出码 0，自动 TTL=3600；未完成分片为 0，已完成小文件和目标额外文件保留 |
| 页面与审计 | 新 Portal 实际打开回收后的运行详情，显示已完成、65 MiB / 2 文件、执行次数 3及已验证文件结果；数据库 attempt 1/2/3 的回执和停止证据均保留 |

2026-10-02 06:07 UTC 复核：旧 namespace 原有 36 个 Job 已自动减少到 1 个真实失败 Job，TTL=86400；它将按原失败时间到期回收，没有为了清空列表缩短失败证据保留期。新 namespace 剩余 14 个本次终态执行 Job 均为 TTL=3600，按结束时间自动回收。每个阶段依然使用短期 Job，平台服务与执行资源已分开：

```bash
kubectl -n ray-train-sync get jobs,pods
kubectl -n ray-train-platform get pods
```

3 个验收计划均未启用定时，4 个运行最终为 3 SUCCEEDED、1 CANCELLED；活动运行、预检和路径锁为 0。7 个自建对象共 136,314,913 字节已按精确前缀/白名单清理，独立客户端复核对象和未完成分片均为 0。临时检查 Pod 已按 UID 删除。保留计划、运行、检查点、增量基线和旧卷；不把 Job TTL 当成检查点磁盘自动清理。

发布后的 05:50 UTC 独立审计未发现存量训练 UID、状态或重启变化；最终 06:07 UTC 审计发现 `tenant-local/job-3d5d0340b13f1009e809d942` 已由 RUNNING 变为 FAILED，因此不能声称整个验收期间所有训练状态不变。只读追查确认该任务在 05:58:13 UTC 因 `ValueError: cls_score contains NaN!`、`ChildFailedError` 和训练退出码 1 达到脚本重试上限，RayJob reason 为 AppFailed。Submitter UID 和重启数未变，容器正常退出；RayCluster 在 06:08:15 UTC 由原控制器按失败生命周期清理。未发现同步 TTL/Secret 回收影响训练资源的证据；同步回收范围仅为明确配置的两个同步/平台 namespace。另观察到两条历史训练的 `managed_attempt_resources` retiring 状态约束告警，非本次同步回收路径，保留为独立待排查项。

证据在构建机 `/tmp/raytrain-storage-sync-lifecycle-20261002/`：`go-focused3.log`、`go-vet3.log`、`go-full4.log`、`worker-final-image.log`、`cutover-test/fixture-results.log`、`cutover-readiness.json`、`source-checkpoint-backup.tar`、`*-dryrun-sanitized.json`、`acceptance-paused-proof.json`、`acceptance-paused-parts.json`、`acceptance-ttl-deleted.json`、`acceptance-resumed-content.json`、`acceptance-cancel-proof.json`、`acceptance-cleanup.json`、`acceptance-inspector-cleaned.json`、`final-independent-audit.json`。训练失败归因在其子目录 `failure-job-3d5d0340b13f1009e809d942-20261002T060939Z/causal-summary.txt`。本机 API 汇总为 `/private/tmp/rtp-sync-lifecycle-live-api-20261002.json`。

## 2026-10-02 首次发布与线上验收（revision 267）

Harbor 恢复后，已完成固定摘要镜像推送与拉取、生产数据库备份及实际恢复验证、0058 迁移、后端与 Worker 发布，以及新 Portal `dev` 推送后的 CI/CD 部署。没有修改 Ceph 配置，也没有为验收停止用户训练。

### 版本与运行配置

| 组件 | 已核验版本 / 状态 |
| --- | --- |
| Helm | `ray-platform` / `ray-train-platform`，revision `267`，`deployed`；266 首次启用，267 仅更新 Worker 配置中的镜像摘要 |
| 后端 | 源码 `6148663fbe6757c7fc082b6eae5cf44b40d22be4`；`ray-train-backend@sha256:26dfcaa7df6dbac35892aaf36ba52ac4ec2f652e1c676ede1491725f18d7ca2c`；2/2 Ready、重启 0 |
| Worker | 源码 `211441689f679db3eb514957b681d7e6ac011821`；`ray-storage-sync@sha256:7c4846e25fac0ba9a68cbe72d97e661f98baaae41c93aa7f30887a5223416210` |
| 新 Portal | `dev` 提交 `2e8d9b46b488063382d61605ddabe560b82ba4d3`；镜像 `sha256:a2c23b48444f130369c8f46280bd98bad758d54f36e82c7b2432718f8f0fbf33`；实际 Deployment 1/1 Ready |
| 数据库 | schema `58`、58 条迁移记录 |
| 执行节点与检查点 | CPU 节点 `172.28.2.65`；`ray-storage-sync-work` PVC，20Gi、`ebs-ssd`、RWO，已 Bound |

镜像均在既有 Harbor，后端和 Worker 仓库前缀为 `harbor.wellspiking.ai/guofeng.su/`。源码提交、组件镜像摘要与 Helm revision 分别记录；Worker 修复没有重新构建后端镜像。Portal 私有 CI job 状态未直接读取，发布结果由候选 SHA 镜像、目标 Deployment 与 Pod `imageID` 一致及登录页面实际验收确认。

生产平台并发活动运行上限为 1，带宽上限 100 MiB/s，文件/分片并发配置上限分别为 4/2；当前实现仍串行处理文件和分片，不将配置上限当作实测并行能力。Worker 实际在 CPU 节点运行，无 GPU 请求；用户 65532、只读根文件系统、只读 NFS、禁用 ServiceAccount token 自动挂载。预检不挂 TOS 写入凭据，传输阶段使用既有 Secret 引用。

### 备份与发布影响

本次使用新备份 `/root/raytrain-release-backups/storage-sync-6148663-20261002T031122Z/raytrain.dump`，3,043,378 字节，SHA256 `c0709c0d1c81b283534e2703ce47089a55ec7c227ad2cfc70166f59c3528c3b9`。备份前后生产库均为 schema 57、57 条迁移、73 张表；在无网络、无暴露端口的临时 PostgreSQL 容器中实际恢复，`pg_dump`、`pg_restore` 均返回 0，恢复库元数据匹配，验证容器已删除。备份保留在构建机管理员受限目录；其内容不进仓库。

首次发布的完整脱敏 server-side dry-run 只包含后端镜像、22 个同步配置环境变量及新 ServiceAccount、Role、RoleBinding、20Gi PVC；未移除或变更训练资源。最终独立审计对比发布前基线中的 4 个 RayJob、4 个 RayCluster、9 个训练/调试 Pod：UID、状态、节点和容器重启数均无变化。后端两副本无 panic、fatal 或迁移错误，`/healthz` 返回 200。该对比证明本次发布未重建这些存量资源，不将资源总数相同代替逐项核对。

### 实际链路与修复

本次验收仅写入 guofeng.su 已有稳定个人空间的新 UUID 子目录：

```text
/mnt/storage/me/files/storage-sync-live-eda59440e95044bf9a62ffeb591a2855/
```

首次 IDC 实际运行发现 `SOURCE_CHANGED`：同一个只读 NFS 文件跨预检与传输 Pod 挂载时，只有本地设备号 `st_dev` 不同，inode、大小、纳秒时间戳与 SHA256 全部相同。修复将跨 Pod 快照和游标中的身份比较改为可移植元数据，同时保留单次打开文件描述符的本地设备校验、内容哈希及旧清单/检查点兼容；没有绕过源变化校验。修复前回归复现失败，修复后 Worker 91/91 测试通过，启用分支统计的覆盖率 89%，非 root、只读根目录镜像内同样 91/91 通过。

| 线上验收 | 实测结果 |
| --- | --- |
| IDC → TOS | CPU Pod 只读访问真实 NFS 的 `visibility.json`，复制 1 文件 / 2 字节，内容校验通过；修复后再次增量传输 0 文件 / 0 字节，复用并校验该文件 |
| TOS → TOS、多映射与增量 | 首次复制 2 文件 / 16 字节；相同计划版本零变化重跑传输 0 文件 / 0 字节；修改其中一个同大小 8 字节源文件后，仅复制该文件，另一文件复用；目标额外 JSON 保留 |
| 真实分片暂停 / 续传 | 65 MiB 文件采用生产 64 MiB 分片配置；暂停后远端第一片 64 MiB 与持久化检查点一致，完成对象尚不存在；旧 Pod 退出后新 attempt / Pod 继续，最终 65 MiB 源与目标 SHA256 一致，原未完成上传消失 |
| 取消 | 分别核验“请求到达时已复制完”的取消与正在分片的取消；后者保留已完成的 8 字节文件，未完成大文件不存在，未完成分片数为 0；不删除已经完成的数据 |
| 自然定时触发 | `Asia/Shanghai` 的 DAILY 规则按真实时钟产生一条 `SCHEDULED` 运行并成功；随后停用计划，`nextRunAt=null` |
| 新 Portal 实际操作 | 登录 guofeng.su，经“平台管理 → 数据同步”完成新建、源目录浏览、保存、预检、开始、完成进度和文件校验结果查看；未用模拟 API 代替该操作链 |
| 既有 Portal 页面 | 任务列表、任务详情、使用说明、实验中心及 MLflow 详情回归通过；不存在的任务详情显示后端具体错误 `training job was not found`，未退化为无信息的通用错误 |
| 认证边界 | 未认证管理接口返回 401 `AUTH_REQUIRED`；真实 PAT 身份已确认后返回 403 `INTERACTIVE_LOGIN_REQUIRED`；当前有效超级管理员交互会话访问成功 |
| 停止证据 | 按运行与 attempt 核对 `requestsDrained`、`stopVerified` 和实际 Job/Pod UID 终止状态，未将页面百分比或暂停/取消请求响应当作停止完成 |

暂停续传、取消和内容回读通过独立 TOS 查询与 Worker/数据库证据核验。最终共有 11 条运行记录：8 条成功、2 条按验收要求取消、1 条修复前的 IDC 失败。首次失败记录保留，修复后两次成功运行分别为 `ssr-d95d2c8e-23c9-44da-a49a-392334d8148d`、`ssr-b25bbb0e-9e48-4f43-82d1-c81ea78b02f0`；没有删除失败记录来形成“全部首次成功”的假象。

验收范围仍有边界：原 135,151 文件、约 270 GB 数据集尚未通过平台功能做规模验证；10 月 1 日的全量模式与分页验证属于真实 TOS SDK 验收，不能自动等同于该规模的线上验证。此次未更改 workspace runtime，guofeng.su 的既有调试环境已停止，因此只查看调试环境页面，未为此新建工作区验证 Jupyter/VS Code 一次性票据。

### 清理状态与证据索引

7 个验收计划均已停用且 `nextRunAt=null`，保留为审计记录。所有活动运行、预检、目录浏览和路径锁的最终清理前检查为 0；测试文件仅位于上述 UUID 前缀。本次只读诊断 Pod 和数据库恢复验证容器均已删除。

**测试数据已清理：**首次删除申请被自动审批拒绝且未执行；用户随后明确批准仅删除此 UUID 前缀的 14 个验收对象（204,472,412 字节，约 195 MiB）。2026-10-02 04:15 UTC 执行前重新核对对象数量、桶版本状态、无活动运行和路径锁，清理返回 0。04:16:28 UTC 使用独立新客户端分页复核：对象数 0、未完成分片数 0；7 个禁用计划和 11 条运行记录保留，活动运行、启用计划、路径锁均为 0。没有删除原有用户数据或数据库审计记录。

构建机 `/tmp/raytrain-storage-sync-evidence-20261002/` 保留：

- `backup-actual-restore-summary.json`、`release-normalized.sanitized.diff`、`release-dryrun-summary.json`：备份实际恢复与首次发布差异。
- `idc-device-red.log`、`idc-device-green3.log`、`idc-device-green3-coverage.log`、`idc-device-green3-nonroot.log`：跨挂载修复的先失败、后通过测试和覆盖率。
- `engine-idc-diagnostic-result.json`、`engine-fixed-idc-diagnostic-result.json`、各 `engine-ssr-*-observations.jsonl`：首次 IDC 故障实证、修复后证据及运行/Pod 生命周期。
- `engine-paused-remote-parts.json`、`engine-resumed-content-verification.json`、`engine-cancel-abort-content.json`、`engine-cancel-after-complete-content.json`、`engine-scheduled-plan.json`：实际控制、内容回读与定时结果。
- `release-auth-boundary.json`、`release267-final-independent-audit.json`：认证边界、最终镜像/schema/健康及存量资源逐项比对。
- `engine-live-acceptance-summary.json`：最终 11 条运行的状态、停止证据与 Worker 安全配置聚合，包含修复前失败及修复后成功记录。
- `engine-owned-prefix-cleanup.log`、`engine-cleanup-independent-verification.json`、`engine-cleanup-approval-status.json`：严格前缀清理、独立分页复核，以及首次拒绝、补充授权和完成记录。

本机 `/private/tmp/raytrain-storage-sync-live-api-20261002.json` 保存实际 API 汇总和 UI 操作链；`/private/tmp/raytrain-storage-sync-portal-published-20261002.md` 保存 Portal 提交、实际镜像与 rollout 证据。这些文件是当次受限运维证据，长期维护入口为本 runbook；不将凭据、完整 Secret 或数据库备份提交到仓库。

## 2026-10-01 构建机验收与证据（历史）

以下为正式上线前的验证记录；其中“尚未上线”“待实际 Worker 验收”等状态已由上面的 10 月 2 日记录更新，保留原记录用于区分验证阶段。

2026-10-01 验证的业务源码为后端 `18b7fe3c37a28e8f64370c5a0d60c081b99a75e7`、新 Portal `2e8d9b46b488063382d61605ddabe560b82ba4d3`。Portal 已合并当时远端 `dev f9ee85aed7a50d73037a2784ebf28f7e9d50f7b4`；发布前须重新核对远端。后续仅文档更新不改变这些测试对应的业务源码。

| 验证 | 结果 | 证据范围 |
| --- | --- | --- |
| Go 格式、go vet、完整回归 | 通过；同步核心包覆盖率 80.4% | 构建机完整候选；`-p 1` 防止测试包争用迁移锁 |
| PostgreSQL | 通过 | 隔离真实 PostgreSQL；全新、重复、旧版本升级和并发事务，未将 SQLite 或 skipped 用例计为通过 |
| Worker | 79 项独立测试通过；启用分支统计的覆盖率 89% | 主机隔离环境及最终非 root、只读根目录镜像内运行 |
| 新 Portal | Node 15/15、浏览器 5/5、标准 lint/build 通过 | 浏览器使用隔离模拟 API；development/staging 的含凭据配置未传输，测试使用公开地址占位配置 |
| 镜像与 Helm | 后端、Worker 验证镜像构建通过；Chart lint/渲染通过 | 未推 Harbor，未应用到集群 |
| 真实 TOS | 9/9 通过，清理错误 0 | guofeng.su 已存在的稳定个人 storage home 下新 UUID 子目录 |

真实验收使用的用户路径：

```text
/mnt/storage/me/files/storage-sync-acceptance-7bc583a7a72b4e0684209b3d3ca28170/
```

在该临时子目录中验证了 1005 对象分页、全量、零变化增量、保留目标 JSON、IDC 本地样本上传及内容回读、同大小且回拨 mtime 的变化、源/目标并发修改保护、6 MiB 分片暂停续传、仅中止本次分片的取消以及条件分片完成。测试完成后已清理本次对象和分片；独立分页复核确认该 UUID 前缀对象数和未完成分片数均为 0。临时凭据文件及其空目录已删除，既有 Kubernetes Secret 未改。未修改既有个人文件或训练任务。

IDC 用例使用构建机临时文件系统样本；尚未证明训练集群 CPU 节点的 NFS 挂载和 Worker 回调链路可用。上述真实 TOS 验收不等于新 Portal → 线上 API → Kubernetes Job 的完整验收；定时规则已通过代码测试，尚未在线触发。正式发布后仍需在同一用户的新测试目录完成这些验证。

构建机证据目录：

- `/tmp/raytrain-storage-sync-evidence-20261001/`：`go-candidate11-full.log`、`go-candidate11.cover`、`python-candidate11.log`、`python-candidate11-coverage.log`、`python-image-candidate11.log`、`backend-image-candidate11.log`、`real-acceptance-candidate11.log`。
- `/tmp/raytrain-storage-sync-portal-verify-a5e3c596/`：`unit-green.log`、`lint-build.log`、`production-build.log`。
- `/tmp/raytrain-storage-sync-portal-verify-2e8d9b46/`：`e2e.log`、`e2e-lint.log`。`a5e3c596 → 2e8d9b46` 仅改测试定位器，产品源码相同。

用户已先后明确授权源码上传与隔离验收，以及正式源码/镜像发布、数据库备份和迁移、CPU Worker、20Gi 检查点盘、新 Portal dev CI/CD 和个人目录线上验收。正式发布授权持续有效，不需要因仓库恢复而重复询问。

## 2026-10-01 正式发布进展（历史，阻塞已解除）

- 后端源码 `6148663fbe6757c7fc082b6eae5cf44b40d22be4` 已同步到本地 main、GitHub main、内部 GitLab main 及正式构建目录；业务代码仍是已验证的 `18b7fe3`，之后仅改文档。原本地工作区的并行改动均保留；正式构建目录干净。
- Portal 候选仍为 `2e8d9b46`，未推送；线上 dev 仍为 `f9ee85ae`。补充的标准 `Dockerfile.dev` staging 编译和 nginx 打包通过，日志为 `/tmp/raytrain-storage-sync-portal-verify-2e8d9b46/dev-build-harbor-frontend.log`。
- 脱敏 server-side Helm dry-run 通过：只改后端镜像与 22 个同步配置环境变量，新增 ServiceAccount、Role、RoleBinding、20Gi PVC，无训练资源变更。Chart 没有 Secret 对象或动态凭据生成，既有 Secret 引用和输入未改；未导出完整 Secret 渲染。
- 生产数据库备份为构建机 `/root/raytrain-release-backups/storage-sync-6148663-20261001T090131Z/raytrain.dump`，自定义格式，3,037,660 字节；SHA256 `a4f05d6a56c41f08014725b90bd379812b1a5bb5eae3a12bd750e1a44980023d`。已在无网络、无端口的临时 PostgreSQL 16 容器实际恢复：版本 57、57 条迁移、73 张表，恢复返回 0；验证容器已删除。生产数据库仍为 57，尚未执行 0058。
- CPU 节点 `172.28.2.65` 已补齐 `nfs-common=1:2.6.1-1ubuntu1.2` 及 libnfsidmap1/rpcbind/keyutils，未升级其他包或重启节点、kubelet、containerd；临时安装策略文件已移除。只读主机挂载协商 NFSv3 成功，回调健康 200、未认证 TOS HTTPS 403，临时挂载清理完成。
- 可用于后续实际 Worker 验收的 IDC 源为 `spk-hybrid:extract/0c9b53cd344943298edc75c81a47651b/nusc/v1.0-mini/visibility.json`，2 字节，SHA256 `4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945`。该证明来自主机只读探测，Pod 内非 root 读取仍需实际 Worker 验收。
- 镜像正式推送两次均遇 Harbor 502。健康接口 HTTP 200 的正文为 unhealthy；不能将 HTTP 状态码当作仓库已恢复。common 集群 `harbor` 命名空间的唯一 registry Pod 没有就绪端点，进程为 `D / wait_on_page_bit`，本地 HTTP 也超时；`/storage` 对应 ext4 `/dev/rbd1`，使用 Ceph RBD。具体 Ceph/OSD 或节点根因尚未证明，未执行重启、强制卸载、Pod 删除或存储修复。
- 线上后端仍为 Helm revision 265、两副本健康，镜像摘要 `sha256:222268eeeb4a060979e7a6491f36e1f0301854e91c4bb28c870cbe81f38f47c8`。未启用数据同步、未创建检查点 PVC、未改运行中的用户训练。所有本次只读探测 Pod、主机临时挂载及恢复验证容器均已清理。

当时记录的恢复后顺序：确认 Harbor 健康正文及实际推拉恢复 → 在干净正式目录仅构建/推送 backend,storage-sync → 记录权威 registry 摘要 → 重新核对活跃训练 UID 与完整脱敏 dry-run → 发布后端并确认全部副本更新 → 推 Portal dev 并验证实际镜像 → 在 guofeng.su 新个人子目录验证预检、实际 Job/回调、复制、进度、控制和定时。此顺序已于 10 月 2 日执行，结果以上方新记录为准。
