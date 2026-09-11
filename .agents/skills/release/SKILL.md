---
name: release
description: "ray-train-platform 的代码定位、开发、测试、构建、四端同步、部署、spk-rayjob 真实提交验收、现网排障与回滚全流程。当需要改这个仓库的代码、跑测试、构建镜像、把改动上线到集群、核对版本、排查任务/队列/调试环境/MLflow/上传问题、验收上线结果或回滚时，使用本 skill。集群操作有若干不查就会踩的坑(构建机连不上 GitHub、overlay 固定旧摘要会静默回滚、Helm 把大整数渲染成科学计数法)，所以即使只是「构建一下镜像」也要先读本 skill。"
---

# ray-train-platform 发布流程

这套流程的正确性标准是**真实生产集群**,不是本地测试通过。下面每一条约束都对应一次真实事故或一次被拦下的事故,不是理论上的谨慎。

本文件是唯一维护源；`.claude/skills/release/SKILL.md` 只作兼容入口。版本核对、架构说明等只读请求不自动授权推送、部署、修改队列或提交验收任务。分别记录“代码已实现”“配置已启用”“真实验收通过”，不能相互替代。

## 按任务读取参考

主文档是发布闸门，不能跳过。遇到下列任务时，再读对应的一份参考：

- 不确定代码在哪、该构建哪个组件、如何核对四端版本：[references/repository-map.md](references/repository-map.md)
- 进行 `spk-rayjob` 用户视角的单机/多机提交验收：[references/acceptance.md](references/acceptance.md)
- 排查登录、路由、上传、队列、RayJob、调试环境、MLflow 或日志：[references/diagnostics.md](references/diagnostics.md)

新会话接手先读 [2026-09-11 现状快照](../../../docs/CURRENT_STATE_20260911.md)，再实时核对分支、镜像和配置。快照里的“已实现/未启用/未验收”不是永久状态，也不是本次任务授权；不要自动执行其后续建议。

## 环境事实

| 项 | 值 |
|---|---|
| 构建机 | `ssh -i ~/.ssh/qomolo-desktop.pem root@14.103.49.106` |
| 构建目录 | `/opt/guofeng/vke-cluster/ray-platform-main` |
| 镜像仓库 | `harbor.wellspiking.ai/guofeng.su` |
| Helm release | `ray-platform`,namespace `ray-train-platform` |
| 本地 | 有 kubectl 但**不是训练集群**;无 docker、无 helm |

### 前后端已经分仓

| 组件 | 权威仓库/分支 | 发布方式 |
|---|---|---|
| 后端、Helm、运行时 | 本仓库 `main` | 本地候选 commit → bundle 到构建机隔离测试 → 通过后推 GitHub/内部 GitLab → 快进正式构建目录 → 构建机镜像与 Helm 发布 |
| Portal 前端 | `ssh://git@gitlab.wellspiking.ai:32022/wellspiking/frontend/wellspiking-frontend.git` 的 `dev` | 只修改 `src/views/rayTrain/` 及其直接依赖，推 `dev` 后由 GitLab CI/CD 自动构建并部署 |

前端迁移后的测试入口为 `https://spiking-dev.wellspiking.ai/raytrain/rayTrain/job/list`。本仓库旧 `frontend/` 不再是 Portal RayTrain 页面发布源；除非用户明确要求维护独立旧入口，否则不要构建或部署它，也不要把 Portal 前端镜像写进后端 Helm 覆盖文件。

### 两套 Portal 代理 Ingress

| 环境 | kubeconfig | 可写边界 | 清单 |
|---|---|---|---|
| 新前端 dev | `~/.kube/test-dev.conf` | `guofeng-su` namespace | `deploy/portal/test-dev-raytrain-ingress.yaml` |
| 旧 common/生产 Portal | `~/.kube/common.conf` | 只可修改 `guofeng-su`，其他 namespace 只读参考 | `deploy/portal/common-raytrain-ingress.yaml` |

