# 训练数据预热实现参考

平台现在推荐使用提交参数 `cache.preload: input`，由 Worker init container 自动调用本目录的 `stage_dataset.py`。普通用户不需要把这个脚本复制到训练仓库。

该脚本仍作为以下用途保留：

- 平台预热镜像的可测试实现源；
- 管理员本地验证双盘分片、容量保护和原子发布；
- 自动预热版本上线前的旧任务兼容参考。

用户用法请阅读 [`docs/NVME_CACHE_GUIDE.md`](../../docs/NVME_CACHE_GUIDE.md)。
