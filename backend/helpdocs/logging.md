### 训练日志为什么重复两行？

同一条 loss 日志出现两行，并不意味着训练执行了两遍，也不能只凭现象判断是平台故障或用户代码问题。先确认重复发生在哪一层，再调整对应配置。仅有重复输出、训练状态和指标正常时，不必为此重启正在运行的任务。

#### 先核对原始输出

1. 在任务详情导出对应时段的日志，同时用 `spk-rayjob logs -f JOB_ID` 对照实时输出，记录时间、进程 PID、global rank、epoch 和 step。两种入口可能共用日志链路；仍有疑问时由平台支持核对 Worker 原始进程日志。
2. 相同步骤但 PID/rank 不同：先检查是否多个 rank 都在输出。训练指标通常只由 global rank 0 上报，不能只检查每台机器的 local rank 0。
3. 同一 PID/rank 的同一消息，出现“时间戳 - 名称 - INFO”和“INFO:名称:”两种格式：检查训练 logger 自身的 handler 与根 logger 是否同时输出，以及是否重复注册了 TextLoggerHook。这是排查线索，不是仅凭格式作出的最终归因。
4. Worker 原始日志只有一条，而平台页面出现两条：向平台支持反馈日志采集或展示问题，不要先修改训练代码。确认前不在页面按文本盲目去重，以免隐藏不同进程的合法消息。

#### 为什么镜像没变，换个训练任务却开始重复？

镜像固定了依赖，但源码包、模型配置和日志器初始化顺序仍可能不同。已核查的一类情况是 MMCV 1.x 的 MlflowLoggerHook 加载 MLflow 2.17.2 的 PyTorch 集成后，新建了根日志处理器；训练 logger 仍向根传播，于是同一记录输出两次。即使 `log_model=False`，该 hook 也可能加载 PyTorch 集成。

例如 LiDAR 训练较晚首次初始化 MMCV 日志器时，会把已有根处理器限制到 ERROR，后续迭代就只显示一行；Fusion 可能在加载相机权重时提前初始化该日志器，之后不再重复执行这段配置，重复输出便持续存在。因此要比较实际代码、模型与初始化顺序，不能仅凭“最近才看到”判断是平台发布引入。

#### 确认是训练端重复传播后，如何处理？

由训练源码维护者在自己的下一版训练入口中处理。以 BEVFusion/MMCV 的 `mmdet3d` logger 为例：先初始化 MLflow 基础客户端和框架日志器，在框架已经安装自身控制台/文件处理器之后、开始训练之前加入下面的兼容设置。其他框架须使用其实际 logger 名称，不要直接套用 `mmdet3d`。

```python
import logging

# 框架已完成日志器初始化；不会新增或删除 handler。
logger = logging.getLogger("mmdet3d")
if any(not isinstance(h, logging.NullHandler) for h in logger.handlers):
    logger.propagate = False
```

这只停止该 logger 向上重复传播。没有自身处理器、依靠根 logger 输出时，不应关闭传播，否则可能丢失日志。若根处理器还承担集中发送等用途，应由维护者确认保留必要输出路径后再调整。

保留 MlflowLoggerHook、Run 生命周期及 `mlflow.log_params()` / `mlflow.log_metrics()`；它们负责参数和指标写入，不依赖这条文本日志的向上传播。不需要个人 PAT，不更换平台已注入的 MLflow 地址，也不要为了消除重复而删除全部根处理器、关闭所有 INFO 日志或禁用 MLflow。

平台不会因发布这篇说明而修改用户源码、旧代码包或运行中的进程。源码维护者应用修改后，需要重新生成 ZIP / working-dir 快照再提交；重试旧快照仍使用原代码，仅更新网页或后端不能改变已加载的训练代码。

#### 修改后验证什么？

- 在资源允许且获准的短任务中，确认同一 PID/rank、同一 step 的文本日志只输出一次；其他进程的日志仍可见。
- 在“实验中心 → 训练记录”打开关联 Run，确认 loss、学习率等指标的 step 持续推进，任务正常结束后 Run 状态符合预期。
- 如果使用文件日志或集中日志发送，确认这些输出仍存在；不同模型入口分别检查，不能用一次 LiDAR 成功代替 Fusion 验证。

仍无法定位时，向平台支持提供 Job ID、出现时间、两行示例、模型/配置名称、镜像摘要，以及一个没有重复的对照 Job ID。不要提供 PAT、Cookie、访问票据或完整环境变量。指标接入见[如何向 MLflow 记录训练参数和指标？](#mlflow-framework-metrics)，页面打不开见[浏览器工具排查](#portal-browser-tools-and-queue)。