这两个 Ingress 只代理清单中明确列出的 `/raytrain/api/...`、`/raytrain/ray/...` 和 `/raytrain/mlflow/...`，绝不能写回 `/raytrain/(.*)`：NGINX 的正则匹配会把 SPA 路由 `/raytrain/rayTrain/...` 一并送到后端，表现为页面 401/404。`rewrite-target` 必须是 `/$1`。dev 清单只拥有 `spiking-dev.wellspiking.ai`；common 清单只拥有 `spiking.wellspiking.ai`，不要让两个集群声明同一个 dev host。清单存在不等于已应用，核对实际 Ingress 后才能报告上线状态。

Portal 浏览器认证走同域 OAuth2 Proxy。Ingress 通过 `auth-url` 验证会话并只转发 `X-Auth-Request-Access-Token` 等响应头；后端还会验证令牌签名、issuer 和 audience，再用 `preferred_username` 映射 RayTrain 成员。Keycloak/Portal 角色不能直接当作 `SuperAdmin` 或租户角色。平台成员表仍是 tenant、角色、配额和历史资源归属的权威来源，`spk-rayjob`/Ray CLI 仍用 PAT 访问生产域名。

修改 Ingress 时先备份、再 server-side diff，并只应用对应环境的清单：

```bash
kubectl --kubeconfig="$HOME/.kube/test-dev.conf" -n guofeng-su get ingress raytrain-spking-vke-proxy -o yaml > /tmp/test-dev-raytrain-ingress-before.yaml
kubectl --kubeconfig="$HOME/.kube/test-dev.conf" diff --server-side -f deploy/portal/test-dev-raytrain-ingress.yaml
kubectl --kubeconfig="$HOME/.kube/test-dev.conf" apply --server-side -f deploy/portal/test-dev-raytrain-ingress.yaml

kubectl --kubeconfig="$HOME/.kube/common.conf" -n guofeng-su get ingress raytrain-spking-vke-proxy -o yaml > /tmp/common-raytrain-ingress-before.yaml
kubectl --kubeconfig="$HOME/.kube/common.conf" diff --server-side -f deploy/portal/common-raytrain-ingress.yaml
kubectl --kubeconfig="$HOME/.kube/common.conf" apply --server-side -f deploy/portal/common-raytrain-ingress.yaml
```

应用后至少验证：SPA 返回 200；未登录 `/raytrain/api/v1/me` 返回 401；已登录 `/me` 返回平台成员的稳定 `subject/tenantId/roles`；大文件使用分片上传；`spk-rayjob` 仍通过 `https://raytrain.wellspiking.ai` 的 PAT 登录与提交。

本机不能构建镜像。VKE 训练集群操作在构建机上做；Portal Ingress 按上表用对应 kubeconfig、namespace 操作，不得混用集群。网络不可达时设置请求超时并报告未验证，不推断服务已经故障。

同目录下还散落着多个陈旧副本(`*-sync-backup-*`、`*-ray-data-staging`、`releases/*` 等),拿错目录会构建到过期代码。**只用 `ray-platform-main`**,用户已明确要求不要再用旧的 `ray-platform` 目录。

## 一、开发与测试边界

本机只编辑、做 `git diff --check` 和审阅 diff；用户已明确要求**编译、单元测试、lint 和镜像构建都不要消耗本机资源**。后端候选提交要先用 bundle 送到构建机的临时 detached worktree 测试，测试通过后才推两个远端并快进正式构建目录。不要为了测试先把未验证的 main 推出去。

构建机 Go builder 镜像有两个已知环境坑，测试命令必须显式处理：`/usr/local/go/bin` 不在 PATH；默认 `proxy.golang.org` 不可达。使用项目 Dockerfile 相同的 Alpine 镜像源和已验证的 `GOPROXY=https://mirrors.tencent.com/go/`，不要关闭 go.sum 校验。完整基线是：

