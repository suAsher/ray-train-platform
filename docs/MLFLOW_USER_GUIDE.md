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

Portal 的普通 API 请求经过 `/raytrain`，原生 MLflow 网页使用 [https://raytrain.wellspiking.ai/mlflow/](https://raytrain.wellspiking.ai/mlflow/)。不要改成 Portal 的 `/raytrain/mlflow/`；页面、重定向和静态资源使用这个独立入口。

开启 `dashboardPublicEnabled` 后，网页无需登录、Cookie 或先从平台跳转。进入目标 Run 后可以直接分享浏览器地址：`https://raytrain.wellspiking.ai/mlflow/#/experiments/EXPERIMENT_ID/runs/RUN_ID`。对方能访问该域名即可打开；原有“打开 MLflow”和“打开该 Run”按钮继续兼容，分享时使用跳转完成后不含 `access_token` 的地址。网页共享全部实验、Run、Artifact 和 Registry，允许读取、创建、修改和删除。

网页与原生程序 API 的匿名开关独立。关闭网页匿名访问时仍需从已登录的平台打开，不改变程序 API 的认证配置。平台任务、个人目录、数据空间、调度和调试工具继续使用原认证。

不要把训练容器中的 `MLFLOW_TRACKING_URI` 复制到浏览器、笔记本或外部平台使用，它是集群内受限写入地址。浏览器 Run 直链也不能作为程序的 Tracking URI。

## 2. 让训练产生 Run 和曲线

平台提供连接与可信任务来源；训练代码仍需主动创建或复用 Run、记录参数和指标。仅选择训练引擎、设置 Tracking URI 或打印 loss，都不会自动产生曲线。镜像应预装兼容的 MLflow 客户端；不要在无外网的训练节点临时安装依赖。

用户在 **使用说明 → MLflow 与 API → 如何向 MLflow 记录训练参数和指标？** 阅读完整可复制模块、接通检查和异常处理。该页面嵌入的唯一辅助模块源是 [platform_mlflow.py](../backend/helpdocs/platform_mlflow.py)，可运行检查的源是 [mlflow_training.go](../backend/helpdocs/mlflow_training.go)。这里不再维护第二份容易漂移的代码模板。

| 训练入口 | 接法 |
| --- | --- |
| 普通 PyTorch、ray-ddp、torchrun | 将 platform_mlflow.py 放在训练入口旁随源码上传；构造 PlatformMLflow(global_rank)，调用 reporter.params、reporter.metrics，训练成功或异常时调用 reporter.finish |
| 已有框架 MLflow Hook | 复用 Hook 的 Run、指标和生命周期，不额外创建另一条 Run；补充指标前确认 active Run 存在且属于本任务 |
| 托管 ray-train | 使用配套运行时 Hook；start_managed_mlflow_run / finish_managed_mlflow_run 需要该引擎注入的 RAYTRAIN_CLUSTER_ATTEMPT，不能当作通用 PyTorch 接口 |

普通示例不要求 cluster attempt，只有平台实际注入时才携带。不要为了调用托管函数手工填写该变量。任务、团队、提交者与签名来源也必须读取真实注入值，不能猜造或复制其他任务的标签。

只由 **global rank 0** 连接 MLflow。torchrun 使用全局 RANK；不能用 LOCAL_RANK 代替。其他分布式框架应传入框架的全局 rank，例如已初始化的 torch.distributed.get_rank() 或 ray.train.get_context().get_world_rank()。缺少必要的全局 rank 信息时应检查启动方式，不能在每个节点都猜成 0。

辅助模块通过 MlflowClient 显式指定 run_id，不创建 fluent active Run。使用它时只能调用 reporter.params / reporter.metrics；混用 mlflow.log_param / mlflow.log_metric 可能隐式创建另一条未关联 Run。已有框架 fluent Hook 则按照页面的 active Run 检查后使用其原有 API。

只有本模块创建的 Run 才由它结束。正式分布式训练由框架确认整个训练成功后标记 FINISHED；原训练异常标记 FAILED 并继续抛出，不吞掉异常。MLflow 辅助请求失败不会终止训练，失败期间的指标不补传；强杀进程时无法保证执行结束代码，最终核对平台任务与 Run 的状态。

平台内训练沿用已注入 MLFLOW_TRACKING_URI、实验名、Run 名和来源信息，**不需要个人 PAT，也不要覆盖为外部原生 API 地址**。该训练网关接收参数、指标和标签；权重、配置和报告写入 PLATFORM_OUTPUT_PATH，不调用 log_artifact。

接入后先检查日志中的 MLflow Run ID，再在任务详情核对关联 Run、参数与带 step 的曲线。Job ID 与 Run ID 不要求相等；只有打印日志、只有参数或没有训练 step 时，不会凭空生成 loss 曲线。托管 Ray Train 的 ray.train.report 仍负责其训练结果和恢复协议，与 MLflow 指标上报分别接入，见 [Ray Train 托管训练说明](RAY_TRAIN_MANAGED_GUIDE.md)。

## 3. 为什么 Artifacts 为空

当前任务的 checkpoint、权重、配置和结果应写入 `PLATFORM_OUTPUT_PATH`，随后从“任务详情 → 训练产物”或“我的运行结果”访问。这是训练结果的唯一数据真相。

`mlflow.log_artifact()`、`mlflow.log_artifacts()`、`mlflow.<flavor>.log_model()` 都需要 Artifact 上传权限；当前训练侧网关按设计返回 `403`。因此即使代码调用这些函数，原生 Run 的 Artifacts 页面也不会出现训练产物。

当前仅支持以下人工附件工作流：

1. 打开 [MLflow 网页](https://raytrain.wellspiking.ai/mlflow/)，进入目标 Run；网页匿名模式下可直接访问。
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

外部程序使用 `https://raytrain.wellspiking.ai/api/v1/mlflow-native` 作为 Tracking URI，完整 SDK / HTTP 示例见 [MLflow API 接入说明](MLFLOW_PARTNER_HANDOFF.md)。开启 `nativePublicEnabled` 后，现有网络可达范围内免令牌开放共享实验、Run、Artifact 和模型注册表的读取、写入及删除。未开启时仍按原 `mlflow:full` PAT 方式调用，以实验中心 MLflow API 页面的实时说明为准。

浏览器兼容接口 `POST /api/v1/mlflow-dashboard-access` 仍向已登录的平台返回一次性跳转地址，以保留现有“打开该 Run”按钮。网页匿名模式下不依赖该票据授权，直接打开 Run 直链即可；该兼容接口不是第三方数据接口。内部训练网关也不是外部 API。

受保护的平台任务实验接口继续保留，已有客户端无需迁移。外部程序直接创建的 Run 不自动属于某个 RayTrain 任务；训练任务要建立可信关联，仍按第 2 节使用实际注入的来源。模型审批、发布、推理和功能仓同步是平台生命周期功能，与原生 MLflow API 分别操作，参见平台使用说明中的对应问题文档。

## 参考

- [MLflow Tracking APIs](https://mlflow.org/docs/latest/ml/tracking/tracking-api/)
- [MLflow Model Registry](https://mlflow.org/docs/latest/ml/model-registry/)
- [MLflow Tracing](https://mlflow.org/docs/latest/genai/tracing/)
