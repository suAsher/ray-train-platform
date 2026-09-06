# 通用训练基础环境（待构建发布）

此目录是构建配方，不表示镜像已经发布或已通过 GPU 验证。发布后管理员应提供实际完整地址、tag/摘要、环境报告和验证状态，再用于用户派生模板。

目标镜像名为 `harbor.wellspiking.ai/guofeng.su/raytrain-base:<发布标签>`。该地址当前仅为命名约定，不能直接当作可拉取镜像使用。包含 Python 3.10、CUDA 12.1、PyTorch 2.4.1、torchvision 0.19.1、Ray 2.58.0（Train/Data）、PyArrow 25.0.1、MLflow skinny 3.14.0、平台启动器与托管运行时。继承的通用依赖还包括 transformers 4.44.2、datasets 2.21.0、accelerate 0.34.2 和 TensorBoard 2.17.1。不包含用户训练代码或特定模型框架。

维护者显式构建：`BUILD_TARGETS=raytrain-base IMAGE_TAG=<发布标签> USE_BUILDX=true bash build-image.sh`。不在默认 `all` 中，不会替换线上镜像。`constraints.txt` 是核心兼容约束，不是完整 lockfile；首次发布必须完成镜像构建、完整依赖清单归档与真实运行验收。

## 用户派生流程

1. 新建独立构建目录，复制 `example/` 中三个文件（包含隐藏的 `.dockerignore`）。只修改依赖列表，不复制训练工程。
2. 从平台镜像目录获取已发布基础镜像地址，设置 `BASE_IMAGE`；为自己的仓库设置 `MY_IMAGE`，例如 `harbor.wellspiking.ai/my-team/my-environment:v1`。其他集群可访问的仓库也可以，tag 与 digest 都支持。
3. 执行 `docker build --platform linux/amd64 --build-arg BASE_IMAGE="$BASE_IMAGE" -t "$MY_IMAGE" .`。依赖冲突时修正依赖，不能删除约束绕过校验。
4. 执行 `docker run --rm --network none --entrypoint raytrain-selfcheck "$MY_IMAGE"`。退出码 0 只表示 CPU 兼容检查通过，GPU/NCCL/多机训练仍需独立验证。检查不会启动 Ray 集群、访问平台或读取凭据。
5. 使用本人已有仓库认证执行 `docker push "$MY_IMAGE"`。将完整地址、环境用途、`python3 -m pip freeze` 清单、自检 JSON 交给团队管理员；共享镜像找集群管理员。私有仓库的拉取凭据由管理员在集群配置，不写入镜像。
6. 登记后选新环境提交小规模训练。代码继续通过 Git、工作区快照或 spk-rayjob 提交；只在依赖改变时重建环境。同一 tag 被覆盖会损害复现，建议每次发布新 tag。

项目必须编译 CUDA 扩展时，应另行构建可版本化 wheel 并作为依赖安装。本模板不承诺包含 CUDA 编译工具链。环境兼容不代表普通脚本自动适配 Ray Train；托管入口、Ray Data 数据读取与 checkpoint 仍按平台说明接入。
