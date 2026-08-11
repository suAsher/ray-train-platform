# 管理员手册

面向平台管理员（`SuperAdmin` / `TenantAdmin`）。日常使用看 [USER_GUIDE.md](USER_GUIDE.md)，部署运维看 [BUILD_AND_DEPLOY.md](BUILD_AND_DEPLOY.md)。

---

## 1. 角色模型

| 角色 | 能做什么 |
|---|---|
| `SuperAdmin` | 全平台：建租户、发布共享镜像、查看所有租户 |
| `TenantAdmin` | 本团队：建账号、登记本团队镜像、配 Git 凭证、管理配额 |
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

> **建完团队还要做一步**：每个租户 namespace 需要有对象存储凭证 Secret，否则该团队的训练 Pod 会停在 `CreateContainerConfigError`。见 [BUILD_AND_DEPLOY.md 第 7 节](BUILD_AND_DEPLOY.md)。

---

## 3. 新建用户账号

「租户与配额」→ 页面下方「平台用户与 RBAC 角色分配」→ **添加用户**。

账号创建在**你自己的租户**下——请求体里传别的 tenantId 会被忽略，租户管理员无法往其他团队塞人。

创建后把初始密码给本人，让 TA 登录后自行修改。

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

「租户与配额」→「私有 Git 仓库凭证」→ **添加凭证**。

| 字段 | 说明 |
|---|---|
| Git 主机 | **只填主机名**，如 `git.internal.example`，不带协议和路径 |
| 用户名 | 留空则用 `git` |
| 访问令牌 | Personal Access Token 或密码 |

配好之后，本团队用户拉该主机的仓库会自动带上认证，用户端无感。

安全上有三点设计：

- **令牌只写入租户 namespace 的 Kubernetes Secret**，平台数据库只存 Secret 名字
- **接口从不回显令牌**，列表里只能看到主机名和 Secret 名
- **通过 `GIT_ASKPASS` 传给 git**，不写进 remote URL——否则会残留在 `.git/config` 和进程参数里

一个主机只能有一个凭证，重复添加会覆盖。

---

## 6. 配额管理

「租户与配额」页顶部显示各团队的 GPU 使用情况。

**集群总配额是自动的**：控制器每几秒把 Kueue 的 `nominalQuota` 对齐到带训练标签且 Ready 的节点总容量。加机器只需要打标签，不用改配额。

**团队配额是手动的**：建租户时设定，决定该团队最多能同时占用多少卡。所有团队配额之和可以超过物理总量（超卖），Kueue 会按队列顺序准入。

如果要给某团队一个刻意小于物理容量的预算，关掉自动同步：

```bash
helm upgrade ray-platform ./helm/ray-train-platform -n ray-train-platform -f <values> --set kueue.autoQuota=false --wait
```

---

## 7. 日常巡检

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

## 8. 常见处置

**任务全部卡在排队**
先看 ClusterQueue 配额是不是 0 或过低。Kueue 配额低于物理容量时任务会静默排队，不报错。

**某个团队报 CreateContainerConfigError**
该租户 namespace 缺 `tos-credentials` Secret。

**用户说镜像里缺依赖**
构建新镜像登记进目录，不要让用户长期靠 `pip install` 绕。

**调试环境占着 GPU 不放**
目前没有空闲自动回收，需要联系用户手动停止，或直接删除对应 RayCluster。

**要下线一个团队**
先确认没有运行中的任务，再删 namespace。数据库记录目前需要手工清理。
