# GPU 节点 NVMe 缓存与平台服务弹性调度设计

日期：2026-08-23  
状态：已确认，进入实施
适用集群：VKE 生产集群及后续同构 Kubernetes 集群

## 1. 目标

本设计同时完成两件事：

1. 利用每个 RTX 4090 节点的 `/data1`、`/data2` 两块独立 NVMe，为新创建的 Ray Head/Worker 提供任务级可丢弃缓存；
2. 取消平台服务只能运行在 CPU 节点的硬约束，改成“CPU 控制面节点优先、GPU 节点允许兜底”。

缓存不能成为数据真相。公共、团队、个人和 IDC 数据仍由现有 TOS/FSX/NFS 数据空间提供，训练结果与 checkpoint 仍写入 `PLATFORM_OUTPUT_PATH`。

## 2. 已确认的现状

- 公共数据唯一根为 `tos://shanghai-data-transfer/ray-train/public/`，Pod 内浏览入口为 `/mnt/storage/public`。
- 全量原始数据主要位于 `labeled/`，PKL/标注决定训练真正读取的样本。
- 两个 GPU 节点各有 `/data1`、`/data2` 两块约 3.5TiB NVMe，二者保持独立，不做 RAID。
- 集群当前只有 `ebs-ssd` StorageClass；没有本地盘 CSI 或本地 StorageClass。
- 平台已经能为 Ray Head/Worker 渲染 generic ephemeral PVC、`PLATFORM_CACHE_PATH=/mnt/cache`、Ray temp-dir 与 object spilling 配置，但生产 Profile 中缓存开关关闭。
- 当前 BEVFusion 由 Ray 编排 `torchrun`，PyTorch DataLoader 直接读数据卷；Ray Object Store 不会自动缓存这些文件。
- 现有双节点读取基准约为首次 27MiB/s、重复读取 128MiB/s。重复读取受 Linux 页缓存、FSX 客户端缓存和对象存储侧缓存共同影响，不能直接宣称为 NVMe 命中。

## 3. 范围与非目标

### 3.1 本期范围

- 安装独立的本地卷供应器和 `ray-cache-local` StorageClass；
- 只允许已登记的生产 GPU 节点使用 `/data1/ray-cache`、`/data2/ray-cache`；
- 新 Ray Head/Worker 自动获得任务级临时缓存；
- Ray session、object spilling 和用户显式写入 `PLATFORM_CACHE_PATH` 的临时文件使用 NVMe；
- 增加容量、PVC 生命周期、供应失败和磁盘水位监控；
- 平台、MLflow、Loki、Prometheus/Grafana 等服务可调度到 GPU 节点，但不申请 GPU，并优先留在 CPU 控制面节点；
- 用控制面滚动升级期间持续运行的训练任务证明升级不影响既有任务。

### 3.2 本期不做

- 不把 10TiB 公共数据完整复制到每个节点；
- 不把 `/data1`、`/data2` 根目录直接暴露给用户；
- 不把 checkpoint 或最终模型只写在 NVMe；
- 不修改、重建或迁移运行中的 RayJob/RayCluster；
- 不重启 kubelet、containerd、FSX Agent、CNI 或 GPU 驱动；
- 不在本期替换 TOS/FSX 为 Alluxio、JuiceFS 或其他数据文件系统；
- 不承诺仅启用 Ray spilling 缓存就会提高 BEVFusion DataLoader 吞吐。

## 4. 方案选择

### 4.1 采用 Rancher Local Path Provisioner 的原因

集群没有本地 CSI，现有 NVMe 已格式化并挂载。OpenEBS LVM LocalPV 需要把磁盘交给 LVM，可能涉及卸载、重分区或清空磁盘，不适合在线直接引入。本期采用固定版本的 Rancher Local Path Provisioner，仅服务可丢弃缓存。

它支持动态本地 PV、`WaitForFirstConsumer`、PV 节点亲和、`Delete` 回收策略和每节点多路径。官方同时明确它不执行请求容量硬限制，因此本设计不把 PVC 的 `200Gi` 当作磁盘配额，而用受控并发、磁盘水位告警和高水位阻断作为保护。若后续需要严格容量隔离，再在维护窗口迁移到经过验证的本地 CSI/LVM。

