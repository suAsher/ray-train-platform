# 任务级 GPU 指标设计

## 目标

让训练用户在任务详情中查看自己任务实际使用的 GPU，而不开放集群全局物理资源信息。

用户可查看：

- 每张 GPU 的利用率、显存使用量、功率和温度；
- 15 分钟、1 小时、6 小时、24 小时和 7 天历史曲线；
- 最新采样时间、近 1 分钟平均值、卡间负载不均和数据延迟提示；
- 任务结束后仍在 Prometheus 保留期内的历史指标。

## 权限模型

- 任务提交人可以查看自己的任务。
- 团队管理员可以查看本团队任务。
- 平台管理员可以查看所有团队任务。
- 具有 `jobs:read` 的个人访问令牌沿用任务读取权限，不能越过任务归属边界。
- 普通用户不能访问 `/cluster/gpu-metrics` 全局物理资源接口。

权限判断必须复用现有 `jobForPrincipal`，不能只依赖请求中的 namespace、Pod 名称或任务 ID。

## API

新增：

`GET /api/v1/jobs/:id/gpu-metrics?window=1h`

`window` 仅允许 `15m`、`1h`、`6h`、`24h`、`7d`，默认 `1h`。响应复用 GPU 历史指标结构，内容只包含该任务的 Worker GPU。

现有 `/api/v1/jobs/:id/metrics` 继续负责 Loss、吞吐、学习率和 Epoch，不把两类刷新频率不同的指标混在一个接口中。

## 指标选择

后端从已授权的任务记录中取得：

- Kubernetes namespace；
- RayCluster name。

Prometheus 查询同时匹配 `exported_namespace` 和 RayCluster Worker Pod 前缀。查询条件由数据库中的任务记录生成，不接受用户传入 Pod 正则表达式或 PromQL。

查询四类 DCGM 指标：

- `DCGM_FI_DEV_GPU_UTIL`；
- `DCGM_FI_DEV_FB_USED`；
- `DCGM_FI_DEV_POWER_USAGE`；
- `DCGM_FI_DEV_GPU_TEMP`。

每条曲线按 `UUID`、`Hostname`、`gpu`、`modelName` 聚合，并使用近 1 分钟平均，避免 Pod 标签变化和 30 秒采样间隙导致断线或错误的瞬时 0%。

## 前端

在“任务详情 → Loss 收敛曲线与指标”中增加“训练 GPU”区域：

- 顶部显示参与训练的 GPU 数、近 1 分钟平均利用率、显存总使用量、总功率和最高温度；
- 四张曲线分别展示利用率、显存、功率和温度；
- 每张物理 GPU 一条线，支持时间范围切换；
- 指标每 30 秒刷新，任务详情和日志原有刷新逻辑不变；
- 无数据时说明任务可能仍在排队、Worker 未启动、Prometheus 暂不可达或指标已超过保留期，不显示虚假的 0。

管理员的“显卡物理矩阵”继续作为全局资源页面，不与任务详情合并。

## 异常与降级

- 无权限或任务不存在统一返回 404，避免泄露其他团队任务是否存在。
- 非法时间范围返回 400，且不会查询 Prometheus。
- Prometheus 不可达返回 502；前端保留上一次成功结果并显示重试提示。
- 任务没有 RayCluster name 时返回空设备列表和明确状态，不回退到模糊的全局任务 ID 查询。
- 已结束任务在 Prometheus 保留期内仍可查看；超过保留期后显示“历史指标已过期”。

## 测试与发布

测试必须覆盖：

- 提交人、团队管理员和平台管理员可查看相应任务；
- 其他团队用户和其他团队管理员不能查看；
- PAT 只能读取自身有权限的任务；
- PromQL 同时包含受控 namespace 和 RayCluster Worker 条件；
- 时间范围白名单和恶意任务元数据防御；
- 前端四类曲线、空状态、延迟状态和定时刷新；
- 现有任务日志、Loss、MLflow 和管理员 GPU 页面回归。

只滚动更新平台前后端，不修改 DCGM、Prometheus、RayJob、RayCluster 或训练镜像。发布前后核对正在运行 RayJob 与 Worker Pod UID，确认训练不受影响。
