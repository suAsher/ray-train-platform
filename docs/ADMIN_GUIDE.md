# 管理员手册

面向平台管理员（`SuperAdmin` / `TenantAdmin`）。日常使用看 [USER_GUIDE.md](USER_GUIDE.md)，部署运维看 [BUILD_AND_DEPLOY.md](BUILD_AND_DEPLOY.md)。

---

## 1. 角色模型

| 角色 | 能做什么 |
|---|---|
| `SuperAdmin` | 全平台：建租户、分配团队 GPU 额度、登记共享镜像、管理公共数据、查看所有租户 |
| `TenantAdmin` | 本团队：建 Engineer 账号、登记本团队镜像、在 Portal 发布团队共享数据、配团队 Git 凭证、查看本团队额度与用量 |
| `Engineer` | 提交任务、用调试环境、查看本团队任务 |

菜单会按角色隐藏，但**权限是后端每个接口独立强制的**，直接调 API 绕不过去。

---

## 2. 新建团队（租户）

「租户与配额」→ **新建租户/团队**。

一次操作会完成三件事：数据库记录、Kubernetes namespace（`tenant-<id>`）、Kueue 队列（`<id>-gpu`）。不需要手工 kubectl。

| 字段 | 说明 |
|---|---|
| 租户 ID | 小写字母/数字/短横线，会成为 namespace 后缀，**建后不可改** |
| 显示名称 | 界面展示用 |
| GPU 配额 | 该团队最多能同时占用多少卡 |

> 不需要向每个租户 namespace 分发 TOS AK/SK Secret。创建本地账号时，平台会立即在受控 TOS 根目录中初始化个人工作区、文件、训练结果和快照目录；用户登录后即可在“我的数据”浏览自己的空间。GPU Pod 挂载则由 FSX CSI 的组件级 IRSA 提供，是独立的第二步；启用前必须完成 [BUILD_AND_DEPLOY.md 第 7 节](BUILD_AND_DEPLOY.md) 的 IRSA 前缀挂载验收。

---

## 3. 新建用户账号

「租户与配额」→ 页面下方「平台用户与 RBAC 角色分配」→ **添加用户**。

TenantAdmin 只能在**自己的租户**创建 Engineer。SuperAdmin 创建本地账号时必须在表单中选择一个已经存在的租户，因此可以为新团队先创建第一位 TenantAdmin；不存在的租户会被后端拒绝。

创建成功表示账号和个人数据空间均已就绪。平台自动初始化该用户的“我的工作区”“我的文件”“我的运行结果”和训练快照目录；目录前缀、桶名、AK/SK、PVC 与 CSI 参数不会显示给管理员或用户。若对象存储暂不可用，创建会报错且账号不会落库，修复存储后重试即可。

启用“个人 TOS 容量”后，添加用户时还要填写初始容量（例如 `100 GiB`）。该数字是 TOS 原生 ObjectSet 的**硬上限**：写满后对象存储拒绝新的写入，不能靠 PVC 容量或用户修改路径绕过。已有账号可在表格的“个人 TOS 容量 → 配置”中增大或调小额度；调小前需确认现有用量不超过新上限。账号与个人根目录一一对应，用户名不会暴露为桶路径。

创建后通过受控渠道把初始密码给本人，让 TA 从右上角「账户与安全」自行修改。不要把密码写入工单、群聊、命令历史或平台备注。

账号表提供以下本地账号操作：

- **重置密码**：为 Engineer 设置新的临时密码，并立即撤销该用户所有本地会话。
- **停用 / 启用**：停用会立即使现有 session 失效，用户不能再登录；启用不会自动修改密码。

权限边界由后端强制：TenantAdmin 只能创建和管理自己租户中的 Engineer；不能创建、重置或停用 TenantAdmin/SuperAdmin。SuperAdmin 可以管理非 SuperAdmin 账号。所有创建、改密、重置、启停动作会写入不含密码和 Token 的审计记录。

---

## 4. 镜像目录（最重要的日常工作）

用户提交任务和启动调试环境时，都是从这个目录里选运行环境。**目录一旦非空，它就是镜像 allowlist**——不在目录里的镜像会被拒绝提交。

「租户与配额」→「镜像目录」→ **登记镜像**。

