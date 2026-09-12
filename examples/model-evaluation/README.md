# 独立评估 worker SDK

`evaluation_sdk.py` 使用 Python 标准库。将它和评估程序放在已审核、固定 commit 的评估器 Git 源码中；运行镜像只提供 Ray/框架依赖，不打包用户代码。由平台独立评估入口创建任务，SDK 不创建训练、不读取 PAT、不修改配额。

必须在 managed Ray Train worker 内调用 `EvaluationClient.from_environment()`。注册评估器入口为 `python examples/model-evaluation/smoke_evaluator.py`（路径按固定 Git 源码布局调整），平台以 `raytrain-managed ... -- python ...` 包装入口；现有 managed driver 的 TorchTrainer 在 worker loop 执行该文件。不要在 RayJob driver 直接执行此文件，也不要在用户入口再次创建嵌套 TorchTrainer。它读取平台注入的 `MODEL_EVALUATION_*` 固定元数据与 `RAYTRAIN_EVENT_TOKEN_FILE` Secret 文件。driver 或本地 shell 缺少这些信息会报错。不要复制 Secret 到配置、日志或环境字符串。

```python
from ray import train
from evaluation_sdk import EvaluationClient

context = train.get_context()
client = EvaluationClient.from_environment()
# 每个 worker 使用自己新建的临时目录；下载后校验完整 SHA-256。
model_path = client.download_model(worker_temp_dir / 'model.pth')
# 按固定的模型架构与配置加载模型，读取实际选定的数据划分，运行前向推理。
# 由具体评估器跨 worker 聚合指标；以下 metrics 必须来自实际计算。
if context.get_world_rank() == 0:
    client.report(metrics, slices=site_metrics, world_rank=0)
```

下载流式写入临时文件，完整 SHA-256 匹配后原子重命名；失败丢弃临时文件，目标已存在时拒绝覆盖。默认上限 20 GiB，`max_bytes` 可显式降低且不得超过 20 GiB；单次网络等待 30 秒，总下载期限 10 分钟。不允许重定向或自动重试。报告最多 256 KiB、128 个指标和 128 个切片。HTTP 错误不输出原始响应或凭据；不确定写入结果应先查询平台评估状态。

平台注入：`MODEL_EVALUATION_ID`、`MODEL_EVALUATION_MODEL_SHA256`、`MODEL_EVALUATION_DATASET_MANIFEST_SHA256`、`MODEL_EVALUATION_DATASET_SPLIT`、`MODEL_EVALUATION_DATASET_SAMPLE_COUNT`、`MODEL_EVALUATION_CONFIG_JSON`、`MODEL_EVALUATION_CONFIG_SHA256`、`MODEL_EVALUATION_EVALUATOR_ID`、`MODEL_EVALUATION_PROTOCOL`、`MODEL_EVALUATION_BASE_URL`。数据路径沿用 `PLATFORM_DATASET_MANIFEST_PATH` 和既有数据加载机制；遵守冻结的 split/sites，不把训练集读成测试集。SDK 验证配置字节摘要并自动填入报告归属字段，调用方只能提供 metrics/slices。

报告协议 `model-evaluation-report/v1`：每个 metric 为 `{name, value, unit, direction}`，direction 为 `higher`、`lower` 或 `neutral`；每个 slice 为 `{name, sampleCount, metrics}`。指标来自具体模型/数据评估器，后端继续校验有限数值、重复指标和冻结的模型/数据/评估器/配置指纹。报告被接受不等于评估成功：需要实际任务成功终态与有效报告共同确认。失败、取消、超时和缺报告分别由平台归档。

`smoke_evaluator.py` 只验证模型下载字节与报告链路，指标是实际校验过的模型字节数。它不读取数据样本、不加载模型、不计算准确率，不可作为 BEVFusion 或任何模型效果验收。完整 BEVFusion 评估器仍需使用匹配架构、依赖、数据schema和真实评测实现；此示例不声称支持任意 `.pth`。

测试仅在构建机执行：`python3 -m unittest discover -s examples/model-evaluation -p 'test_*.py'`。

评估任务继续以 `streaming` 保存固定数据版本与只读挂载，但可信评估入口给 managed driver 传 `--data-mode mount`。因此不会在评估脚本前创建或预热 `train` shard，也不提供 `train.get_dataset_shard('train')`。评估器必须通过冻结的 `PLATFORM_DATASET_MANIFEST_PATH`/`PLATFORM_DATASET_ROOT` 和 `MODEL_EVALUATION_DATASET_SPLIT` 显式读取 val/test；固定 sites 从 `PLATFORM_DATASET_SITES_JSON` 取得。只有 val/test、train 样本数为零的数据版本允许用于评估；普通训练仍要求 train 样本数为正。
