# MLflow 用户使用标准

本平台使用 MLflow 3.14 记录实验信息。MLflow 不是训练框架，也不会从 stdout 中猜测 loss、自动保存 checkpoint，或将任何训练结果自动发布成模型。训练任务要写入什么，由训练代码显式决定。

## 先看能力边界

| 内容 | 当前训练任务 | 原生 MLflow 界面 | 需要什么 |
| --- | --- | --- | --- |
| Run、参数、指标、标签 | 支持 | 支持查看、比较和编辑 | 代码在 global rank 0 调用 MLflow |
| Artifact | 不支持直接上传 | 支持手动上传、查看、下载和删除 | 通过 `/mlflow/` 明确上传；训练输出不会自动复制 |
| Models / Model Registry | 不支持从训练 Pod 直接登记 | 支持管理已存在的 MLflow Model / Registry | 先有符合 MLflow 格式的 model Artifact，再显式 Register |
| Traces | 未开放训练侧 Trace 写入 | 服务端与界面具备 Trace 能力 | 后续为推理/Agent 接入专用受控通道和代码埋点 |

训练任务只能访问受限的写入网关。网关仅允许创建或更新 Experiment / Run、记录参数、标量和标签；Artifact、模型仓、搜索列表和 Trace 路由都会被拒绝。这是为了防止训练 Pod 借 MLflow 读取其他用户数据或下载对象。

## 1. 查看实验

1. 在平台左侧打开“实验中心”，按团队或自己的任务浏览可信 Run。
2. 在任务详情的“Loss 收敛曲线与指标”查看 MLflow 详情：实验名、Run 名称、Run ID、状态、时间和训练参数；点击“打开该 Run”可直达原生 Run 页面。“打开 MLflow 管理界面”进入完整原生界面。
3. 平台任务 ID、MLflow `run_id` 和模型版本不是同一 ID：平台任务 ID 用于调度与权限；`run_id` 由 MLflow 创建；模型版本只在显式登记后产生。

Portal 的普通 API 请求经过 `/raytrain`，原生 MLflow 新标签页则使用 `https://raytrain.wellspiking.ai/mlflow/`。不要把该地址改成 Portal 的 `/raytrain/mlflow/`：MLflow 的 Cookie、重定向和静态资源以根路径为契约。访问票据是一次性的，成功换取 HttpOnly Cookie 后地址栏不应继续保留 `access_token`。

原生界面入口是同域 `/mlflow/`，应始终从平台打开。不要把训练容器中的 `MLFLOW_TRACKING_URI` 复制到浏览器、笔记本或外部平台使用，它是集群内受限写入地址。

## 2. 让训练产生 Run 和曲线

自定义代码和任意 `python tools/train.py ...` 入口都需要主动接入。仅选择 `--engine ray-train` 或看见 `MLFLOW_TRACKING_URI` 环境变量，并不会自动创建 Run。

镜像需要预装与 Python 兼容的 `mlflow-skinny==3.14.0`（需要使用完整 MLflow Model / Trace SDK 时另行评估 `mlflow` 或 `mlflow-tracing`，不能在每次训练启动时临时安装）。

下面是普通 PyTorch / DDP 训练的最小安全模板。只允许 global rank 0 写入，避免每张卡各产生一个 Run：

```python
import os
import mlflow


def is_global_rank_zero() -> bool:
    return int(os.environ.get("RANK", "0")) == 0


def platform_tags() -> dict[str, str]:
    return {
        "platform.job_id": os.environ["RAYTRAIN_JOB_ID"],
        "platform.tenant_id": os.environ["RAYTRAIN_TENANT_ID"],
        "platform.submitter_user_id": os.environ["RAYTRAIN_SUBMITTER_USER_ID"],
        "platform.provenance": os.environ["RAYTRAIN_MLFLOW_PROVENANCE"],
        "platform.cluster_attempt": os.environ["RAYTRAIN_CLUSTER_ATTEMPT"],
    }


if is_global_rank_zero():
    mlflow.set_tracking_uri(os.environ["MLFLOW_TRACKING_URI"])
    mlflow.set_experiment(os.environ["MLFLOW_EXPERIMENT_NAME"])
    mlflow.start_run(
        run_name=os.environ["MLFLOW_RUN_NAME"], tags=platform_tags()
    )
    mlflow.log_params({"batch_size": batch_size, "learning_rate": learning_rate})

# 在训练循环中；global_step 必须单调递增。
if is_global_rank_zero():
    mlflow.log_metrics(
        {"train/loss": float(loss), "train/lr": float(current_lr)},
        step=global_step,
    )

# 验证结束后。
if is_global_rank_zero():
    mlflow.log_metrics({"val/mAP": float(map_value), "val/NDS": float(nds_value)}, step=global_step)
    mlflow.end_run(status="FINISHED")
```

