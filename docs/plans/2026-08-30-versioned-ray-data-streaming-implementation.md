# 版本化 Ray Data 流式训练实施计划

> **For Codex:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` or `superpowers:subagent-driven-development` to implement this plan task-by-task. Every task follows RED → GREEN → REFACTOR and is committed independently.

日期：2026-08-30  
关联规格：`docs/plans/2026-08-30-versioned-ray-data-streaming-design.md`  
首个验收对象：`bev_3dod_s1h`，对照 `yihan.she` 的真实 2×8 训练配置

## 目标与边界

把持续增量同步到 `tos://shanghai-data-transfer/ray-train/public/labeled/` 的原始小文件发布为不可变、内容寻址的 Parquet 数据版本；训练提交时把 `latest` 固定为具体版本，使用 Ray Train `TorchTrainer` 和 Ray Data worker shard 流式训练，双 NVMe 只保存有界工作集。现有 `mount`、`cache`、`ray-data-stage` 与正在运行的任务保持不变。

技术栈：Go 1.25、Gin、GORM、PostgreSQL、Kubernetes/KubeRay/Kueue、Python 3.10、Ray 2.58 canary、Torch 2.4.1/CUDA 12.1、PyArrow/Parquet、Vue 3、Prometheus/Loki/MLflow。

全局门禁：

1. `DATASET_VERSIONING_ENABLED=false` 和 `RAY_DATA_STREAMING_ENABLED=false` 默认关闭。
2. 迁移只增不改，历史任务不回填；运行中任务不停止、不重建。
3. 用户不能提交内部 TOS URI，也不能获得 publisher 的 AK/SK/IRSA。
4. `latest` 只在 preflight 中使用，任务持久化具体 version ID 和 manifest digest。
5. CBGS 正确性未通过前不做性能结论。
6. Ray 2.58 先作为 canary，不替换 Ray 2.56.1 默认运行时。
7. 每个任务按 RED → GREEN → REFACTOR 执行，测试通过后单独提交。

## Task 1：功能开关与兼容边界

**Files**

- Modify: `backend/config/config.go`
- Modify: `backend/config/config_test.go`
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `helm/ray-train-platform/templates/backend.yaml`
- Create: `ops/platform/test/feature-flag-render-test.sh`

**RED**

- 测试两个开关默认关闭、只接受显式布尔值。
- Helm 测试要求关闭时不创建 publisher 身份、不改变旧任务提交。

Run: `cd backend && go test ./config -run 'DatasetVersioning|RayDataStreaming'`  
Run: `bash ops/platform/test/feature-flag-render-test.sh`  
Expected: FAIL。

**GREEN / REFACTOR**

- 增加两个开关和内部派生根 `ray-train/platform/datasets`。
- 开关关闭时新 API 返回 404/feature disabled，旧 API 行为不变。

Verify: `cd backend && go test ./config`  
Commit: `feat: gate versioned streaming datasets`

## Task 2：数据集领域模型与不可变状态机

**Files**

- Create: `backend/domain/dataset.go`
- Create: `backend/domain/dataset_test.go`

**RED**

测试 Dataset slug/source/visibility/tenant 校验；版本状态转换；READY manifest、schema、样本数不可修改；`latest` 不是可持久化 ID；public/team 权限判断。

Run: `cd backend && go test ./domain -run 'Dataset|Publication'`  
Expected: FAIL。

**GREEN / REFACTOR**

实现 `Dataset`、`DatasetVersion`、`DatasetPartition`、`DatasetPublicationRun`、`DatasetCacheObservation` 及纯函数状态转换，不访问数据库/Kubernetes。

Verify: `cd backend && go test ./domain`  
Commit: `feat: model immutable dataset versions`

## Task 3：数据库迁移与仓储

**Files**

- Create: `backend/db/migrations/0024_dataset_versioning.up.sql`
- Modify: `backend/db/migration_targets_test.go`
- Create: `backend/repositories/datasets.go`
- Create: `backend/repositories/datasets_test.go`
- Modify: `backend/repositories/jobs.go`
- Modify: `backend/repositories/schema_mapping_test.go`

**RED**

- 迁移测试要求表、唯一键、外键、状态约束和 latest READY 查询索引存在。
- 仓储测试覆盖创建、幂等版本、租户过滤、只解析 READY、引用计数。
- 任务新增 provenance 列可空，不破坏历史记录。