| 字段 | 说明 |
|---|---|
| 名称 | 用户在下拉里看到的名字，写清楚框架和版本 |
| 用途 | `训练任务` 或 `交互式调试`，两个列表互不干扰 |
| 镜像 | **必须带 `@sha256:` digest**，可变 tag 会被拒绝 |
| 框架标注 | 可选，帮用户快速识别 |
| 设为默认 | 每种用途只有一个默认，表单会预选它 |
| 全平台共享 | 只有 `SuperAdmin` 能勾，勾了所有团队都能用 |

### 为什么强制 digest

`:latest` 这类可变 tag 今天和三个月后可能是两个完全不同的环境，训练结果就无法复现了。构建脚本结束时会打印每个镜像的 digest，直接复制过来。

### 怎么准备一个训练镜像

仓库里 [images/train-pytorch](../images/train-pytorch) 是参考实现（Ray + PyTorch + CUDA + transformers/datasets/accelerate）。按需要改 Dockerfile 后：

```bash
BUILD_TARGETS=train-pytorch IMAGE_TAG=v1 PUSH_IMAGE=true bash build-image.sh
```

把输出的 `@sha256:...` 登记进目录即可。

---

## 5. 私有 Git 仓库凭证

工程师可在「账户与安全」添加**个人凭证**；TenantAdmin 可在「租户与配额」添加**团队凭证**。两者均只匹配一个 Git 主机名。

| 字段 | 说明 |
|---|---|
| Git 主机 | **只填主机名**，如 `git.internal.example`，不带协议和路径 |
| 用户名 | 留空则用 `git` |
| 访问令牌 | Personal Access Token 或密码 |

拉取代码时，平台先查提交者的个人凭证，再回退到租户团队凭证。个人凭证不会被其它用户使用；团队凭证适合受控的团队共享仓库。

安全上有三点设计：

- **令牌只写入租户 namespace 的 Kubernetes Secret**，平台数据库只保存受控引用
- **接口从不回显令牌或 Secret 名**，列表仅显示用途、主机名和作用域
- **通过 `GIT_ASKPASS` 传给 git**，不写进 remote URL——否则会残留在 `.git/config` 和进程参数里

每位用户对同一主机最多有一个个人凭证；每个租户对同一主机最多有一个团队凭证。重复添加会更新自己的同一作用域凭证，不会覆盖别人的凭证。

---

## 6. 配额管理

「租户与配额」页顶部显示各团队的 GPU 使用情况。

租户 GPU 配额是并发预算，不是节点探测结果。当前生产基线有两台 8 卡 Ready 节点，物理容量和 `local` 团队额度均为 16。新增并正确标签 GPU 节点后，平台会自动扩大物理容量与 Kueue ClusterQueue；是否给某个团队增加额度，仍由 SuperAdmin 在页面决定。

**集群总配额是自动的**：控制器每几秒把 Kueue 的 `nominalQuota` 对齐到带训练标签且 Ready 的节点总容量。加机器只需要打标签，不用改配额。

**团队配额是手动的**：建租户时设定，决定该团队最多能同时占用多少卡。所有团队配额之和可以超过物理总量（超卖），Kueue 会按队列顺序准入。

不要为限制某个团队而关闭 `kueue.autoQuota`；该开关是物理 ClusterQueue 容量同步，不是团队预算开关。请直接在“租户与配额”修改目标团队额度。TenantAdmin 只能看本团队分配额度、已用和当前可提交上限，不能给自己加额度。

---

## 7. 数据空间管理

“我的数据”是训练数据入口，不要求用户了解 TOS、PVC 或 AK/SK。每个本地账号创建成功时，平台已经为其自动分配一个独立的受管个人空间；平台不支持管理员替用户填写任意桶路径，也不支持用户选择其他人的目录。

- 用户可在“我的工作区”“我的文件”“我的训练结果”下浏览、上传、覆盖和创建目录；页面不提供数据下载。
- `TenantAdmin` 可在“团队共享数据”下浏览、上传、覆盖和创建目录；Engineer 只能浏览和选作输入。无论角色，训练和调试 Pod 中的 `/mnt/storage/team` 始终只读。
- `SuperAdmin` 可在“公共数据”下上传、覆盖和创建目录；训练和调试 Pod 中的 `/mnt/storage/public` 始终只读。
- IDC 数据目录由基础设施侧登记为只读 NFS PV/PVC；Portal 不枚举其文件，但用户可把已登记的逻辑目录作为训练输入。

页面有两个独立状态，务必不要混淆：

