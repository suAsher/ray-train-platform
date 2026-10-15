# Wellspiking 个人存储与调试挂载

设计确认：2026-10-15。用户批准实现，要求 storage-wellspiking 合规区、个人绑定、独立密钥、网页本人目录和调试中可直接 ls。本文仅授权范围内的候选开发与隔离验证，不记录生产发布授权。

## 所有权和权限

绑定以稳定 RayTrain user ID 为主键，不含团队归属；用户名来自已认证平台账户。用户不能请求其他用户 ID、SFTP endpoint 或远端根目录。每个用户独立生成 Ed25519 公私钥，公钥仅登记在本人 Wellspiking 存储账户；私钥仅进入受控 Secret，不返回页面，不进入调试容器、环境变量、代码快照或训练镜像。团队调整不重新创建绑定。平台角色不增加他人个人文件访问权。

上游 SFTP 服务必须按用户账号强制隔离个人根；客户端路径检查不能代替服务端账户隔离。连接验证要求固定主机的已验证 known_hosts，不采用自动接受主机密钥。网页和文件系统均拒绝绝对路径、穿越、符号链接、硬链接和根下 .shared/.spiking-group-shared 入口。隔离夹具使用两个具有独立服务端根的真实 SFTP 账号，另设共享目录与逃逸链接进行负向验证。生产只有在上游账户隔离和合规区网络经过验证后才能启用。

## 用户流程

数据与存储新增独立空间 wellspiking-personal，名称 Wellspiking 个人文件。未连接时显示一次性公钥登记与验证流程；绑定后网页提供目录浏览、新建目录和有界流式上传，操作受交互会话与本人绑定约束。SFTP provider 不复用 TOS 分片、配额、视频或代码快照合同。通过显式 capabilities 禁止训练选择、下载及未实现的预览功能。

连接状态、网页可访问状态和实际 workspace 挂载状态分开。绑定可验证后，新建本人调试 workspace 默认在 GPU/CPU 编辑器 worker 挂载 /mnt/storage/wellspiking；head 无须挂载。未绑定用户继续现有工作流。已绑定但连接/挂载失败时明确失败，不启动一个冒充远端目录的本地空目录。已有运行实例不改模板、不重启；当前 /workspace、/mnt/storage/me 和只读 /mnt/.wellspiking 保持各自合同。训练 RayJob 不挂新空间，不把新 ID 自动接受为训练输入/checkpoint/output。

## 实现边界

后端新增 personalstorage 包封装个人绑定、独立 Secret、固定 endpoint、SFTP 文件访问和统一路径策略。GORM 持久化新增 0059，只存公开元数据与 Secret 引用。API 位于交互会话路由；请求中无可选 owner。固定平台账户 username 与远端账户匹配，不允许任意账号重绑定。首版不提供在线换绑/轮换/删除密钥，撤销运维须先停止本人挂载，再移除上游公钥和平台 Secret，避免将删除 Secret 冒充现有会话已失效。

挂载由独立 CSI node 组件提供，每用户每代绑定独立 retained PV/namespace-local PVC。NodePublish 凭据只交给节点驱动，Ray Pod 仅引用 PVC。驱动固定上游 endpoint/host-key policy，校验 owner/binding/generation 与卷身份，只接受 kubelet publish target。FUSE 文件系统与网页共用路径及共享目录排除策略，不提供链接和任意特殊文件创建。默认关闭新功能；CSI driver 属新增受信节点组件，其部署权限和生命周期单独审阅，不能放宽调试容器权限代替。

## 验证和交付

本机仅编辑、审阅与 diff --check。候选通过 Git bundle 到既有构建机 detached worktree；测试先 RED 再 GREEN。验证包括个人唯一公钥、团队迁移绑定不变、A/B 网页和挂载隔离、路径/符号链接/共享目录拒绝、未认证/PAT/管理员越权拒绝、真实 SFTP 与 FUSE ls/读写/停止重建/断线、有界上传和错误脱敏、CSI foreign volume/target/Secret 拒绝、PostgreSQL 新装/重复/升级与完整 Go 回归、Portal 合同/lint/dev 构建。真实 Wellspiking 的登记公钥、账户隔离、网络和生产挂载另留独立证据，未通过不宣称已上线。

参考：https://docs.wellspiking.ai/begin/storage/concepts.html 、https://docs.wellspiking.ai/begin/storage/mount_linux.html 、https://kubernetes-csi.github.io/docs/secrets-and-credentials.html 。