Run: `cd backend && go test ./db ./repositories -run 'Dataset|SchemaMapping|Migration'`  
Expected: FAIL。

**GREEN / REFACTOR**

新增 `datasets`、`dataset_versions`、`dataset_partitions`、`dataset_publication_runs`、`dataset_version_shards`、`dataset_cache_observations`；向 `training_jobs` 增加可空 dataset/version/digest/mode/cache policy 列。

Verify: `cd backend && go test ./db ./repositories`  
Commit: `feat: persist datasets and immutable versions`

## Task 4：数据集目录 API 与权限

**Files**

- Create: `backend/api/datasets.go`
- Create: `backend/api/datasets_test.go`
- Modify: `backend/main.go`

**RED**

- 普通用户只看 public 与授权团队数据集。
- 团队管理员管理团队定义；平台管理员管理 public 定义。
- 普通用户不能指定内部 prefix、修改 READY 版本或触发全局 GC。
- 响应不包含内部 publisher 身份、凭据或签名 URL。

Run: `cd backend && go test ./api -run 'DatasetAPI|DatasetAuthorization'`  
Expected: FAIL。

**GREEN / REFACTOR**

新增列表、详情、版本、latest、管理员定义、触发发布、弃用和 GC dry-run API；复用现有角色/租户授权模型。

Verify: `cd backend && go test ./api`  
Commit: `feat: expose governed dataset catalog`

## Task 5：提交时固定 latest 与 preflight

**Files**

- Modify: `backend/domain/training_job.go`
- Modify: `backend/domain/training_engine.go`
- Modify: `backend/api/submission_service.go`
- Modify: `backend/api/submission_service_test.go`
- Modify: `backend/api/ray_submission.go`
- Create: `backend/api/dataset_preflight_test.go`

**RED**

- `labeled-full:latest` 被解析成具体 version ID/digest。
- 非 READY、越权、schema/runtime 不兼容、manifest 缺失在申请 GPU 前失败。
- READY 之后变化不改变已经创建的任务。
- 用户提供内部 URI 被拒绝；旧 data-space/path 提交仍通过。

Run: `cd backend && go test ./api ./domain -run 'DatasetPreflight|Submission.*Dataset|Streaming'`  
Expected: FAIL。

**GREEN / REFACTOR**

扩展 JobSpec：`datasetRef`、`dataMode=streaming`、`cachePolicy=auto|off|bounded`。提交事务中解析并保存不可变 provenance。

Verify: `cd backend && go test ./api ./domain`  
Commit: `feat: pin dataset versions during submission`

## Task 6：增量发布规划器与 manifest

**Files**

- Create: `backend/datasetpublisher/planner.go`
- Create: `backend/datasetpublisher/planner_test.go`
- Create: `backend/datasetpublisher/manifest.go`
- Create: `backend/datasetpublisher/manifest_test.go`

**RED**

使用内存 inventory 测试：

- size/ETag 未稳定不发布；`_SUCCESS` 仍需完整性校验。
- 未变化场景复用 shard，只重打变化场景。
- digest 与对象枚举顺序无关且可复现。
- 缺失标注、train/val 重叠、非法点云长度标记 FAILED。
- dry-run 正确计算新增、复用和预计存储字节。

Run: `cd backend && go test ./datasetpublisher`  
Expected: FAIL。

**GREEN / REFACTOR**

实现纯规划层：输入 inventory、旧 manifest、数据集规则，输出 publication plan；此任务不访问 TOS。

Verify: `cd backend && go test ./datasetpublisher`  
Commit: `feat: plan incremental dataset publications`

## Task 7：TOS publisher 适配器与安全边界

**Files**

- Create: `backend/datasetpublisher/object_store.go`
- Create: `backend/datasetpublisher/object_store_test.go`
- Create: `backend/datasetpublisher/service.go`
- Create: `backend/datasetpublisher/service_test.go`
- Modify: `backend/objectstore/store.go`
- Modify: `backend/objectstore/tos.go`

**RED**

- 原始 prefix 仅 List/Get/Head；只有内部 dataset prefix 可 Put/Delete。
- 写入采用 temp key → checksum → manifest commit。
- publication ID 重试幂等，断点只补缺失 shard。
- 删除只处理超过保留期且引用为 0 的内部 shard。
- 路径穿越、跨 dataset 写入和日志泄露凭据全部失败。

Run: `cd backend && go test ./datasetpublisher ./objectstore`  
Expected: FAIL。