供应器版本固定为 `v0.0.36`；升级版本另走安全评审和回归流程。清单与镜像必须保存到项目和内部 Harbor，不在生产部署时从公网拉取。`pathPattern` 使用供应器当前安全约束，不启用 `allowUnsafePathPattern`。

### 4.2 StorageClass 契约

`ray-cache-local` 必须满足：

```yaml
provisioner: rancher.io/local-path
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: false
```

节点路径映射只列出生产 GPU 节点；默认节点路径为空，CPU 节点和未来未登记节点不能误供应缓存：

```text
GPU 节点 A: /data1/ray-cache, /data2/ray-cache
GPU 节点 B: /data1/ray-cache, /data2/ray-cache
其他节点:   禁止供应
```

新增 GPU 节点必须先完成磁盘、目录、权限、读写和删除验收，再加入供应器路径映射。供应器在两条路径间选择一条；两块盘不组成 RAID，单盘异常只影响该盘上的可丢弃缓存。

## 5. Ray 任务缓存契约

生产初始值：

```yaml
training:
  localCache:
    enabled: true
    storageClass: ray-cache-local
    size: 200Gi
    mountPath: /mnt/cache
```

平台只给 Ray Head 和 Worker 添加 generic ephemeral PVC；Submitter 不挂载本地缓存。容器内：

```text
PLATFORM_CACHE_PATH=/mnt/cache
Ray temp-dir=/mnt/cache/ray
Ray object spilling=/mnt/cache/ray-spill/objects
```

每个 Pod 拥有独立目录和 PVC。Pod 删除后，PVC、PV 和节点目录由所有者引用及 `Delete` 策略回收。删除失败必须告警，但不得自动删除无法证明为 `ray-cache-local` 所有的目录。

PVC 请求容量是调度与审计信息，不是底层硬配额。平台初期保持每个 Pod `200Gi`。供应器的受控 `setup` 脚本会读取请求字节数和目标文件系统剩余容量；如果创建该卷后预计可用空间低于文件系统总量的 15%，则拒绝创建目录，使新任务保持 `PROVISIONING`，但不影响已运行任务。

独立的 `ray-cache-monitor` DaemonSet 只读挂载 `/data1`、`/data2`，导出总量、可用量、使用率、缓存目录数和清理失败指标。磁盘使用率达到 75% 告警，85% 触发高水位告警并由上述 `setup` 门禁拒绝新卷，92% 触发严重告警。监控组件不自动删除目录、不终止训练，也不修改节点调度状态。

## 6. 公共数据热缓存的后续门槛

任务级 NVMe 上线后，使用相同数据目录、文件数、字节数、镜像和 Worker 数重跑 `examples/io-benchmark`。只有同时满足下列条件，才进入第二期“公共数据热缓存”：

- 首次读取或 DataLoader 等待明显限制 GPU 利用率；
- 相同数据集版本会被不同任务重复使用；
- 预热后端到端训练时间改善至少 20%，或小文件 P95/吞吐改善至少 2 倍；
- 能为数据集生成不可变 manifest（路径、大小、ETag/摘要）。

第二期缓存只针对公共只读数据，以 `dataset + version + manifest digest` 为键，支持断点预热、校验、原子发布、LRU 水位回收和远端回退。个人、团队可写数据和训练输出不进入共享节点缓存。本期不实现该能力，避免把未经验证的数据复制机制带进生产训练。

## 7. 平台服务调度

### 7.1 目标语义

- CPU 控制面节点是优先位置，不再是必须位置；
- GPU 节点无 taint 时可承接控制面、MLflow、日志和监控服务；
- 所有这些服务的容器 GPU request/limit 均为空；
- 训练 Worker 仍由生产 GPU 标签硬约束；
- 本地缓存供应器只能在已登记 GPU 节点创建卷。

### 7.2 Helm 表达

平台 Chart 增加统一的软节点偏好：

```yaml
placement:
  preferredNodeSelector:
    platform.wellspiking.ai/pool: control-plane
  allowGPUNodeFallback: true
```

