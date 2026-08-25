# 训练数据路径与读取性能基准

本文说明训练 Pod 中公共数据路径的关系，并给出一项可以随普通训练任务重复执行的多节点读取基准。用户不需要 TOS 凭据、PVC 名称或 Kubernetes 权限。

## 1. `/mnt/storage/public` 与 `/mnt/data/input`

它们不是两份数据，也不存在任务启动时的整目录复制。

当用户提交任务并选择“公共数据”根目录时，平台把同一个公共数据卷、同一个只读子目录同时挂到：

```text
/mnt/storage/public   公共空间的稳定浏览入口
/mnt/data/input       当前任务选中输入的稳定别名
```

如果任务只选择公共空间中的 `bevfusion/fz-3dod-v1`，则 `/mnt/data/input` 只指向该子目录，而 `/mnt/storage/public` 仍代表用户有权浏览的公共根。这样做有三个目的：

1. 训练代码始终读取 `PLATFORM_DATASET_PATH`，不需要知道输入来自公共、团队、个人或 IDC 空间；
2. 任务只获得被选中子目录的只读入口，减少误读其他目录的可能；
3. 同一代码在 Portal、`spk-rayjob` 和其他集群 Profile 中保持一致。

因此在调试环境中直接浏览 `/mnt/storage/public` 没问题；正式训练代码应使用：

```python
import os
from pathlib import Path

dataset_root = Path(os.environ["PLATFORM_DATASET_PATH"])
output_root = Path(os.environ["PLATFORM_OUTPUT_PATH"])
```

数据怎么读、checkpoint 写成什么格式，仍由用户的 Dataset、配置和训练脚本决定。平台只负责把已选择的目录挂载为只读输入，把任务独立的个人目录挂载为可写输出，并注入上述环境变量。

## 2. 可重复的双节点基准

基准源码在 [`examples/io-benchmark`](../examples/io-benchmark/README.md)。它通过 `spk-rayjob` 随任务上传，不需要额外构建镜像：

```bash
cd examples/io-benchmark

RUN_ID="$(date +%Y%m%d-%H%M%S)"
spk-rayjob submit \
  --name "public-io-${RUN_ID}" \
  --output-path "benchmarks/public-io-${RUN_ID}" \
  --watch
```

默认资源为 2 个 Worker、每个 Worker 1 GPU；两个进程读取互不重复的文件分片。结果会：

- 以 `RAYTRAIN_IO_BENCHMARK_JSON=` 输出到任务日志；
- 写入本任务结果目录的 `io-benchmark.json`；
- 在 RayCluster 回收后继续从任务“产物”页查看。

## 3. 2026-08-21 生产基线

最终复测任务 `job-cb59214e3483201a4be247da` 以普通用户身份执行成功，两个 Worker 分布在不同 GPU 节点。测试公共数据子目录 `cnfzhjyg`，由 rank 0 固定选择并广播 8,192 个文件，共 5,199,813,050 字节；两个 Worker 各读取 4,096 个互不重复的文件。

| 指标 | 第 1 次读取 | 第 2 次读取 |
| --- | ---: | ---: |
| 聚合吞吐 | 26.978 MiB/s | 127.964 MiB/s |
| 聚合文件速率 | 44.566 files/s | 211.393 files/s |
| 最慢 Worker 用时 | 183.816 s | 38.752 s |
| Worker 0 单文件 P95 | 98.168 ms | 11.079 ms |
| Worker 1 单文件 P95 | 94.875 ms | 11.756 ms |

文件清单由 rank 0 生成，用时 1.850 秒。第 1 次读取不宣称为严格冷缓存；第 2 次读取会同时受到 Linux 页缓存、FSX 客户端缓存和对象存储侧缓存影响，因此不能直接称作“NVMe 命中率”。这组数字适合作为当前小文件训练负载的基线，后续启用节点 NVMe 缓存时应使用完全相同的目录、文件数量、字节上限、镜像 digest 和并发度重测。

## 4. 2026-08-25 TOS/FSX 与 NVMe 对照

本次使用同一公共目录 `cnfzhjyg`、同一镜像 digest、2 个 Worker、8,192 个文件和
5,199,813,050 字节完成顺序 A/B。测试没有执行宿主机 `drop_caches`，因此“首次读”是
业务可复现口径，不是实验室意义上的裸盘冷缓存。

| 指标 | 持久数据首次读 | 持久数据同 Pod 重复读 | NVMe 预热后第 1 次读 |
| --- | ---: | ---: | ---: |
| 聚合吞吐 | 19.140 MiB/s | 114.849 MiB/s | 5,625.340 MiB/s |
| 聚合文件速率 | 31.618 files/s | 189.727 files/s | 9,292.892 files/s |
| 最慢 Worker 用时 | 259.093 s | 43.178 s | 0.882 s |
| 单文件 P95 | 约 146 ms | 约 15 ms | 约 1.8 ms |

