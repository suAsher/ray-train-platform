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

## 临时安装系统包（sudo / apt）

平台升级后，使用已更新调试镜像新建的调试环境支持 sudo 和 apt；终端默认仍以普通用户运行，已有调试实例不会自动获得这项能力。在调试终端执行：

```bash
# 将 PACKAGE_NAME 替换为所需的系统包名
sudo apt-get update && sudo apt-get install --no-install-recommends PACKAGE_NAME
```

旧镜像可能没有 sudo。如果出现 `sudo: command not found`，先保存所需文件和受管 Python 依赖版本，再自行新建使用已更新镜像的调试环境；不希望重建时可请管理员准备所需镜像。

**系统包不会进入保存的训练环境，重建后不会保留。** apt 安装只修改当前调试容器的系统层；重新创建容器后需要重新安装。保存功能仍从固定 Base 和受管 Python 依赖重建训练镜像，不生成整个容器或整个 home 的快照。正式训练依赖这些系统包时，应通过专用训练镜像的 Dockerfile 固定它们并按摘要登记。

`/workspace` 和 `/mnt/storage/me` 的个人文件仍由个人持久存储保留。用普通用户写代码、解压和管理项目文件，避免使用 sudo 在这些目录中产生 root 所有者的文件，影响后续编辑。Python 依赖继续使用 `/opt/raytrain/environment/bin/python` 与 `python -m pip`，按原流程验证并保存；不要用 `sudo pip` 改写系统 Python 或绕过受管环境。

## 捕获与重建合同

`/usr/local/bin/raytrain-environment capture` 输出不超过 1 MiB 的 schema 1 JSON。字段为 `schemaVersion`、`baseImage`、`pythonVersion`、`packages`、`checks`；新增包条目只有规范名称、固定版本和实际安装文件内容指纹 `filesHash`。不输出用户文件、源码、环境变量或凭据。

基础包实际文件与构建时基线比较；新增包校验 RECORD、拒绝直接来源、软链接和未登记文件。捕获前后再次比对避免安装过程中的不一致。解释器、venv 配置与启动脚本按构建时记录的哈希校验；替换 Python、Ray、torchrun 或平台命令不属于支持的保存方式。允许 sudo 不扩大捕获范围，也不保证捕获能识别系统层的所有修改。对有解释器路径差异的其他生成脚本校验安装记录，但不加入可移植 wheel 指纹；忽略 RECORD/INSTALLER/REQUESTED、bytecode 等安装生成项。wheel 的 `.data` 只支持 Python 库与环境内 `share/` 资料（例如 IPykernel kernelspec），外部路径、headers 和自带 scripts 明确拒绝。

捕获失败 stderr、构建失败终止消息只包含固定 `code`，不拼接底层异常、源地址或凭据。UI 依据代码提示依赖发生变化、wheel 不可用、文件被修改、超时或临时空间不足。

集群构建 Pod 在固定 base 的 Python/ABI 下从管理员配置的 HTTPS 包源下载 wheel。源与版本不能直接保证文件一致，因此必须比对 wheel 库文件指纹与捕获值，然后锁定 wheel SHA-256。构建 Pod 在同路径 venv 离线安装并验证，再只导出受管依赖层。独立可信 OCI 组装程序把这一层附加到原固定 base，不执行用户 Dockerfile，不启动嵌套容器。镜像不包含 VS Code、独立 Jupyter 服务环境或用户源码。调试预置的 IPykernel 和新增依赖也进入捕获清单，确保 Notebook 与训练解释器相同。

材料下载失败或指纹不匹配不会发布。检查结果中 CPU、Ray Actor、GPU、多节点分别记录，不能用 CPU 检查声称 GPU 或多机训练通过。

## 构建与验证

升级调试镜像时，同步更新镜像目录项与 `backend.environmentBuilds.workspaceImage`。将仍在使用、且基于同一训练 Base 和捕获协议的旧调试镜像固定摘要加入 `backend.environmentBuilds.compatibleWorkspaceImages`；它只允许明确列出的旧版本继续保存依赖，不改变默认镜像，不信任任意登记镜像或可变标签。后端仍检查实际容器镜像摘要、工作区归属与 Pod UID。现有工作区不会自动重建，因此也不会自动获得新 Pod 的 sudo 权限。

新增 workspace Dockerfile 和 builder Dockerfile 都使用仓库根作为上下文。构建、测试仍遵守 release skill，在构建机验证平台运行时代码；用户点击保存后的依赖构建、OCI 组装、publish 操作由集群临时 Job 承载。

```bash
python3 -m unittest discover -s images/environment-workspace -p 'test_*.py'
python3 -m unittest discover -s images/environment-builder -p 'test_*.py'
```

`runtime_smoke.py` 默认验证 CPU 导入和 Python/launcher 路径；`--ray-local` 在专用验收容器检查本地 Ray worker。完整 GPU 与训练验证由平台专用验收任务完成。
