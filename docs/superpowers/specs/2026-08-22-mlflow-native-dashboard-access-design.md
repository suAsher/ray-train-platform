# MLflow 原生 Dashboard 同域完整访问设计

## 背景

平台已经部署高可用 MLflow Tracking Server，并通过“实验中心”向普通用户提供按身份过滤的任务指标。用户还需要在浏览器中打开 MLflow 原生 Dashboard，使用原生的实验列表、Run 对比和曲线查看能力。

本设计补充原生界面入口，不替代现有实验中心。它显式取代《MLflow 长期实验中心设计》中“浏览器不直连原生 MLflow UI”的范围限制；实验中心已有的租户过滤规则保持不变。

## 已确认需求

- 所有已登录平台用户，包括 Engineer、TenantAdmin 和 SuperAdmin，都可以打开原生 MLflow Dashboard。
- 原生 Dashboard 展示全平台实验；用户已接受跨团队可见和管理操作互相影响。
- 入口使用现有域名 `https://raytrain.wellspiking.ai/mlflow/`。
- 用户不使用 `kubectl port-forward`，不接触 Kubernetes、TOS AK/SK 或 MLflow 内部地址。
- MLflow Service 继续使用 `ClusterIP`，不创建 NodePort，也不让浏览器直接访问 `mlflow-system` Service。
- 原生界面开放 MLflow 当前提供的完整功能，包括查看、比较、创建、修改、删除、恢复、模型注册表和 Artifact 上传下载。

## 方案选择

采用“平台鉴权访问票据 + 同域完整反向代理”。平台只控制谁能进入 MLflow，不对已登录用户的 MLflow 功能做二次裁剪。

未选择的方案：

1. 独立 `mlflow.*` 域名需要新增 IDC 代理、DNS、证书和 Keycloak Client，并会形成第二套登录流程。
2. 直接把 MLflow Ingress 暴露给内网无法复用平台身份，也不能记录对应的平台用户。
3. 仅扩展实验中心不能满足用户打开原生 MLflow 界面的明确需求。

## 访问流程

1. 已登录用户在“实验中心”点击“打开 MLflow”。
2. 前端使用现有 Bearer 会话调用 `POST /api/v1/mlflow-dashboard-access`。
3. 后端验证用户属于 Engineer、TenantAdmin 或 SuperAdmin，签发 2 分钟有效的一次性访问 URL。
4. 浏览器打开 `/mlflow/?access_token=...&tenant=...&subject=...`。
5. 代理校验签名和主体，把访问票据交换为 Path 限定在 `/mlflow/` 的 HttpOnly、Secure、SameSite=Strict Cookie，并重定向到不含令牌的干净 URL。
6. 后续页面、静态资源和查询请求由后端代理到 `http://mlflow.mlflow-system.svc.cluster.local:5000`。

浏览器不会获取 MLflow 内部 Service 地址、数据库连接串、TOS 凭据或对象存储直连地址。

## 完整代理策略

通过访问票据建立会话后，代理透明转发 MLflow 原生 UI 和 API：

- 支持 MLflow 使用的 `GET`、`HEAD`、`OPTIONS`、`POST`、`PUT`、`PATCH` 和 `DELETE`。
- 保留实验、Run、Tag、模型注册表和 Artifact 的完整 API 行为。
- 不移除 `artifact_uri`、`artifact_location` 等 MLflow 原生响应字段。
- 保留足够的请求体大小和长操作超时，支持 Artifact 上传及模型管理。
- 记录平台主体、方法、MLflow 路径、状态码、耗时和请求 ID，但不记录 Cookie、Authorization、请求体或 Artifact 内容。
- 对会改变状态的方法校验请求来自 `https://raytrain.wellspiking.ai` 同源页面；结合 SameSite=Strict Cookie 防止第三方站点伪造操作。

平台不会为 MLflow 业务 API 维护单独允许列表。MLflow 升级时必须回归登录、页面资源、CRUD、模型注册表和 Artifact 上传下载，确认子路径代理仍兼容。

## 子路径兼容

MLflow 原生 UI 默认以站点根路径运行。部署将设置 `--static-prefix /mlflow`，代理同时完成以下转换：

- 浏览器 `/mlflow/...` 转发到 MLflow 对应上游路径；
- 把前端资源中的绝对 `/ajax-api/`、`/api/` 查询路径改写到 `/mlflow/...`；
- 上游重定向的 `Location` 保持在 `/mlflow/` 下；
- Cookie、缓存和安全响应头不越过 `/mlflow/`。

