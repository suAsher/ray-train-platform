# 独立评估 worker SDK

`evaluation_sdk.py` 使用 Python 标准库。将它和评估程序一起打包为源码 ZIP，经平台上传后登记为共享评估方案。平台保存独立不可变的代码快照，评估 Worker 通过内网和本次任务凭据取得源码，无需训练节点连接 Git 或外网。运行镜像只提供 Ray、框架及业务依赖，不打包用户代码。SDK 不创建训练、不读取个人 PAT、不修改配额。

## 准备和登记源码

维护者在本机或构建机准备业务评估程序。ZIP 根目录应包含入口文件和 `evaluation_sdk.py`，需要导入的业务模块也一并放入；不要多包一层外部目录。示例布局：

```text
evaluator.zip
├── evaluate.py
├── evaluation_sdk.py
└── model_impl/
```

在已整理好的源码目录中打包，例如：

```bash
python3 -m zipfile -c ../evaluator.zip evaluate.py evaluation_sdk.py model_impl
```

ZIP 文件不得超过 64 MiB。只放入已审阅的源码和所需非敏感配置，排除 `.git`、缓存、数据、模型权重和凭据；依赖预先安装在已登记镜像中，脚本运行时不要依赖外网安装。

平台管理员（SuperAdmin）在「实验中心 → 评估 → 评估方案」选择“上传源码 ZIP”并创建方案，核对文件名和 SHA-256，配置已登记镜像、入口参数、数据 schema 和报告协议。上传复用平台源码 relay；登记接口 `POST /api/v1/model-evaluators` 通过 `sourceArtifactId` 引用上传结果，平台再保存独立源码快照。不要把本地路径、对象存储地址或 ZIP 文件名当作 artifact ID。入口按 ZIP 内路径填写，例如 `entryPoint: ["python", "evaluate.py"]`。代码和镜像摘要固定后不可修改；变更代码应上传新 ZIP 并创建新方案。登记前确认共享代码的用途：它会在成员选定的权重和数据上执行。

已有 Git 方案保持原合同，仍依赖训练节点访问对应仓库，不会自动迁移。要在不通 Git 的内网环境运行，应重新上传源码 ZIP 并登记新方案。

普通成员不需要创建方案或取得管理员 PAT：从「实验中心 → 模型」打开未归档模型的 READY 版本，点击“发起评估”，选择启用且适配模型与数据的方案、具体 READY 数据版本和非空 val/test 划分，再点击“检查固定来源”。预检不会创建任务；确认后点击“确认并创建评估”才提交 API 请求并进入配额与排队流程。`sourceArtifactId` 是方案登记字段，不由普通用户在每次评估中任意替换。

业务方案必须实现正确的模型结构、权重加载、预处理、类别映射、数据读取和指标计算。没有匹配方案时先补齐这些实现；不能承诺任意 `.pth` 都可由同一个示例评估。数据集定义与发布由 TenantAdmin（本团队 TEAM 数据）或 SuperAdmin 管理；普通成员只选择可见的 READY 版本。

## 在评估 Worker 中调用 SDK

必须在 managed Ray Train worker 内调用 `EvaluationClient.from_environment()`。若 ZIP 根目录放置本仓库的 `smoke_evaluator.py`，冒烟入口为 `python smoke_evaluator.py`；业务入口按实际 ZIP 布局填写。平台以 `raytrain-managed ... -- python ...` 包装入口；现有 managed driver 的 TorchTrainer 在 worker loop 执行该文件。不要在 RayJob driver 直接执行此文件，也不要在用户入口再次创建嵌套 TorchTrainer。它读取平台注入的 `MODEL_EVALUATION_*` 固定元数据与 `RAYTRAIN_EVENT_TOKEN_FILE` Secret 文件。driver 或本地 shell 缺少这些信息会报错。不要复制 Secret 到配置、日志或环境字符串。

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

评估任务继续以 `streaming` 保存固定数据版本与只读挂载，但可信评估入口给 managed driver 传 `--data-mode mount`。因此不会在评估脚本前创建或预热 `train` shard，也不提供 `train.get_dataset_shard('train')`。评估器必须通过冻结的 `PLATFORM_DATASET_MANIFEST_PATH`/`PLATFORM_DATASET_ROOT` 和 `MODEL_EVALUATION_DATASET_SPLIT` 显式读取 val/test；固定 sites 从 `PLATFORM_DATASET_SITES_JSON` 取得。当前评估固定所选划分的全部场地，不接受 `latest` 或空划分；仅有 val/test、train 样本数为零的数据版本也允许用于评估，普通训练仍要求 train 样本数为正。

PUBLIC 数据集的评估结果对平台成员可见；TEAM 数据集结果按数据集所属团队授权，平台管理员也可查看。有权读取评估的发起者、评估所属团队的 TenantAdmin 或 SuperAdmin 可以取消尚未结束的记录，包括 CREATING。权重、代码、训练数据来源和本次评估数据分别固定，评估不会覆盖历史训练来源。评估报告也不等同于审批或 Serving 发布。