```bash
GO_TEST_IMAGE='swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/golang:1.25-alpine@sha256:a9316ea600fe38d4527999823d67764dbd5b5ce4b4a0895266faf0134aa28264'
docker run --rm \
  -e GOPROXY=https://mirrors.tencent.com/go/ \
  -e PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  -v "$verify_dir:/workspace:ro" \
  -w /workspace/backend \
  "$GO_TEST_IMAGE" \
  sh -c 'alpine_version=$(cut -d. -f1-2 /etc/alpine-release); printf "%s/v%s/main\n%s/v%s/community\n" https://mirrors.aliyun.com/alpine "$alpine_version" https://mirrors.aliyun.com/alpine "$alpine_version" >/etc/apk/repositories; apk add --no-cache bash git build-base jq >/dev/null; go test -timeout=20m ./...'
```

必须挂载整个候选 worktree，只把工作目录设为 `/workspace/backend`；`config`、`domain` 和 `k8s` 的合同测试会读取仓库根下的 `helm/`、`deploy/`、`ops/` 与 `scripts/`。必须用 `sh -c`，不能用 `sh -lc`：Alpine 登录 shell 会重置 PATH，再次造成 `go: not found`。`bash` 与 `jq` 是 `scripts/e2e-training.sh` 合同测试的运行依赖，不是可选工具。

Portal 前端也不在本机安装依赖或运行 lint。先核对远端 `dev` 和本地分支，不得拿旁边的 `master` checkout 代替。把 `dev` 候选 commit 用 `git archive` 生成归档；归档不含 `.git` 和未跟踪文件，但**会包含已跟踪的 `.env*`**，传输前审查文件列表，保留所需的非敏感构建配置，不携带私密配置。送到构建机临时目录后执行 `docker build --pull -f docker/Dockerfile.lint .`。以候选提交的 Dockerfile/CI 为准，至少运行 `pnpm lint:check`、`pnpm check:ep`、`pnpm check:store`，以及已存在的 RayTrain 路由、日志导出、访问合同测试；通过后才推 `dev`。推送成功、CI 成功、线上镜像更新、登录浏览器验收是四项独立证据。不要从本仓库构建 Portal 前端。

Portal 仓库的 pre-push hook 可能在本机安装依赖、自动修改文件或重复构建。候选已在构建机通过上述完整门禁时，推送使用 `git push --no-verify`，推送后再核对 GitLab CI/CD；禁止让 hook 在本机消耗构建资源或产生未审阅改动。

开始 Portal 变更前，用 `git remote -v`、`git branch --show-current`、`git ls-remote <Portal仓库URL> refs/heads/dev` 确认仓库与远端基线；候选基于该 dev 提交创建。推送前再核对远端，若别人已更新，先整合并重测，不能 force push 覆盖。

本仓库独立旧前端若被明确要求维护，测试命令是 `npm test && npm run build`；测试跑 `node --test`，**不是 vitest**。直接 `npx vitest run` 会把测试工具用错。

### 数据库迁移

`backend/db/migrations/NNNN_name.up.sql`,编号连续,**只有 up 没有 down**。开头固定两行:

```sql
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
```

加完迁移必须同步检查 `backend/db/postgres_test.go` 的 `TestMigrationVersionsEmbedded` 与 `backend/db/postgres_integration_test.go` 的完整迁移版本断言。保留分阶段升级测试刻意停在历史版本的 fixture，不得机械替换全部版本号。除普通 Go 测试外，在构建机隔离 PostgreSQL 测试库验证全新安装、重复执行及带旧数据的升级；没有配置真实 PostgreSQL 时 integration test 的 skip 不能算通过。迁移在后端启动时自动执行，无需手工应用 SQL。

团队调整必须保留稳定个人存储归属，授权来自当前有效 membership，不能从历史资源归属反推访问权限；验证旧团队 PAT 被拒绝、目标团队权限生效且个人存储根不变。不要借发布流程直接修改用户或团队数据。

### 策略闸门测试

仓库里有一类**故意写成"某功能不得存在"**的测试,例如 `frontend/src/dataDownloadPolicy.test.js`、`backend/api/data_space_operations_test.go` 里的 `...RouteIsNotRegistered`。它们不是过时的测试。