这些行为用固定的 HTML、JavaScript、JSON 和重定向响应夹具测试，避免仅靠人工浏览器验证。

## 前端体验

“实验中心”保留当前按用户或团队过滤的日常视图，并增加按钮：

```text
打开 MLflow 管理界面
```

点击后调用访问票据 API，并在新标签页打开返回 URL。按钮旁说明：

- 原生界面展示全平台实验；
- 所有平台用户都能看到全平台实验；
- 创建、修改、删除、模型注册和 Artifact 操作会直接改变共享 MLflow 数据。

任务详情中的 Loss 曲线和单任务指标保持原样，不要求用户为日常排障切换到原生界面。

## Kubernetes 与网络边界

- `mlflow`、`mlflow-ingest` 和 `mlflow-postgres` 继续全部使用 ClusterIP。
- 现有私网 ALB 和 IDC 反向代理不新增域名；`/mlflow/` 仍经过 `raytrain.wellspiking.ai`。
- 前端 Nginx 把 `/mlflow/` 转发给平台后端；平台后端是唯一可访问原生 MLflow UI 的浏览器代理。
- 训练 Pod 继续只访问 `mlflow-ingest:8080` 写入网关，不能借此读取全局实验或 Artifact。
- NetworkPolicy 继续只允许平台后端、Prometheus 和 MLflow 自身访问 Tracking Server。

## 文档修正范围

同一次交付修正以下文档问题：

1. 在 BEVFusion 端到端指南开头集中列出平台账号、GitLab 权限、镜像目录和团队 GPU 配额前提。
2. 安装章节覆盖 Linux、macOS、Windows，或明确引导到平台“集群外提交”页面中的当前版本命令。
3. 正式 2×8 资源统一为每 Worker `64 CPU / 256GiB`；`32 CPU / 128GiB` 只用于 smoke。
4. 删除“新版根锚定 `/` 无效”的过时故障说明，并用自动化测试锁定 `.rayignore` 语义。
5. 在用户指南顶部增加日常最短流程：修改代码、`submit`、跟日志、看实验、再次提交、续训。
6. 更新 MLflow 运维和用户文档，说明实验中心与原生完整 Dashboard 的用途、全局可见性和变更风险。

## 测试与验收

### 自动化测试

- 未登录请求不能获得访问票据，也不能访问 `/mlflow/`。
- 三种平台角色都能获得访问票据。
- 过期、篡改或主体不匹配的票据被拒绝。
- Cookie 具有 Path、HttpOnly、Secure 和 SameSite 限制，URL 交换后不再含令牌。
- 页面、静态资源、实验搜索、Run 搜索和指标查询成功。
- 使用验收专用名称创建、修改、删除和恢复实验/Run 成功。
- 模型注册表基本操作成功。
- 验收 Artifact 的上传、列出和下载成功，且训练数据空间和 TOS AK/SK 仍不会因此暴露。
- 审计日志包含平台主体和 MLflow 操作元数据，但不包含认证信息、请求体和 Artifact 内容。
- 文档链接、代码块、公开路径、资源模板和 `.rayignore` 说明一致。

### 集群验收

- MLflow 仍为 2 副本，独立 PostgreSQL、PDB、ServiceMonitor 和 NetworkPolicy 正常。
- 所有 MLflow Service 均为 ClusterIP，集群中不存在 MLflow NodePort。
- 普通用户从实验中心按钮打开 `/mlflow/`，无需再次登录。
- 原生界面可看到现有 `FINISHED` Run、Loss 和 mAP/NDS，并能执行 Run 对比。
- 使用验收专用对象完成实验/Run CRUD、模型注册和 Artifact 上传下载；验收结束后只清理本次创建的对象。
- 原有实验中心、任务详情、训练上报和 2×8 训练链路不回归。

## 发布与回滚

发布顺序：后端鉴权代理与测试 → 前端按钮 → MLflow `static-prefix` → 构建不可变镜像 → Helm 滚动升级 → 浏览器与接口验收。

回滚时先隐藏前端按钮并撤销 `/mlflow/` 路由；MLflow Tracking、实验中心和训练上报不依赖该入口，因此继续可用。任何阶段都不需要把 MLflow Service 改成 NodePort。
