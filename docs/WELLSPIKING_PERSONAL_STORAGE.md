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

既有 `/workspace`、`/mnt/storage/me`、只读 `/mnt/.wellspiking` 各自保持原用途。

Wellspiking 账号根下的已知虚拟、共享或内部入口 `.minio`、`.original`、`.wellspiking`、`.spk-hybrid`、`.spk-ssd`、`.shared`、`.spiking-group-shared` 不在个人视图中显示，也不能通过此适配器直接读写。网页与调试挂载共用这条路径策略。排除仅针对这些精确的根层名称；本人的 `.config`、`.private.txt` 等隐藏文件，以及私人子目录中同名的普通目录仍可使用。上游增加新虚拟入口时，需要重新核对用途和访问边界，不能把所有点开头的文件都当作系统入口。

## 部署前提

功能及 CSI 默认为关闭。启用包含数据库迁移 0059、后端镜像和新增可信 CSI 节点组件，必须作为单独的存储发布审阅；开发验证通过不表示生产已启用。

首轮发布使用指定测试账号范围，两个稳定个人账号完成了当前目录与实际 PVC 挂载的双账号验证。随后用户明确授权向全体平台用户开放，新增独立的 `allUsersEnabled` 开关控制开放范围；它不表示上游隔离配置已经核实。

全员发布设置 `enabled: true`、`allUsersEnabled: true`、`testOwnerIDs: []`，`personalRootIsolationVerified` 继续保持 `false`。每个用户仍须独立生成本人公钥，在本人的 Wellspiking 账号登记并验证连接；全员开放不共享密钥、不按团队绑定，也不替未连接的用户建立挂载。已经运行的调试环境保持原模板，本人下次新建时才使用已验证的个人目录。管理员身份不增加选择他人目录的权限。

`allUsersEnabled` 与任何非空 `testOwnerIDs` 名单互斥，避免全员与部分账号范围混淆。默认 `allUsersEnabled: false`、测试名单为空，功能仍默认关闭。旧的指定账号模式和已取得全局证明的模式继续保留；未显式选择全员模式且全局确认仍为 `false` 时，必须给出非空、合法、无重复的测试名单。两个开关仅接受布尔值，不能用字符串或空名单绕过启用闸门。

`testOwnerIDs` 是用户 ID 字符串列表，经 Helm 以 `PERSONAL_STORAGE_TEST_OWNER_IDS` 传入后端；全员开关通过 `PERSONAL_STORAGE_ALL_USERS_ENABLED` 传入。不得在名单中填团队 ID、显示名称或用户名。切换模式或移除名单前，仍须遵循撤销流程处理本人现有挂载；仅改变开放范围不等于已撤销已建立的 SSH/FUSE 会话。

本轮证据证明两个真实账号的当前行为与挂载隔离，并已核对九个目标节点的网络和 CSI 注册；尚未取得覆盖所有 Wellspiking 账号的上游有效 home、存储后端范围、组继承、虚拟目录及权限配置证明。`personalRootIsolationVerified: false` 如实保留这一未完成项，不能因开启全员入口而设为 `true`。

目录名称过滤不会改变上游账号或密钥的实际权限，也不构成上游 jail。将专用公钥登记到现有 Wellspiking 账号后，它仍继承该账号的有效权限。SFTPv3 客户端检查无法证明已有硬链接来源，也无法消除其他原生客户端替换路径的竞态。平台拒绝观察到的符号链接、特殊文件、路径穿越及已知根入口，并禁止创建链接；这些检查不能代替上游账户隔离。后续取得完整上游证明后，才可另行明确全局隔离确认。

需核对合规区固定上游地址、端口与已验证的 SSH 主机公钥，准备只包含该主机公钥的 `known_hosts` Secret。禁止首次自动信任。确认编辑器可调度节点上的 CSI 注册、FUSE 挂载权限及网络连通性，后台使用的 CSI 镜像与节点组件必须为同一个摘要。

配置入口为 `personalStorage`，已有 `personalStorage.objectSetQuota` 配置保持独立：

- `enabled`：显式启用个人存储功能。
- `allUsersEnabled`：默认 `false`；经授权显式向全部平台用户开放个人连接入口，与非空 `testOwnerIDs` 互斥，不表示上游隔离配置已经证明。
- `personalRootIsolationVerified`：只记录已取得证据的全局上游隔离确认。本轮全员发布保持 `false`。
- `testOwnerIDs`：默认 `[]`；指定账号测试模式使用合法的稳定个人用户 ID，与全员模式互斥。不是团队授权，也不是上游全员隔离确认。
- `host`、`port`、`existingKnownHostsSecret`、`knownHostsKey`：操作人员固定的合规区服务与主机公钥引用。
- `maxUploadBytes`：1 至 67108864；`mountCapacity` 是 Kubernetes 静态卷声明容量，不是上游配额。
- `csi.enabled`、`csi.image`、`csi.registrarImage`、`csi.kubeletRoot`：可信节点组件配置。镜像须固定摘要，kubelet 根须匹配集群实际配置。
- `csi.nodeSelector`：发布覆盖中按真实编辑器可调度节点设置；默认 values 不写入生产节点标签。本次实查建议 `platform.wellspiking.ai/gpu-pool: production` 与 `platform.wellspiking.ai/cache-ready: "true"`，覆盖现有 9 个编辑器节点。只装一个节点而不改变调试调度，会导致调试环境在其他节点挂载失败。

