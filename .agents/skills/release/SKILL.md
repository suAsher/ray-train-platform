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

本机不能构建镜像,集群操作一律在构建机上做。

同目录下还散落着多个陈旧副本(`*-sync-backup-*`、`*-ray-data-staging`、`releases/*` 等),拿错目录会构建到过期代码。**只用 `ray-platform-main`**,用户已明确要求不要再用旧的 `ray-platform` 目录。

## 一、本地开发

```bash
cd backend && gofmt -l . && go build ./... && go test ./...
cd frontend && npm test && npm run build
```

前端测试跑的是 `node --test`,**不是 vitest**。直接 `npx vitest run` 会让 55 个文件全部报 "No test suite found",那是跑错了工具,不是代码坏了。

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

## 二、同步代码到构建机

构建机**到 GitHub 两条路都不通**:SSH 无密钥,HTTPS 间歇性 `GnuTLS recv error`。所以走 git bundle,**不要用 rsync 覆盖源码**:

```bash
# 本地
git push origin main
git bundle create /tmp/rtp-<short-sha>.bundle <上游commit>..main
scp -i ~/.ssh/qomolo-desktop.pem /tmp/rtp-<short-sha>.bundle root@14.103.49.106:/tmp/

# 构建机
cd /opt/guofeng/vke-cluster/ray-platform-main
git bundle verify /tmp/rtp-<short-sha>.bundle
git fetch /tmp/rtp-<short-sha>.bundle main
git merge --ff-only FETCH_HEAD
git rev-parse --short HEAD   # 必须等于计划发布的 commit
git status --short           # 必须为空
rm -f /tmp/rtp-<short-sha>.bundle
```

构建前确认 HEAD 正确且工作区干净。**不要在构建机上临时改源码再构建**,那样产出的镜像与任何 commit 都对不上,事后无法追溯。

## 三、构建

Harbor 凭据已存在于构建机 `/root/.docker/config.json`。**不要代替用户执行 `docker login`**,也不要把密码写进命令。验证凭据有效的方式是看报错是 `not found` 还是 `unauthorized`。

只构建本次真正改动的组件。`BUILD_TARGETS=all` 会连带构建大量训练和工作区镜像,几十分钟起步:

```bash
cd /opt/guofeng/vke-cluster/ray-platform-main
IMAGE_TAG=release-$(date -u +%Y%m%d)-01 \
REGISTRY=harbor.wellspiking.ai/guofeng.su \
BUILD_TARGETS=backend,frontend \
PUSH_IMAGE=true USE_BUILDX=true BUILD_PLATFORM=linux/amd64 \
bash build-image.sh
```

可选目标:`backend`、`frontend`、`spk-rayjob`、`dataset-publisher`、`workspace`、`bevfusion-ray258-canary`、`bevfusion-runtime`、`source-materializer`、`tos-prefix-init`、`test-training`。

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
