# 双 NVMe 训练缓存设计

**日期：** 2026-08-26

**状态：** 已通过方案评审，等待书面规格复核

**适用范围：** Ray 单机多卡、多机多卡训练任务的任务级临时缓存

## 1. 目标

平台需要让每个 GPU Worker 同时利用节点上的两块 3.5 TB NVMe，并保持以下性质：

- 单机 8 卡与多机 16 卡及后续更多节点使用同一提交契约；
- 缓存是可丢弃加速层，TOS、IDC 和 `PLATFORM_OUTPUT_PATH` 仍是数据真相；
- 不使用 RAID0，不因一块缓存盘故障扩大到另一块盘；
- 现有只读取 `PLATFORM_CACHE_PATH=/mnt/cache` 的代码继续工作；
- 任务结束后，两个临时卷及节点目录自动回收；
- 容量不再受 500Gi 上限限制，同时避免任务耗尽节点磁盘；
- 新 GPU 节点可通过标准注册流程接入，不修改训练代码。

## 2. 不在本次范围

- 不缓存整个约 10 TB 的公共数据空间；
- 不提供跨任务共享的持久数据集缓存；
- 不将 Checkpoint、最终权重或唯一数据副本保存在 NVMe；
- 不把两块盘组成 RAID、LVM stripe 或单一故障域；
- 不把 Local Path PVC request 描述为操作系统硬配额；
- 不在本次改造中引入 Ray Data 或修改 BEVFusion 算法。

## 3. 用户场景

### 3.1 兼容模式

用户继续提交：

```bash
spk-rayjob submit --cache-mode runtime --cache-size 500Gi --watch
```

旧代码继续使用：

```python
cache_root = os.environ["PLATFORM_CACHE_PATH"]
```

`PLATFORM_CACHE_PATH` 固定指向第一块缓存盘 `/mnt/cache`。

### 3.2 双盘数据缓存模式

新缓存工具读取：

```text
PLATFORM_CACHE_PATHS=/mnt/cache:/mnt/cache2
```

工具根据稳定文件摘要把数据分到两个缓存根，并生成统一只读视图。训练代码只接收统一的数据根，不需要理解文件位于哪块盘。

### 3.3 无缓存模式

`--cache-mode off` 不渲染任何 NVMe PVC，训练仍直接读取所选 TOS/IDC 数据，现有行为不变。

## 4. 方案选择

采用两个独立 Local Path Provisioner 和两个 StorageClass：

| 逻辑盘 | StorageClass | 节点目录 | Pod 挂载 |
| --- | --- | --- | --- |
| cache data1 | `ray-cache-local-data1` | `/data1/ray-cache` | `/mnt/cache` |
| cache data2 | `ray-cache-local-data2` | `/data2/ray-cache` | `/mnt/cache2` |

保留现有 `ray-cache-local` 一段兼容窗口，但新的双盘任务不再依赖其“多个候选路径随机选一条”的行为。两个独立 provisioner 保证同一 Worker 的两个 PVC 不会落到同一物理盘。

## 5. Pod 与环境变量契约

启用 runtime 缓存时，每个 Ray Worker 获得两个 generic ephemeral PVC：

```text
local-cache-data1 -> /mnt/cache
local-cache-data2 -> /mnt/cache2
```

环境变量：

```text
PLATFORM_CACHE_PATH=/mnt/cache
PLATFORM_CACHE_PATHS=/mnt/cache:/mnt/cache2
```

Ray 运行时目录保持向后兼容：

```text
Ray temp-dir       -> /mnt/cache/ray
Ray object spilling -> /mnt/cache/ray-spill/objects
```

第二块盘优先用于显式数据集预热、中间特征和用户临时 shard。双盘数据工具可以同时使用两块盘。

Ray Head 只需要第一块缓存盘用于 session 和 object spilling；双盘数据集缓存只渲染到 Worker，避免 Head 无意义占用第二个大容量 PVC。

## 6. 容量语义

`--cache-size` 表示一个 Worker 的双盘总请求容量，平台平均拆分到两个 PVC。允许值调整为：

```text
200Gi, 500Gi, 1Ti, 2Ti, 4Ti, 5Ti
```

拆分规则：

| 总请求 | data1 PVC | data2 PVC |
| --- | ---: | ---: |
| 200Gi | 100Gi | 100Gi |
| 500Gi | 250Gi | 250Gi |
| 1Ti | 512Gi | 512Gi |
| 2Ti | 1Ti | 1Ti |
| 4Ti | 2Ti | 2Ti |
| 5Ti | 2560Gi | 2560Gi |

当前单盘约 3.5 TB，5Ti 总请求拆分后每盘约 2.5Ti，能够保留至少 15% 文件系统水位。Provisioner 在创建目录前继续校验请求容量、实时可用空间和安全水位。

Local Path 目录不是硬配额。平台 UI 和文档必须明确：请求容量用于准入、调度、审计和水位保护，不能阻止恶意或错误代码写超。严格限额需要后续迁移到 Local CSI/LVM 或 XFS project quota。

## 7. 数据分片与统一视图

交付通用缓存工具，不把缓存逻辑写死在 BEVFusion 中。输入为平台选择的数据根与不可变 manifest；每条文件记录至少包含相对路径、大小和摘要。

分片规则：

```text
sha256(relative_path) 的最低位为 0 -> /mnt/cache
sha256(relative_path) 的最低位为 1 -> /mnt/cache2
```

规则只依赖相对路径，因此所有 Worker 对同一 manifest 得到一致分片。每个 Worker 仍只复制自己训练所需的 shard；本次性能基准为保持两组语义一致，两个 Worker 使用了相同的 25.15GiB 数据集副本。

统一视图位于第一块盘：

```text
/mnt/cache/dataset-view
```

