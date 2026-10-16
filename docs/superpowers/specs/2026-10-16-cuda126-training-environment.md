# CUDA 12.6 调试与训练环境保存

## 用户要求

新增非默认调试镜像：Python 3.10、PyTorch 2.13.0 cu126、TorchVision 0.28.0、CUDA 12.6 编译工具链、Jupyter 与 VS Code。支持“保存为训练环境”，明确展示版本。现有默认镜像、训练、调试环境、配额和存储归属不变。

## 镜像与信任边界

新增训练 Base、调试镜像和构建助手三个显式构建目标，不加入 `all`。训练 Base 不含用户代码或编辑器；调试与构建助手继承同一固定 Base 摘要，使用相同 Python 补丁版本与 `/opt/raytrain/environment`。编辑器独立安装，不能进入保存的用户依赖层。

控制面新增管理员配置的固定摘要配对：`workspaceImage → baseImage + prepareImage + environment`。保留旧单 Base 配置和原有 compatible 列表，仅用于原 CUDA 12.1 配对。CUDA 12.6 不加入该兼容列表。用户提交不能指定基础镜像；根据所属工作区实际 Pod 镜像和 UID 确认配对，所有构建阶段再次核对。未知、冲突和可变引用必须拒绝。

capture 与 prepare 使用镜像构建时生成的 root 只读 profile，不能使用用户环境变量或 capture manifest 选择可信 Base。原有 wheel-only、指纹、bootstrap、体积、权限与来源校验继续生效。另建 venv、系统 apt 包、源码和凭据不属于环境保存内容。

新 profile 保存层同时携带 baseline 生成的完整依赖约束，保证保存前后文件一致。导出前核对固定路径、普通文件、root:root、`0444`、64 KiB 上限，以及内容与可信 baseline 包列表排序生成结果完全一致。OCI 组装器仅对同层存在合法 profile 的约束文件开放该精确路径，不放宽其他文件规则；旧 CUDA 12.1 层保持原行为。

## 保存与版本展示

持久化已有的 `Build.BaseImage`、`Build.WorkspaceImage`。最终登记时原子写入已有镜像目录 `environment_json`，版本来自可信配对，不从用户 capture 推断；不新增数据库迁移。保留个人/团队可见性与授权边界。

capabilities 保留旧字段，新增配对列表。本次不发布 Portal；现有页面沿用保存入口，镜像目录名称与 environment 标注版本。为兼容现有页面的安装说明，capabilities 可额外返回 `eligibleWorkspaceId`：仅根据交互登录的稳定用户与团队身份查询本人 RUNNING 工作区，二次核对归属与资源标识，并由运行时检查确认实际 Pod UID 与可信固定摘要配对。查询或检查失败时在两秒预算内省略提示、保留正常能力响应；仅记录不含身份和原始错误的调试阶段，不接受客户端指定工作区或 Base。该提示不构成保存授权，Create 的独立校验保持不变。保存完成的镜像可以通过现有目录用于训练。CPU 拉取与依赖校验不等于 GPU 业务验证，仍明确标注验证范围。

## 验证与交付边界

本机只编辑、审阅与 `git diff --check`。先在既有构建机隔离候选进行 RED/GREEN、完整 Go 回归、格式与 vet、真实 PostgreSQL 生命周期/目录元数据验证、Python capture/离线重建/verify、Portal 合同与 lint/build。

CUDA 12.6 基础镜像为 NVIDIA 12.6.3 devel Ubuntu 22.04 的固定 amd64 摘要。实际受控 GPU 启动已观察到原 NVIDIA prestart hook 因 `cuda>=12.6` 拒绝，测试代码未执行。节点为 GeForce RTX 4090 D、550 系列驱动、CUDA driver API 12.4；550 满足 CUDA 12.x 小版本兼容最低驱动要求，但不等于完整支持新驱动功能。只在 Base 最后一条配置指令保留全部原要求并追加 `brand=geforce,driver>=550.127.05,driver<551,cuda>=12.4,arch=8.9` 这一窄 OR 分支，不改任何 rootfs、依赖、宿主驱动或检查开关。重建调试与助手镜像以绑定新的 Base 摘要，重新验证可信 profile、零增量 capture 与保存恢复。约束模型通过不等于实际 NVIDIA parser、GPU 运算或 550.127.05 补丁版本已验收；真实验收须记录加载的 `libcuda` 路径、API 版本、原生 sm_89 SASS/扩展执行和 forward/backward。新 PTX JIT、需要新驱动的功能与任意用户代码不在小版本兼容承诺内。未验证项不得宣称通过。

原建议 PyTorch 2.6.0 经依赖审计确认受 CVE-2026-24747 影响，用户已选择 2.13.0 / TorchVision 0.28.0 重新审计与验证。新版本不等于全部漏洞报告已解决；保留完整依赖审计结果、适用条件与未确认项。隔离运行验证仅使用平台自有 fixture 和无敏感数据的测试依赖，不加载外部权重或漏洞利用样例。

源码推送、镜像推送/登记、控制面与 Portal 发布、真实资源验收分别记录授权和证据，不继承历史发布例外。发布前核对最新远端与正式构建目录，只同步已验证候选；审阅最小 Helm 差异，保持已有训练和调试资源 UID/重启数。
