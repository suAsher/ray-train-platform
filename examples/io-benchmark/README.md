# 公共训练数据读取基准

这项测试和真实训练使用同一数据挂载、同一任务调度链路。平台在两个 Worker 上各启动一个进程，两个进程读取互不重复的文件分片，最后汇总：

- 文件枚举时间；
- 每个 Worker 的 MiB/s、files/s、单文件 P50/P95 延迟；
- 两个 Worker 的聚合 MiB/s 和 files/s；
- 相同文件的第 1 次和第 2 次完整读取。

第 1 次读取不宣称是严格“冷缓存”，第 2 次读取也不等同于纯 NVMe 缓存测试；对象存储服务端、FSX 客户端和 Linux 页缓存都可能影响结果。对外展示时应同时给出文件数、字节数、Worker 数和运行时间。

## 运行

```bash
cd examples/io-benchmark

RUN_ID="$(date +%Y%m%d-%H%M%S)"
spk-rayjob submit \
  --name "public-io-${RUN_ID}" \
  --output-path "benchmarks/public-io-${RUN_ID}" \
  --watch
```

模板默认测试公共根下的 `cnfzhjyg`，最多选择 8,192 个文件，并严格限制每个 Worker 最多读取 8 GiB。文件清单只由 rank 0 生成并广播，所有 Worker 使用同一份不可变清单。临时更换目录或规模时直接覆盖入口：

```bash
RUN_ID="$(date +%Y%m%d-%H%M%S)"
spk-rayjob submit \
  --name "public-io-${RUN_ID}" \
  --entrypoint 'python3 benchmark.py --path 0429_pkl/fz --passes 2 --max-files 4096 --max-bytes-per-worker 4294967296' \
  --output-path "benchmarks/public-io-${RUN_ID}" \
  --watch
```

任务结束后，控制台日志中搜索 `RAYTRAIN_IO_BENCHMARK_JSON=`。完整 JSON 同时写入该任务个人结果目录下的 `io-benchmark.json`。

## 指标口径

| 指标 | 含义 |
| --- | --- |
| `metadata_scan_wall_seconds` | rank 0 生成并广播确定性文件清单的耗时 |
| `aggregate_mib_per_second` | 所有 Worker 实际读取字节数之和 / 最慢 Worker 耗时 |
| `aggregate_files_per_second` | 所有 Worker 读取文件数之和 / 最慢 Worker 耗时 |
| `latency_ms_p95` | 单 Worker 读完一个文件的 P95 耗时，适合观察小文件负载 |

每次对比都应固定镜像 digest、目录、文件上限、字节上限和 Worker 数；不同条件下的数字不能直接比较。