**GREEN / REFACTOR**

抽象最小 ObjectStore 接口并复用 TOS client；所有路径经过 allowlist validator，训练用户接口不暴露内部 URI。

Verify: `cd backend && go test ./datasetpublisher ./objectstore`  
Commit: `feat: publish dataset shards through scoped TOS access`

## Task 8：Parquet v1 packer 与发布镜像

**Files**

- Create: `images/dataset-publisher/Dockerfile`
- Create: `images/dataset-publisher/requirements.txt`
- Create: `images/dataset-publisher/raytrain_publisher/schema.py`
- Create: `images/dataset-publisher/raytrain_publisher/pack.py`
- Create: `images/dataset-publisher/tests/test_schema.py`
- Create: `images/dataset-publisher/tests/test_pack.py`
- Modify: `build-image.sh`
- Modify: `scripts/test-ray-runtime-images.sh`

**RED**

- 点云 pack/unpack 与 `np.fromfile(float32)` 字节级一致。
- token/scene/split/class_ids/timestamp/source_digest 完整。
- shard 256–512 MiB、row group 32–128 MiB（测试可注入小阈值）。
- 按场景切分且 digest 确定；S1H lidar-only 不打包相机。
- `info` pickle 只接受可信发布器生成内容。

Run: `python3 -m unittest discover -s images/dataset-publisher/tests -v`  
Run: `bash scripts/test-ray-runtime-images.sh --contract-only`  
Expected: FAIL。

**GREEN / REFACTOR**

实现 PyArrow schema、packer、partition summary、manifest；构建脚本新增独立 publisher target。

Verify: `python3 -m unittest discover -s images/dataset-publisher/tests -v`  
Commit: `feat: pack S1H datasets into immutable Parquet shards`

## Task 9：发布控制器、Kueue 与 IRSA

**Files**

- Create: `backend/datasetpublisher/controller.go`
- Create: `backend/datasetpublisher/controller_test.go`
- Create: `helm/ray-train-platform/templates/dataset-publisher-serviceaccount.yaml`
- Create: `helm/ray-train-platform/templates/dataset-publisher-rbac.yaml`
- Create: `helm/ray-train-platform/templates/dataset-publisher-config.yaml`
- Modify: `helm/ray-train-platform/values.yaml`
- Create: `ops/platform/test/dataset-publisher-render-test.sh`

**RED**

测试状态推进、失败重试、幂等 Job 名、Kueue 低优先级、CPU-only request；Helm 仅在开关开启时创建独立 ServiceAccount/IRSA，不向用户 namespace 创建 Secret。

Run: `cd backend && go test ./datasetpublisher -run Controller`  
Run: `bash ops/platform/test/dataset-publisher-render-test.sh`  
Expected: FAIL。

**GREEN / REFACTOR**

backend reconciler 创建 CPU publication Job。CPU 节点资源不足时可调度到 GPU 节点的 CPU/内存，但不得请求 GPU。

Verify: `cd backend && go test ./datasetpublisher && cd .. && bash ops/platform/test/dataset-publisher-render-test.sh`  
Commit: `feat: reconcile governed dataset publications`

## Task 10：S1H Ray Data 桥与 CBGS 等价性

**Files**

- Create: `images/workspace/raytrain_runtime/s1h_dataset.py`
- Create: `images/workspace/raytrain_runtime/test_s1h_dataset.py`
- Modify: `images/workspace/raytrain_runtime/ray_data.py`
- Modify: `images/workspace/raytrain_runtime/test_ray_data.py`
- Create: `examples/bevfusion/patches/ray_data_s1h.py`
- Create: `examples/bevfusion/patches/ray_data_s1h_test.py`

**RED**

- `train.get_dataset_shard("train")` 的 row 可恢复现有 pipeline 字典。
- points dtype/shape/digest 与旧 loader 一致。
- 固定 seed/epoch 下 CBGS 每类数、总样本数、前 N token 与旧算法一致。
- Ray Data 分 shard 后不再叠加 DistributedSampler。
- `samples_per_gpu` 仍是每 GPU batch；损坏 row 有明确错误。

Run: `python3 -m unittest images.workspace.raytrain_runtime.test_s1h_dataset -v`  
Run: `python3 -m unittest examples.bevfusion.patches.ray_data_s1h_test -v`  
Expected: FAIL。

**GREEN / REFACTOR**