Backend、Frontend、CLI 发布服务、内置 PostgreSQL、MLflow、Loki、Prometheus、Grafana 和相关网关移除硬 `nodeSelector`，改为权重 100 的 preferred node affinity。PDB、滚动策略和副本反亲和保持生效。DaemonSet 按其职责决定覆盖范围，不通过“排除 GPU 节点”实现容量保护。

CPU/内存 request 与 limit 必须保留。调度到 GPU 节点的系统 Pod 不消耗 `nvidia.com/gpu`，但会消耗 CPU/内存；管理员页面和 Prometheus 应能看出系统负载，避免将 CPU/内存不足误判为 GPU 配额问题。

## 8. 在线升级与现有训练保护

缓存和调度更新只影响未来创建的 Pod 模板。已经存在的 RayJob、RayCluster、Worker、数据挂载和训练进程不变。

发布顺序固定为：

1. 记录现有 RayJob、RayCluster、Pod UID 和 GPU 分配；
2. 部署本地卷供应器，但保持平台缓存关闭；
3. 分别在两个 GPU 节点完成 PVC/Pod 创建、写入、节点亲和、删除和目录回收 smoke；
4. 启动一个持续输出心跳并写个人结果的训练任务；
5. 滚动升级平台服务调度配置，确认该训练 Pod UID、重启次数和日志连续；
6. 启用 `training.localCache`，这一步只重启 Backend 控制面；
7. 提交新的 1×1 缓存 smoke，再提交 2×8 回归；
8. 比较升级前后的现有任务、日志、MLflow 和 checkpoint。

任何一步失败都停止推进。缓存回滚只需先关闭 `training.localCache.enabled`，新任务恢复无缓存模板；已创建的缓存任务继续运行到结束。所有 `ray-cache-local` PVC/PV 清理完之前不得卸载供应器。

## 9. 失败处理

- PVC Pending：任务保持 `PROVISIONING`，平台展示“本地缓存卷未供应”，不回退到错误节点；
- 单块盘不可写：把对应路径移出供应器配置，新卷使用另一块盘；已运行任务不自动迁移；
- 节点丢失：该节点缓存视为丢失，任务按 Ray/Kubernetes 原有策略失败或重建，数据真相不受影响；
- 删除残留：告警并由受控清理脚本按 PV UID 精确删除；禁止通配符清理；
- 高水位：`setup` 容量门禁让新缓存 PVC 保持 Pending，不删除运行中任务目录；
- 供应器异常：关闭平台缓存开关，新任务回到现有 TOS/FSX 路径，训练代码无需修改。

## 10. 验收标准

- `ray-cache-local` 为 `WaitForFirstConsumer`、`Delete`、非默认 StorageClass；
- CPU 节点无法供应该 StorageClass；两个 GPU 节点均可从 `/data1`、`/data2` 创建并回收卷；
- 新 Head/Worker 有 `/mnt/cache` 和 `PLATFORM_CACHE_PATH`，Submitter 没有；
- Ray temp-dir 与 object spilling 位于 NVMe；
- 任务删除后 PVC、PV 和节点目录回收；
- 升级期间既有训练 Pod UID、重启次数、日志和训练进度连续；
- 新 2×8 任务完成 NCCL、训练、MLflow 和 checkpoint；
- 平台服务可落到 GPU 节点但不申请 GPU；
- 本地卷供应失败、磁盘水位和残留卷有监控/告警；
- 用户数据路径、提交命令和现有 BEVFusion 文档不发生变化。

## 11. 安全边界

- 供应器拥有创建/删除 PV 和临时 helper Pod 的集群级 RBAC，只使用固定 ServiceAccount 和独立 namespace；
- helper 镜像、供应器镜像固定 digest 并来自内部 Harbor；
- `nodePathMap` 默认拒绝未知节点；
- 不启用不安全路径模板，不接受用户提供宿主机路径；
- 训练 Pod 只看到自己的 `/mnt/cache`，看不到节点缓存根和其他任务目录；
- NVMe 中不保存长期 AK/SK、PAT、Git Token 或平台数据库备份；
- 缓存可随时删除，所有业务真相都能从 TOS/IDC/数据库恢复。
