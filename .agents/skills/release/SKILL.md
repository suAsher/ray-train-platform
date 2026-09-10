---
name: release
description: "ray-train-platform 的开发、构建、部署全流程。当需要改这个仓库的代码、跑测试、构建镜像、把改动上线到集群、验收上线结果或回滚时，使用本 skill。触发场景包括:「帮我上线」「发布一下」「部署到集群」「构建镜像」「helm 升级」「跑一下测试」「加个数据库迁移」「回滚」，以及任何改完 backend/ 或 frontend/ 代码后需要让改动生效的情况。集群操作有若干不查就会踩的坑(构建机连不上 GitHub、overlay 固定旧摘要会静默回滚、Helm 把大整数渲染成科学计数法)，所以即使只是「构建一下镜像」这种看起来很简单的请求也要先读本 skill。"
---

# ray-train-platform 发布流程

这套流程的正确性标准是**真实生产集群**,不是本地测试通过。下面每一条约束都对应一次真实事故或一次被拦下的事故,不是理论上的谨慎。

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
| 后端、Helm、运行时 | 本仓库 `main` | 本地开发后同时推 GitHub 与内部 GitLab，再用 bundle 同步构建机；镜像和 Helm 只在构建机发布 |
| Portal 前端 | `ssh://git@gitlab.wellspiking.ai:32022/wellspiking/frontend/wellspiking-frontend.git` 的 `dev` | 只修改 `src/views/rayTrain/` 及其直接依赖，推 `dev` 后由 GitLab CI/CD 自动构建并部署 |

前端迁移后的测试入口为 `https://spiking-dev.wellspiking.ai/raytrain/rayTrain/job/list`。本仓库旧 `frontend/` 不再是 Portal RayTrain 页面发布源；除非用户明确要求维护独立旧入口，否则不要构建或部署它，也不要把 Portal 前端镜像写进后端 Helm 覆盖文件。

### 两套 Portal 代理 Ingress

| 环境 | kubeconfig | 可写边界 | 清单 |
|---|---|---|---|
| 新前端 dev | `~/.kube/test-dev.conf` | `guofeng-su` namespace | `deploy/portal/test-dev-raytrain-ingress.yaml` |
| 旧 common/生产 Portal | `~/.kube/common.conf` | 只可修改 `guofeng-su`，其他 namespace 只读参考 | `deploy/portal/common-raytrain-ingress.yaml` |

这两个 Ingress 只代理 `/raytrain/api/...` 和 `/raytrain/ray/...`，绝不能写回 `/raytrain/(.*)`：NGINX 的正则匹配会把 SPA 路由 `/raytrain/rayTrain/...` 一并送到后端，表现为页面 401/404。`rewrite-target` 必须是 `/$1`。dev 清单只拥有 `spiking-dev.wellspiking.ai`；common 清单只拥有 `spiking.wellspiking.ai`，不要让两个集群声明同一个 dev host。

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

本机不能构建镜像,集群操作一律在构建机上做。

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

Portal 前端也不在本机安装依赖或运行 lint。把 `dev` 候选 commit 用 `git archive` 生成不含 `.git`、`.env*` 和本地未跟踪文件的归档，送到构建机临时目录后执行 `docker build --pull -f docker/Dockerfile.lint .`。该门禁会依次运行 `pnpm lint:check`、`pnpm check:ep`、`pnpm check:store`；通过后才推 `dev`，随后由 GitLab CI/CD 再次验证并自动部署。不要从本仓库构建 Portal 前端。

Portal 仓库的 pre-push hook 可能在本机安装依赖、自动修改文件或重复构建。候选已在构建机通过上述完整门禁时，推送使用 `git push --no-verify`，推送后再核对 GitLab CI/CD；禁止让 hook 在本机消耗构建资源或产生未审阅改动。

本仓库独立旧前端若被明确要求维护，测试命令是 `npm test && npm run build`；测试跑 `node --test`，**不是 vitest**。直接 `npx vitest run` 会把测试工具用错。

### 数据库迁移

`backend/db/migrations/NNNN_name.up.sql`,编号连续,**只有 up 没有 down**。开头固定两行:

```sql
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
```

