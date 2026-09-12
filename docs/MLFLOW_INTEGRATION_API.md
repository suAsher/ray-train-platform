# MLflow 外部对接说明

本文保留**已有训练 Job/Run** 的兼容合同。独立外部实验的创建、分页和受控 SDK 子集请使用 [外部实验接口](MLFLOW_EXTERNAL_TRACKING_API.md)。两种用途的 scope、ID 和路径不同，不要混用；新增能力的上线状态以该文档及发布验证记录为准。

本说明对应已部署后端 `release-20260912-01-3800b753`（Helm revision 212、schema 45）。精确 Run 读取已用线上登录身份验证；读写协议已通过隔离 MLflow 3.14 验证，尚未向生产 Run 写入演示数据。具体证据与未完成项见 [发布验证记录](QUOTA_MLFLOW_VALIDATION_20260912.md)。现有 `/api/v1/experiments` 与 `/api/v1/jobs/{job_id}/experiment` 仍保持兼容。

## 需要交给对接方什么

可直接使用 [对接交付单](MLFLOW_PARTNER_HANDOFF.md)、[OpenAPI 定义](api/mlflow-integration.openapi.json) 和 [Python REST 调用工具](../examples/mlflow_integration/README.md)。后续完整功能规格见 [模型生命周期设计](superpowers/specs/2026-09-12-mlflow-lifecycle-design.md)；未实现的接口不能作为当前服务地址交付。

1. 平台 API 服务地址：`https://raytrain.wellspiking.ai`，请求路径以下表为准。所有调用使用 HTTPS。
2. 对接方获准使用的平台身份与团队；令牌绑定创建时的团队。读写本人训练记录时，应由该任务所有者创建专门用于此次集成的 PAT，不共享管理员账号。
3. 独立短期 PAT：在新 Portal“账户与安全 → 创建访问令牌”选择“实验读写”。scope 为 `jobs:read` 和 `mlflow:write`；仅查询时选择“实验只读”（`jobs:read`）。令牌通过获准的秘密传递渠道交付，不放在文档、URL、代码仓库或日志中。
4. 一个对方有权访问的 `job_id` 和明确的 `run_id`，以及双方约定的参数、指标、step 和时间戳含义。
5. 这份请求合同、错误与重试说明、令牌到期/撤销方式。未给对接方创建账号或令牌前，不能宣称它已获访问权限。

`job_id` 是 RayTrain 调度和资源标识；`run_id` 是 MLflow 实验记录标识，通常为 32 位小写十六进制。两者不应相等，一个任务可能产生多个 Run。先用实验列表获取关联，再按具体 Run 查询；旧任务实验接口只返回最新 Run。

当前 PAT 没有按单个 Job/Run 限权，也不是第三方委托凭据。它允许该用户与团队下按角色及 scope 获准的操作；若对接方只应访问少数指定 Run，或需代写他人的任务，必须先建设资源 grant/集成身份，不能认为仅提供某个 Run ID 就限制了令牌权限。

不要提供集群内 `MLFLOW_TRACKING_URI`、数据库连接、对象存储密钥或 `/mlflow/` 浏览器 Cookie。本文训练接口是 RayTrain REST 合同，不能把平台根地址当作完整 SDK Tracking Server。新版外部实验 SDK 使用独立 `/api/v1/mlflow-tracking` 前缀，仅支持 [明确列出的子集](MLFLOW_EXTERNAL_TRACKING_API.md)，并使用 REST 预创建的**平台 Run ID**。

## 请求合同

所有请求使用 `Authorization: Bearer <PAT>`。POST 使用 `Content-Type: application/json`。响应保持平台信封，成功读取 `data`，失败读取 `error.code`、`error.message` 和 `request_id`。

| 方法与路径 | 权限 | 结果 |
| --- | --- | --- |
| `GET /api/v1/experiments?limit=100` | `jobs:read` | 当前身份可见的可信 Job/Run 关联；最近记录窗口，上限 100，非全量导出 |
| `GET /api/v1/jobs/{job_id}/experiment` | `jobs:read` | 该任务最新 Run；兼容旧客户端 |
| `GET /api/v1/jobs/{job_id}/mlflow/runs/{run_id}` | `jobs:read` | 精确 Run、参数、最新指标和受限长度的指标序列 |
| `POST /api/v1/jobs/{job_id}/mlflow/runs/{run_id}/log-batch` | 同时有 `jobs:read`、`mlflow:write` 的 PAT | 仅向当前团队本人任务的已有 RUNNING Run 写参数、指标、自定义标签 |

读权限沿用平台任务归属规则：普通用户读本人任务，管理员按平台既有授权范围读取。写权限更窄，管理员也不能代写别人的 Run。历史合法 Run 若缺少旧版本未写入的 tenant/submitter 标签仍可只读；写入必须通过全部关联校验。未开始 MLflow 记录的任务不会因此自动创建 Run。