- **个人空间已就绪**：用户可在网页浏览、上传、覆盖和创建自己的 TOS 文件夹，但不能从页面下载数据。
- **GPU 挂载已就绪**：调试/训练 Pod 可在固定 Linux 路径中读写同一份空间。若显示“GPU 挂载待配置”，对象空间仍可正常使用，只是不能被 GPU Pod 作为训练输入或输出；完成 CSI/IRSA 验收后再启用。

IDC 使用 `idcDataSpaces` 三个固定导出（`original`、`wellspiking`、`shared`），启用后平台按租户创建受控 `ReadOnlyMany` PV/PVC。NFS 地址与导出路径只存在部署 Profile；它们不进入数据库、浏览器、任务请求或用户 Pod 的环境变量。不得用旧的 `storage.existingClaim` 将一个临时通用 PVC 作为用户数据空间。

不要在租户 namespace 创建或复制 TOS 凭据给用户 Pod。浏览、上传签名和目录初始化只由平台控制面完成；RayJob 和调试容器不应包含长期对象存储凭据。

### 首次启用个人 TOS 硬配额

在测试 Profile 中启用 `personalStorage.objectSetQuota` 后，以 SuperAdmin 登录并在「租户与配额」点击 **初始化 TOS 目录配额**。这是唯一一次桶级操作：它将 ObjectSet 目录层级固定为 5，正好对应平台受控的个人根目录 `ray-train/tenants/<tenant>/users/<user>/`。操作不会删除、移动或授予用户桶级权限；若桶已使用不兼容的 ObjectSet 层级，平台会拒绝操作而不是覆盖配置。

此后由平台在创建用户或调整额度时为该用户的固定根目录创建/更新 ObjectSet。用户只在 Portal 与 `/workspace`、`/mnt/storage/me` 看到自己的逻辑空间，调试/训练 Pod 通过 FSX CSI 的组件级 IRSA 挂载，不会收到 TOS AK/SK。

> ObjectSet 配额由 TOS 强制执行；用量统计可能有短暂延迟。因此缩容必须先确认用户当前占用量，扩容可以随时生效。

---

## 8. 日常巡检

```bash
kubectl -n ray-train-platform get pods && kubectl get clusterqueue cluster-gpu-queue -o jsonpath='{.spec.resourceGroups[0].flavors[0].resources}'
```

```bash
kubectl -n ray-train-platform logs deploy/ray-train-backend --tail=50 | grep -iE 'error|quota sync'
```

关注点：

- 三个平台 Pod 都是 `Running`
- ClusterQueue 配额等于物理卡数（不等说明节点标签或自动同步有问题）
- 是否有长期占用 GPU 的调试工作区

---

## 9. 常见处置

**任务全部卡在排队**
先看 ClusterQueue 配额是不是 0 或过低。Kueue 配额低于物理容量时任务会静默排队，不报错。

**某个团队报 CreateContainerConfigError**
先查看对应 Pod 的 Event。数据空间正式启用后，优先检查 csi-fsx 组件的 IRSA、该用户数据绑定 PV/PVC 是否为 `Bound`、以及 TOS 前缀挂载预检；不要向租户 Pod 注入 `tos-credentials` 作为修复手段。

**用户说镜像里缺依赖**
构建新镜像登记进目录，不要让用户长期靠 `pip install` 绕。

**调试环境占着 GPU 不放**
目前没有空闲自动回收，需要联系用户手动停止，或直接删除对应 RayCluster。

**任务显示“未知”或一直在启动中**
先查看 RayJob 的 `status.jobDeploymentStatus` 与 Event。`Suspended` 是等待 Kueue 准入，`Initializing` 是创建 RayCluster/Submitter。新版本会正确映射这些状态；若旧任务的 Submitter 模板缺少 `restartPolicy: Never`，不要原地修改 CR，应由提交者确认后取消并重新提交。

**已结束任务没有日志或指标**
检查 Loki、Prometheus 和 DCGM Exporter。Alloy 只负责采集/转发，不能替代 Loki 存储。成功任务终态后默认在 60 秒内关闭 RayCluster并释放 GPU，失败任务默认保留 600 秒原生排障窗口；数据库任务历史不删除。没有 Loki 时，Pod 与 RayCluster 清理后的日志不可恢复。

**要下线一个团队**
先确认没有运行中的任务，再删 namespace。数据库记录目前需要手工清理。
