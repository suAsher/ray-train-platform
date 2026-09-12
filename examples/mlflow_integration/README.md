# RayTrain MLflow REST 客户端

本页 `client.py` 继续服务已有训练 Job/Run。新增独立外部实验请参阅 [外部实验接口与 SDK 子集](../../docs/MLFLOW_EXTERNAL_TRACKING_API.md) 和本目录 `external_tracking.py`；它们使用显式 `experiments:read/write`，不会扩展已有训练令牌。

Python 3.10+，仅依赖标准库。用于已开放的实验列表、指定 Run 读取和 `log-batch` 写入；不是完整 MLflow SDK，也不会创建 Experiment、Run 或训练任务。

完整权限与请求合同见 [MLflow 外部对接说明](../../docs/MLFLOW_INTEGRATION_API.md)。先取得获准的任务 ID、Run ID 和短期 PAT。读取需要 `jobs:read`；写入同时需要 `jobs:read`、`mlflow:write`，且只能写当前团队本人任务的已有 RUNNING Run。其他管理员不能代写。

## 凭据与地址

通过获准的秘密管理方式把 PAT 注入 `RAYTRAIN_PAT` 环境变量。不要把 PAT 放入命令参数、URL、代码或日志。脚本不会打印该环境变量，错误中的相同字符串也会脱敏。

每次明确传入 `--base-url https://raytrain.wellspiking.ai`。地址必须是 HTTPS origin，不能携带账号、路径、查询参数或 fragment。客户端拒绝所有重定向，防止凭据被转发；不要改成 `/mlflow/` 浏览器地址或集群内 Tracking URI。

## 只读命令

在仓库根目录运行，下列 `$JOB_ID` 和 `$RUN_ID` 应由双方明确约定：

```bash
python3 examples/mlflow_integration/client.py \
  --base-url https://raytrain.wellspiking.ai list --limit 100

python3 examples/mlflow_integration/client.py \
  --base-url https://raytrain.wellspiking.ai read \
  --job-id "$JOB_ID" --run-id "$RUN_ID"
```

列表只返回最近记录窗口，上限 100；输出 `complete: false`。Run 不在窗口中，不代表它不存在或调用方没有权限。已有获准 Job/Run 对时直接使用 `read`。读取数据本身也受平台 20 个指标键、每键 500 点等上限约束，不能当作完整训练历史导出。

## 明确写入一次

准备一个本地 `batch.json`，只放本次获准写入的数据，例如：

```json
{
  "tags": [{"key": "external.source", "value": "quality-service"}]
}
```

以下命令会对指定 Run 写入一次；没有这个子命令不会写入：

```bash
python3 examples/mlflow_integration/client.py \
  --base-url https://raytrain.wellspiking.ai log-batch \
  --job-id "$JOB_ID" --run-id "$RUN_ID" --payload ./batch.json
```

文件只读，不上传文件本身，也不把路径当作 URL 下载。最多 256 KiB，必须是无重复字段的严格 JSON，非有限数值被拒绝。服务端继续校验归属、系统保留键和参数/指标/标签限制。只对专门获准的测试 Run 联调，勿向正在训练的用户 Run 写入示例数据。

## 错误与嵌入使用

CLI 成功退出码为 0，请求或输入失败为 1，参数用法错误为 2。错误输出包含 HTTP 状态、平台 `error.code`、`error.message` 和 `request_id`，不输出原始响应或网络诊断。程序接入可捕获 `PlatformAPIError`，分别读取 `status`、`code`、`message`、`request_id` 和 `retry_after`。

`retry_after` 保留服务端 `Retry-After` 的有效非负整数秒数（0～2147483647）；缺失、重复、日期格式或无效值返回 `None`。有效值也会显示在 CLI 错误中。调用方可以据此决定等待时间，客户端自身不会等待或自动重试。

```python
import os
from examples.mlflow_integration.client import PlatformAPIError, RayTrainMLflowClient

integration = RayTrainMLflowClient(
    "https://raytrain.wellspiking.ai", os.environ["RAYTRAIN_PAT"]
)
# job_id、run_id 来自调用方明确授权的目标配置。
experiment = integration.read_run(job_id, run_id)
```

