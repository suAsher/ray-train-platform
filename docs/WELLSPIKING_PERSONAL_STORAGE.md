# Wellspiking 个人存储

本功能接入合规区 `storage-wellspiking`，绑定稳定的平台用户 ID 与本人 Wellspiking 用户名。每人生成独立 Ed25519 密钥；团队调整沿用原绑定。团队管理员和平台管理员没有通过此接口选择其他用户目录的入口。

## 用户使用

在“数据与存储”的“Wellspiking 个人文件”卡片生成本人公钥，按页面链接在本人 Wellspiking 存储账户登记，再点击验证连接。私钥由平台的独立不可变 Secret 保存，不返回浏览器，不挂入调试容器。

验证通过后可浏览目录、新建文件夹和上传文件。单次上传最多 64 MiB，可由部署配置进一步降低；文件完整接收并关闭后才重命名发布，不承诺上游存储的持久化刷盘语义。此空间首版不进入训练输入、输出或 Checkpoint 选择，也不支持平台下载、视频预览或代码快照。

之后新建的本人调试环境默认挂载 `/mnt/storage/wellspiking`，终端可执行：

```bash
ls -la /mnt/storage/wellspiking
```

当前运行环境保持原模板；本人下次停止并新建环境时才接入。连接状态和当前实例的挂载状态独立显示。已绑定但尚未验证、凭据异常或驱动未就绪时，新建环境会明确失败，不使用本地空目录冒充远端数据。

既有 `/workspace`、`/mnt/storage/me`、只读 `/mnt/.wellspiking` 各自保持原用途。个人根的 `.shared` 和 `.spiking-group-shared` 不在新视图中公开。

## 部署前提

功能及 CSI 默认为关闭。启用包含数据库迁移 0059、后端镜像和新增可信 CSI 节点组件，必须作为单独的存储发布审阅；开发验证通过不表示生产已启用。

启用前取得上游的实际权限证据：本人账号只能进入本人根，不能通过其他路径、链接或共享入口访问其他个人目录。SFTPv3 的客户端检查无法证明已有硬链接来源，也无法消除其他原生客户端替换路径的竞态。平台拒绝观察到的符号链接、特殊文件、路径穿越及共享入口，并禁止创建链接；这些检查不能代替上游账户隔离。`personalRootIsolationVerified` 是操作人员完成验证后的显式确认，不能凭该布尔值宣称已验证。

需核对合规区固定上游地址、端口与已验证的 SSH 主机公钥，准备只包含该主机公钥的 `known_hosts` Secret。禁止首次自动信任。确认编辑器可调度节点上的 CSI 注册、FUSE 挂载权限及网络连通性，后台使用的 CSI 镜像与节点组件必须为同一个摘要。

配置入口为 `personalStorage`，已有 `personalStorage.objectSetQuota` 配置保持独立：

- `enabled`、`personalRootIsolationVerified`：显式启用与上游隔离确认。
- `host`、`port`、`existingKnownHostsSecret`、`knownHostsKey`：操作人员固定的合规区服务与主机公钥引用。
- `maxUploadBytes`：1 至 67108864；`mountCapacity` 是 Kubernetes 静态卷声明容量，不是上游配额。
- `csi.enabled`、`csi.image`、`csi.registrarImage`、`csi.kubeletRoot`：可信节点组件配置。镜像须固定摘要，kubelet 根须匹配集群实际配置。

仅按实际改动显式选择 `BUILD_TARGETS=backend,personal-storage-csi`。Portal 是独立仓库的 `dev` 候选，不能使用本仓库旧 `frontend/` 替代。

节点驱动接收 kubelet 交付的本人凭据。调试 Pod 仅引用静态 PVC，不获得私钥、`/dev/fuse` 或新增的挂载权限。卷身份含本人绑定、代次与 namespace，团队迁移会生成目标 namespace 的静态卷声明，保持同一远端个人账号。保留卷不会自动清理上游用户文件。

启用时后端增加对指定 CSIDriver 的 `get` 及 CSINode 的 `get` 权限，只用于核对注册状态。节点驱动不挂载 ServiceAccount token；DaemonSet 使用 `OnDelete`，更新镜像不会自动重启已有节点挂载。

新增写接口要求 `X-RayTrain-Personal-Storage: 1`；JSON 操作要求 `application/json`，拒绝浏览器标记的跨站请求。个人文件上传单独设置 2 分钟读取期限，不改变既有 TOS 大文件传输策略。

## 撤销与恢复

首版不提供在线换绑、删除密钥或轮换接口。撤销时先停止本人的使用实例，确认挂载已卸载，再移除上游公钥及平台 Secret；删除 Secret 不会撤销已经建立的 SSH 会话。先保留绑定和证据，不通过删除数据库行重新分配账号。

驱动重启后不收养仅凭路径看起来相同的旧挂载。恢复需核对卷来源、完整身份和 kubelet 生命周期，按已证明属于该卷的目标卸载并重新挂载；不得批量卸载未知挂载。错误会显式暴露为挂载或 I/O 失败，不回退为本地缓存目录。

当前版本断连或操作超时后需要卸载并重新挂载，不提供挂载会话自动重连。支持普通文件与目录读写、重命名及删除；链接、特殊文件、chmod/chown/utime 等完整 POSIX 行为不在首版合同中。上游未提供的 fsync 语义会明确失败，不能把此挂载当作本地 POSIX 文件系统。

## 验证边界

构建机夹具验证独立 SSH 密钥、两个独立 SFTP 根、实际内核 FUSE 的 `ls` 和读写、重命名删除、断线失败及卸载重建。夹具采用真实 SSH/SFTP 协议的独立测试服务，不是生产 Wellspiking 账号权限证明。生产另需本人登记公钥、上游隔离、合规区网络及实际调试实例验证。

参考：[存储概念](https://docs.wellspiking.ai/begin/storage/concepts.html)、[Linux 挂载](https://docs.wellspiking.ai/begin/storage/mount_linux.html)、[CSI 凭据传递](https://kubernetes-csi.github.io/docs/secrets-and-credentials.html)。