2026-10-15 只读上线前核对：9 个目标物理 Linux 节点均 Ready，`/dev/fuse` 字符设备存在；既有受控 CSI 的 hostPath 与实际目录一致，kubelet 根为 `/var/lib/kubelet`。该根权限为 `0700`，`pods`、`plugins_registry` 为 `0750`，均属 root。可复用已在目标节点现有 EBS/NAS CSI 中实际运行的 registrar：`vke-cn-shanghai.cr.volces.com/vke/csi-node-driver-registrar@sha256:11f199f6bec47403b03cb49c79a41f445884b213b382582a60710b8c6fdc316a`。这些是当时快照，发布前仍须重新核对。

新增节点组件在每个选中节点申请 `60m` CPU、`80Mi` 内存，无 GPU 请求；9 节点总计 `540m` CPU、`720Mi` 内存。它使用独立 CSI 驱动名、socket 和插件目录，不替换既有存储驱动。server-side dry-run 必须确认没有改写既有训练/调试 RayCluster 模板；发布前后逐项比较活跃 RayJob、RayCluster、Pod UID、状态及重启数。

首次交付后端和节点组件时显式选择 `BUILD_TARGETS=backend,personal-storage-csi`。本次只新增全员开放配置与后端门禁，单独重建 `backend`；现有 CSI 与 Portal 版本保持不变。Portal 是独立仓库的 `dev` 候选，不能使用本仓库旧 `frontend/` 替代。

节点驱动接收 kubelet 交付的本人凭据。调试 Pod 仅引用静态 PVC，不获得私钥、`/dev/fuse` 或新增的挂载权限。卷身份含本人绑定、代次与 namespace，团队迁移会生成目标 namespace 的静态卷声明，保持同一远端个人账号。保留卷不会自动清理上游用户文件。

启用时后端增加对指定 CSIDriver 的 `get` 及 CSINode 的 `get` 权限，只用于核对注册状态。节点驱动不挂载 ServiceAccount token；DaemonSet 使用 `OnDelete`，更新镜像不会自动重启已有节点挂载。

新增写接口要求 `X-RayTrain-Personal-Storage: 1`；JSON 操作要求 `application/json`，拒绝浏览器标记的跨站请求。个人文件上传单独设置 2 分钟读取期限，不改变既有 TOS 大文件传输策略。

## 撤销与恢复

首版不提供在线换绑、删除密钥或轮换接口。撤销时先停止本人的使用实例，确认挂载已卸载，再移除上游公钥及平台 Secret；删除 Secret 不会撤销已经建立的 SSH 会话。先保留绑定和证据，不通过删除数据库行重新分配账号。

驱动重启后不收养仅凭路径看起来相同的旧挂载。恢复需核对卷来源、完整身份和 kubelet 生命周期，按已证明属于该卷的目标卸载并重新挂载；不得批量卸载未知挂载。错误会显式暴露为挂载或 I/O 失败，不回退为本地缓存目录。

当前版本断连或操作超时后需要卸载并重新挂载，不提供挂载会话自动重连。支持普通文件与目录读写、重命名及删除；链接、特殊文件、chmod/chown/utime 等完整 POSIX 行为不在首版合同中。上游未提供的 fsync 语义会明确失败，不能把此挂载当作本地 POSIX 文件系统。

## 验证边界

构建机夹具验证独立 SSH 密钥、两个独立 SFTP 根、实际内核 FUSE 的 `ls` 和读写、重命名删除、断线失败及卸载重建。夹具采用真实 SSH/SFTP 协议的独立测试服务，不是生产 Wellspiking 账号权限证明。生产另需本人登记公钥、上游隔离、合规区网络及实际调试实例验证。单个真实本人账号的浏览和挂载成功，只能证明该账号的功能连通；它不能替代上游配置证明或真实双账户隔离验证。

参考：[存储概念](https://docs.wellspiking.ai/begin/storage/concepts.html)、[Linux 挂载](https://docs.wellspiking.ai/begin/storage/mount_linux.html)、[CSI 凭据传递](https://kubernetes-csi.github.io/docs/secrets-and-credentials.html)。
