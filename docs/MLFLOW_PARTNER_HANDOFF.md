# 调用 RayTrain MLflow API

RayTrain 已开放原生 MLflow Tracking API。你的程序可以直接用 MLflow SDK 或 HTTP 调用共享 MLflow，不需要先创建平台训练任务。Portal 中进入「实验中心」后，用「训练记录」查看 RayTrain Job 关联的 Run，用「MLflow API」复制下面的地址和示例；需要浏览完整页面时点击「打开 MLflow」。

```bash
pip install 'mlflow==3.14.0'
export MLFLOW_TRACKING_URI='https://raytrain.wellspiking.ai/api/v1/mlflow-native'
export MLFLOW_TRACKING_TOKEN="$RAYTRAIN_PAT"
```

`RAYTRAIN_PAT` 是你在「账户与安全」创建的个人 PAT。调用 MLflow 需要显式勾选 `mlflow:full`；旧 PAT 不会自动获得该权限。`mlflow:full` 对应共享 MLflow 的完整读写能力，包含实验、Run、Metric、Param、Tag、Artifact、删除和 Model Registry 操作。平台训练任务、个人目录、调度和受控数据空间仍使用 RayTrain 自身权限。

运行机器需要能访问 `raytrain.wellspiking.ai` 的 HTTPS/443，并验证证书。不要把浏览器 Cookie、数据库、对象存储凭据或集群内地址交给程序。

## HTTP：列实验、分页和读取 Run

原生 REST 路径是在 Tracking URI 后接 `/api/2.0/mlflow/...`，认证头为 `Authorization: Bearer <PAT>`。

```bash
export MLFLOW_API='https://raytrain.wellspiking.ai/api/v1/mlflow-native/api/2.0/mlflow'

curl -sS --fail-with-body -X POST \
  -H "Authorization: Bearer ${RAYTRAIN_PAT}" \
  -H 'Content-Type: application/json' \
  -d '{"max_results":100,"view_type":"ACTIVE_ONLY"}' \
  "${MLFLOW_API}/experiments/search"
```

如果响应里有 `next_page_token`，下一页把它作为请求里的 `page_token` 原样传回：

```bash
curl -sS --fail-with-body -X POST \
  -H "Authorization: Bearer ${RAYTRAIN_PAT}" \
  -H 'Content-Type: application/json' \
  -d '{"max_results":100,"view_type":"ACTIVE_ONLY","page_token":"REPLACE_NEXT_PAGE_TOKEN"}' \
  "${MLFLOW_API}/experiments/search"
```

从搜索结果中选择真实 `experiment_id` 后再查 Run：

```bash
curl -sS --fail-with-body -X POST \
  -H "Authorization: Bearer ${RAYTRAIN_PAT}" \
  -H 'Content-Type: application/json' \
  -d '{"experiment_ids":["REPLACE_EXPERIMENT_ID_FROM_SEARCH"],"max_results":100}' \
  "${MLFLOW_API}/runs/search"

curl -sS --fail-with-body \
  -H "Authorization: Bearer ${RAYTRAIN_PAT}" \
  "${MLFLOW_API}/runs/get?run_id=REPLACE_RUN_ID_FROM_SEARCH"

curl -sS --fail-with-body \
  -H "Authorization: Bearer ${RAYTRAIN_PAT}" \
  "${MLFLOW_API}/metrics/get-history?run_id=REPLACE_RUN_ID_FROM_SEARCH&metric_key=validation%2Faccuracy"

curl -sS --fail-with-body \
  -H "Authorization: Bearer ${RAYTRAIN_PAT}" \
  "${MLFLOW_API}/artifacts/list?run_id=REPLACE_RUN_ID_FROM_SEARCH&path=reports"
```

## SDK：查询实验、Run 和历史指标

