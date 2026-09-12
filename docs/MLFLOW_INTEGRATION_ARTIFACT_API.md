# MLflow 集成身份、实验授权与受控产物合同

本合同描述本轮实现，不等于已经部署验收。实际版本、生产请求与未完成项以发布验收记录为准。这里扩展平台外部实验 REST，不是原生 MLflow 全量代理，也不开放 SDK 的 Artifact、Registry、Serving、Traces 或 autolog 自动建 Run。

## 身份与权限

人类通过平台交互式登录管理自己的集成身份；机器使用独立集成令牌。身份绑定创建人的用户及团队，不能携带 Header 任意选择用户或团队。令牌限制调用能力，实验级 grant 限制可访问的资源，两者必须同时满足，后端每次请求核对当前授权状态。

| 用途 | 令牌 scope | 实验 grant permission |
| --- | --- | --- |
| 读取实验与 Run | `experiments:read` | `read` |
| 创建/写入/结束已有实验中的 Run | `experiments:read`, `experiments:write` | `read`, `write` |
| 读取产物元数据/下载 | `experiments:read`, `artifacts:read` | `read`, `artifacts:read` |
| 初始化/上传/完成/取消产物 | `experiments:read`, `artifacts:read`, `artifacts:write` | `read`, `artifacts:read`, `artifacts:write` |

所有 scope 组合必须含 `experiments:read`；`artifacts:write` 必须伴随 `artifacts:read`。所有 grant 含 `read`，产物写授权同样需要产物读授权，但不要求实验元数据 `write`。同时记录指标和上传文件的应用可以选择四项 scope/grant；仅上传产物不必扩大为实验元数据写权限。授权不会从令牌 scope 自动推导到既有任意实验。身份的 `allowCreateExperiments` 默认 false；打开时机器可按外部实验合同创建实验，创建归属与自动 grant 由服务端确定，不能据此访问其他实验。

接入前读取 `/api/v1/mlflow/capabilities`。`integrationsAvailable` 表示身份管理配置可用；`artifacts.available`、`artifacts.protocol=platform-rest-parts-v1` 及其 readScope/writeScope、partSizeBytes、maxFileBytes、ownerBudgetBytes、maxPending、uploadLifetimeHours 描述平台产物通道。`artifacts.sdkCompatible=false` 明确表示它不兼容原生 MLflow Artifact SDK。

集成所有者仍能通过平台读取其归属实验；其他人、其他团队、无 grant 集成不得读取。管理员角色不是替代所有权与 grant 的通行证。撤销单枚令牌影响该凭据；撤销 grant 移除该实验访问；撤销身份使其令牌失效。长期对接不要共享管理员 PAT、浏览器 Cookie 或内部对象存储凭据。

## 人类管理 API

Base 为获准 HTTPS origin 加 `/api/v1`。以下 `/mlflow/integrations` 路径要求交互式登录，普通 PAT 与集成令牌均不能调用。本文示例客户端不实现登录、发令牌或授权操作。

| 方法与路径 | 正文/行为 |
| --- | --- |
| `GET /mlflow/integrations` | 列出当前所有者的身份，`data.items` |
| `POST /mlflow/integrations` | `{"name":"quality-service","allowCreateExperiments":false}`；201 返回身份 |
| `DELETE /mlflow/integrations/{integrationId}` | 撤销身份 |
| `GET /mlflow/integrations/{integrationId}/tokens` | 列令牌元数据，不返回令牌明文 |
| `POST /mlflow/integrations/{integrationId}/tokens` | `{"scopes":["experiments:read","artifacts:read"],"expiresInDays":7}`；有效期 1–30 天；只在创建响应交付明文，不能记录整个响应 |
| `DELETE /mlflow/integrations/{integrationId}/tokens/{tokenId}` | 撤销令牌 |
| `GET /mlflow/integrations/{integrationId}/grants` | 列实验级授权 |
| `POST /mlflow/integrations/{integrationId}/grants` | `{"experimentId":"平台ExperimentID","permissions":["read","artifacts:read"]}`；设置该实验权限集合 |
| `DELETE /mlflow/integrations/{integrationId}/grants/{experimentId}` | 撤销该实验授权 |

管理正文限制 16 KiB；未识别字段被拒绝。身份名最多 128 UTF-8 字节。所有上述 ID 为 32 位小写十六进制字符串。每个所有者最多 20 个身份（含已撤销）；每个身份最多 20 枚未撤销令牌，列表最多 100 条且未撤销记录优先；每个身份最多 100 个实验 grant（含撤销 tombstone）。撤销不等于擦除审计历史或重置所有上限。错误通过平台 `success/error/request_id` 信封返回；管理 API 不是 SDK 协议。

## 机器端文件流程

