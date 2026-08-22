# MLflow 长期实验中心设计

## 目标

为每个登录用户提供长期可访问的训练实验入口。RayCluster 和 Ray Dashboard 随任务清理后，MLflow 中的训练指标、参数、开始结束时间仍可在平台查看。

## 选定方案

平台增加 `/experiments` 页面，并由控制面提供租户作用域 API。浏览器不直连 MLflow，MLflow 继续保持 `ClusterIP` 且无 Ingress。

- Engineer 只查看本人提交任务对应的 MLflow runs。
- TenantAdmin 查看本租户全部 runs。
- SuperAdmin 仍按当前登录租户查看，跨租户切换不在本期范围。
- API 仅返回 run 元数据、参数和指标；不返回 artifact URI，也不提供 artifact 下载代理。
- 实验卡片链接回平台任务详情；任务详情保留单任务完整曲线。

## 数据流

1. 平台创建 RayJob 时注入 `MLFLOW_TRACKING_URI`、租户实验名、任务 ID 和提交者 ID。
2. 训练代码只在 rank 0 上报参数与指标。
3. MLflow 将元数据写入独立 PostgreSQL，将小型 artifact 写入平台 TOS 前缀。
4. 平台后端按认证主体生成服务端过滤条件并查询 MLflow。
5. 前端实验中心展示 run 列表与关键指标；RayCluster 是否存在不影响历史展示。

## 安全边界

- MLflow 不创建 NodePort/Ingress，训练用户不获取 TOS AK/SK。
- 租户名、提交者和过滤条件来自认证主体，不接受任意 MLflow filter。
- API 限制分页大小、响应体和参数长度。
- 不复用 Ray Dashboard 反向代理。Ray Dashboard 是单任务、只读、短时访问；原生 MLflow UI 是全局实验管理面，直接代理会绕过租户过滤。

## 验收

- Engineer 看不到同租户其他用户的 run；TenantAdmin 可以看到本租户 run。
- 任一已结束任务在 RayCluster 删除后仍能出现在实验中心。
- 页面可看到状态、任务、开始/结束时间、Loss、学习率、Epoch、mAP/NDS 等已上报指标。
- API 和页面均不出现 artifact URI、TOS bucket、AK/SK 或下载按钮。
- MLflow 两副本、独立 PostgreSQL、ServiceMonitor、NetworkPolicy 和健康检查保持通过。
