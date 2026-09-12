# 给对接方的 MLflow 接入交付单

本平台支持两种用途：**独立外部实验**先用 REST 创建实验和 Run，再用 REST 或 MLflow SDK 子集读写；**已有平台训练记录**使用 Job/Run 关联接口。请先选用途，不混用 scope、ID 或地址。

## 需要交付的内容

| 项目 | 给对接方的值或要求 |
| --- | --- |
| API Base URL | `https://raytrain.wellspiking.ai` |
| REST 版本 | `/api/v1`；HTTPS JSON，成功读取 `data`，失败读取 `error` 和 `request_id` |
| SDK 版本 | 固定 `mlflow==3.14.0`，仅支持本文列出的六个方法 |
| 外部实验 SDK Tracking URI | `https://raytrain.wellspiking.ai/api/v1/mlflow-tracking` |
| 身份 | 明确平台用户/集成身份、有效团队、用途、负责人、到期日 |
| 凭据 | 机器接入使用「实验中心 → 集成接入」独立身份、实验授权及短期凭据；个人 PAT 仍在「账户与安全」申请。通过获准秘密渠道交付，集成扩展以本轮发布验收为准 |
| 外部实验只读 scope | `experiments:read` |
| 外部实验读写 scope | 同时有 `experiments:read`、`experiments:write` |
| 产物读取/写入 | 读取为 `experiments:read` + `artifacts:read`；写入再加 `artifacts:write`；集成同时需要目标实验 read/artifacts 对应 grant，单纯文件上传不要求实验元数据 write |
| 目标记录 | REST 创建返回的**平台** Experiment ID 和 Run ID |
| 指标约定 | 名称、含义、单位、数据/代码版本、step 含义、毫秒 timestamp |
| 接口与调用工具 | [外部实验 OpenAPI](api/mlflow-external-tracking.openapi.json)、[完整合同](MLFLOW_EXTERNAL_TRACKING_API.md)、[Python REST 客户端](../examples/mlflow_integration/README.md) |

先在对接程序实际运行的机器上验证 DNS、443/TLS 和公司网络访问。用户浏览器连接 VPN 可以访问，不等于对接方服务器已经可达；不要关闭证书校验。不要交付内部 MLflow 地址、数据库或对象存储凭据、浏览器 Cookie。

个人 PAT 继续绑定用户和团队，只访问其本人资源，不是某个 Run 的专属令牌。**本轮新增独立集成身份与实验级 grant，当前实现待本轮发布验收确认。** 所有者在「实验中心 → 集成接入」创建身份、选择获准实验及权限、签发 1–30 天令牌；实际权限取令牌 scope 与实时实验 grant 的交集。默认不允许机器创建新实验，需要时由所有者明确开启。可分别撤销令牌、grant 或身份，不删除原实验。当前粒度为实验，尚无单 Run grant，也不扩展已有训练 Job API。不要共享管理员 PAT 代替机器授权。

管理上限：所有者 20 个身份（含撤销）、每身份 20 枚未撤销令牌、100 个实验 grant（含撤销历史）；令牌列表最多 100 条、未撤销优先。详见[集成与产物合同](MLFLOW_INTEGRATION_ARTIFACT_API.md)及[OpenAPI](api/mlflow-integration-artifacts.openapi.json)。接口存在不代表已为实际对接方创建或交付凭据。

## 独立外部实验：先创建，再读写

由秘密管理工具注入 `RAYTRAIN_PAT`，配置非敏感地址：

```bash
export RAYTRAIN_API='https://raytrain.wellspiking.ai'
curl -sS --fail-with-body \
  -H "Authorization: Bearer ${RAYTRAIN_PAT}" \
  "${RAYTRAIN_API}/api/v1/mlflow/capabilities"
```

核对 `available`、`sdkClientVersion`、`sdkCompatible` 与 `sdkMethods`。浏览器登录身份的 `write=false` 表示当前通道不能写，程序写入需使用正确 scope 的 PAT。