如果你在有意变更策略(比如开放某个下载入口),**改断言、不要删测试**:保留原有仍然成立的边界,把新边界写成新断言钉住。删掉等于把护栏拆了。

## 二、验证候选提交并同步四端

构建机**到 GitHub 两条路都不通**:SSH 无密钥,HTTPS 间歇性 `GnuTLS recv error`。所以走 git bundle,**不要用 rsync 覆盖源码**:

```bash
# 1. 本地把已经审阅的改动提交为候选；作者必须是平台身份
git -c user.name=guofeng.su -c user.email=guofeng.su@westwell-lab.com commit ...
git bundle create /tmp/rtp-<short-sha>.bundle <构建机当前HEAD>..main
scp -i ~/.ssh/qomolo-desktop.pem /tmp/rtp-<short-sha>.bundle root@14.103.49.106:/tmp/

# 2. 构建机只 fetch，不先修改正式 main；在 /tmp detached worktree 跑完整测试
cd /opt/guofeng/vke-cluster/ray-platform-main
git bundle verify /tmp/rtp-<short-sha>.bundle
git fetch /tmp/rtp-<short-sha>.bundle main
verify_dir="$(mktemp -d /tmp/rtp-verify.XXXXXX)"
git worktree add --detach "$verify_dir" FETCH_HEAD
# 使用上一节的固定 Go builder 命令，在只读挂载的完整 $verify_dir 上运行全部测试
git worktree remove "$verify_dir"

# 3. 测试通过后，本地同时推两个 main
git push origin main
git -c core.sshCommand='ssh -i ~/.ssh/id-spiking -p 32022 -o IdentitiesOnly=yes' push gitlab main

# 4. 构建机正式目录只做快进，并再次核对
git merge --ff-only FETCH_HEAD
git rev-parse HEAD             # 必须等于本地/GitHub/GitLab 的计划 commit
git status --short             # 必须为空
rm -f /tmp/rtp-<short-sha>.bundle
```

最终必须核对本地 `main`、GitHub `origin/main`、内部 GitLab `gitlab/main`、构建机 `ray-platform-main` 四个完整 SHA 相同。构建前正式目录还必须干净。**不要在构建机上临时改源码再构建**,那样产出的镜像与任何 commit 都对不上,事后无法追溯。

比较远端时先 fetch 或直接 `git ls-remote`，不能只读可能陈旧的本地 remote-tracking ref。只有文档/skill/图片变化时无需重建镜像；审阅、链接/格式校验即可，报告未部署的文档差异。没有推送授权就保留本地交付，并明确四端 HEAD 与本地未提交文件的区别。

本仓库 `.github/workflows/ci.yml` 仍含 push 后构建旧前后端镜像的历史工作流，不是 Harbor/Helm 的生产发布链路。仅文档/skill/图片同步且无需构建时，可用 `[skip ci]` 提交标记避免触发无关 CI；业务代码不能靠该标记绕过门禁。不要为了本次推送顺手修改工作流或注册表凭据。

## 三、构建

Harbor 凭据已存在于构建机 `/root/.docker/config.json`。**不要代替用户执行 `docker login`**,也不要把密码写进命令。验证凭据有效的方式是看报错是 `not found` 还是 `unauthorized`。

只构建本次真正改动的后端/运行时组件。Portal 前端已分仓，推送后由自己的 CI/CD 发布，不能在这里构建。`BUILD_TARGETS=all` 会连带构建大量训练和工作区镜像,几十分钟起步:

```bash
cd /opt/guofeng/vke-cluster/ray-platform-main
IMAGE_TAG=release-$(date -u +%Y%m%d)-01 \
REGISTRY=harbor.wellspiking.ai/guofeng.su \
BUILD_TARGETS=backend \
PUSH_IMAGE=true USE_BUILDX=true BUILD_PLATFORM=linux/amd64 \
bash build-image.sh
```

可选目标以 `build-image.sh --help` 为准。常用：`backend`、`frontend`、`spk-rayjob`、`dataset-publisher`、`idc-sync`、`workspace`、`raytrain-base`、`bevfusion-ray258-canary`、`bevfusion-runtime`、`source-materializer`、`tos-prefix-init`、`test-training`。IDC 增量同步镜像的目标是 `idc-sync`，不是 `source-materializer`。

