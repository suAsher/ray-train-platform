# CUDA 12.6 调试环境

新增可选镜像，不替换现有默认镜像，也不修改已运行的调试环境。

| 组件 | 固定版本 |
| --- | --- |
| 系统 | NVIDIA CUDA 12.6.3 devel / Ubuntu 22.04，linux/amd64 |
| Python | 3.10（系统补丁版本以构建证据为准） |
| PyTorch / TorchVision | 2.6.0+cu126 / 0.21.0+cu126 |
| CUDA runtime / nvcc | 均为 12.6 |
| Ray | 2.58.0 |
| JupyterLab / Jupyter Server / IPykernel | 4.2.5 / 2.14.2 / 6.29.5 |
| VS Code Server | code-server 4.93.1 |

终端、Ray Actor 与 Notebook kernel 都使用
`/opt/raytrain/environment/bin/python`。固定依赖装在不可写系统基础层；
通过 `python -m pip install 包名==版本` 添加的 wheel 位于受管 venv。
Jupyter 服务位于独立 editor venv，训练镜像不包含编辑器或用户源码。

“保存为训练环境”需要平台为该调试镜像登记匹配的 CUDA 12.6 基础摘要、
依赖构建器及可信捕获 profile。保存按固定基础镜像重建新增 Python wheel，
不会捕获整个容器、APT 安装、用户代码或数据，也不能改变已锁定的核心依赖。
登记、保存和发布的实际状态以本次验收报告为准；构建成功本身不代表这些链路已启用。

`/workspace` 和个人数据目录仍由平台持久挂载；受管 venv 在容器可写层，
停止或重建前应保存需要的训练环境版本。用户另外创建在工作区中的 venv
不属于该捕获合同。基础镜像包含 nvcc、C++ 编译器、Python headers 和 ninja，
可调试 CUDA 扩展；保存时新增依赖仍要求可验证的索引 wheel。

## 构建与门禁

只在既有构建机的隔离候选目录执行。先构建 `cuda126-base`，验证并取得
其不可变仓库摘要，再把同一个摘要传给 workspace 和匹配的 dependency builder。

```bash
BUILD_TARGETS=cuda126-base PUSH_IMAGE=false USE_BUILDX=true \
  IMAGE_TAG=cuda126-candidate bash build-image.sh

CUDA126_BASE_IMAGE=harbor.wellspiking.ai/guofeng.su/raytrain-cuda126-base@sha256:实际摘要 \
  BUILD_TARGETS=workspace-cuda126 PUSH_IMAGE=false USE_BUILDX=true \
  IMAGE_TAG=cuda126-candidate bash build-image.sh

python3 -m unittest discover -s images/cuda126-workspace -p 'test_*.py' -v
```

`cuda126-base`、`workspace-cuda126` 和 `environment-prepare-cuda126`
三个目标均不包含在 `all` 中。后两个目标拒绝可变基础 tag；
不提供从 cu126 下载失败后回退 CPU 或 cu121 的路径。

镜像内 `raytrain-cuda126-selfcheck` 验证 CPU 导入、固定版本、解释器、
nvcc 与 pip 依赖。独立 `actor_smoke.py` 验证真实本地 Ray Actor；
Jupyter HTTP/kernel 和 VS Code HTTP 在 workspace 构建时验证。
这些均不宣称 GPU、驱动或多节点训练已通过。

`gpu_smoke.py` 仅在获授权的新 GPU 测试资源运行：验证真实 CUDA forward/backward、
nvcc 编译并执行 `sm_89` 二进制，以及 Torch CUDA extension 编译和执行。
无 GPU 时直接失败，不会跳过后报告成功。RTX 4090 的测试显式编译 SASS，
避免把旧驱动无法 JIT 的新 PTX 当作完整兼容性。
保留 NVIDIA 基础镜像的驱动保护，不设置 `NVIDIA_DISABLE_REQUIRE`，
也不降低 `NVIDIA_REQUIRE_CUDA`；实际节点驱动与应用扩展仍需实测。