1. `POST /api/v1/mlflow/experiments`，正文 `{"name":"external-evaluation"}`，保存响应 `data.id` 为平台 Experiment ID。
2. `POST /api/v1/mlflow/experiments/{平台ExperimentID}/runs`，正文 `{"name":"candidate-run"}`，保存 `data.id` 为平台 Run ID。
3. 两次创建分别提供稳定的 `Idempotency-Key`（1–128 个字母、数字或 `._:-`）。同一次创建重试复用原键及原正文，新资源使用新键；所有 POST 均加 `Content-Type: application/json`。
4. 列表用 `GET /api/v1/mlflow/experiments` 或 `GET /api/v1/mlflow/experiments/{平台ExperimentID}/runs`；默认 50、最多 100 条，按响应 `data.nextCursor` 请求同一查询的下一页。
5. REST 读取 `GET /api/v1/mlflow/runs/{平台RunID}`；写入 `/log-batch`；结束 `/finish`，正文为 `{"status":"FINISHED"}`、`FAILED` 或 `KILLED`。

**REST 路径和 SDK 的 `run_id` 都使用平台 Run ID。** 返回的 `mlflowRunId` 仅用于核对上游记录；`mlflowExperimentId` 也不是平台 Experiment ID。独立外部 Run 没有训练 `job_id`，创建它不会申请 GPU 或启动训练。

SDK 示例仅针对已由 REST 创建、状态为 `RUNNING` 的专用测试 Run。设置 `RAYTRAIN_PLATFORM_RUN_ID` 为平台 Run ID：

```bash
export MLFLOW_TRACKING_URI="${RAYTRAIN_API}/api/v1/mlflow-tracking"
export MLFLOW_TRACKING_TOKEN="${RAYTRAIN_PAT}"
```

```python
import os
import time
from mlflow.tracking import MlflowClient

client = MlflowClient(tracking_uri=os.environ["MLFLOW_TRACKING_URI"])
run_id = os.environ["RAYTRAIN_PLATFORM_RUN_ID"]
client.get_run(run_id)
client.log_param(run_id, "external.evaluator_version", "v1")
client.log_metric(run_id, "external/quality_score", 0.91,
                  timestamp=int(time.time() * 1000), step=1)
client.set_tag(run_id, "external.source", "quality-service")
result = client.get_run(run_id)
assert result.data.metrics["external/quality_score"] == 0.91
# 全部写入完成后结束；终态不再允许追加或重开。
client.set_terminated(run_id, status="FINISHED")
```

支持的方法仅为 `get_run`、`log_batch`、`log_metric`、`log_param`、`set_tag`、`set_terminated`。get_run 提供有界的参数、安全自定义标签及最新指标视图；最新指标保留实际 timestamp/step，系统归属标签不对外返回。SDK 返回自身的 `error_code/message`，不使用 REST 响应信封。

不支持 SDK `create_experiment/create_run/start_run`、自动创建 Run 的 autolog、完整历史导出、Artifact 上传、模型注册、审批、Traces 或 Serving。`raytrain-disabled:` artifact URI 表示原生 SDK 文件通道未开放；本轮产物通过独立平台 REST 上传，不能调用 `mlflow.log_artifact()` 代替。原生 `/mlflow/` 是既有共享浏览器管理入口，不能用作本 SDK 地址，也不承诺与外部 API 同等的所有权隔离。

## 本轮扩展：受控 Run 产物

待对应版本部署验收后，可从「实验中心 → 外部实验 → Run 产物」浏览记录，机器使用 `/api/v1/mlflow/runs/{平台RunID}/artifacts` 初始化、读取状态与上传。上传 init 提供文件名、sizeBytes、完整 SHA256 和稳定 Idempotency-Key；按 1 起编号 PUT 固定 8 MiB 分片并带分片 SHA256，POST complete 完整校验后才成为不可变 READY。只允许在 RUNNING Run 中上传，**先完成文件再结束 Run**。