机器沿用[外部实验 REST](MLFLOW_EXTERNAL_TRACKING_API.md)创建实验、Run。以下路径中 `runId` 是**平台外部 Run ID**，不是训练 `job_id` 或上游 MLflow Run ID。先完成产物再结束 Run；初始化、分片上传、完成要求 RUNNING。取消仍需当前写授权，但允许在 Run 结束后或上传过期后清理 PENDING 会话。READY 文件可在 Run 结束后继续读取。

1. `POST /mlflow/runs/{runId}/artifacts`：带 `Idempotency-Key`（8–128 个 ASCII 字母、数字或 `._:-`），JSON `{"name":"checkpoint.pt","sizeBytes":12345,"sha256":"完整文件64位小写SHA256"}`。201 返回 artifact，保存其 `id`、固定分片大小与到期时间；相同请求幂等重试仍可能返回 201，以相同 ID 判断。
2. `GET /mlflow/runs/{runId}/artifacts/{artifactId}`：获取状态与 `uploadedParts`。续传先对比本地文件大小/完整 SHA256，再对比已上传分片的 index、sizeBytes、sha256。
3. `PUT /mlflow/runs/{runId}/artifacts/{artifactId}/parts/{partNumber}`：`Content-Type: application/octet-stream`，`X-Content-SHA256` 为该片 64 位小写 SHA256。编号 **1 起**；每片 8 MiB，末片为剩余精确长度。同一编号同长度同 SHA256 重传幂等，内容不同拒绝。单片请求读取有 2 分钟上限。
4. `POST /mlflow/runs/{runId}/artifacts/{artifactId}/complete`：可发送 `{}`。服务端流式核对所有分片、大小与整文件 SHA256，成功后 READY；完整校验有 15 分钟硬上限。超时读取状态后再决定是否用同一 artifact ID 重试。
5. `GET /mlflow/runs/{runId}/artifacts/{artifactId}/content`：仅 READY 返回二进制流。客户端下载到明确的新文件路径，流式核对总大小和 SHA256，不信任服务端文件名作为本地路径。
6. `DELETE /mlflow/runs/{runId}/artifacts/{artifactId}`：取消 PENDING 上传。READY 不可改写、覆盖或用取消接口删除；不迁移或删除用户原有数据。

列表 `GET /mlflow/runs/{runId}/artifacts?limit=50&cursor=...` 返回 `data.items/nextCursor`；默认 50、最大 100。当前 cursor 是上一页边界的 32 位平台 artifact ID，每次查询仍重新授权；与实验列表的签名游标协议不同，不可互换。

## 文件与容量边界

- 单文件 `0 < sizeBytes <= 21474836480`（20 GiB），不支持零字节文件。
- 固定分片 `8388608` 字节（8 MiB），最多 2560 片；客户端内存只需容纳一个分片。
- 同团队同资源所有者逻辑预算 100 GiB（PENDING 与 READY 声明大小合计），最多 16 个 PENDING 上传，上传有效期 24 小时。过期未取消记录仍占预算；这不是物理存储容量保障。不能通过换集成身份绕过所有者额度。
- 文件名最多 255 UTF-8 字节，不能含路径分隔符、控制字符或 `.`/`..`；不是调用方可控存储 key/path。
- 服务端生成 artifact ID 和分片存储位置。产物目录与原生 MLflow 共享存储、训练输出目录分离，调用方不取得内部地址或对象存储凭据。
- 状态为 `PENDING`、`READY`、`CANCELLED`；过期 PENDING 不能再写，不代表自动完成或已删除。取消/清理以服务端明确结果为准。

## 重试、错误与保密

服务端使用标准平台 JSON 信封；内容下载成功为二进制。400 修正字段，401 修正过期/撤销凭据，403 核对 scope，404 可能是不存在或无资源权限，409 核对状态、配额和内容冲突，429 按 Retry-After 等待，503 或超时先读取状态。不同类型配额错误以实际 error.code 为准。

不会承诺所有多步上传事务化；分片与完成请求超时可能已生效。不得用新 key、新 SHA256 或新 artifact ID 无条件重放。初始化响应丢失时先列出并核对文件；明确确认后才能用原 key、原正文重试。只有服务器 READY 与本地下载完整校验成功才算产物验收完成。

[示例客户端](../examples/mlflow_integration/integration_artifacts.py)只从 `RAYTRAIN_API/RAYTRAIN_PAT` 获取 origin 与凭据，拒绝 HTTP、含账号/路径/查询的 origin 及重定向。receipt 0600 只存 origin、平台 ID、文件名/大小/hash 和幂等键，不保存令牌或服务端令牌创建响应。下载使用新文件独占创建，失败删除本次不完整文件，既有文件不被覆盖。
