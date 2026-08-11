# Ray 分布式训练平台

运行在火山云 VKE 上的多租户 Ray 训练平台：Web Portal 提交训练任务，KubeRay 执行，Kueue 管配额，支持本地账号或企业 SSO 登录。

## 文档

**使用者**

| 文档 | 内容 |
|---|---|
| [docs/USER_GUIDE.md](docs/USER_GUIDE.md) | 算法工程师日常使用：提交任务、选镜像、调试环境、命令行提交 |
| [docs/ADMIN_GUIDE.md](docs/ADMIN_GUIDE.md) | 管理员：建租户、建账号、镜像目录、私有仓库凭证、配额、巡检 |

**平台与运维**

| 文档 | 内容 |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 架构图、逐层实现状态、与原设计的差异、迭代建议 |
| [docs/DEPLOY_FROM_SCRATCH.md](docs/DEPLOY_FROM_SCRATCH.md) | 从零部署 / 换集群 / 换节点 / 切正式部署 |
| [docs/BUILD_AND_DEPLOY.md](docs/BUILD_AND_DEPLOY.md) | 日常构建发布、登录配置、扩容、发版 |
| [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) | 仓库结构、本地开发、已踩过的坑、常见改动怎么做 |
| [docs/CLUSTER_DEPLOYMENT_GUIDE.md](docs/CLUSTER_DEPLOYMENT_GUIDE.md) | 集群前置条件、Keycloak/LDAP、IDC 存储 |

## 技术栈

Go 1.25 · Gin · GORM · PostgreSQL · client-go · KubeRay · Kueue · Vue 3 · Element Plus · Helm

## 快速开始

```bash
cd backend && go test ./... && cd ../frontend && npm install && npm run build
```

部署到集群见 [docs/BUILD_AND_DEPLOY.md](docs/BUILD_AND_DEPLOY.md)。

## 冒烟验证

提交一个 1 卡训练任务并等待结束：

```bash
API_URL=http://<portal-host> PLATFORM_USER=admin PLATFORM_PASSWORD=<密码> IMAGE=<镜像@sha256:...> python3 scripts/submit_smoke_job.py
```

## 当前状态

训练闭环已在真实 GPU 上跑通：提交 → Kueue 准入 → RayJob 运行 → GPU 计算 → 产物读写 → 成功 → GPU 自动回收。

未完成的主要有三块：可观测性采集侧（Loki / Prometheus / DCGM / Grafana 均未部署）、任务模板中心、TOS 凭据（当前 Secret 中的 SK 无效）。详见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)。