单文件大于 0 且不超过 20 GiB；每所有者逻辑预算 100 GiB、最多 16 个待完成上传、24 小时有效期。下载仅 READY，通过 `.../content`，调用方校验大小和 SHA256。断连先读取 uploadedParts，仅续传缺失部分，不改 key 盲目重放；READY 不覆盖，未完成上传可明确取消。此存储与原生 MLflow 共享目录、用户训练输出目录分离，不迁移个人数据，也不执行模型文件。

交付[标准库上传/续传/下载示例](../examples/mlflow_integration/integration_artifacts.py)和[运行说明](../examples/mlflow_integration/README.md)。receipt 仅记录非敏感平台 ID/hash/key，权限 0600；下载只创建明确的新文件路径，不覆盖已有文件。完整校验最长 15 分钟，文件成功不代表模型候选、独立评估或审批已完成。

## 已有平台训练记录：保留 Job/Run 接口

如果对接方要读取或补充已有训练，使用以下独立合同：

| 目标 | 权限与接口 |
| --- | --- |
| 查近期 Job/Run 关联 | `jobs:read`；`GET /api/v1/experiments?limit=100` |
| 读任务最新 Run | `jobs:read`；`GET /api/v1/jobs/{job_id}/experiment` |
| 精确读某个 Run | `jobs:read`；`GET /api/v1/jobs/{job_id}/mlflow/runs/{run_id}` |
| 补充参数/指标/标签 | `jobs:read` + `mlflow:write`；`POST /api/v1/jobs/{job_id}/mlflow/runs/{run_id}/log-batch` |

此处 `run_id` 是与 Job 可信关联的上游 MLflow Run ID，**与外部实验平台 Run ID 不同**；一个 Job 可有多个 Run，Job ID 与 Run ID 不要求相等。列表最多返回近期 100 条，并非全量分页；列表里没出现不代表记录不存在。

只允许向当前团队本人任务的合规 `RUNNING` Run 写入；管理员也不能代写他人的任务。没有 Run 时接口不会自动创建。训练记录接口不是 SDK Tracking Server，详见[训练接口合同](MLFLOW_INTEGRATION_API.md)和[对应 OpenAPI](api/mlflow-integration.openapi.json)。

## 首次联调与限制

- 使用获准的专用测试资源；不要向用户正在训练的 Run 写演示指标。先读、再写独立 `external/` 指标和自定义标签、再读回，最后验证终态拒写、只读 PAT 拒写及跨用户/团队拒绝。
- batch 最大 256 KiB，metrics/params/tags 各最多 100 条。参数不能覆盖；`platform.*`、`mlflow.*` 等系统字段保留。查询最多 20 个指标键、每键 500 点、100 个参数，是有界视图。
- 400/413 修正请求；401/403 修正凭据和权限；404 核对用途和 ID；409 核对状态、处理中操作或参数冲突；429 按 `Retry-After` 等待。
- 超时、502/503 可能已部分写入，先读取再决定是否重试；批量写不是事务或 exactly-once。重试保留原 key/value/step/timestamp，不生成新时间戳无条件重放。
- 保存测试时间、客户端版本、平台/上游 ID、request_id、响应状态和读回结果，不记录真实 PAT。到期或撤销后停止重试并更新获准凭据。

**2026-09-12 已用明确获准的 guofeng.su / local 一天 PAT 完成专用生产外部实验的 REST、SDK 六方法、读回、幂等、只读拒写与终态拒写验收；两枚验收 PAT 已立即撤销，并实查撤销后返回 401。** 详见[生产补验记录](HELP_SDK_ACCEPTANCE_20260912.md)。没有为对接方创建或发送长期凭据，实际调用方身份、部署机器网络和跨真实用户/团队隔离仍需按交付目标验收。此前版本与隔离测试见[阶段 B 发布记录](MLFLOW_EXTERNAL_TRACKING_VALIDATION_20260912.md)，不能把管理员本次验收等同于对接方已获准接入。