实现轻量 sample refs → CBGS plan → worker shard → payload decode → 原增强/model pipeline；禁止把完整数据集装入内存。

Verify: `python3 -m unittest images.workspace.raytrain_runtime.test_s1h_dataset images.workspace.raytrain_runtime.test_ray_data -v`  
Commit: `feat: stream S1H samples through Ray Data shards`

## Task 11：双 NVMe 内容寻址有界缓存

**Files**

- Create: `images/workspace/raytrain_runtime/shard_cache.py`
- Create: `images/workspace/raytrain_runtime/test_shard_cache.py`
- Modify: `images/workspace/raytrain_runtime/ray_data.py`
- Modify: `images/workspace/raytrain_runtime/test_ray_data.py`

**RED**

- digest 稳定分配到 `/mnt/cache` 或 `/mnt/cache2`。
- 同节点 8 rank 并发只下载一次，使用 lock/temp/atomic rename。
- checksum 失败清理临时文件并回退 source。
- 85% 高水位触发 LRU，回收到 70%；锁定文件不回收。
- cache off/bounded/auto、空间不足和 source fallback 行为明确。
- hit/miss/download/eviction/source latency/cache latency/prefetch wait 指标可读取。

Run: `python3 -m unittest images.workspace.raytrain_runtime.test_shard_cache -v`  
Expected: FAIL。

**GREEN / REFACTOR**

实现按任务/租户隔离的工作集缓存。容量来自可用空间/平台预算，不要求数据集小于缓存；未命中继续流式读取。

Verify: `python3 -m unittest images.workspace.raytrain_runtime.test_shard_cache images.workspace.raytrain_runtime.test_ray_data -v`  
Commit: `feat: add bounded dual-NVMe Ray Data cache`

## Task 12：托管 driver、RayJob 与数据性能指标

**Files**

- Modify: `images/workspace/raytrain_runtime/managed_driver.py`
- Modify: `images/workspace/raytrain_runtime/test_managed_driver.py`
- Modify: `images/workspace/raytrain_runtime/mmcv_hook.py`
- Modify: `images/workspace/raytrain_runtime/test_mmcv_hook.py`
- Modify: `backend/k8s/rayjob.go`
- Create: `backend/k8s/rayjob_dataset_version_test.go`
- Modify: `backend/k8s/rayjob_managed_test.go`
- Modify: `backend/k8s/rayjob_data_modes_test.go`
- Modify: `backend/k8s/rayjob_cache_test.go`

**RED**

- manifest/digest、shuffle seed、prefetch、cache policy 传入 Ray Dataset。
- streaming mode 挂载双 NVMe、保留独立 Ray spill、禁止 full preload。
- Pod 环境包含非秘密 provenance；公共 API 不泄露内部 manifest path。
- MLflow/Prometheus 记录 dataset/version/runtime/worker/rank 和数据等待指标。

Run: `python3 -m unittest images.workspace.raytrain_runtime.test_managed_driver images.workspace.raytrain_runtime.test_mmcv_hook -v`  
Run: `cd backend && go test ./k8s -run 'Managed|DataMode|Cache|DatasetVersion'`  
Expected: FAIL。

**GREEN / REFACTOR**

增加原生 `streaming` mode；保留 `ray-data-stage` 全量复制旧语义。Ray object spill 与 shard cache 使用独立目录和独立指标。

Verify: `python3 -m unittest discover -s images/workspace/raytrain_runtime -v`  
Verify: `cd backend && go test ./k8s`  
Commit: `feat: render managed streaming Ray Train jobs`

## Task 13：Ray 2.58 S1H canary 镜像

**Files**

- Modify: `images/bevfusion-runtime/Dockerfile`
- Modify: `images/bevfusion-runtime/Dockerfile.test.sh`
- Modify: `images/train-pytorch/Dockerfile`
- Modify: `images/workspace/Dockerfile`
- Modify: `build-image.sh`
- Modify: `scripts/test-ray-runtime-images.sh`
- Modify: `helm/ray-train-platform/values.yaml`

**RED**

contract 要求 Ray 2.58.0、Python 3.10、Torch 2.4.1/CUDA 12.1、PyArrow、managed runtime、S1H adapter 可导入；MMCV/mmdet3d CUDA 扩展不被 working-dir 遮蔽；现有 2.56 target 仍是生产默认。

Run: `bash images/bevfusion-runtime/Dockerfile.test.sh`  
Run: `bash scripts/test-ray-runtime-images.sh --contract-only`  
Expected: FAIL。

