# 用户文档入口

这组文档面向算法工程师。目标是让用户从已有代码和数据开始，完成 GPU 调试、提交分布式训练、查看日志与 Loss、保存 Checkpoint，并能从历史结果继续训练。

## 第一次训练

按以下顺序操作：

1. 阅读[用户使用手册](../USER_GUIDE.md)，确认账号、个人空间、团队空间和公共数据目录。
2. 在调试环境中打开 GPU Worker，检查代码、依赖、`nvidia-smi` 和数据路径。
3. 按[多方式提交手册](../SUBMIT_GUIDE.md)选择 Portal、`spk-rayjob` 或原生 Ray Jobs API。
4. 在任务详情查看排队、RayCluster、Pod 日志和 GPU 指标；在“实验中心”比较 Loss、学习率和评估指标。
5. Checkpoint 写入个人结果空间；需要续训时创建新任务并显式选择历史任务结果。

## 数据从哪里读、写到哪里

| 位置 | 权限 | 用法 |
| --- | --- | --- |
| `/mnt/storage/public` | 只读 | 平台公共训练数据。 |
| `/mnt/storage/team` | 只读 | 团队管理员发布的数据。 |
| `/mnt/storage/me` | 读写 | 个人文件、结果和 Checkpoint。 |
| `/workspace` | 读写 | 调试代码、配置和用户环境。 |

任务提交后优先使用环境变量：

```python
import os
from pathlib import Path

dataset_root = Path(os.environ["PLATFORM_DATASET_PATH"])
output_root = Path(os.environ["PLATFORM_OUTPUT_PATH"])
checkpoint_root = os.getenv("PLATFORM_CHECKPOINT_PATH")
```

输入目录只读，训练结果必须写到 `PLATFORM_OUTPUT_PATH`。用户不需要知道对象存储桶名、PVC 名、AK/SK 或节点路径。

## 如何选择提交入口

- 想要页面选择数据、镜像和资源：使用 **Web Portal**。
- 在本地或外部服务器频繁改代码：使用 **`spk-rayjob submit --dir .`**。
- 已有 Ray 自动化：使用 **`ray job submit --working-dir .`**。

三种方式都会创建平台任务记录和任务专属 RayCluster。代码修改后重新提交即可；只有 CUDA、PyTorch、系统库或基础依赖变化时才需要构建新镜像。

## 项目接入

- 普通 PyTorch/DDP 项目：[新训练代码接入](../NEW_TRAINING_CODE_GUIDE.md)
- BEVFusion 从拉代码到 2×8 卡提交：[端到端操作手册](../BEVFUSION_END_TO_END_GUIDE.md)
- BEVFusion 项目：[代码改造](../BEVFUSION_CODE_CHANGES.md)与[交付手册](../BEVFUSION_RUNBOOK.md)
- 所有提交参数和命令：[多方式提交手册](../SUBMIT_GUIDE.md)

## 常见边界

- Ray Dashboard 只在任务 RayCluster 存活期间可用；历史日志和指标从平台任务详情查看。
- MLflow 实验中心长期保留训练参数和指标。训练代码需显式调用 MLflow；平台不会从任意 stdout 自动猜测指标。
- 团队/公共目录在训练 Pod 中只读。发布新数据需要 TenantAdmin 或 SuperAdmin 权限。
- 页面不提供数据下载；允许的上传、发布和训练读写范围由角色与数据空间共同决定。
- 调试 Pod 可以停止和重建；持久的是工作区、个人数据、快照和用户环境，不是 Pod 本身。