POST 请求体示例（metric 时间戳单位毫秒，step 是明确的训练步或评估步）：

```json
{
  "metrics": [{"key": "external/quality_score", "value": 0.87, "timestamp": 1789171200000, "step": 100}],
  "params": [{"key": "external.evaluator_version", "value": "v1"}],
  "tags": [{"key": "external.source", "value": "quality-service"}]
}
```

单次最大 256 KiB；metrics、params、tags 各最多 100 条。键最长 128 字节，仅允许英文字母、数字、点、下划线、连字符与斜杠；参数值最长 1024 字节，标签值最长 5000 字节。数值必须有限，step 和 timestamp 必须是非负整数，timestamp 不超过 253402300799999；指标必须明确提供 value、step、timestamp。字段名称严格区分大小写，重复 JSON 字段或重复参数/标签键被拒绝。系统字段和 `platform.*`、`mlflow.*` 命名空间保留；不允许传入 run_id、tenant_id 等字段覆盖 URL 或服务端归属。不得把密码、访问令牌或内部地址写进参数/标签。

读取结果最多包含 20 个有效指标键、每个指标最多 500 点、100 个参数；这是现有 UI 查询的有界视图，不适合声称已导出全部训练历史。缺失值保持未知，原始指标键不同就按不同指标处理。不同数据集/评测协议下的数值不能直接用来判定模型优劣。

## Python 示例

以下脚本只依赖 Python 标准库。示例不包含真实账号、令牌或训练 ID；从环境变量读入获准身份和明确目标。此脚本可嵌入已有外部程序，而无需更换训练框架。

```python
import json
import os
import time
from urllib.request import Request, urlopen
from urllib.parse import quote

base = "https://raytrain.wellspiking.ai"
token = os.environ["RAYTRAIN_PAT"]
job_id = quote(os.environ["RAYTRAIN_JOB_ID"], safe="")
run_id = quote(os.environ["MLFLOW_RUN_ID"], safe="")
path = f"/api/v1/jobs/{job_id}/mlflow/runs/{run_id}"

def call(method, suffix, payload=None):
    body = None if payload is None else json.dumps(payload, allow_nan=False).encode()
    request = Request(base + suffix, data=body, method=method, headers={
        "Authorization": "Bearer " + token,
        "Content-Type": "application/json",
    })
    with urlopen(request, timeout=30) as response:
        envelope = json.load(response)
    if not envelope.get("success"):
        raise RuntimeError(envelope.get("error", {}).get("message", "request failed"))
    return envelope["data"]

experiment = call("GET", path)
assert experiment["run"]["id"] == os.environ["MLFLOW_RUN_ID"]
batch = {"metrics": [{
    "key": "external/quality_score", "value": 0.87,
    "timestamp": int(time.time() * 1000), "step": 100,
}]}
call("POST", path + "/log-batch", batch)
```

上线联调应使用专门获准的测试任务/Run，不能向正在运行的用户训练写入演示指标。本文不会自动提交训练、创建 Run 或签发凭据。

## 错误与重试

- 401：凭据缺失、过期、撤销或无效，先处理身份问题。
- 403：权限范围、任务归属或 PAT 通道不满足。申请正确权限，不重试绕过。
- 404：任务或可信 Run 不存在/不可见；核对 Job/Run 对，不能根据 job_id 推测 run_id。
- 400/413：字段、数量或体积不符合合同；修正请求再试。
- 409：Run 已终止或参数冲突。相同 Run 的参数不可覆盖；要记录随时间变化的值应使用 metric。
- 429：按 `Retry-After` 等待后重试。第一阶段为每身份每后端实例的内存限流，不是跨副本全局配额。
- 502/503 或网络超时：可能是上游/审计故障。写入可能已经部分生效，先读取确认，不自动无条件重放。

MLflow batch 不保证所有字段原子写入；本接口不提供 exactly-once 幂等账本。确需重试 metric 时保留同一个 key/value/step/timestamp，不每次生成新时间戳。为集成使用独立 `external/` 指标命名，避免与训练主进程争用不可变参数。

## 第一阶段以外

外部 Run 创建、官方 SDK 全协议、Artifact 上传/下载、Model Registry 写入、独立评测、审批发布与 Model Serving 不在这个接口内。详见 [完整生命周期设计](superpowers/specs/2026-09-12-mlflow-lifecycle-design.md)。后续应从显式候选模型包和不可变评测证据出发，经过审批再提升生产别名、发布到外部仓或部署服务。

原生 [MLflow REST API](https://mlflow.org/docs/latest/api_reference/rest-api.html) 是上游协议参考；平台开放范围以本文和实际发布版本为准。