**GREEN / REFACTOR**

新增显式 `bevfusion-ray258-canary` target，不放宽现有 production version guard；镜像按 digest 登记，仅 admin/yihan canary 可见。

Build-host smoke:

```bash
USE_BUILDX=false PUSH_IMAGE=true TARGETS=bevfusion-ray258-canary bash build-image.sh
docker run --rm --gpus all <canary-image> python -c 'import ray,torch,pyarrow,mmcv,mmdet3d; print(ray.__version__, torch.__version__)'
```

Commit: `feat: build Ray 2.58 BEVFusion canary runtime`

## Task 14：`spk-rayjob` 数据集提交

**Files**

- Modify: `backend/spkrayjob/command.go`
- Modify: `backend/spkrayjob/project.go`
- Modify: `backend/spkrayjob/defaults.go`
- Modify: `backend/spkrayjob/client.go`
- Modify: `backend/spkrayjob/command_test.go`
- Modify: `backend/spkrayjob/command_workflow_test.go`
- Modify: `backend/spkrayjob/execution_profile_test.go`
- Modify: `backend/spkrayjob/validation_workflow_test.go`

**RED**

覆盖：

```text
spk-rayjob datasets
spk-rayjob dataset versions labeled-full
spk-rayjob submit --engine ray-train --dataset labeled-full:latest --data-mode streaming --workers 2 --gpus-per-worker 8 --watch
```

preflight 显示固定版本、样本数、容量、镜像 digest、GPU、cache policy；禁止内部 URI；旧 yaml 继续可用。

Run: `cd backend && go test ./spkrayjob -run 'Dataset|Streaming|Preflight'`  
Expected: FAIL。

**GREEN / REFACTOR**

实现目录/版本查询与提交参数，输出不暴露内部 prefix。

Verify: `cd backend && go test ./spkrayjob`  
Commit: `feat: submit versioned streaming datasets from CLI`

## Task 15：Portal 数据集、版本与提交体验

**Files**

- Create: `frontend/src/api/datasets.js`
- Create: `frontend/src/datasetCatalog.js`
- Create: `frontend/src/datasetCatalog.test.js`
- Create: `frontend/src/views/Datasets/index.vue`
- Create: `frontend/src/components/DatasetVersionPicker.vue`
- Modify: `frontend/src/router/index.js`
- Modify: `frontend/src/components/job/StepData.vue`
- Modify: `frontend/src/components/job/StepRuntime.vue`
- Modify: `frontend/src/submission.js`
- Modify: `frontend/src/submission.test.js`
- Modify: `frontend/src/components/job/DataPerformancePanel.vue`
- Modify: `frontend/src/jobTrainingPerformance.js`
- Modify: `frontend/src/jobTrainingPerformance.test.js`

**RED**

- 数据集页按权限显示版本、状态、样本/容量、增量差异、失败原因。
- 提交页普通模式只选数据集/版本、1×8 或 2×8、自动读取；高级项显示缓存/预取。
- latest 经 preflight 后显示具体版本。
- 详情区分 source throughput、Ray backpressure、NVMe cache、object spill。
- 旧 mount/cache 表单和历史任务仍渲染。

Run: `cd frontend && npm test`  
Expected: FAIL。

**GREEN / REFACTOR**

实现数据集工作流，普通用户不再输入 TOS/PVC/Parquet 路径。

Verify: `cd frontend && npm test && npm run build`  
Commit: `feat: add versioned dataset workflow to portal`

## Task 16：指标、告警与低基数标签

**Files**

- Modify: `backend/monitoring/metrics.go`
- Modify: `backend/monitoring/metrics_test.go`
- Create: `helm/ray-train-platform/templates/dataset-prometheusrule.yaml`
- Create: `helm/ray-train-platform/templates/dataset-servicemonitor.yaml`
- Create: `ops/platform/test/dataset-monitoring-render-test.sh`

**RED**

覆盖发布失败/停滞、source P95、cache hit/checksum failure/eviction、prefetch wait、Ray spill、GPU data stall、manifest mismatch；object key/token 不得作为 Prometheus label。

Run: `cd backend && go test ./monitoring -run Dataset`  
Run: `bash ops/platform/test/dataset-monitoring-render-test.sh`  
Expected: FAIL。

**GREEN / REFACTOR**

实现指标、ServiceMonitor 和 PrometheusRule；详情页从平台 API 读取一致口径。

