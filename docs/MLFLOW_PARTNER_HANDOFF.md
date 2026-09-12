# 给对接方的 MLflow 接入交付单

先按对接用途选择合同：**已有训练记录**使用下表保留的 Job/Run 接口；**独立外部实验**使用 [外部实验接口](MLFLOW_EXTERNAL_TRACKING_API.md)，通过 REST 创建资源，再用 REST 或受控 MLflow 3.14.0 SDK 子集读写。新增能力的上线状态以 [发布验证记录](MLFLOW_EXTERNAL_TRACKING_VALIDATION_20260912.md) 为准。两者都不提供完整 SDK、模型文件上传、Registry 写入或启动推理服务。

独立外部实验需要另行提供 `experiments:read` / `experiments:write` PAT、平台 Experiment/Run ID、`https://raytrain.wellspiking.ai/api/v1/mlflow-tracking`（SDK 专用前缀）、指标合同及 [新 OpenAPI](api/mlflow-external-tracking.openapi.json)。SDK 的 `run_id` 是**平台 Run ID**；返回的 `mlflowRunId` 仅用于核对原生 MLflow 记录。外部实验 REST/SDK 只授权当前团队本人资源，原生 MLflow 管理界面仍是既有共享管理入口，并不承诺同等隔离。

## 交付内容

| 内容 | 给对接方的值或说明 |
| --- | --- |
| API Base URL | `https://raytrain.wellspiking.ai` |
| 协议 | HTTPS REST JSON，平台 success/data/error 响应信封 |
| 认证 | `Authorization: Bearer <PAT>`；真实 PAT 通过获准秘密渠道单独交付 |
| 读权限 | `jobs:read` |
| 读写权限 | 同时具备 `jobs:read` 和 `mlflow:write` |
| 身份与团队 | 填写令牌所属平台身份、当前有效团队、用途、负责人、到期日 |
| 目标记录 | 明确的 `job_id` 与 `run_id`，由资源所有者确认 |
| 指标合同 | 指标名、含义、单位、训练/评估数据版本、step 含义、毫秒 timestamp |
| 接口定义 | [OpenAPI JSON](api/mlflow-integration.openapi.json)，可导入支持 OpenAPI 的调用工具 |
| 调用工具 | [Python REST 客户端](../examples/mlflow_integration/README.md)，Python 3.10+ 标准库，无需安装 MLflow |
| 完整约束 | [请求、错误及重试合同](MLFLOW_INTEGRATION_API.md) |

先用对接方运行程序的机器验证 DNS、443/TLS 和公司网络访问。浏览器在 VPN 下可访问，不能直接证明对接方服务器同样可达。不要关闭证书校验，也不要把管理员浏览器 Cookie 当作 API 凭据。

## 身份和授权必须先确定

当前 PAT 绑定一个平台用户和团队；它不是“只允许某个 Job/Run”的专属令牌。`jobs:read` 可读取该身份按角色原本有权查看的任务；`mlflow:write` 可写该身份当前团队下本人任务的合规 RUNNING Run。仅把某个 Run ID 告诉对方，不会把 PAT 的能力限制在该 Run。

因此：

1. 如果对接方读取/写入**其本人平台任务**，由其身份申请独立用途、短有效期的 PAT。
2. 如果外部系统需**代写你的任务**，必须明确该系统获准使用何种身份和范围。当前尚无按应用、按 Run 的委托授权；不能给对方自己的 PAT 后假定它能写你的任务，也不应共享管理员 PAT。
3. 如果只允许访问**某个项目或少数 Run**，应先落地资源 grant/集成身份，当前普通 PAT 无法表达这种范围。服务账号与委托授权已纳入 [后续设计](superpowers/specs/2026-09-12-mlflow-lifecycle-design.md)，尚未实现。

令牌由所有者在 Portal“账户与安全 → 创建访问令牌”选择“实验读写”；不勾选无关训练提交权限。到期/撤销由令牌所有者管理；撤销后对接方应停止重试并更新获准凭据。本文没有创建或发送真实令牌。

## 当前接口

| 操作 | 方法与路径 |
| --- | --- |
| 找到可见的 Job/Run 对 | `GET /api/v1/experiments?limit=100` |
| 读取某任务最新 Run | `GET /api/v1/jobs/{job_id}/experiment` |
| 精确读取指定 Run | `GET /api/v1/jobs/{job_id}/mlflow/runs/{run_id}` |
| 写入该 Run | `POST /api/v1/jobs/{job_id}/mlflow/runs/{run_id}/log-batch` |

列表只返回最近记录窗口，最多 100 条，不是全量分页接口。结果里没找到某个 Run，不足以证明其不存在或无权限。已知 Job/Run 对应使用精确读取；一个 Job 可以有多个 Run，两个 ID 不应相等。

示例写入体（仅合同示例，不会自动执行）：

```json
{
  "metrics": [
    {"key": "external/quality_score", "value": 0.87, "timestamp": 1789171200000, "step": 100}
  ],
  "params": [{"key": "external.evaluator_version", "value": "v1"}],
  "tags": [{"key": "external.source", "value": "quality-service"}]
}
```

timestamp 在首次产生数据时填写，重试保留原值；step 由双方约定，不能用每次 HTTP 调用次数冒充训练步。自定义指标采用 `external/` 前缀，避开训练主进程的参数与指标。

## 首次联调步骤

1. 提供获准身份、团队、令牌到期日、明确测试 Job/Run；不得把用户正在训练的 Run 当演示写入目标。
2. GET 精确 Run，核对响应 Run ID、状态、参数和已有指标。保存 request_id，不记录 PAT。
3. 对明确获准的测试 Run 写一个独立命名的指标和标签，再读取指标确认。参数具有不可覆盖约束，不能反复改同一个参数。
4. 验证只读 PAT 写入被拒绝、其他用户或团队的 Run 被拒绝、终态 Run 写入返回 409。
5. 对 429 按 Retry-After 等待；502/503/超时可能已经部分生效，先查后重试，不无条件重放。
6. 记录测试时间、客户端版本、Job/Run、request_id、响应状态和读回结果。与对接方确认后结束联调，按用途撤销测试凭据。

真实生产联调尚未完成。现有精确读取已通过线上登录身份验证，写入协议此前通过隔离 MLflow 3.14 服务测试；这些不等于外部系统已经获得访问权限。

## 如果对方要求官方 MLflow SDK

要先确认其实际调用，例如 `MlflowClient.get_run/log_batch`、`mlflow.start_run/log_model/log_artifact`、Registry 或 Serving。当前不要提供一个猜测的 `MLFLOW_TRACKING_URI`，也不要把 `/mlflow/` 管理页面地址或集群内服务地址给他。

官方 SDK 兼容网关、Artifact 代理、模型版本和审批的详细方案见 [生命周期设计](superpowers/specs/2026-09-12-mlflow-lifecycle-design.md)。以逐项端点和固定版本真实测试定义兼容范围，不能用“支持 MLflow”概括所有能力。
