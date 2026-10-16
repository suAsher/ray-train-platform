# CUDA 12.6 调试环境

新增可选镜像，不替换现有默认镜像，也不修改已运行的调试环境。

| 组件 | 固定版本 |
| --- | --- |
| 系统 | NVIDIA CUDA 12.6.3 devel / Ubuntu 22.04，linux/amd64 |
| Python | 3.10（系统补丁版本以构建证据为准） |
| PyTorch / TorchVision | 2.13.0+cu126 / 0.28.0+cu126 |
| CUDA runtime / nvcc | 均为 12.6 |
| Ray | 2.58.0 |
| JupyterLab / Jupyter Server / IPykernel | 4.5.11 / 2.21.0 / 6.29.5 |
| VS Code Server | code-server 4.99.4 |

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

只在既有构建机的隔离候选目录执行。先构建 `cuda126-base` 并验证。
`PUSH_IMAGE=false` 仅将镜像载入构建机，**不会在 Harbor 生成可拉取的摘要**；
本地 image ID / config digest 也不是仓库 manifest digest。

```bash
BUILD_TARGETS=cuda126-base PUSH_IMAGE=false USE_BUILDX=true \
  IMAGE_TAG=cuda126-candidate bash build-image.sh

python3 -m unittest discover -s images/cuda126-workspace -p 'test_*.py' -v
```

未获推送授权时，隔离验证可导出同一候选的 OCI layout，取真实 manifest
digest，再用 BuildKit named context 将规范 Harbor 引用映射到本机 OCI 内容。
此操作不向仓库推送，profile 仍绑定同一个规范引用和 manifest digest。

```bash
docker buildx build --platform linux/amd64 --provenance=false \
  --file images/cuda126-workspace/Dockerfile.base \
  --output type=oci,dest=/tmp/cuda126-base.oci.tar .
mkdir /tmp/cuda126-base-oci
tar -xf /tmp/cuda126-base.oci.tar -C /tmp/cuda126-base-oci
base_digest="$(python3 -c 'import json; print(json.load(open("/tmp/cuda126-base-oci/index.json"))["manifests"][0]["digest"])')"
CUDA126_BASE_IMAGE="harbor.wellspiking.ai/guofeng.su/raytrain-cuda126-base@${base_digest}"

docker buildx build --platform linux/amd64 \
  --file images/cuda126-workspace/Dockerfile \
  --build-arg "CUDA126_BASE_IMAGE=${CUDA126_BASE_IMAGE}" \
  --build-context "${CUDA126_BASE_IMAGE}=oci-layout:///tmp/cuda126-base-oci@${base_digest}" \
  --tag ray-workspace-cuda126:candidate --load .

docker buildx build --platform linux/amd64 \
  --file images/environment-builder/Dockerfile \
  --build-arg "ENVIRONMENT_BASE_IMAGE=${CUDA126_BASE_IMAGE}" \
  --build-arg ENVIRONMENT_BASE_PYTHON=/usr/bin/python3.10 \
  --build-context "${CUDA126_BASE_IMAGE}=oci-layout:///tmp/cuda126-base-oci@${base_digest}" \
  --tag raytrain-environment-prepare-cuda126:candidate --load .
```

只有获准发布并推送后，才从实际仓库检查权威摘要；正式构建通过
`CUDA126_BASE_IMAGE=规范Harbor仓库@sha256:已发布manifest摘要` 显式选择
`workspace-cuda126` 或 `environment-prepare-cuda126`，不使用本机 context 覆盖。

`cuda126-base`、`workspace-cuda126` 和 `environment-prepare-cuda126`
三个目标均不包含在 `all` 中。后两个目标拒绝可变基础 tag；
不提供从 cu126 下载失败后回退 CPU 或 cu121 的路径。

固定版本在常用 PyPI 镜像缺失时，`official-wheels.json` 记录官方 PyPI
元数据、确切 wheel URL 与 SHA256；`official-wheels` 阶段通过
`ADD --checksum` 下载，安装再以 `--no-index --no-deps --require-hashes`
核验。wheel 通过只读构建挂载提供，不复制进最终镜像层，也不增加全局
备用索引。隔离验证可将逐文件核验过完整 SHA256 的同一组 wheel 放在
独立目录的 `wheels/` 下，以 `--build-context official-wheels=/绝对路径`
覆盖下载阶段；必须保留每个官方来源、长度与完整摘要校验的证据。

镜像内 `raytrain-cuda126-selfcheck` 验证 CPU 导入、固定版本、解释器、
nvcc 与 pip 依赖。独立 `actor_smoke.py` 验证真实本地 Ray Actor；
Jupyter HTTP/kernel 和 VS Code HTTP 在 workspace 构建时验证。
这些均不宣称 GPU、驱动或多节点训练已通过。nvcc 与 Torch CUDA extension
可以在 CPU 构建机另行编译、检查 `sm_89` 目标并加载扩展；这仍不等于
执行 GPU 二进制或 CUDA kernel，实际编译证据以本次报告为准。

`gpu_smoke.py` 仅在获授权的新 GPU 测试资源运行：验证真实 CUDA forward/backward、
nvcc 编译并执行 `sm_89` 二进制，以及 Torch CUDA extension 编译和执行。
无 GPU 时直接失败，不会跳过后报告成功。RTX 4090 的测试显式编译 SASS，
避免把旧驱动无法 JIT 的新 PTX 当作完整兼容性。
保留 NVIDIA 基础镜像的驱动保护，不设置 `NVIDIA_DISABLE_REQUIRE`，
也不降低 `NVIDIA_REQUIRE_CUDA`；实际节点驱动与应用扩展仍需实测。

基础与可写 Python 使用同一套已审计 bootstrap：pip 26.2.1、setuptools 84.0.0、wheel 0.48.0。只在可写 venv 内保留这组小型基础包，Torch/CUDA 等仍继承系统 site-packages。
