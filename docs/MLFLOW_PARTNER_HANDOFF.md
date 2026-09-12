# 给对接方的 MLflow 接入交付单

对接方可以直接使用平台现有的全部共享 MLflow，原生 SDK/REST 入口已上线。无需先创建“外部实验”，也无需把已有训练重新导入另一套接口。平台页面的「实验中心 → API 接入」提供同一套接入说明；版本与测试结果见[验收记录](MLFLOW_NATIVE_HELP_VALIDATION_20260912.md)。

## 交付这四项即可

| 项目 | 内容 |
| --- | --- |
| Tracking URI | `https://raytrain.wellspiking.ai/api/v1/mlflow-native` |
| 凭据 | 调用方获准平台账号在「账户与安全」创建的 `mlflow:full` PAT；通过秘密管理渠道交付，注明负责人和到期日 |
| 客户端 | `mlflow==3.14.0`，使用标准 `MlflowClient` 或 fluent API |
| 数据约定 | 要使用的实验名称/原生 Experiment ID、指标含义与单位、step、代码和数据版本；查询全部实验时不需要预先指定某个 Job |

`mlflow:full` 表示共享 MLflow 的完整读写能力，包含实验、Run、Metric、Param、Tag、Artifact、删除和 Model Registry 操作。它与现有共享网页的资源范围一致，不按平台用户或训练 Job 过滤。旧令牌不会自动获得该权限；平台训练任务、个人目录、调度和受控数据空间仍使用各自权限。

生产已验证全量实验查询、专用 Run 写入、指标历史、文件上传下载及令牌撤销。Model Registry 的版本和 alias 操作已通过隔离 SDK 验证；这不表示已有模型部署为生产推理服务。

对接方运行机器需要能访问上述域名的 HTTPS/443，并验证证书；需要公司网络的机器应连接公司 VPN。不要提供浏览器 Cookie、集群内地址、数据库或对象存储凭据。程序只需设置：

```bash
export MLFLOW_TRACKING_URI='https://raytrain.wellspiking.ai/api/v1/mlflow-native'
# MLFLOW_TRACKING_TOKEN 由秘密管理工具注入，不把真实令牌写进代码或聊天。
```

## 查看全部实验和 Run

```python
from mlflow import MlflowClient

client = MlflowClient()
page_token = None
while True:
    page = client.search_experiments(max_results=100, page_token=page_token)
    for experiment in page:
        print(experiment.experiment_id, experiment.name)
    page_token = page.token
    if not page_token:
        break

# 对选定实验查询 Run；继续用返回的 token 翻页即可查询完整列表。
runs = client.search_runs(experiment_ids=["1"], max_results=100)
for run in runs:
    print(run.info.run_id, run.info.status, run.data.metrics)
```

默认查询活动记录；需要已删除记录时使用 MLflow 原生 `ViewType.ALL`。查询上限是分页大小，不是平台近期 100 条训练记录窗口。这里返回的是原生 MLflow ID，直接传给 `get_run`、`get_metric_history`、`log_metric` 等方法。

## 写入新的联调实验

在新建测试实验中联调，不向正在训练的 Run 写示例数据。下面会创建一个新的实验和 Run：

```python
import tempfile
import uuid
from pathlib import Path
import mlflow

mlflow.set_experiment("integration-demo-" + uuid.uuid4().hex)
with mlflow.start_run(run_name="first-connection") as run:
    mlflow.log_param("code_version", "your-git-commit")
    mlflow.log_metric("validation/accuracy", 0.91, step=1)
    with tempfile.TemporaryDirectory() as directory:
        report = Path(directory) / "report.txt"
        report.write_text("connection test\n", encoding="utf-8")
        mlflow.log_artifact(str(report), artifact_path="reports")
    print(run.info.run_id)
```

上传普通文件用 `log_artifact`；保存符合 MLflow 格式的模型用对应框架的 `log_model`。Model Registry 可用 `create_registered_model`、`create_model_version`、alias 等原生接口管理版本。注册版本不会自动部署推理服务，也不代表已经经过平台独立评估和审批。已有实验若显式配置了非代理 artifact URI，其文件仍按原存储配置访问，本次不会迁移旧文件。

原生 REST 路径在 Tracking URI 后接 `/api/2.0/mlflow/...`，认证头为 `Authorization: Bearer <PAT>`，返回 MLflow 原生 JSON。原生方法、字段与客户端说明参见 [MLflow Client 官方文档](https://mlflow.org/docs/latest/api_reference/python_api/mlflow.client.html)。

## Job ID、Run ID 与独立实验

- **Job ID** 标识平台训练任务；**Run ID** 标识 MLflow 的一次记录。一个 Job 可以关联多个 Run，两者无需相等。已有训练通过平台关联查看对应 Run；程序访问原生 MLflow 使用原生 Run ID。
- **独立实验**原名“外部实验”，表示不用创建平台训练 Job、通过可选平台接口管理的实验。它不申请 GPU，也不表示这是全部 MLflow。
- 原生共享入口能看到上游全部实验；可选平台 REST/六方法 SDK 返回自身创建或明确授权的记录。其受控产物独立存放，不会自动变成原生 Run 的 Artifact 文件。

仅当对接确实需要限制到指定实验时，才使用「集成接入」及[独立实验接口](MLFLOW_EXTERNAL_TRACKING_API.md)、[实验授权与受控产物](MLFLOW_INTEGRATION_ARTIFACT_API.md)。这些接口保留兼容，不是标准 MLflow 接入的前置步骤。

## 首次联调

先列出实验、读取一个已有 Run；再在专用新实验中记录参数、指标、文件，结束并读回。核对文件大小/摘要与 `get_metric_history`；最后验证测试令牌撤销后失效。不要在日志中记录令牌。

本轮原生共享 MLflow、集成身份、受控文件和六方法 SDK 的生产证据分别见[原生共享验收](MLFLOW_NATIVE_HELP_VALIDATION_20260912.md)与[集成能力验收](MLFLOW_INTEGRATIONS_VALIDATION_20260912.md)。实际对接方的账号、凭据交付与运行机器网络仍需按其部署目标落实，本文不表示已经向第三方发送凭据。
