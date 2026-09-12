# MLflow 接入交付包与后续设计验证记录

日期：2026-09-12。此次完成后续详细设计和现有 REST 接口的交付工具，未修改 backend、Portal、Helm、数据库或训练运行时。完整 MLflow SDK、外部 Run 创建、文件管理、评估审批及 Serving 仍未实现，不能把设计完成当成全部功能上线。

## 版本与范围

- 开始时重新核对：本地/GitHub/内部 GitLab/构建机 main 均为 `9105a47dfe873c6f0301abfdb0347f671e229889`；工作区干净。
- 线上后端仍是 `release-20260912-01-3800b753`，Helm 212；两个后端 Pod Ready、imageID 匹配 `sha256:3eaa6da5ad3508451fcd2f578c107ccb7f3f3df90f9941e297f7f184507d2713`。本轮没有发布新镜像。
- RED 候选：`52e4965474122e04108eba954db0539ff893a780`，只有客户端合同测试。
- 已验证实现候选：`b3ad45f26aaa19cc0dff02a4467292467c92cc2e`，包含标准库客户端、OpenAPI、规范验证脚本、交付单与生命周期设计。后续只有本文等文档尾提交。
- 构建机通过候选 bundle 创建 detached worktree；正式 main 在验证完成前未快进。

## 交付物

| 内容 | 位置 |
| --- | --- |
| 发给对接方的清单 | [接入交付单](MLFLOW_PARTNER_HANDOFF.md) |
| 可导入 API 工具的合同 | [OpenAPI 3.1 JSON](api/mlflow-integration.openapi.json) |
| Python 3.10+ 标准库调用工具 | [客户端与说明](../examples/mlflow_integration/README.md) |
| 当前 API 权限、错误、重试与限制 | [接口说明](MLFLOW_INTEGRATION_API.md) |
| 后续完整实施规格 | [生命周期设计](superpowers/specs/2026-09-12-mlflow-lifecycle-design.md) |

客户端只提供显式 list/read/log-batch，不创建 Run 或训练任务；PAT 从环境注入，所有重定向拒绝，单次写入不重试，保留 error.code、request_id 和有效 Retry-After 秒数。列表明确是最近最多 100 条的窗口，不据缺失记录断言权限。

交付单说明：当前 PAT 没有单 Job/Run 授权，也没有服务账号委托；对接方自己的 PAT 不能代写他人任务。短期 PAT、目标身份/团队、Job/Run 与指标含义仍需获准配置，文档和工具不签发凭据。

## 构建机验证

| 验证 | 结果 |
| --- | --- |
| RED | 未实现 client 模块时 ImportError，符合预期；未访问生产 |
| 客户端 unittest | **19 项通过**，含显式读写、目标匹配、拒绝重定向、错误脱敏、只读本地 payload、JSON/大小边界、限流等待信息及不自动重试 |
| 覆盖率 | client.py，193 statements、81 branches，综合 **93%**，超过 80% 门禁；不代表全仓覆盖率 |
| OpenAPI | `openapi-spec-validator 0.7.2` 验证 3.1 规范；4 个已部署路径、27 个示例、4 个合法/17 个非法 batch、Run ID 边界通过 |
| 独立审阅 | 设计/权限/OpenAPI 与客户端分别复审，无 HIGH/阻断问题；保留服务端为细粒度 batch 校验权威 |

全部测试、覆盖率和规范校验在构建机执行，本机只编辑、审阅和 diff 检查。测试使用 mock HTTPS transport，没有真实 PAT，也未向生产 Run 写入。首次 coverage 使用模块路径参数错误导致未采集数据，修正为文件 include 后重新验证得到上述 93%。构建机缺少 venv 的 ensurepip，验证依赖安装在独立 `/tmp` 目录，不修改系统或产品依赖。

构建机证据：

- `/tmp/rtp-mlflow-client-red-20260912.log`
- `/tmp/rtp-mlflow-client-green-20260912.log`
- `/tmp/rtp-mlflow-client-coverage-20260912.log`
- `/tmp/rtp-mlflow-openapi-20260912.log`

本轮不改变可部署组件，不重建后端/前端/CLI/训练镜像。文档收尾完成后按流程同步四端；避免因文档尾提交触发旧 GitHub 镜像工作流，生产版本仍独立按 digest 核对。

## 未完成

1. 对接方尚未确认使用官方 SDK 还是平台 REST，以及是否需要新建 Run、上传模型、Registry、评估或 Serving。现阶段交付包覆盖已上线 REST；不要给出虚构的 SDK Tracking URI。
2. 外部系统真实获准身份联调、凭据发放和生产测试目标仍未完成；不向正在运行的用户训练注入样例。
3. 集成身份/资源 grant、外部 Run 数据模型、SDK 白名单、Artifact 代理、候选模型/评估/审批/发布/Serving 已完成详细设计，后端和 UI 尚待逐阶段实现。尤其原生共享 MLflow UI 可能绕过模型别名审批，治理完成前不能验收审批闭环。
4. 上一轮 Portal 完整生产浏览器验收和 Pod imageID 独立核实缺口仍保留；本轮客户端测试不替代这些证据。