NVMe 组在读取前还发生了一次不能忽略的预热：5.20 GB、8,192 个文件共用时
223.146 秒，聚合源到缓存吞吐为 22.223 MiB/s。以这组数据估算，只读取一遍时预热没有
收益；同一数据在一个任务内重复读取约两遍后才接近回本，epoch 更多、小文件更密集或
远端读取长尾更严重时收益才会继续扩大。

对应任务：

- 缓存关闭：`job-d7489711966bad0f742871c9`；
- NVMe 预热：`job-bd7827af5a33faa9ec07ec4a`。

## 5. 标准 A/B 命令

先执行缓存关闭组：

```bash
cd examples/io-benchmark
RUN_ID="$(date +%Y%m%d-%H%M%S)"
spk-rayjob submit \
  --name "public-io-off-${RUN_ID}" \
  --cache-mode off \
  --output-path "benchmarks/public-io-off-${RUN_ID}" \
  --watch
```

待任务结束并释放 Worker 后，再执行缓存组：

```bash
RUN_ID="$(date +%Y%m%d-%H%M%S)"
spk-rayjob submit \
  --name "public-io-nvme-${RUN_ID}" \
  --entrypoint 'python3 benchmark.py --path cnfzhjyg --passes 2 --max-files 8192 --max-bytes-per-worker 8589934592 --stage-to-cache' \
  --cache-mode runtime \
  --cache-size 100Gi \
  --output-path "benchmarks/public-io-nvme-${RUN_ID}" \
  --watch
```

两次任务日志都必须保存 `RAYTRAIN_IO_BENCHMARK_JSON=`。缓存组还必须保存
`RAYTRAIN_IO_CACHE_STAGE=`，否则不能计算预热成本和回本点。

## 6. 对外展示口径

展示性能时至少同时说明：

- 数据类型和目录版本；
- 文件数与总字节数；
- Worker 数和每 Worker 并发；
- 首次读取和重复读取；
- 测试任务 ID、镜像 digest 和测试时间。

不要只展示最高 MiB/s。训练数据包含大量图片和点云小文件时，`files/s` 与单文件 P95 延迟通常比大文件顺序吞吐更能解释 DataLoader 是否会等待。

## 7. FSX Agent 与慢挂载排查

FSX Agent 是 FSX CSI 在每个节点上的用户态数据客户端。Kubelet 请求挂载 TOS/FSX 卷时，CSI Node 组件通过它完成身份获取、FUSE 客户端启动、健康检查和卸载；它不是业务 Pod，也不保存用户训练代码。

排查顺序：

1. 检查 `csi-fsx-controller`、`csi-fsx-node` 和每个节点的 `fsx-agent` 是否 Ready；
2. 检查 FSXSet 的 `updatedNumberScheduled` 是否等于 `desiredNumberScheduled`；
3. 从 Agent Pod 内验证 TOS、OIDC 和区域 STS 域名，而不是只在宿主机验证；
4. 查看 CSI Node 日志中的 `health pre-check failed`、挂载超时和重复卸载；
5. 只在确认该节点没有活跃 FSX 请求后，逐节点收敛旧 Agent，禁止同时强制删除全部 Agent。

当前检查结果是 5/5 Agent Ready，但 FSXSet 配置更新只应用到 2/5：新加入的 GPU 节点使用期望 revision，三个已有 CPU 节点仍是上一 revision，且当前均报告无活跃挂载请求。FSXSet 的 `scaleDownAfterNoDependency=false` 表示没有开启“节点无依赖时自动清理 FSX 客户端”；火山引擎也明确说明，这种情况下新节点会使用新客户端，而已有节点的存量客户端不会自动升级。因此这不是数据损坏，也不能仅凭 `updated=2/5` 判断当前挂载失败。

历史日志中确实出现过健康预检失败和一分钟挂载超时，所以仍需单独治理 DNS 与挂载稳定性。维护窗口内应按官方升级路径处理存量 Agent，或联系火山引擎支持，不应直接批量强删。日常增加 `updated != desired`、`FailedMount`、挂载耗时以及区域 STS/TOS/OIDC 域名解析告警；出现异常时按官方工具日志文档检查 `/var/log/fsx` 并采集诊断包。

参考火山引擎官方说明：[FSX TOS 静态卷挂载](https://www.volcengine.com/docs/6460/1828107)、[IRSA 方式挂载存储](https://www.volcengine.com/docs/6460/1861517)、[FSX 客户端升级](https://www.volcengine.com/docs/6460/1828940)、[FSX 工具日志](https://www.volcengine.com/docs/6349/2275450)。
