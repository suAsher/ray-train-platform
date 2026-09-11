# 代码与版本地图

## 四端后端代码

| 端 | 位置 | 角色 |
|---|---|---|
| 本地 | `ray-train-platform` 的 `main` | 开发与审阅源 |
| GitHub | `origin/main` | 外部代码镜像 |
| 内部 GitLab | `gitlab/main` | Wellspiking 内部代码镜像 |
| 构建机 | `/opt/guofeng/vke-cluster/ray-platform-main` 的 `main` | 唯一构建和 Helm 发布源 |

远端必须核对为 `origin=git@github.com:suAsher/ray-train-platform.git`、`gitlab=ssh://git@gitlab.wellspiking.ai:32022/guofeng.su/ray-train-platform.git`；内部 GitLab 使用 `~/.ssh/id-spiking`。SSH 身份负责远端授权，commit author/committer 仍需设置平台身份，二者不是一回事。

四端一致指四个**完整 commit SHA** 相同，不是文件看起来相同。镜像 digest 和 Helm revision 是另外两个版本维度：

```text
源码版本 = commit SHA
可执行版本 = Harbor image digest
集群配置版本 = Helm revision
```

核对时分别取值，不能用 tag 代替 digest：

```bash
git rev-parse HEAD
git ls-remote origin refs/heads/main
git -c core.sshCommand='ssh -i ~/.ssh/id-spiking -p 32022 -o IdentitiesOnly=yes' ls-remote gitlab refs/heads/main
ssh -i ~/.ssh/qomolo-desktop.pem root@14.103.49.106 \
  'cd /opt/guofeng/vke-cluster/ray-platform-main && git rev-parse HEAD && git status --short'
ssh -i ~/.ssh/qomolo-desktop.pem root@14.103.49.106 \
  "kubectl -n ray-train-platform get pods -o custom-columns='N:.metadata.name,ID:.status.containerStatuses[0].imageID' --no-headers; helm history ray-platform -n ray-train-platform --max 3"
```

## 前端是独立的第五个代码面

Portal RayTrain 的权威源不在四端后端仓库中：

- 仓库：`ssh://git@gitlab.wellspiking.ai:32022/wellspiking/frontend/wellspiking-frontend.git`
- 分支：`dev`
- 核心目录：`src/views/rayTrain/`
- 发布：推送 `dev` 后由 GitLab CI/CD 构建部署
- 开发验收：`https://spiking-dev.wellspiking.ai/raytrain/`

本仓库 `frontend/` 是独立旧前端，不是 Portal RayTrain 发布源。报告版本时不得把 Portal commit 写成后端四端的一部分。

## 功能与构建目标

| 改动 | 典型代码位置 | 必须构建/发布 |
|---|---|---|
| REST API、权限、数据库、Kueue/RayJob 渲染 | `backend/api/`, `backend/httpapi/`, `backend/domain/`, `backend/db/`, `backend/k8s/` | `backend` + Helm backend digest |
| `spk-rayjob` 命令、帮助、提交合同 | `backend/spkrayjob/`, `backend/cmd/spk-rayjob/` | `backend,spk-rayjob` + Helm 两个 digest |
| Helm 参数与集群资源 | `helm/ray-train-platform/`, `deploy/`, `ops/` | 先做 server-side dry-run/diff，再 Helm |
| 数据集发布运行时 | `images/dataset-publisher/` 及对应后端调度代码 | `dataset-publisher`；如合同变更同时构建 `backend` |
| IDC 增量同步 | `images/idc-sync/`、`backend/idcsync/` 及对应后端代码 | `idc-sync`；如 API/渲染变更同时构建 `backend` |
| 代码包/源码物化 | `images/source-materializer/` 及对应后端代码 | `source-materializer`；如 API/渲染变更同时构建 `backend` |
| 调试环境运行时 | workspace 镜像与 Helm 配置 | `workspace` |
| Portal 页面/API 适配 | Portal `src/views/rayTrain/` | Portal lint 闸门 + 推 `dev`；不从本仓库构建 |

改动前先用 `git diff --name-only <base>...HEAD` 映射目标，不要用 `BUILD_TARGETS=all` 规避判断。

## 提交与同步约束

- 后端 commit 作者固定为 `guofeng.su <guofeng.su@westwell-lab.com>`。
- 本地只编辑、审阅和 `git diff --check`；候选 commit 通过 bundle 到构建机的 detached worktree 测试。
- 先测候选 commit，再推 GitHub/GitLab，最后快进构建机正式目录。
- 不用 rsync/scp 覆盖正式源码，不在构建机正式目录直接改代码。
- 四端核对还必须分别报告本地和构建机 `git status --short`。源码 HEAD 相同不代表工作区干净；正式构建/发布要求工作区干净，只读状态交接则明确列出未提交的文档或代码，不自动提交推送。
