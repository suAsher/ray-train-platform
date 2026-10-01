# 调试环境临时 sudo 与 APT

## 用户要求

调试环境允许用户通过 sudo 获得容器内 root 权限、安装 APT 系统包。用户接受这些安装在停止或重建环境后丢失。默认 Base 应提供 vim。现有训练和调试实例不由本次更新自动重启。

## 实现边界

- 仅交互式调试 RayCluster 的 Worker 允许提权；仍从镜像默认用户启动。
- Worker 明确保持 `privileged: false`、RuntimeDefault seccomp。移除全部 capability 后，仅增加 SETUID、SETGID、CHOWN、DAC_OVERRIDE、FOWNER、FSETID、AUDIT_WRITE。最后一项用于 sudo 正常发送审计事件；未增加 SYS_ADMIN、SYS_CHROOT、宿主机 namespace 或挂载。
- 调试 Head 与训练 RayJob 的提权策略保持原状。调试 Pod 不自动挂载 Kubernetes API Token，即使指定了 ServiceAccount。
- Base 配套调试镜像预装 sudo、vim，并使用项目既有 Ubuntu Jammy 内网包源。ray 可免密码 sudo。系统层仅在容器内修改；个人持久目录中的文件仍会保留，应使用普通用户管理这些文件。
- “保存训练环境”继续只在独立可信构建环境中重建受管 Python wheel 依赖。工作区内的 capture 结果属于不可信输入；系统包、用户源码与整个根文件系统不进入保存镜像。

## 新旧镜像并存

新增 `backend.environmentBuilds.compatibleWorkspaceImages`，对应 `ENVIRONMENT_COMPATIBLE_WORKSPACE_IMAGES`。它是管理员明确批准、基于同一训练 Base 和捕获协议的旧调试镜像固定摘要列表，缺省为空。列表不接受 tag 或其他源仓库，不由镜像目录自动扩展。

默认镜像继续来自 `workspaceImage`。保存操作记录工作区实际镜像；工作区归属、容器运行时摘要及捕获前后 Pod UID/镜像绑定继续验证。升级后保留仍在使用的旧 Base 摘要，避免旧工作区无法保存依赖。旧实例不会自动获得新 Pod 权限。

## 验证与发布

构建机证据目录：`/tmp/rtp-workspace-sudo-evidence-20261001`。

- sudo 策略与旧镜像缺 vim 已取得 RED 证据；新增回归覆盖仅 Worker 提权、Token 禁用、共享挂载只读、训练权限不变。
- 候选调试镜像已在无用户挂载、无凭据的一次性容器验证：ray 可 sudo 至 UID 0，vim 可执行，APT 更新并安装原本不存在的 hello 包，前后 Python/Ray/PyTorch/CUDA 版本与 pip check 正常。
- 第二个容器确认临时 hello 安装不保留，预装 vim 保留。集群 server-side dry-run 接受新权限配置，未创建验收 Pod。
- 完整 Go/vet/真实临时 PostgreSQL、环境捕获与构建 Python 回归在构建机执行。升级兼容性补充后必须再次通过完整门禁，以最新 `verification-source.txt` 和 `full-verification` 日志为准。

发布组件只有 backend 与 environment-workspace。正式发布时按 release skill 同步已验证源码、构建并推送固定镜像摘要，最小覆盖后端镜像和环境保存配置，并更新现有默认 Base 调试镜像目录项；不重复登记其他运行时镜像，不发布 CLI 或 Portal。

保存仍在用的旧 Base 摘要到兼容列表；真实验收新建专用无 GPU 调试环境的 sudo、APT 与环境保存。仅清理此次验收资源，核对原有工作负载 UID 和重启数。候选镜像构建及准入 dry-run 不代表已经生产上线。
