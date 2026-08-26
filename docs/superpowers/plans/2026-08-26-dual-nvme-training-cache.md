# 双 NVMe 训练缓存与四组性能验收实施计划

> 目标：在不改变用户训练镜像、不中断现有 RayJob 的前提下，让每个 GPU Worker 同时使用节点 `/data1`、`/data2` 两块 NVMe；提供四个可直接执行的复现脚本，并以真实 BEVFusion 单机 8 卡、多机 16 卡任务完成无缓存/双盘缓存对照验收。

## 交付边界

- 保留现有 `ray-cache-local` StorageClass，供已创建任务兼容使用。
- 新增 `ray-cache-local-data1` 与 `ray-cache-local-data2` 两个独立 provisioner/StorageClass，不做 RAID。
- 缓存关闭时，RayJob 模板保持现状。
- 缓存开启时：Worker 挂载 `/mnt/cache` 与 `/mnt/cache2`；Head 只挂载 data1 用作 Ray 临时目录和对象溢写。
- 用户仍只使用一个 `--cache-size` 总容量参数；平台将总容量平均拆到两块盘。
- 缓存数据按相对路径哈希分片到两块盘，并通过统一只读视图提供给训练代码。
- 四个脚本不依赖公共环境变量文件，镜像、资源、数据路径和入口命令全部写明。
- 最终提交并验证四组真实训练：1×8 无缓存、2×8 无缓存、1×8 双盘缓存、2×8 双盘缓存。

## Task 1：先建立四个可复现脚本包

**文件：**

- 新增 `benchmarks/raytrain-perf-bevfusion/01-single-8gpu-no-cache.sh`
- 新增 `benchmarks/raytrain-perf-bevfusion/02-multi-16gpu-no-cache.sh`
- 新增 `benchmarks/raytrain-perf-bevfusion/03-single-8gpu-dual-nvme.sh`
- 新增 `benchmarks/raytrain-perf-bevfusion/04-multi-16gpu-dual-nvme.sh`
- 新增 `benchmarks/raytrain-perf-bevfusion/training_benchmark.py`
- 新增 `benchmarks/raytrain-perf-bevfusion/README.md`
- 新增 `benchmarks/raytrain-perf-bevfusion/test_bundle.sh`

### 1.1 先写失败校验

`test_bundle.sh` 必须验证：

1. 四个脚本均存在、可执行且 `bash -n` 通过。
2. 四个脚本均显式指定固定 digest 镜像、认证文件、CPU、内存、GPU、公共数据路径和唯一任务名。
3. 两个无缓存脚本明确使用 `--cache-mode off`。
4. 两个缓存脚本明确使用 `--cache-mode runtime --cache-size 5Ti`。
5. 单机脚本为 `workers=1/gpusPerWorker=8`，多机脚本为 `workers=2/gpusPerWorker=8`。
6. 所有脚本都调用同目录 `training_benchmark.py`，不得引用 `/tmp`、构建机私有工具包或 kubeconfig。

执行：

```bash
bash benchmarks/raytrain-perf-bevfusion/test_bundle.sh
```

预期：在四个脚本尚未齐全时失败。

### 1.2 写最小实现并转绿

脚本使用固定绝对工作目录 `/opt/guofeng/welldrive-train/raytrain-perf-bevfusion`，任务名只在脚本内部以时间戳生成，其他参数不抽到共享变量文件。固定训练条件：

- 镜像：`harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553`
- 配置：`configs/westwell/det/transfusion/secfpn/lidar/voxelnet_0p075.yaml`
- 训练标注：`0429_pkl/fz/merged_nuscenes_infos_train.pkl`
- 验证标注：`0429_pkl/fz/merged_nuscenes_infos_val.pkl`
- 样本：train 4096、val 512
- `samples_per_gpu=1`、`workers_per_gpu=4`
- 每 Worker：8 GPU、128 CPU、512Gi 内存

执行同一测试，预期全绿。

### 1.3 同步首批脚本到构建机

同步到：`/opt/guofeng/welldrive-train/raytrain-perf-bevfusion`。仅同步脚本包，不执行缓存组任务，直到双盘后端与 StorageClass 上线。

## Task 2：用纯函数实现总容量到双盘容量的拆分

**文件：**

- 新增 `backend/k8s/local_cache.go`
- 新增 `backend/k8s/local_cache_test.go`

### 2.1 RED

表驱动测试覆盖：

| 用户总容量 | data1 | data2 |
|---|---:|---:|
| 200Gi | 100Gi | 100Gi |
| 500Gi | 250Gi | 250Gi |
| 1Ti | 512Gi | 512Gi |
| 2Ti | 1Ti | 1Ti |
| 4Ti | 2Ti | 2Ti |
| 5Ti | 2560Gi | 2560Gi |

