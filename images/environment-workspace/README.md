# RayTrain Base 配套调试环境

这是新增的可选调试镜像。原 `raytrain-base` 的文件、摘要、训练目录项和默认选择不变。两者以固定基础摘要关联，不从用户正在运行的根文件系统制作镜像。

## 用户如何安装依赖

在这个调试环境的终端或其 **RayTrain Environment** Notebook kernel 中使用：

```bash
which python
# /opt/raytrain/environment/bin/python
python -m pip install <包名>==<版本>
python -m pip check
```

调试与导出的训练镜像都使用 `/opt/raytrain/environment`。基础 Python、CUDA、PyTorch、Ray 与已有包不能在本版保存操作中替换；需要另一套核心环境时选另一个基础镜像。不要用 `pip --user`、另建 venv、editable、Git/本地路径安装，或者直接修改 site-packages。训练源码、数据和权重仍通过平台源码/数据/产物功能管理。

安装完成后先调试，再使用平台“保存为训练环境”。保存期间不要同时安装/卸载包；如果捕获检测到变化会失败并要求重试。依赖目前保留在本次调试 Pod 的可写层，**停止/重建调试环境前应保存版本**，工作区文件快照不包含此 venv。

新调试镜像的 `/etc/pip.conf` 和默认安装源使用 `https://mirrors.ivolces.com/pypi/simple/`。这只作用于本次新增镜像，不修改原 Base、现有调试环境或节点宿主机的 pip、APT、Docker 配置。离线 wheel 可以用于调试安装，但保存时仍需从固定源取得内容一致的 wheel；私有或仅本地存在的 wheel 材料上传尚未包含在本版能力中。

## 捕获与重建合同

`/usr/local/bin/raytrain-environment capture` 输出不超过 1 MiB 的 schema 1 JSON。字段为 `schemaVersion`、`baseImage`、`pythonVersion`、`packages`、`checks`；新增包条目只有规范名称、固定版本和实际安装文件内容指纹 `filesHash`。不输出用户文件、源码、环境变量或凭据。

基础包实际文件与构建时基线比较；新增包校验 RECORD、拒绝直接来源、软链接和未登记文件。捕获前后再次比对避免安装过程中的不一致。对有解释器路径差异的生成脚本校验安装记录，但不加入可移植 wheel 指纹；忽略 RECORD/INSTALLER/REQUESTED、bytecode 等安装生成项。第一版不支持 wheel 的非库 `.data` 内容，明确失败而非静默遗漏。

集群构建 Pod 在固定 base 的 Python/ABI 下从管理员配置的 HTTPS 包源下载 wheel。源与版本不能直接保证文件一致，因此必须比对 wheel 库文件指纹与捕获值，然后锁定 wheel SHA-256。构建 Pod 在同路径 venv 离线安装并验证，再只导出受管依赖层。独立可信 OCI 组装程序把这一层附加到原固定 base，不执行用户 Dockerfile，不启动嵌套容器。镜像不包含 VS Code、独立 Jupyter 服务环境或用户源码。调试预置的 IPykernel 和新增依赖也进入捕获清单，确保 Notebook 与训练解释器相同。

材料下载失败或指纹不匹配不会发布。检查结果中 CPU、Ray Actor、GPU、多节点分别记录，不能用 CPU 检查声称 GPU 或多机训练通过。

## 构建与验证

新增 workspace Dockerfile 和 builder Dockerfile 都使用仓库根作为上下文。构建、测试仍遵守 release skill，在构建机验证平台运行时代码；用户点击保存后的依赖构建、OCI 组装、publish 操作由集群临时 Job 承载。

```bash
python3 -m unittest discover -s images/environment-workspace -p 'test_*.py'
python3 -m unittest discover -s images/environment-builder -p 'test_*.py'
```

`runtime_smoke.py` 默认验证 CPU 导入和 Python/launcher 路径；`--ray-local` 在专用验收容器检查本地 Ray worker。完整 GPU 与训练验证由平台专用验收任务完成。