```python
from mlflow import MlflowClient
from mlflow.entities import ViewType

client = MlflowClient()
selected_experiment_id = None
page_token = None
while True:
    page = client.search_experiments(
        max_results=100,
        page_token=page_token,
        view_type=ViewType.ACTIVE_ONLY,
    )
    for experiment in page:
        print(experiment.experiment_id, experiment.name)
        if experiment.name == "REPLACE_EXPERIMENT_NAME":
            selected_experiment_id = experiment.experiment_id
    page_token = page.token
    if not page_token:
        break

# 需要包含已删除实验时，把 view_type 改成 ViewType.ALL。
if selected_experiment_id is None:
    raise SystemExit("没有找到目标实验，请从上面输出选择已有 Experiment ID")

run_token = None
while True:
    runs = client.search_runs(
        experiment_ids=[selected_experiment_id],
        max_results=100,
        page_token=run_token,
    )
    for run in runs:
        print(run.info.run_id, run.info.status, run.data.params, run.data.metrics)
        print(client.get_metric_history(run.info.run_id, "validation/accuracy"))
        print(client.list_artifacts(run.info.run_id, "reports"))
    run_token = runs.token
    if not run_token:
        break
```

`max_results` 是每页大小，不是总量上限。SDK 返回 `page.token` 或 `runs.token` 时继续传给下一次调用的 `page_token`。

## SDK：写参数、指标和文件

先在专用测试实验里联调，不要向正在训练的 Run 写演示数据。

```python
import tempfile
import uuid
from pathlib import Path
import mlflow
from mlflow import MlflowClient

mlflow.set_experiment("program-demo-" + uuid.uuid4().hex)
with mlflow.start_run(run_name="first-connection") as run:
    mlflow.log_param("code_version", "your-git-commit")
    mlflow.log_param("dataset_version", "your-dataset-version")
    for step, score in enumerate([0.82, 0.87, 0.91], start=1):
        mlflow.log_metric("validation/accuracy", score, step=step)
    with tempfile.TemporaryDirectory() as directory:
        report = Path(directory) / "report.txt"
        report.write_text("connection test\n", encoding="utf-8")
        mlflow.log_artifact(str(report), artifact_path="reports")
    run_id = run.info.run_id

client = MlflowClient()
print(client.get_run(run_id).data.metrics)
print(client.get_metric_history(run_id, "validation/accuracy"))
print(client.list_artifacts(run_id, "reports"))
local_path = client.download_artifacts(run_id, "reports/report.txt")
print(local_path)
```

上传普通文件用 `log_artifact`；保存符合 MLflow 格式的模型用对应框架的 `log_model`。Model Registry 可用 `create_registered_model`、`create_model_version`、alias 等原生接口管理版本。注册版本不会自动部署为生产推理服务。已有实验若显式配置了非代理 artifact URI，其文件仍按原存储配置访问，RayTrain 不会迁移旧文件。

## ID 怎么对应

- **Job ID** 是 RayTrain 训练任务 ID，用于队列、日志、产物、调试和任务详情。
- **Experiment ID** 和 **Run ID** 是 MLflow 原生 ID，用于 SDK 和 HTTP API。
- 一个 RayTrain Job 可以关联多个 MLflow Run，Job ID 不等于 Run ID。
- 在「训练记录」里查看平台训练和 Run 的关联；程序读写 MLflow 时使用原生 Experiment ID / Run ID。

## 常见错误

| 状态 | 先检查什么 |
| --- | --- |
| 401 | PAT 是否有效、过期或已撤销；环境变量是否传到了当前进程 |
| 403 | PAT 是否包含 `mlflow:full`；当前账号是否允许访问共享 MLflow |
| 404 | Experiment ID / Run ID 是否来自当前 Tracking URI 的搜索结果 |
| 429 | 按 `Retry-After` 等待后重试 |
| 502 / 503 / 超时 | 结果可能已部分生效，先读回确认，再决定是否重试 |

不要在 URL、日志、截图、Issue 或聊天里写入 PAT。需要排查时提供不含凭据的请求时间、接口路径、HTTP 状态、错误响应和相关 Experiment ID / Run ID。
