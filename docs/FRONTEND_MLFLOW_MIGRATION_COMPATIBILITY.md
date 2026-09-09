# 实验中心 MLflow 入口：前端迁移兼容约定

本文记录当前 Vue UI 与后端的最小稳定契约。后续迁移到其他前端框架或重做实验中心时，必须保留这些行为；不能通过浏览器直连集群内 MLflow Service。

## 两类入口

| 用户动作 | 平台请求 | 预期结果 |
| --- | --- | --- |
| 打开完整 MLflow 管理界面 | `POST /api/v1/mlflow-dashboard-access`，请求体 `{}` | 新标签页打开 `/mlflow/`，可使用原生 MLflow 全局管理界面 |
| 打开某一条实验 Run | 同一接口，请求体 `{"runId":"<32 位 MLflow run_id>"}` | 新标签页经一次性票据直接打开该 Run 的原生详情页 |

两种响应均只接受同源的 `/mlflow/?access_token=<opaque-ticket>`。前端不得接收、拼接或保存 MLflow 内部地址、Cookie、服务凭据、experiment ID 或任意跳转 URL。

## 实验中心展示契约

- 继续显示平台任务 ID 和 MLflow `runId`：前者用于任务详情、日志和结果；后者由 MLflow 生成。它们不是同一个 ID。
- 每行的“训练任务”继续链接平台任务详情；新增的“MLflow 详情”只在该行有 `runId` 时启用。
- 点击按钮时必须在第一次 `await` 前同步打开 `about:blank` 新标签页、清除 `opener`，再用后端返回的一次性 URL 替换位置；这样既不会被浏览器拦截，也不留下可复用的票据历史。
- 深链请求失败时关闭预先打开的窗口并显示错误；不影响原有“打开 MLflow 管理界面”入口。

## 后端安全边界（迁移时不可下放到前端）

后端以当前认证主体查询可信实验目录、验证 Run 与平台任务的归属，再将仅允许的 `#/experiments/<numeric-id>/runs/<run-id>` Fragment 绑定到一次性票据。票据兑换后才设置 HttpOnly、Secure、SameSite=Strict 的 MLflow 会话 Cookie 并重定向。

因此前端迁移不应自行根据任务 ID 推测 MLflow Run、把 Run ID 写成路由参数后直连 `/mlflow/`，或把原生 MLflow 的全局权限模型误当作实验中心的任务权限模型。

## 回归清单

1. 根入口仍发送空对象并打开 `/mlflow/`。
2. Run 入口发送精确的 `runId`，无效、不可见或未可信关联的 Run 不创建票据。
3. 票据单次使用；兑换后目标只可为平台允许的 MLflow Fragment。
4. 普通用户只能从实验中心看到自己的 Run；TenantAdmin 按现有团队范围查看。
5. 不在 HTML、前端状态、控制台日志或分析埋点中出现内部 MLflow URL、票据、Cookie 或对象存储位置。
