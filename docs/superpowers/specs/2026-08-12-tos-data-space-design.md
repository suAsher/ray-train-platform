# TOS 数据空间与训练挂载设计

日期：2026-08-12  
状态：已确认，待用户审阅书面设计  
范围：以 TOS 为训练数据平台、用户数据浏览与选择、Ray 调试/训练挂载、IDC 只读数据接入

## 1. 决策摘要

`shanghai-data-transfer` 是训练平台的用户数据桶。它是数据的唯一持久化来源；平台不再把“数据集、Checkpoint、产物”建模为用户需要理解和维护的 PVC。

PVC、PV 和 CSI/FSX 仅是内部的 **挂载适配器**：平台根据用户和任务生成、绑定并回收它们，使特定 TOS 前缀以文件夹形式出现在 Ray Pod 中。用户不会看到 Bucket、前缀、PVC 名称、PV 名称、AK 或 SK。

`vke-cluster` 保持为系统桶，只供 Loki、备份及平台内部服务使用；它不出现在训练用户的文件浏览器或 Pod 挂载中。

用户不会获得整桶读写权限。所谓“充分使用整个 TOS”是指：整个训练桶按受控命名空间承载所有个人、团队和公共数据；每个用户只能看到其授权根及其子目录。这是体验简单与真实隔离同时成立的唯一做法。

## 2. 非目标与兼容边界

- 不在此变更中删除现有 `tos-local-datasets`、`tos-local-checkpoints`、`tos-local-outputs` PVC，已提交任务和历史产物继续可用。
- 不把用户节点上的 SSHFS 个人目录带入 Ray Pod。它无法保证每个 Worker 一致可见，也会将节点身份带入用户工作负载。
- 不让用户在网页输入 `tos://`、PVC、NFS 路径、AK/SK 或任意挂载参数。
- 不默认自动删除用户训练数据、代码或产物；生命周期策略必须经管理员单独配置后才可启用。
- TOS 不是本地 NVMe 缓存的替代品。GPU 节点本地数据盘只承担可丢失的读取缓存和临时盘。

## 3. 存储命名空间

新任务使用下列稳定布局；`<subject>` 是 Keycloak `sub`，不会因为用户改名而改变。

```text
shanghai-data-transfer
└── ray-train/
    ├── public/                                      # 全平台公共数据，只读
    └── tenants/
        └── <tenant>/
            ├── shared/                              # 本租户共享数据，只读
            ├── users/
            │   └── <subject>/
            │       ├── workspace/                   # 调试代码、环境配置
            │       ├── files/                       # 私有输入数据
            │       └── runs/                        # 每个任务的产物和 checkpoint
            └── legacy/                              # 仅历史兼容，平台不为新任务写入
```

首次访问时，平台幂等创建用户的 `workspace/`、`files/`、`runs/` 标记对象并保存 `subject → TOS 空间` 映射。TOS 的“目录”本质上是对象前缀；目录标记仅用于让空目录能在页面中可见。

用户拥有的逻辑空间如下：

| 页面根目录 | TOS 根 | 权限 | 管理者 |
| --- | --- | --- | --- |
| 我的工作区 | `users/<subject>/workspace/` | 读写 | 用户 |
| 我的文件 | `users/<subject>/files/` | 读写 | 用户 |
| 我的训练结果 | `users/<subject>/runs/` | 读写 | 用户；任务自动新建子目录 |
| 团队共享数据 | `tenants/<tenant>/shared/` | 只读 | TenantAdmin 发布 |
| 公共数据 | `public/` | 只读 | SuperAdmin 发布 |
| IDC 只读数据 | 静态 NFS 导出 | 只读 | 基础设施管理员 |

团队和公共数据只能由“发布”操作生成：管理员从已授权个人空间或受控导入来源复制到目标空间，不能通过用户任务直接写入。

## 4. 权限与身份模型

权限必须同时在 Portal、Kubernetes 和 TOS 三层执行，任何一层都不能单独作为安全边界。

### 4.1 Portal 与控制面

- Keycloak `sub` 是用户空间的唯一身份；本地测试账号在启用后也映射到不可变平台 subject。
- 文件浏览 API 只接受 `space`、相对路径和分页游标；拒绝绝对路径、`..`、反斜杠、URL、Bucket 名和未登记前缀。
- API 响应只返回逻辑空间名、相对路径、文件名、大小和修改时间；不返回 TOS Bucket、完整前缀、Secret 或 PVC 名。
- 上传使用平台签发的单对象或分片预签名请求，且每一个请求都绑定到当前用户可写空间下的目标相对路径。浏览器永不持有长期 AK/SK。

### 4.2 TOS 与挂载身份