异常路径应调用 `mlflow.end_run(status="FAILED")`，或让进程以非零退出码结束后由平台终态协调器关闭已经带有可信归属标签的 Run。不要吞掉训练异常，也不要让每个 rank 分别调用 `start_run()`。

MMCV 1.x 的 `MlflowLoggerHook` 会把训练指标写成 `train/loss`、`train/stats/...`，而学习率通常写成 `learning_rate`。平台任务详情与通用 Loss 曲线会识别 `loss` 和 `train/loss`，但不会把 `val/loss` 冒充训练 Loss；验证指标继续使用 `val/...`。使用 MMCV Hook 时先在 global rank 0 创建带上述平台标签的 active Run，再把 `MlflowLoggerHook` 加入 `log_config.hooks`，并设置 `log_model=False`：训练 Pod 当前不能向 MLflow Artifact 仓上传模型。

### Ray Train 代码

Ray Train 不会替用户训练循环记录业务 loss。将 `mlflow.log_metrics()` 放在 `train_loop_per_worker` 内，并通过 `ray.train.get_context().get_world_rank() == 0` 限制写入；同时继续使用 `ray.train.report()` 上报 Ray Train 的 checkpoint / 恢复状态。两者职责不同，缺一不可。

平台提供的 `raytrain_runtime` Hook 只有在项目代码显式导入并配置时才会创建 Run 与转发 `report_metrics()`；它不是对任意训练脚本的隐式注入。项目接入请参考 [Ray Train 托管训练说明](RAY_TRAIN_MANAGED_GUIDE.md)。

## 3. 为什么 Artifacts 为空

当前任务的 checkpoint、权重、配置和结果应写入 `PLATFORM_OUTPUT_PATH`，随后从“任务详情 → 训练产物”或“我的运行结果”访问。这是训练结果的唯一数据真相。

`mlflow.log_artifact()`、`mlflow.log_artifacts()`、`mlflow.<flavor>.log_model()` 都需要 Artifact 上传权限；当前训练侧网关按设计返回 `403`。因此即使代码调用这些函数，原生 Run 的 Artifacts 页面也不会出现训练产物。

当前仅支持以下人工附件工作流：

1. 从平台打开 `/mlflow/`，进入目标 Run。
2. 在原生 MLflow 的 Artifacts 区明确上传小型、允许共享的附件，例如评估报告、可视化图或模型说明。
3. 不要上传训练数据、密钥、`.env`、完整数据索引，或不应跨团队共享的权重。

需要把某个 `PLATFORM_OUTPUT_PATH` 下的 checkpoint 安全复制为 MLflow Artifact 时，平台尚未提供“选择产物并发布”的受控复制功能；不能通过 TOS 凭据、Pod 内部地址或手工挂载绕过。该功能应由平台新增带任务归属、大小限制、审计和审批的发布接口后再使用。

## 4. 为什么 Models 为空，以及如何登记

MLflow 的 Model Registry 只管理已经显式记录的 **MLflow Model**，不是自动扫描 `.pth`、`.pt` 或 `.ckpt` 文件的目录。一个模型版本需要先有模型 Artifact，再登记到指定的 Registered Model；它才会获得版本、标签和别名（例如 `@champion`）。

由于训练侧 Artifact / Registry API 尚未开放，当前任务不会自动出现 Models。原生界面中已有合规 MLflow Model Artifact 时，可在该 Run 的 Artifact 目录选择模型文件夹并使用“Register Model”创建或追加版本。登记前至少确认：