客户端对读取和写入都不自动重试。遇到 429 先按服务端规则等待；写入出现 409、502、503 或网络超时，可能已有部分数据生效，应先读取核对。batch 不是事务，不能承诺 exactly-once；重试应保留原来的 key/value/step/timestamp，不能每次重造时间戳。

## 隔离测试

测试使用标准库 mock HTTPS transport，不访问生产、不需要真实 PAT。遵循项目发布规则，在构建机候选目录运行：

```bash
python3 -m unittest discover -s examples/mlflow_integration -p 'test_*.py' -v
```

## 独立集成与受控产物（新增 REST 示例）

`integration_artifacts.py` 面向**已存在的平台外部 Run**，不接受训练 Job ID 或上游 MLflow Run ID，不创建训练、不结束 Run，也不管理凭据。完整身份/grant/文件合同见[集成身份与受控产物](../../docs/MLFLOW_INTEGRATION_ARTIFACT_API.md)。是否已上线以发布验收记录为准；这不是 MLflow SDK Artifact API。

通过秘密管理渠道注入获准的 `RAYTRAIN_PAT`（集成令牌也使用此变量），配置 `RAYTRAIN_API=https://raytrain.wellspiking.ai`。上传需 scope 与目标实验 grant 同时允许产物读写；读取需产物读权限。先上传完成，再结束 Run。

```bash
# 文件只读；第一次必须指定不存在的 receipt 路径。
python3 examples/mlflow_integration/integration_artifacts.py upload \
  --run-id "$PLATFORM_RUN_ID" --file ./checkpoint.pt \
  --idempotency-key quality-checkpoint-v1 \
  --receipt ./checkpoint-upload.json

# 中断后先查状态，再显式续传：保留相同文件、Run、key、receipt。
python3 examples/mlflow_integration/integration_artifacts.py status \
  --run-id "$PLATFORM_RUN_ID" --artifact-id "$ARTIFACT_ID"
python3 examples/mlflow_integration/integration_artifacts.py upload --resume \
  --run-id "$PLATFORM_RUN_ID" --file ./checkpoint.pt \
  --idempotency-key quality-checkpoint-v1 \
  --receipt ./checkpoint-upload.json

# 仅 READY 可以下载；输出必须是新文件，下载后校验整文件SHA256。
python3 examples/mlflow_integration/integration_artifacts.py download \
  --run-id "$PLATFORM_RUN_ID" --artifact-id "$ARTIFACT_ID" \
  --output ./downloaded-checkpoint.pt

# 只读列举；nextCursor 表示还有下一页，下次加 --cursor "$NEXT_CURSOR"。
python3 examples/mlflow_integration/integration_artifacts.py list \
  --run-id "$PLATFORM_RUN_ID"

# 明确取消一个未完成上传，不删除 READY 或现有训练文件。
python3 examples/mlflow_integration/integration_artifacts.py cancel \
  --run-id "$PLATFORM_RUN_ID" --artifact-id "$ARTIFACT_ID"
```

单文件最大 20 GiB，固定 8 MiB 分片；示例流式计算整文件与分片 hash，内存不会读入完整文件。每片相同编号/大小/hash 可重传；示例先读取状态，只发送缺失片，不自动重试任何网络失败。源文件上传中变化时拒绝 complete。receipt 不含 PAT，以 0600 保存。初始化超时且 receipt 尚无 artifact ID 时，先 list 核对状态，再决定是否用原 key/正文显式重试；不要换 key 盲目创建第二份。

示例 list 每页 20 条，避免大量分片元数据超过客户端有界 JSON 读取；如返回 nextCursor，显式传 `--cursor` 读取下一页。

完整校验和下载最长按服务端 15 分钟限制；中断或 hash 不匹配不算成功。下载不会覆盖已有路径，失败删除本次创建的不完整文件。错误日志不输出令牌及原始响应。现有 `client.py`、`external_tracking.py` 和六方法 SDK 子集继续保持原合同。