脚本帮助中的 `frontend`/“portal image”指本仓库的**独立旧前端**，不是迁移后的 Portal。脚本默认 `all` 会构建无关组件，执行时必须显式指定审阅后的 `BUILD_TARGETS`。

修改 `backend/spkrayjob/` 时必须同时构建 `backend,spk-rayjob`，并在 Helm 最小覆盖中同时更新 `backend.image` 与 `spkRayjobRelease.image`。只更新后端而不更新下载服务，会造成文档已显示新命令、用户下载的 CLI 却不认识它。

构建耗时几分钟,放后台跑。完成后取权威摘要:

```bash
docker buildx imagetools inspect harbor.wellspiking.ai/guofeng.su/ray-train-backend:${IMAGE_TAG} | grep '^Digest:'
```

生产部署用 `sha256` 摘要,不要只依赖 tag。

## 四、部署

### 先备份并判断对运行中任务的影响

```bash
kubectl get rayjob -A -o json | jq '[.items[] | select(.status.jobStatus != "SUCCEEDED" and .status.jobStatus != "FAILED") | {namespace:.metadata.namespace,name:.metadata.name,uid:.metadata.uid,state:.status.jobStatus}]'
umask 077
helm get values ray-platform -n ray-train-platform -a > /root/ray-platform-values-before-$(date -u +%Y%m%d).yaml
```

发布包含数据库迁移时，还需记录现有 schema version、可恢复的数据库备份或存储快照标识及恢复方法；仅备份 Helm values 不足以保护数据库。备份置于受限位置，不提交仓库或展示内容。没有可恢复备份或兼容性验证时，不执行该迁移发布。

有运行中训练并非所有发布的阻塞条件。用户已授权“不影响训练即可部署”时，仅控制面镜像滚动更新可以继续，但必须同时满足：现有后端多副本健康；迁移向后兼容；新 reconciler 不会修改/回收既有任务；dry-run 除预期控制面变化外不修改调度、节点、存储与训练资源。发布前后对比活跃任务的 RayJob/RayCluster/Pod UID、重启数和状态，不能只比较资源总数。

涉及 Kueue TAS/抢占、节点归属、挂载、训练运行时或破坏性数据库迁移时，单独核对影响和授权；需要维护窗口则等待用户安排，不暗中启用抢占。**本发布流程不得删除或重启既有 RayJob、RayCluster、训练 Pod。** 条件无法证明时暂停部署并报告具体缺项，而非宣称零影响。

### 最小覆盖文件

生产的 values 尚未与 `deploy/profiles/vke-cpu-ha.yaml` 完全归一化,所以热更新要保留当前 release 的 values(`--reuse-values`),只覆盖本次镜像 —— **不要改成整份 profile 覆盖**,那会把线上手工调过的字段一起冲掉。

只写本次重建的组件,没重建的一律不写,让它保持现状:

```yaml
backend:
  image:
    repository: ray-train-backend
    tag: release-20260904-01
    digest: sha256:<实际摘要>
```

**绝对不要附加 `deploy/overlays/idc-readonly-sources.yaml`** —— 它固定着一个旧 backend 摘要(tag `idc-r2`)。`--reuse-values -f` 的覆盖顺序会让它盖掉新镜像,把 backend 静默降级。

这个坑真实发生过:另一个 overlay(`s1h-streaming-f978cce.yaml`)曾固定旧摘要,导致 revision 156 上线 33 分钟后被 157 回滚。那个文件里的摘要已由 commit `3335a92` 摘除,但 idc 这个**至今仍固定着**。附加任何 overlay 前先 `grep digest` 看一眼。

### Dry-run 并逐行 diff —— 这一步不能跳

