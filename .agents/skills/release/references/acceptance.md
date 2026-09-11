# `spk-rayjob` 真实提交验收

## 原则

- 验收从构建机发起，用 `guofeng.su` 平台 PAT 和当次发布的 `spk-rayjob`，不用管理员 API 或 `kubectl create/apply` 代替用户提交。
- `kubectl` 只用于提交后的只读证据：节点布局、Kueue 准入、Pod 事件和资源回收。
- 先查现有任务和配额。容量被真实用户任务占满时，不停任务、不抢占、不反复提交；保留预检失败记录，使用同版本已有的成功证据或延后验收。
- 验收命令、stdout/stderr、JOB ID、状态变化和日志必须保存在 `/root/raytrain-release-<sha>-*.log`。

## 提交前

1. 使用 Portal 创建绑定目标团队的 PAT，通过 `--token-stdin` 登录；不把 PAT 写入 shell history 或验收日志。
2. 执行 `spk-rayjob version` 和 `spk-rayjob login-check`。
3. 执行 `spk-rayjob images`，确认镜像当前团队可见且支持所选 engine。
4. 执行 `spk-rayjob datasets` / `dataset versions`，确认输入合同。
5. 查询当前团队配额、全集群可分配 GPU 和活跃调试环境。计算本次需求 `workers × gpusPerWorker`。

以下 submit 示例只展示资源/引擎参数，前提是在已准备好的代码目录运行，且 `.spk-rayjob.yaml` 已填写已登记且当前团队可用的 image、实际 entrypoint、输入与输出路径。没有项目配置时必须显式提供这些参数，不能将缺少 image/entrypoint 的失败算作集群故障。原生 Ray 命令若在本次验收范围内，也需使用同一身份/团队和数据契约单独验证。

## 最小与多机验收

下例的 `--execution-mode ray_train` 是多节点执行模式，至少两个 Worker；不要把 `1×1` 当作多机验收。训练引擎 `--engine ray-train` 与 execution mode 是两个不同字段，不应从这一限制推断所有托管训练都不能单卡：

```bash
spk-rayjob submit \
  --engine ray-train \
  --execution-mode ray_train \
  --workers 2 \
  --gpus-per-worker 1 \
  --watch
```

多机成功需同时有三类证据：

1. `spk-rayjob` 显示任务进入 `SUCCEEDED`。
2. 训练日志显示与预期总训练进程数一致的 world_size、不同 rank 和 CUDA 设备；下例 `2×1` 通常为 2，`2×8` 的 DDP 总进程数应为 16，不能两者都写死为 2。
3. 只读集群事件或 Pod 节点列显示两个 Worker 落在不同 `kubernetes.io/hostname`。

`2 Worker × 1 GPU` 可能被 bin-pack 到同一节点，也可能跨节点，所以必须看实际节点证据。在每台 8 卡的 4090 集群上，`2 Worker × 8 GPU` 会强制每个 Worker 独占一台机，是容量充足时的强验收：

```bash
spk-rayjob submit \
  --engine ray-train \
  --execution-mode ray_train \
  --workers 2 \
  --gpus-per-worker 8 \
  --watch
```

该强验收需要团队至少 16 GPU 配额且有两台完整空闲节点。如果预检返回 `GPU_QUOTA_EXCEEDED`，说明请求在创建 RayJob 前已被正确拦截，不是多机调度故障。

## 验收内容

- 帮助：`spk-rayjob --help`、`submit --help`、`login --help`、`logs --help`、`connect --help` 均无需登录即可用。
- 提交：用户代码不进镜像，由 CLI 打包当前目录；镜像仅提供环境。
- 数据：检查 `PLATFORM_DATASET_PATH`、`PLATFORM_OUTPUT_PATH`；如用 Ray Data/Parquet/NVMe，记录 manifest 摘要、分片数、样本数和缓存命中/预热证据。
- 训练：检查 rank/world size、每个 Worker 的 GPU、至少数个 step 和正常终态。
- 可观测：`status`、`logs -f`、`logs --limit 0`、MLflow run 链接、Worker 指标及产物列表均与 JOB ID 对应。
- 连接：如任务持续时间允许，验证 `spk-rayjob connect <JOB> --worker 0`；退出连接不停止任务。
- 收尾：验收任务终态后 GPU 配额回收，它之前已运行的任务没有重启。
