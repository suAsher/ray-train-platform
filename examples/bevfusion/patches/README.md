# BEVFusion 平台兼容补丁源码

两个用户分支的 `tools/westwell_train.py` 有相同的两个入口问题：直接单卡执行时读取不存在的 `args.local_rank`，并且无论是否启用 launcher 都把 `distributed` 固定为 `true`。

用户不需要访问平台维护机器，也不需要为 Python 代码变化重建镜像。完整、可复制的逐文件改造以 [`docs/BEVFUSION_END_TO_END_GUIDE.md`](../../../docs/BEVFUSION_END_TO_END_GUIDE.md) 为准；本目录保存同版本补丁和回归测试，供仓库维护者验证文档没有漂移。

在本仓库开发时，可对自己的临时 checkout 验证补丁：

```bash
git apply /path/to/ray-train-platform/examples/bevfusion/patches/0001-ddp-entrypoint.patch
git add tools/westwell_train.py
git commit -m 'fix: support torchrun entrypoint'
```

S1H 的 checkpoint 与 `post_center_range`、数据路径、MLflow 和 `.rayignore` 还需要同版本手册中的其余步骤，不能只应用这一份历史 DDP diff。
