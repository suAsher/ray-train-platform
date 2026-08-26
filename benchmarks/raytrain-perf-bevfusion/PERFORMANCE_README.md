# BEVFusion 单机/多机与双 NVMe 缓存复现包

本目录用于同一数据、同一镜像、同一训练参数下比较四组任务：

1. `01-single-8gpu-no-cache.sh`：单机 8 卡，直接读取公共数据。
2. `02-multi-16gpu-no-cache.sh`：双机 16 卡，直接读取公共数据。
3. `03-single-8gpu-dual-nvme.sh`：单机 8 卡，先分片预热到节点两块 NVMe。
4. `04-multi-16gpu-dual-nvme.sh`：双机 16 卡，每个 Worker 先分片预热到所在节点两块 NVMe。

构建机固定目录：

```text
/opt/guofeng/welldrive-train/raytrain-perf-bevfusion
```

执行前应满足：

- 该目录已经包含完成平台适配的 BEVFusion `bev_3dod` 代码；
- `spk-rayjob` 已安装；
- `/root/raytrain-benchmark-auth.json` 是有效的平台登录配置；
- 公共数据根中存在 `0429_pkl/fz/merged_nuscenes_infos_train.pkl` 与 `merged_nuscenes_infos_val.pkl`；
- 缓存脚本只能在平台已启用 data1/data2 双 StorageClass 后执行。

四个脚本会重新打包当前目录代码并生成唯一任务名。终端输出同时保存到 `runs/`。不需要 kubeconfig，也不需要重建训练镜像。
