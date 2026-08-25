# 训练数据预热到节点 NVMe

`stage_dataset.py` 把平台已经选中的只读输入根从 `PLATFORM_DATASET_PATH` 原子复制到
当前 Worker 的 `PLATFORM_CACHE_PATH/dataset`。同一个 Worker 中只有 `LOCAL_RANK=0`
执行复制，其他 GPU rank 等待就绪标记。

把 `stage_dataset.py` 放进自己的代码仓库，例如 `tools/stage_dataset.py`，然后让训练入口
先取得实际路径：

```bash
PLATFORM_DATASET_PATH="$(python3 tools/stage_dataset.py)"
export PLATFORM_DATASET_PATH
python3 tools/train.py --data-root "$PLATFORM_DATASET_PATH"
```

提交配置必须同时选择精确的数据子目录和 runtime 缓存：

```yaml
cache:
  mode: runtime
  size: 100Gi
input:
  space: public
  path: <数据集版本或 shard>
```

不要选择整个 `public` 根。每个 Worker 都有独立 NVMe 卷，因此 2 个 Worker 会分别复制
一份选中数据。Checkpoint、模型和训练报告仍写 `PLATFORM_OUTPUT_PATH`，不能只放在缓存。

任务日志会出现：

```text
RAYTRAIN_DATASET_CACHE={"path":"/mnt/cache/dataset","copied":true,...}
```

其中 `files`、`bytes` 和 `seconds` 是预热成本。比较加速效果时，必须把这段时间计入总
墙钟时间；训练数据只读一遍时，预热可能比直接远端读取更慢。