- 现有宽权限 AK/SK 只保存在平台控制面 Secret，用于初始化目录、受控列举、签发上传请求及管理员发布操作；绝不注入训练或调试容器。
- VKE FSX CSI 使用 IRSA 作为正式工作负载挂载鉴权。平台为每一个用户空间创建或复用受限 Kubernetes ServiceAccount/IAM Role，其 TOS 权限只能覆盖：自己的前缀读写、所属租户 `shared/` 只读和 `public/` 只读。
- 如 IRSA 无法满足 CSI 的实际版本能力，退回方案只能是平台服务端以 STS 下发短期、前缀受限凭据给 CSI 挂载辅助组件，并实现自动刷新；不得把长期凭据写入任务 Spec 或用户容器环境。
- 团队/公共空间使用只读 TOS 策略和只读 `volumeMount` 双重约束。个人空间使用仅限个人根的读写策略。
- End-user 不拥有目标租户 namespace 的 `pods/exec`、`secrets`、`serviceaccounts/token`、`persistentvolumes`、`persistentvolumeclaims` 或自建 Pod 权限；只有平台控制器可创建 RayJob、RayCluster、PV/PVC 和绑定工作负载身份。

火山文档说明 FSX CSI 可将 TOS 作为 POSIX 文件系统挂载，支持指定前缀，且支持 IRSA；若采用临时 Token 鉴权，需要处理 Token 刷新。实现前必须以当前 VKE 组件版本作一次单卡实际挂载验收，而不是只依赖模板。 [VKE FSX TOS 静态卷](https://www.volcengine.com/docs/6460/1828107) [TOS 前缀挂载](https://www.volcengine.com/docs/6720/161878?lang=zh)

### 4.3 为什么仍可能存在 PVC

PVC 是 Kubernetes 中“把一个已经授权的远端文件系统交给某个 Pod”的句柄，不是数据资产。实现上可存在下列平台托管卷：

- 每个用户一个个人 TOS 根挂载；
- 每个租户一个团队只读根挂载；
- 一个公共只读根挂载；
- IDC 的静态 NFS 只读卷；
- 每个任务/节点的可丢失本地缓存卷。

它们均由平台生成和管理，不在 Portal 的数据选择界面出现。这样既能使用整个 TOS 作为数据平台，也能在 Pod 层落实前缀授权。

## 5. Ray 运行时数据契约

### 5.1 调试环境

调试环境运行在 GPU Worker 上。Head 只承担 Ray 控制面和入口转发；JupyterLab、VS Code、Terminal 进入 GPU Worker。

Worker 内固定呈现：

```text
/workspace                         → 我的工作区（TOS，读写）
/mnt/storage/me                    → 我的 TOS 空间（读写）
/mnt/storage/team                  → 团队共享数据（只读）
/mnt/storage/public                → 公共数据（只读）
/mnt/idc/original                  → IDC original（只读）
/mnt/idc/wellspiking               → IDC wellspiking（只读）
/mnt/idc/shared                    → IDC shared（只读）
/mnt/cache                          → 节点本地可丢失缓存
```

`/workspace` 是个人空间的 `workspace/` 子目录，而不是 `emptyDir`；用户在 IDE 中保存的代码会直接持久化到 TOS。FSX 的 POSIX 兼容性、并发编辑与 Git 操作必须在单卡验证中验收；如果实测不能满足交互编辑，再将 `/workspace` 切为持久工作盘并以显式同步任务把 TOS 保持为源数据，不能静默丢失更改。

### 5.2 训练任务

Ray Head 和所有 Worker 均获得同一组逻辑根，保证多机路径一致：

```text
/workspace                         → 任务提交时冻结的代码快照，只读
/mnt/storage/me                    → 我的 TOS 空间（读写）
/mnt/storage/team                  → 团队共享数据（只读）
/mnt/storage/public                → 公共数据（只读）
/mnt/data/input                    → 表单选中的输入目录，只读别名
/mnt/data/output                   → 我的训练结果/runs/<job-id>/，读写别名
/mnt/data/checkpoints              → output/checkpoints/，读写别名
/mnt/idc/*                         → 已登记的 IDC NFS 数据，只读
/mnt/cache                          → 节点本地可丢失缓存
```

用户可从 `/mnt/storage/*` 查看被授权的数据；`/mnt/data/*` 是用于训练脚本的稳定、可复现快捷路径。提交时记录逻辑空间、相对路径、代码快照版本和输出目录；绝不记录或接受原始 Bucket URI。

## 6. 用户体验与 API 设计

### 6.1 “我的数据”页面

将当前“训练数据目录册/缓存”替换为“我的数据”。首屏是资源管理器，而不是资产卡片：

- 左侧固定树：我的工作区、我的文件、我的训练结果、团队共享、公共数据、IDC 只读数据；
- 右侧列出当前目录中的文件和子目录，显示大小、更新时间、来源、读写状态；
- 支持新建文件夹、上传、下载、重命名、删除；只读根仅显示允许的读下载操作；
- 允许用户把当前目录直接标为“作为训练输入”或“作为本次输出位置”；
- 管理员在个人目录项目上执行“发布到团队/公共”，并且后台进行受控 TOS Copy。

基础 API：

```text
GET    /api/v1/data-spaces
GET    /api/v1/data-spaces/{space}/entries?path=&cursor=
POST   /api/v1/data-spaces/{space}/folders
POST   /api/v1/data-spaces/{space}/uploads
POST   /api/v1/data-spaces/{space}/downloads
POST   /api/v1/data-spaces/{space}/publish
DELETE /api/v1/data-spaces/{space}/entries
```

所有 `path` 均为逻辑根内的相对路径。服务端做授权、规范化、分页和审计。预签名 URL 仅服务于校验通过的一个对象/分片，过期时间短，且只在当前浏览器操作流程中使用。

### 6.2 新建训练任务

页面从“技术配置表单”调整为引导式流程：

1. 选择工作区和训练模板；平台显示可运行的入口文件、最近一次调试状态和镜像预设。
2. 在“我的数据”树中选择输入目录；可选 IDC、个人、团队或公共数据，但只展示有读权限的根。
3. 输出默认到“我的训练结果/`<任务名>-<job-id>`”；用户可选择其个人空间内的上级目录，不能选择团队、公共或 IDC 目录。
4. 选择 GPU 数量、节点数和模板的少量高级覆盖项；提交前显示实际本地路径、权限和资源预览。

从调试页面点击“用此工作区创建训练”会自动带入工作区快照和最近选择的输入目录，减少重复填写。

## 7. IDC 与节点本地存储

IDC 数据不是 TOS 的替代品，也不出现在 TOS 树中。基础设施创建三个独立静态 NFS PV/PVC，分别对应：

```text
192.168.117.117:/storage/data-e2e/original
192.168.117.117:/storage/data-e2e/wellspiking
storage.westwell-lab.info:/mnt/yrfs/wellspiking-training/shared
```

所有挂载采用 NFS 只读与 Pod `readOnly: true` 双重限制。Portal 只展示 IDC 逻辑名称和可读目录，不能接受任意 NFS 路径。

未来 4090 节点的多块数据盘由节点初始化清单统一格式化和挂载。Ray Worker 仅以 `emptyDir`/Local PV 使用它们作为 `/mnt/cache`；缓存键包含 TOS 对象版本或 ETag，缓存可随节点淘汰，不是用户持久数据。

## 8. 迁移、错误处理与可观测性

### 8.1 迁移

- 保留旧静态 PVC 与历史任务，Portal 在过渡期将其标记为“历史数据”，不再允许新任务选择。
- 新提交任务只使用逻辑空间。已有 `outputs/...` 下的历史内容由管理员按需复制到用户 `runs/`，不自动移动或删除。
- 任何一个 TOS/IRSA/CSI 挂载预检失败时，任务保持“配置检查失败”，不创建 RayJob；UI 展示可行动原因，例如“个人数据空间尚未创建”或“节点无法挂载 TOS”。

### 8.2 可观测性

- 平台记录每次空间初始化、挂载预检、数据发布、上传和训练路径解析的审计事件，绝不记录 Secret、Token 或预签名 URL。
- TOS 挂载就绪、挂载错误、上传失败、FSX 客户端错误与缓存命中率进入 Prometheus/Loki。
- 日志架构拆为：物理节点 Alloy DaemonSet 收集节点/容器日志；控制面 Alloy Deployment 收集 Kubernetes API 事件。未来不再依赖虚拟节点。

## 9. 一次单卡验收

在移除当前单卡测试节点前完成以下不破坏性验收：

1. 创建两个测试用户；用户 A 通过页面创建个人目录并上传一个测试文件，用户 B 无法列举或读取该对象。
2. 用户 A 启动调试环境，GPU Worker 能读写 `/workspace` 与 `/mnt/storage/me`，能读取团队/公共/IDC 根，向只读根写入被拒绝。
3. 从工作区提交单卡 RayJob；Head 和 Worker 均看到相同路径，脚本读 `/mnt/data/input` 并向 `/mnt/data/output` 写入结果与 checkpoint。
4. 训练完成后，Portal 在“我的训练结果”中列出任务产物；用户 A 可下载，用户 B 不可见。
5. 训练 Pod 与浏览器网络响应中都不存在长期 AK/SK；任务失败时返回可解释的挂载或授权原因。

## 10. 依赖与实施顺序

1. 确认并在测试集群验证 csi-fsx 版本、IRSA 是否已启用、其对当前 TOS Bucket 的前缀挂载和令牌刷新行为。
2. 落实最小权限 IAM Role 与服务账号映射；在这一步完成前，不以广权限 Secret 挂载用户数据。
3. 实现数据空间领域模型、受控文件浏览/上传/发布 API 和“我的数据”页面。
4. 实现平台托管 TOS 挂载协调器与 RayJob/Dev RayCluster 统一路径契约，先完成单卡验收。
5. 改造训练提交引导和从调试工作区到训练的快捷路径。
6. 将 Alloy 拆分为物理节点 DaemonSet 与控制面采集器，部署 DCGM Exporter、Prometheus、Grafana；Loki 继续使用系统 TOS 桶。

任何步骤都不以用户提供的长期 AK/SK 作为训练工作负载凭据。火山也建议长期密钥只保存于服务端，并优先用 STS 临时凭据。 [STS 临时凭据说明](https://www.volcengine.com/docs/86081/1660350) [TOS 桶策略说明](https://www.volcengine.com/docs/6349/196032?lang=zh)