还需验证：空值走默认值、非法单位拒绝、奇数 Gi 拒绝、超过 5Ti 拒绝，不得静默截断。

执行：

```bash
cd backend && go test ./k8s -run 'TestSplitLocalCacheCapacity' -count=1
```

预期：函数不存在，测试失败。

### 2.2 GREEN

实现 `splitLocalCacheCapacity(total string) (perDisk string, err error)`；使用 Kubernetes quantity 解析，不自行实现单位换算。错误消息包含用户输入和允许值。

### 2.3 REFACTOR

将允许容量定义为不可变列表并由配置层注入，避免 API、渲染器和前端各自维护一份。

## Task 3：扩展平台配置与 API 限制

**文件：**

- 修改 `backend/config/config.go`
- 修改 `backend/config/config_test.go`
- 修改 `backend/api/limits.go`（以仓库实际文件为准）
- 修改对应 API 测试
- 修改 `helm/ray-train-platform/templates/backend-deployment.yaml`
- 修改 `helm/ray-train-platform/values.yaml`
- 修改 `deploy/profiles/vke-cpu-ha.yaml`

### 3.1 RED

测试要求：

- 配置能分别读取 data1/data2 的 StorageClass 与挂载点。
- 旧的单盘环境变量仍可作为 data1 兼容回退，但 data2 缺失时缓存请求必须失败，不能悄悄退回单盘。
- `/api/v1/limits` 返回 `200Gi,500Gi,1Ti,2Ti,4Ti,5Ti`，最大值为 `5Ti`。
- 默认缓存模式仍为 `off`，默认容量保持 `200Gi`。

### 3.2 GREEN

增加配置项：

```text
LOCAL_CACHE_STORAGE_CLASS_DATA1=ray-cache-local-data1
LOCAL_CACHE_STORAGE_CLASS_DATA2=ray-cache-local-data2
LOCAL_CACHE_MOUNT_PATH_DATA1=/mnt/cache
LOCAL_CACHE_MOUNT_PATH_DATA2=/mnt/cache2
LOCAL_CACHE_ALLOWED_SIZES=200Gi,500Gi,1Ti,2Ti,4Ti,5Ti
LOCAL_CACHE_MAX_SIZE=5Ti
```

Helm profile 使用同一组值，前端继续通过 limits API 动态展示，不写死容量。

## Task 4：RayJob 模板挂载双 PVC

**文件：**

- 修改 `backend/k8s/rayjob.go`
- 修改 `backend/k8s/rayjob_cache_test.go`（如不存在则新增）

### 4.1 RED

渲染测试验证：

- `cacheMode=off` 时不产生任何缓存 PVC/volume/mount/env。
- `cacheMode=runtime, cacheSize=5Ti` 时 Worker 出现两个 generic ephemeral PVC，各请求 `2560Gi`，StorageClass 分别正确。
- Worker 挂载 `/mnt/cache` 和 `/mnt/cache2`，并注入：
  - `PLATFORM_CACHE_PATH=/mnt/cache`
  - `PLATFORM_CACHE_PATHS=/mnt/cache:/mnt/cache2`
- Head 只挂载 data1，Ray temp/spill 仍使用 `/mnt/cache`，避免元数据双写。
- 每个 Worker Pod 的两个 PVC 都随 Pod 生命周期回收。

### 4.2 GREEN

扩展 `LocalCacheOptions` 为双盘字段；根据 `head` 参数分别渲染 Head/Worker。不得改变无缓存任务的 YAML。

### 4.3 回归

```bash
cd backend && go test ./k8s ./config ./api/... -count=1
```

## Task 5：安装两个独立本地盘 provisioner

**文件：**

- 新增 `helm/ray-cache-local/values-vke-data1.yaml`
- 新增 `helm/ray-cache-local/values-vke-data2.yaml`
- 修改 `ops/storage/nvme-cache/install.sh`
- 修改 `ops/storage/nvme-cache/verify.sh`
- 修改 `ops/storage/nvme-cache/preflight.sh`
- 新增/修改对应 shell contract tests

### 5.1 RED

渲染测试必须证明：

- 两个 Helm release 的 provisioner 名、StorageClass 名、ConfigMap 名互不冲突。
- data1 release 只允许 `/data1/ray-cache`，data2 release 只允许 `/data2/ray-cache`。
- 只登记明确的 GPU 节点；未知节点拒绝供应。
- ReclaimPolicy 为 `Delete`，VolumeBindingMode 为 `WaitForFirstConsumer`。
- 预检验证 `/data1` 与 `/data2` 是不同块设备，并检查每盘至少保留 15% 空间。

### 5.2 GREEN

在不卸载现有 `ray-cache-local` 的情况下安装两个新 release。部署前检查正在运行任务；新增 StorageClass 不触碰现有 RayJob。