实际文件分别写入：

```text
/mnt/cache/data/...
/mnt/cache2/data/...
```

`dataset-view` 只包含指向上述实际文件的相对符号链接。链接目标必须经过根路径校验，禁止绝对路径逃逸、`..` 和跟随不受控符号链接。训练代码最终读取 `dataset-view`。

预热采用临时文件加原子 rename；仅 `LOCAL_RANK=0` 复制，同一 Worker 的其他 rank 等待原子 ready 标记。中断后重新提交会重新创建任务级卷，不复用不完整缓存。

## 8. 清理与故障处理

两个缓存卷均为 generic ephemeral PVC，所有者为对应 Ray Pod。清理链路：

```text
Ray Pod 删除
  -> 两个 ephemeral PVC 删除
  -> 两个 PV 按 Delete 回收
  -> 各 provisioner teardown 删除精确 PVC 目录
```

teardown 必须继续满足：

- 只接受精确的 `/data1/ray-cache/<eligible-pvc-dir>` 或 `/data2/ray-cache/<eligible-pvc-dir>`；
- 拒绝根目录、符号链接、非 canonical 路径和未知卷名；
- 删除失败累计指标并触发 `RayCacheTeardownFailure`；
- 不自动递归删除无法证明属于平台缓存的目录。

一块盘供应失败时，启用双盘缓存的任务在创建阶段失败并显示具体盘和原因，不悄悄降级到单盘。用户可改为 `--cache-mode off` 重提。

## 9. 新节点接入

节点必须具有：

```text
/data1/ray-cache
/data2/ray-cache
```

注册脚本校验：

- 两个目录分别位于不同块设备；
- 目录为 root 所有，权限符合 provisioner 契约；
- 单盘可用空间和 15% 安全水位；
- 节点 GPU 标签与生产训练池标签；
- 两个 StorageClass 均可在该节点完成 smoke PVC 创建、写入、删除。

注册完成后同时更新 data1/data2 provisioner 的精确 nodePathMap。未登记节点保持拒绝供应，避免误写系统盘。

## 10. API、CLI 与 UI

保持现有字段：

```yaml
cache:
  mode: runtime
  size: 2Ti
```

CLI 参数不变：

```bash
--cache-mode runtime --cache-size 2Ti
```

平台 `/api/v1/limits` 返回新的 allowlist、默认值和 5Ti 最大值。Web 页面把容量描述为“每个训练 Worker 的双盘总缓存请求”，并显示预计每盘容量。

原生 Ray Job metadata 继续使用：

```json
{
  "platform.cache.mode": "runtime",
  "platform.cache.size": "2Ti"
}
```

未知容量、奇数且不能稳定拆分的容量、超出 allowlist 的值在创建 RayCluster 前拒绝。

## 11. 兼容与发布

发布顺序：

1. 安装 data1/data2 两套 provisioner 和非默认 StorageClass；
2. 在两台 GPU 节点分别运行双盘 smoke；
3. 部署支持双盘渲染但默认缓存仍为 off 的后端；
4. 发布 CLI、UI 和平台 limits；
5. 提交 1×8 双盘缓存任务；
6. 提交 2×8 双盘缓存任务；
7. 确认任务结束后 PVC、PV、节点目录均清零；
8. 保留旧 `ray-cache-local`，直到没有旧任务和旧 PVC 后再单独退役。

后端滚动升级不修改已创建 RayJob；正在运行的单盘缓存任务继续使用旧模板直到结束。

## 12. 回滚

发现问题时：

1. 将平台缓存能力标记为不可用，拒绝新的 runtime 提交；
2. 不删除正在运行任务的 PVC 或 Pod；
3. 等双盘任务自然结束并确认 PVC/PV 清零；
4. 回滚后端、CLI/UI limits；
5. 最后卸载两个新 provisioner；
6. 无缓存训练始终可继续提交。

## 13. 四个性能脚本与报告

构建机交付目录：

```text
/opt/guofeng/welldrive-train/raytrain-perf-bevfusion/
```

包含：

```text
01-single-8gpu-no-cache.sh
02-multi-16gpu-no-cache.sh
03-single-8gpu-dual-nvme.sh
04-multi-16gpu-dual-nvme.sh
training_benchmark.py
README.md
TRAINING_CACHE_BENCHMARK_REPORT_20260826.md
```

脚本不嵌入密码、PAT 或 Git Token，只接受已有 `spk-rayjob` 登录配置。每次生成唯一任务名和输出目录，固定代码、镜像 digest、4096 训练样本、512 验证样本、batch、DataLoader worker、CPU 和内存口径。

报告记录四个正式任务 ID、镜像、代码 commit、数据 manifest、资源、step P50/P95、data_time P50/P95、预热文件数/字节数/秒数、复制吞吐、端到端时间、扩展效率和缓存盈亏平衡点。

## 14. 验收条件

- 单机无缓存和多机无缓存任务成功；
- 单机双盘和多机双盘任务成功；
- 每个 Worker 同时存在 `/mnt/cache` 与 `/mnt/cache2`，且位于不同块设备；
- 预热后的实际数据在两块盘均非空，字节分布不偏离 40%/60%；
- 统一视图中的所有链接都留在两个缓存根内；
- `PLATFORM_CACHE_PATH` 旧代码不改即可运行；
- 5Ti 请求拆为两个 2560Gi PVC，并通过安全水位检查；
- 任务结束后测试相关 PVC/PV 和节点精确目录均为零；
- `--cache-mode off` 的 RayJob YAML 与改造前等价；
- 正在运行的训练任务不因平台滚动发布被重建或中止；
- 自动测试覆盖容量拆分、Pod 渲染、路径安全、清理脚本、Helm 渲染、CLI 和 UI limits。