Verify: `cd backend && go test ./monitoring`  
Verify: `bash ops/platform/test/dataset-monitoring-render-test.sh`  
Commit: `feat: observe streaming dataset health`

## Task 17：完整本地验证与安全审查

**Files**

- Modify only defects found by tests/review.

**Verification**

```bash
cd backend && go test ./...
cd ../frontend && npm test && npm run build
cd .. && python3 -m unittest discover -s images/workspace/raytrain_runtime -v
python3 -m unittest discover -s images/dataset-publisher/tests -v
bash scripts/test-ray-runtime-images.sh --contract-only
bash images/bevfusion-runtime/Dockerfile.test.sh
for test in ops/platform/test/*-test.sh; do bash "$test"; done
git diff --check
```

安全审查：pickle 信任边界、路径穿越/symlink、SQL 参数、租户越权、内部 URI、IRSA/RBAC、缓存跨租户、日志泄密。`git diff` 不得出现 PAT、密码、AK/SK。

修复必须先增加回归测试。  
Commit: `fix: harden versioned streaming data path`

## Task 18：无行为变更部署与 canary 开启

**Files**

- Modify: `deploy/profiles/vke-cpu-ha.yaml`
- Create: `deploy/profiles/vke-raydata-canary.yaml`
- Modify: `docs/BUILD_AND_DEPLOY.md`

**Preflight**

在构建机确认本地/构建机 commit，读取当前 RayJob 和 Helm diff；不得停止/删除任何现有任务。

```bash
git rev-parse HEAD
kubectl get rayjobs -A
helm diff upgrade ray-platform helm/ray-train-platform -n ray-train-platform -f deploy/profiles/vke-cpu-ha.yaml
```

先以两个开关关闭部署 backend/frontend/CLI/publisher/canary 镜像，验证旧流程无变化；再仅对 admin 与 `yihan.she` 开 canary。

```bash
bash ops/platform/deploy.sh --profile deploy/profiles/vke-raydata-canary.yaml --verify-fsx-irsa --timeout 15m
bash ops/platform/smoke.sh --profile deploy/profiles/vke-raydata-canary.yaml
```

Commit: `ops: enable versioned Ray Data canary`

## Task 19：发布真实 `s1h-yihan` 数据版本

**Files**

- Create: `examples/bevfusion/datasets/s1h-yihan.yaml`
- Create: `examples/bevfusion/scripts/verify_s1h_manifest.py`
- Create: `examples/bevfusion/scripts/verify_s1h_manifest_test.py`

**RED**

manifest verifier 测试样本数、split、class_ids、固定 token、points digest、missing、重复和 train/val 交叉。

Run: `python3 -m unittest examples.bevfusion.scripts.verify_s1h_manifest_test -v`  
Expected: FAIL。

**GREEN / REAL DATA**

1. 构建机新建干净目录，从 GitLab 拉取 `bev_3dod_s1h` 并记录 commit；不复用 `/root/work/bevfusion`。
2. 以 yihan 当前 PKL/配置定义真实 train/val/test。
3. 对 `public/labeled` dry-run，显示对象/样本/类别/缺失/预计新增与复用字节；missing 不为 0 时停止。
4. 发布内容寻址 Parquet；校验所有 digest 和 CBGS 数量。
5. UI/CLI 只显示逻辑数据集和版本，不显示内部 URI。

Verify: `python3 examples/bevfusion/scripts/verify_s1h_manifest.py --dataset s1h-yihan --version <fixed-version>`  
Commit: `test: define real S1H dataset publication`

## Task 20：A/B/C/D 真实全 train split 验收

**Files**

- Create: `examples/bevfusion/benchmarks/run-a-legacy-raw.sh`
- Create: `examples/bevfusion/benchmarks/run-b-ray-train-raw.sh`
- Create: `examples/bevfusion/benchmarks/run-c-ray-data-streaming.sh`
- Create: `examples/bevfusion/benchmarks/run-d-ray-data-cache.sh`
- Create: `examples/bevfusion/benchmarks/collect_results.py`
- Create: `examples/bevfusion/benchmarks/test_collect_results.py`
- Create: `docs/benchmarks/S1H_RAY_DATA_FULL_EPOCH_REPORT.md`

**RED**

结果收集器测试拒绝不同 commit/seed/version/global batch/GPU 数的对比，拒绝 smoke/sample 截断，校验指标完整性。