## Task 6：实现双盘分片预热与统一数据视图

**文件：**

- 修改 `examples/dataset-cache/stage_dataset.py`
- 修改/新增 `examples/dataset-cache/test_stage_dataset.py`
- 修改 `benchmarks/raytrain-perf-bevfusion/training_benchmark.py`

### 6.1 RED

测试使用两个临时目录，验证：

- 同一个相对路径总是落到同一块盘。
- 大量文件分布在两块盘，文件数和字节数偏差可统计。
- 使用 SHA-256 相对路径哈希，不依赖 Python 随机哈希种子。
- 生成统一视图时，目录保持原结构，文件以 symlink 指向对应缓存盘。
- 拒绝 `..`、绝对路径、逃逸 symlink 和缓存根目录重叠。
- 单进程负责复制，其他本地 rank 等待 `.ready`；失败不写 ready 标记。
- 仅提供一个缓存路径时保持旧行为，便于已有镜像兼容。

### 6.2 GREEN

从 `PLATFORM_CACHE_PATHS` 读取缓存根，按 `sha256(relative_path)[0] % len(roots)` 分片，原子写入临时文件后 rename。统一视图固定为 `/mnt/cache/dataset-view`，训练 wrapper 将数据根映射到该路径。

### 6.3 性能日志

预热阶段输出机器可读汇总：总文件、总字节、每盘文件/字节、复制秒数、MiB/s、校验失败数、ready 命中状态。

## Task 7：构建、部署并做平台级无损验证

### 7.1 本地完整验证

```bash
cd backend && go test ./... -count=1
bash benchmarks/raytrain-perf-bevfusion/test_bundle.sh
helm lint helm/ray-cache-local -f helm/ray-cache-local/values-vke-data1.yaml
helm lint helm/ray-cache-local -f helm/ray-cache-local/values-vke-data2.yaml
helm lint helm/ray-train-platform -f deploy/profiles/vke-cpu-ha.yaml
```

### 7.2 构建与部署

1. 同步本地代码到 `/opt/guofeng/vke-cluster/ray-platform`。
2. 构建并推送新的 backend 镜像；若前端仅消费动态 limits API，则不重建前端。
3. 安装 data1/data2 两个 cache provisioner release。
4. 先创建最小 PVC/Pod，分别写入两块盘并核对节点真实目录。
5. Helm upgrade 平台 backend，等待滚动发布完成。
6. 验证现有运行中 RayJob UID、Pod 和状态未变化。

### 7.3 失败回滚

- 后端回滚到上一 revision。
- 保留两个新 StorageClass；它们未被引用时不会影响任务。
- 不删除现有 `ray-cache-local`。
- 若双盘探针失败，禁止执行缓存组性能任务。

## Task 8：执行四组真实训练验收

**执行顺序：**

1. `01-single-8gpu-no-cache.sh`
2. `02-multi-16gpu-no-cache.sh`
3. `03-single-8gpu-dual-nvme.sh`
4. `04-multi-16gpu-dual-nvme.sh`

每组必须保存：任务 ID、创建/开始/结束时间、排队时长、训练时长、总时长、GPU 拓扑、每 step `time/data_time`、P50/P95、缓存预热文件数/字节/吞吐、两盘分布、最终状态和 checkpoint。

缓存组额外检查：

- 两个 Worker 均挂载两块盘。
- data1/data2 均实际写入，且没有超出 15% 保留线。
- 训练使用 `/mnt/cache/dataset-view`，不是回源路径。
- 任务完成后两个 ephemeral PVC/PV 均自动删除，节点任务目录被回收。

任何一组失败，都先记录平台用户可见错误，再定位；不得把失败组从报告中删除。

## Task 9：产出详细对比报告与交付提交

**文件：**

- 新增 `benchmarks/raytrain-perf-bevfusion/TRAINING_CACHE_BENCHMARK_REPORT_20260826.md`
- 更新 `benchmarks/raytrain-perf-bevfusion/README.md`
- 更新运维文档中的双 provisioner 部署/回滚章节（沿用现有文档位置，不新建顶层文档）

报告至少包含：

- 完整测试条件与公平性说明。
- 四组结果总表。
- 单机到多机扩展效率。
- 冷缓存预热成本、热缓存收益和 break-even epoch 估算。
- 每块 NVMe 的实际容量、占用和吞吐。
- 当前 BEVFusion 是否真正受 I/O 限制的结论。
- 推荐用户何时开启缓存、何时保持关闭。
- 一键复现命令和清理验证。

最后执行安全检查、`git diff` 审阅、测试与 Helm 渲染验证，使用 conventional commit 提交；不得提交认证文件、PAT、GitLab token、kubeconfig 或构建机私有路径中的秘密。