```bash
helm upgrade ray-platform helm/ray-train-platform -n ray-train-platform \
  --reuse-values -f /root/<覆盖文件>.yaml \
  --dry-run=server --hide-secret > /root/dr.txt 2>&1

helm get manifest ray-platform -n ray-train-platform > /root/c.yaml
awk '/^MANIFEST:/{f=1;next} f' /root/dr.txt > /root/n.yaml
diff /root/c.yaml /root/n.yaml
```

**差异必须精确等于你预期的那几行**(通常每个镜像 4 行)。多出任何一行都要查清楚再继续 —— `--reuse-values` 会带偏字段,这个 diff 是唯一能发现的地方。

这一步真实拦下过一次会导致 backend 崩溃循环的事故:Helm 把 `2592000` 当浮点处理,`quote` 渲染成 `"2.592e+06"`,后端 `strconv.Atoi` 解析失败。**模板里的大整数要加 `| int64`**。

### 升级

```bash
helm upgrade ray-platform helm/ray-train-platform -n ray-train-platform \
  --reuse-values -f /root/<覆盖文件>.yaml \
  --atomic --wait --timeout 10m
```

`--atomic` 在 Helm 升级失败时尝试回滚 Kubernetes release；**不会回滚已经成功提交的数据库迁移**。迁移失败的事务回滚与应用版本回滚是不同的事。旧后端必须能读取升级后的 schema，否则不能以 `--atomic` 当作安全保障。放后台跑并保留会话/日志标识。

## 五、验收

```bash
kubectl -n ray-train-platform rollout status deployment/ray-train-backend --timeout=5m
kubectl -n ray-train-platform get pods -o custom-columns='N:.metadata.name,ID:.status.containerStatuses[0].imageID' --no-headers | grep -E "backend|frontend"
kubectl -n ray-train-platform get pods --no-headers | grep backend   # 确认不是 CrashLoopBackOff
kubectl -n ray-train-platform logs deploy/ray-train-backend --tail=200 | grep -iE "panic|fatal|migration"
curl -fsS -o /dev/null -w "%{http_code}\n" https://raytrain.wellspiking.ai/healthz
kubectl get raycluster -A --no-headers         # 还需逐项对比发布前记录的 UID/重启数
```

Pod 的 `imageID` 要等于你构建出的摘要 —— rollout 成功不等于跑的是新镜像。

平台 API 的本地可达性受网络影响，健康检查和接口探测优先在构建机上做。未认证返回 401 只能证明请求到达某个认证边界，通用 auth 也可能拦截不存在的路由；新增路由还必须用获准身份验证成功响应或预期业务错误，不能以 401 证明路由已注册。

Portal 发布后不能只看 SPA 首页 200。至少直接打开并验证：任务列表、任务详情、实验中心每行 MLflow 详情、使用说明 `/raytrain/rayTrain/help`、调试环境的 Jupyter/VS Code 一次性票据。同时用后端返回的一个 403/404 响应确认 Portal 显示 `error.message`，不得退化成无信息的“系统错误”。

## 六、回滚

```bash
helm history ray-platform -n ray-train-platform
helm rollback ray-platform <上一个正常revision> -n ray-train-platform --wait --timeout 10m
```

执行前检查本次数据库 schema 变化与旧版本兼容性；不得自动运行破坏性逆向 SQL。成功回滚后仍需验证镜像摘要、API、存量任务与数据库状态。

## 收尾

清理构建机上的临时文件(`c.yaml`、`n.yaml`、`dr.txt`、bundle),**保留 values 备份和覆盖文件**作为发布记录。

如果排查过程中用了 `kubectl debug` 或建了辅助 Pod,**离开前必须删掉**。这个仓库的历史上多次留下 `node-debugger-*`、`rt-verify-helper` 之类的残留,需要用户手工清理。

## 上线后要主动说明的事

报告结果时如实说清楚,别把"部署成功"等同于"问题解决":

- 如果改动是延迟生效的(例如保留期策略要等数据到期),明确告诉用户**现在不会有任何可见变化**,以及大约何时生效
- 如果某类资源不在改动覆盖范围内(例如手工创建、没有平台数据库记录的 RayJob),明确说明它们不会被处理
- 测试失败就贴输出,跳过的步骤就说跳过了