Run: `python3 -m unittest examples.bevfusion.benchmarks.test_collect_results -v`  
Expected: FAIL。

**GREEN / E2E**

相同代码 commit、数据版本、seed、global batch、2×8 GPU，串行跑完整真实 train split 1 epoch：

- A：旧 ray-ddp + raw mount。
- B：Ray Train + raw。
- C：Ray Train + Ray Data Parquet streaming + cache off。
- D：C + bounded dual-NVMe cache。

四个脚本只能使用用户可用的 `spk-rayjob`/Portal API，不依赖 kubectl，输出任务 ID、固定版本、镜像 digest。

收集：启动/首 batch/epoch wall、samples/s、source GB/s、P50/P95 data wait、step、GPU/CPU/内存/网络、cache、spill、loss、mAP/NDS、checkpoint digest。

正确性门禁：样本/step/CBGS 数、固定 token/points digest 一致；无 NaN/OOM；checkpoint 可恢复。通过后选择推荐组运行 yihan 原始完整 epoch 数，并验证一次取消后续训。

Commit: `perf: validate S1H streaming Ray Train end to end`

## Task 21：用户、管理员与运维文档

**Files**

- Modify: `README.md`
- Create: `docs/user/DATASET_VERSION_AND_STREAMING_GUIDE.md`
- Create: `docs/user/S1H_RAY_TRAIN_SUBMISSION_GUIDE.md`
- Create: `docs/ops/DATASET_PUBLISHER_RUNBOOK.md`
- Create: `docs/ops/RAY_DATA_NVME_CACHE_RUNBOOK.md`
- Modify: `docs/OPERATIONS_HANDBOOK.md`
- Modify: `docs/architecture/README.md`
- Modify: `scripts/test-doc-links.sh`

**RED**

文档 contract 校验 README 链接、CLI 参数和示例文件存在，拒绝构建机私有目录、PAT/密码/AK/SK。

**GREEN**

用户文档回答：public raw 与版本关系、代码改造、代码随任务上传、latest 固定、日志/MLflow/Ray Dashboard、checkpoint/续训、streaming/cache/legacy 的选择、缓存装不下 10 TiB 时为何仍能训练。

运维文档覆盖：增量发布、失败重试、稳定窗口、TOS/DNS/FSX、manifest 校验、缓存回收、Ray 2.58 回滚、DB 备份与 GC dry-run。

Verify:

```bash
bash scripts/test-doc-links.sh
rg -n 'glpat-|rpt_|AKLT|12345678|/root/work/bevfusion' README.md docs examples
```

Commit: `docs: deliver versioned Ray Data training workflow`

## Task 22：三端一致、release tag 与最终验收

**Consistency**

```bash
git status --short
git rev-parse HEAD
ssh -i ~/.ssh/qomolo-desktop.pem root@14.103.49.106 'cd /opt/guofeng/vke-cluster/ray-platform && git rev-parse HEAD && git status --short'
git ls-remote origin refs/heads/main
```

本地、构建机、GitHub commit 必须一致；镜像和 Helm release 记录 commit/tag/digest。用户未跟踪文件不得误提交。

**Final acceptance**

- Portal：数据版本、提交、任务详情、日志、GPU/CPU/内存/数据性能、MLflow、Ray Dashboard。
- CLI：login、datasets、preflight、submit、status、logs、cancel、resume。
- 权限：普通用户不能看/停其他团队任务，管理员看全局占用和 publisher。
- 数据：原始 public 不变，内部版本不可浏览，训练输出沿用禁止下载策略。
- 回滚：关闭 feature flags 后旧 mount/cache 任务仍可提交，已有任务不受影响。
- 一台无 kubeconfig 的新机器可完全按用户文档复跑。

Release：`v1.2.0-raydata`；得到用户最终确认后再 push tag。

## 完成定义

只有以下条件全部满足才可声明生产可用：

1. Go、前端、Python、镜像 contract、Helm render 全绿。
2. 真实 `public/labeled` 增量发布可重试、可校验且无凭据泄漏。
3. S1H A/B/C/D 均完成真实全 train split 1 epoch，正确性门禁通过。
4. 推荐组完成原始长训练和一次 checkpoint 恢复。
5. guofeng.su、yihan.she 和临时普通用户只用 Portal/CLI 完成提交与查看。
6. feature flag 回滚演练通过，旧模式可继续使用。
7. 本地、构建机、GitHub、Helm 和镜像 provenance 一致。