- 模型格式、加载入口和依赖版本可复现；裸 `.pth` 通常不等于完整 MLflow Model。
- 标记来源任务 ID、代码 commit、数据集版本、关键指标和验证结论。
- 先以 `candidate` / `staging` 等标签评审；只把通过评审的版本赋予 `champion` 等生产别名。

对 PyTorch 等受支持 flavor，标准 MLflow 写法是 `mlflow.pytorch.log_model(..., name="model")`，再用 `mlflow.register_model(model_uri, name)` 或界面登记。该写法是 MLflow 标准能力，但在本平台训练 Pod 内目前会被 Artifact 网关阻止；不要把它加到训练脚本后期待自动成功。

## 5. Traces 适用于什么

Trace 主要用于 LLM / Agent / 推理服务的一次请求链路：输入、输出、工具调用、延迟、token 和中间步骤。普通离线视觉训练应记录指标和参数，不需要为每个 iteration 生成 Trace。

MLflow Trace 需要代码或 OpenTelemetry 显式埋点，例如 `@mlflow.trace`，或对受支持 LLM SDK 调用 `mlflow.openai.autolog()`。Trace 可能包含 prompt、响应、用户标识或业务数据，接入前必须完成脱敏、采样率和保留策略评审。

当前平台未给训练 / 推理工作负载开放 Trace 写入路由；直接调用 Trace 或 OTLP `/v1/traces` 会失败。需要接入推理 Trace 时，联系平台管理员开通独立受认证的 Trace 网关，再以小流量验收，不要复用训练指标网关。

## 6. 常见空页面排查

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| 实验中心没有 Run | 代码未调用 `start_run()`，或 Run 缺少可信平台标签 | 按第 2 节接入，只在 rank 0 创建 Run |
| 有 Run 但没有曲线 | 未调用 `log_metric(s)`，没有 `step`，或记录发生在非 rank 0 | 在主训练循环写带 step 的标量 |
| MLflow 详情有 `train/loss`，通用 Loss 卡片仍为空 | 页面或后端版本过旧，未兼容框架前缀 | 刷新页面；仍为空时把任务 ID、Run ID 和指标键交给管理员，不要改成解析 stdout |
| Artifacts 为空 | 任务侧不允许 Artifact 上传 | 结果写入 `PLATFORM_OUTPUT_PATH`；小附件从原生界面明确上传 |
| Models 为空 | 没有显式记录 / 登记 MLflow Model | 先完成合规 Artifact 发布，再显式 Register |
| Traces 为空 | 没有 Trace 埋点，且平台未开放 Trace 网关 | 对推理/Agent 提交 Trace 接入申请 |

## 7. 与其他平台集成

2026-09-12 已发布“指定 Run 只读 + 本人 RUNNING Run 批量写入”的平台 REST 接口与独立 `mlflow:write` 权限，见 [外部对接说明](MLFLOW_INTEGRATION_API.md)。线上精确读取已验证，写入通过隔离 MLflow 验证，尚未向生产 Run 写入演示数据。这不是完整官方 SDK Tracking URI；完整证据与待验收项见 [发布记录](QUOTA_MLFLOW_VALIDATION_20260912.md)。

当前对外的浏览器接口 `POST /api/v1/mlflow-dashboard-access` 只签发一次性原生界面跳转票据，不是第三方数据接口。内部训练网关也不是外部 API。

完整外部实验与模型生命周期集成仍需明确方向：

- **它写入本平台 MLflow**：第一阶段提供本人任务 PAT、任务/Run 绑定、限流和审计；服务账号委托与更广泛的生命周期操作仍待建设，不要给出内部 ClusterIP 地址。
- **本平台发布到它的模型仓**：对方需提供认证方式、创建版本 / 幂等键、Artifact 上传或受控 URI、状态回调、失败语义和权限模型；平台再做异步、可重试的显式发布。

接口已发布；首次生产读写联调仍需使用获准身份和专门测试 Job/Run。不要共享数据库连接、对象存储凭据或内部服务地址；新接口发布也不会开放完整模型生命周期或官方 SDK 全协议。

## 参考

- [MLflow Tracking APIs](https://mlflow.org/docs/latest/ml/tracking/tracking-api/)
- [MLflow Model Registry](https://mlflow.org/docs/latest/ml/model-registry/)
- [MLflow Tracing](https://mlflow.org/docs/latest/genai/tracing/)