加完迁移必须同步更新 `backend/db/postgres_test.go` 里 `TestMigrationVersionsEmbedded` 的版本号列表,否则测试必挂。迁移在后端启动时自动执行,无需手工步骤。

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

可选目标:`backend`、`frontend`、`spk-rayjob`、`dataset-publisher`、`workspace`、`bevfusion-ray258-canary`、`bevfusion-runtime`、`source-materializer`、`tos-prefix-init`、`test-training`。

修改 `backend/spkrayjob/` 时必须同时构建 `backend,spk-rayjob`，并在 Helm 最小覆盖中同时更新 `backend.image` 与 `spkRayjobRelease.image`。只更新后端而不更新下载服务，会造成文档已显示新命令、用户下载的 CLI 却不认识它。

构建耗时几分钟,放后台跑。完成后取权威摘要:

```bash
docker buildx imagetools inspect harbor.wellspiking.ai/guofeng.su/ray-train-backend:${IMAGE_TAG} | grep '^Digest:'
```

生产部署用 `sha256` 摘要,不要只依赖 tag。

## 四、部署

### 先备份并确认没有训练在跑

```bash
kubectl get rayjob -A --no-headers | grep -Eiv "SUCCEEDED|FAILED" | wc -l   # 期望 0
umask 077
helm get values ray-platform -n ray-train-platform -a > /root/ray-platform-values-before-$(date -u +%Y%m%d).yaml
```

升级只滚动平台 API/UI,不会动已创建的 RayJob,但有训练在跑时仍应等它结束再发,避免同时排查两件事。**任何情况下不要删除或重启已有的 RayJob、RayCluster、训练 Pod。**

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

`--atomic` 在失败时自动回滚。放后台跑,约 2 分钟。

## 五、验收

```bash
kubectl -n ray-train-platform rollout status deployment/ray-train-backend --timeout=5m
kubectl -n ray-train-platform get pods -o custom-columns='N:.metadata.name,ID:.status.containerStatuses[0].imageID' --no-headers | grep -E "backend|frontend"
kubectl -n ray-train-platform get pods --no-headers | grep backend   # 确认不是 CrashLoopBackOff
kubectl -n ray-train-platform logs deploy/ray-train-backend --tail=200 | grep -iE "panic|fatal|migration"
curl -fsS -o /dev/null -w "%{http_code}\n" https://raytrain.wellspiking.ai/healthz
kubectl get raycluster -A --no-headers | wc -l   # 训练资源未受影响
```

Pod 的 `imageID` 要等于你构建出的摘要 —— rollout 成功不等于跑的是新镜像。

平台 API 从**本机连不通**,健康检查和接口探测要在构建机上做。新增路由验证未认证时应返回 401 而非 404(401 = 已注册)。

Portal 发布后不能只看 SPA 首页 200。至少直接打开并验证：任务列表、任务详情、实验中心每行 MLflow 详情、使用说明 `/raytrain/rayTrain/help`、调试环境的 Jupyter/VS Code 一次性票据。同时用后端返回的一个 403/404 响应确认 Portal 显示 `error.message`，不得退化成无信息的“系统错误”。

## 六、回滚

```bash
helm history ray-platform -n ray-train-platform
helm rollback ray-platform <上一个正常revision> -n ray-train-platform --wait --timeout 10m
```

## 收尾

清理构建机上的临时文件(`c.yaml`、`n.yaml`、`dr.txt`、bundle),**保留 values 备份和覆盖文件**作为发布记录。

如果排查过程中用了 `kubectl debug` 或建了辅助 Pod,**离开前必须删掉**。这个仓库的历史上多次留下 `node-debugger-*`、`rt-verify-helper` 之类的残留,需要用户手工清理。

## 上线后要主动说明的事

报告结果时如实说清楚,别把"部署成功"等同于"问题解决":

- 如果改动是延迟生效的(例如保留期策略要等数据到期),明确告诉用户**现在不会有任何可见变化**,以及大约何时生效
- 如果某类资源不在改动覆盖范围内(例如手工创建、没有平台数据库记录的 RayJob),明确说明它们不会被处理
- 测试失败就贴输出,跳过的步骤就说跳过了
