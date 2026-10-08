# 夜莺增量建设候选

此目录只拥有 `observability-alerting` 中的新夜莺及专用依赖。现有平台 PostgreSQL、Prometheus、Alertmanager、Grafana、Loki、Alloy、训练任务及入口均不修改。当前是待构建机验证的候选，文件存在不代表部署或验收完成。

2026-10-08 用户选择先保留当前 CNI，新数据面部署暂停；此目录继续保留候选与离线验证证据，没有部署夜莺或其数据库。

新组件可以使用 CPU 或 GPU 节点的 CPU/内存；不请求 GPU，不添加通用 toleration，不驱逐训练。统一使用 `raytrain-observability-additive`（负优先级、`preemptionPolicy: Never`）；显式排除当前高内存 `172.28.1.222` 和 algorithm 专属 `172.28.3.32`。其余节点由调度器选择，多个副本必须分散到不同 hostname。资源不足时保留 Pending，不降低训练保障。

## 固定候选与前提

| 组件 | 候选 | 作用与约束 |
| --- | --- | --- |
| Nightingale | `9.1.1`，既有 Harbor `hub` 代理，已固定摘要 | 2 个 center/alert 实例，共用专用 PG/Redis；关闭 EmbeddedTSDB、Ibex、匿名查询、Agent 写入口、A2A/MCP |
| CloudNativePG | chart `0.29.1`，operator `1.30.1` | 2 个控制器、仅 watch 本 namespace；新 CRD/webhook 属集群范围，先检查名称/所有权冲突 |
| PostgreSQL | `17.11-202608170816-standard-trixie`，清单已固定官方摘要 | 新 `n9e-postgres`，1 主 2 备；一份同步备库确认，另一份备用；不借用平台库 |
| Redis/Sentinel | DandyDeveloper `redis-ha` chart `4.39.0`，Redis `8.8.0-alpine`，已固定摘要 | 3 个 Redis+Sentinel Pod，1 主 2 备、Sentinel quorum=2、AOF；无 HAProxy |
| 存储 | `ebs-ssd-observability-retain` | 由父层创建，Retain、WFFC、可扩容；PG/Redis 各 3×20Gi |

CNPG 1.30 系列官方支持 Kubernetes 1.36。Redis chart 使用稳定的 Kubernetes API，但这不等于本次 VKE/CSI 已验收；必须完成 Helm 渲染、server-side dry-run、Secret/TLS、Sentinel 故障恢复和数据库初始化验证。不可用旧 EOL 版本、单副本或现有数据库悄悄替代。

参考固定版本：[Nightingale 发布](https://github.com/ccfos/nightingale/releases/tag/v9.1.1)、[官方镜像构建](https://github.com/ccfos/nightingale/blob/v9.1.1/.goreleaser.yaml)、[CNPG 支持矩阵](https://cloudnative-pg.io/docs/1.30/supported_releases/)、[CNPG chart](https://github.com/cloudnative-pg/charts/tree/cloudnative-pg-v0.29.1/charts/cloudnative-pg)、[Redis chart](https://github.com/DandyDeveloper/charts/tree/redis-ha-4.39.0/charts/redis-ha)。

所有运行时镜像完整摘要见 `versions.yaml`，清单已引用固定摘要。构建机直连 Docker Hub 超时，因此夜莺使用既有 Harbor hub 代理；代理多架构 index 的 amd64 子摘要与既有华为代理一致，真实镜像 `n9e -version` 为 `v9.1.1-4e056150142b2e1ed6966c9317a9e08442c399f7`。部署前仍需验证节点访问这些注册表的权限；需要 pull Secret 时由父层安全配置，不能输出 Docker 认证配置。

范围检查对 CNPG 的 cluster-scoped API 定义仅允许固定三元组：`CustomResourceDefinition/clusterimagecatalogs.postgresql.cnpg.io`、`spec.group=postgresql.cnpg.io`、`spec.scope=Cluster`。这只是官方完整 CRD 集中的类型定义，不允许创建 `ClusterImageCatalog` 实例，本候选 PG 只用固定 `imageName`。`clusterWide=false` 约束业务资源控制范围，但固定 chart 的 ClusterRole 仍包含 catalog 的 `get/list/watch`，不能宣称从不读取全局 catalog；此只读权限以及 CRD/ClusterRole 所有权必须由安全审阅明确接纳。不要为通过通用“仅namespaced API”检查而删掉官方 CRD、隐去 RBAC 或谎报作用域。

Redis chart 的可选 `n9e-redis-configmap-test` Helm test Pod 没有关闭开关/优先级配置，且不属于运行时健康验收。审阅与直接应用的渲染入口必须用 `helm template --skip-tests`，避免额外 ShellCheck Pod 混入部署清单。普通 Helm install/upgrade 不运行 test hook；本候选不执行 `helm test`，相关脚本检查在构建机做。values 为其补有基础 resources，但不能因此宣称 test Pod 已满足非抢占/节点隔离合同；若以后要运行，先改造单独测试 Job 并固定镜像、PC、affinity。Helm 不把 test hook 交给 post-renderer，不能靠它删除 hook，也不能把 `configmapTest.enabled=false` 当有效开关。`scope-redis.py` 只为同 namespace Redis RoleBinding 的 ServiceAccount 显式补齐 namespace；Kubernetes 原本允许此处省略，不代表跨 namespace 绑定。

## Secret 契约

凭据由发布操作者随机创建到本 namespace，仓库不包含 Secret 内容，不把解码结果或生成后的配置输出到日志。

| Secret | 键 | 使用者 |
| --- | --- | --- |
| `n9e-db-app`，`kubernetes.io/basic-auth` | `username` 固定 `n9e`；`password` 随机 32–128 字符，仅字母、数字、下划线、连字符 | CNPG initdb、seed Job、夜莺配置渲染器 |
| `n9e-redis-auth` | `auth`、`sentinel-password` 各独立随机 | Redis chart、夜莺配置渲染器 |
| `n9e-bootstrap-admin` | `password` 恰好 24 个随机字母数字字符 | 一次性 seed Job；仅创建新夜莺的 `root`，与平台用户无关 |
| `n9e-postgres-ca` | `ca.crt`，由 CNPG 生成 | PostgreSQL `verify-full` 客户端校验 |
| `n9e-vm-read` | `token`，镜像新 metrics namespace 的 `vm-self-read` | 后续受控创建只读数据源，不能写 VM |

24 字符是当前上游 `InitRoot` 的兼容约束：它将长度大于 31 的值视作已哈希，不能直接放 64 字符随机明文。初始化前已有随机密码，首次单实例启动将它哈希；不使用上游默认密码。首次成功后不重跑 seed，不用修改此 Secret 来“重置”既有账号。[初始化实现](https://github.com/ccfos/nightingale/blob/v9.1.1/models/user.go)

Nightingale 该版文件加载不进行环境变量插值，因此 `config/render-config.py` 在 init container 中读 Secret，编码为 TOML 后写入内存 `emptyDir`。主容器不挂 DB/Redis 原始 Secret，仅只读挂载生成配置；Pod/exec 权限仍需限制。配置变更需受控更新新组件 Pod。

数据库 DSN 必须采用该版支持的 `host=… user=n9e dbname=n9e password=…` 形式，不使用 PostgreSQL URI：上游预检自行按空格解析 `dbname`，URI 会被错误解析并可能把密码带入错误日志。配置渲染器限定随机数据库凭据字符集并保持固定用户名，发布前用假凭据验证失败日志不含密码。[上游数据库预检](https://github.com/ccfos/nightingale/blob/v9.1.1/pkg/ormx/ormx.go)

## 安装顺序与构建机门禁

所有命令在已审阅候选的构建机隔离目录执行。先记录现有活跃训练 UID/节点/重启数和原观测 release 状态；不要对本目录 `kubectl apply -R`，其中 values、patch、bootstrap 并非可同时应用。

1. 父层建立 namespace、低优先级 PriorityClass、独立 Retain StorageClass 和 Secret；检查至少 3 个可调度节点、EBS 数量及资源余量。确认 hostname 标签确实为清单中的 IP。
2. 先只读检查 `*.postgresql.cnpg.io` CRD、CNPG webhook 固定名称和现有 CNPG release/CR。已有资源时暂停本次新安装、核对所有权，不能接管或重写。
3. 构建机拉取固定 chart 和镜像，记录 archive checksum、manifest digest；审阅渲染结果后执行 server-side dry-run。CNPG 使用 `scope-cnpg-webhooks.py` 为所有 webhook 加本 namespace selector 和 `scope: Namespaced`；Redis 渲染加 `--skip-tests`，并用 `scope-redis.py` 显式化 subject namespace。每次安装/升级都使用各自相同的 post-renderer；不要调用 Redis `helm test`。`clusterWide: false` 只约束控制器业务资源 watch，不能代替 webhook 限定。
4. 应用 `45-networkpolicy.yaml`，部署 CNPG/Redis，再应用 `20-postgres.yaml`。等待新 PG 3/3、Redis 3/3 和 3 个 Sentinel 健康。验证 CNPG CA 与 `n9e-postgres-rw.observability-alerting.svc` 匹配。
5. 从固定源码准备 seed ConfigMap，创建 `n9e-config-v911`，仅执行一次 `35-bootstrap.yaml`。数据库已有 `users` 表时 Job 拒绝重置；DDL 和随机 root 种子在同一事务中完成。
6. 应用 `40-nightingale.yaml`，先启动 1 个实例，等待 schema 迁移结束，确认不存在迁移错误、root 密码已哈希、登录及 Redis/JWT 正常。上游部分迁移失败只记日志，TCP Ready 不能代替 SQL/登录验证。
7. 应用 `50-ha-patch.yaml` 把新夜莺扩到 **2 个**，核对两个不同 Pod IP 的同一 `vke-additive-canary` engine 心跳，验证重新登录/会话、规则分工与单实例故障接管。之后的渲染清单应保持 2，不能重套 bootstrap 的 1 副本。
8. 只接入新 VM 的合成数据源和 canary，不迁入旧规则，不增加真实通知渠道。验收后保留新依赖，不卸载 CRD/PVC。

渲染入口示例（`obs_candidate` 是构建机上的本目录）：

```bash
helm template n9e-cnpg cloudnative-pg \
  --repo https://cloudnative-pg.github.io/charts --version 0.29.1 \
  --namespace observability-alerting --kube-version 1.36.1 \
  -f "$obs_candidate/10-cnpg-values.yaml" \
  --post-renderer "$obs_candidate/scope-cnpg-webhooks.py" > cnpg-rendered.yaml

helm template n9e-redis redis-ha \
  --repo https://dandydeveloper.github.io/charts --version 4.39.0 \
  --namespace observability-alerting --kube-version 1.36.1 --skip-tests \
  -f "$obs_candidate/30-redis-values.yaml" \
  --post-renderer "$obs_candidate/scope-redis.py" > redis-rendered.yaml
```

正式 Helm install/upgrade 同样固定 release 名（`n9e-cnpg`、`n9e-redis`）、版本、values、post-renderer，并先保留和审阅 dry-run。上述模板命令不能替代 CRD 处理及 live dry-run；不要对未知 CRD 使用 `--take-ownership`。

seed 准备只下载非敏感源码，脚本校验 v9.1.1 源文件 git blob hash 后去掉默认密码，用 `psql` 从 Secret 环境变量读取随机密码，并修复官方显式 ID 种子的 3 个序列起点：

```bash
curl --fail --silent --show-error --location \
  https://raw.githubusercontent.com/ccfos/nightingale/v9.1.1/docker/compose-postgres/initsql_for_postgres/a-n9e-for-Postgres.sql \
  -o n9e-postgres-upstream.sql
python3 "$obs_candidate/prepare-seed.py" n9e-postgres-upstream.sql n9e-postgres-seed.sql
kubectl -n observability-alerting create configmap n9e-postgres-seed-v911 \
  --from-file=seed.sql=n9e-postgres-seed.sql --dry-run=client -o yaml > seed-configmap.yaml
kubectl -n observability-alerting create configmap n9e-config-v911 \
  --from-file="$obs_candidate/config/render-config.py" \
  --from-file="$obs_candidate/config/config.toml.template" \
  --dry-run=client -o yaml > runtime-configmap.yaml
```

只在审阅后应用生成的非敏感 ConfigMap。seed Job 使用 `psql --single-transaction`，不使用 shell 展开拼接 SQL，不打印环境变量。它不运行 MySQL seed，也不执行现有平台库迁移。

## 只读 VM 与合成验收

- 数据源 URL：`http://vmauth-raytrain-next.observability-metrics.svc:8427`；该 VMAuth 身份只映射新 tenant `1001`。
- HTTP Header 使用 `Authorization: Bearer <n9e-vm-read.token>`，通过受控 UI/API 创建到新夜莺专用数据库；token 不写进本目录、命令行或输出。
- 数据源仅关联 `vke-additive-canary` engine，PromQL 先测 `vector(1)`；再创建专用 canary `vector(1) > 0`，60 秒持续条件，无通知规则、联系人、回调和自愈。验收记录新夜莺事件后暂停该规则。
- 先验证 UI/API 查询正常，再核对 event 的 firing/resolved。canary 转为 `vector(0) > 0` 验证恢复；改变仅限本次 canary，不修改真实规则。
- HTTP 端口只为 ClusterIP；初期管理员可通过受控 `kubectl port-forward` 验收，不创建公共 Ingress，不连接 Portal SSO。
- `45-networkpolicy.yaml` 只放行新 PG/Redis、DNS、自身 engine 和新 VMAuth 8427，夜莺不具备公网通知出口。必须验证 VKE CNI 确实执行此策略，不能只看资源创建成功。验收 Pod 用 `observability.raytrain.io/nightingale-client=true`，临时资源由父层明确登记。

## 资源与 HA 限制

最终常驻 requests 合计约 **3.66 CPU、7.47Gi 内存、120Gi EBS**；另需启动/滚动副本余量。limits 合计约 16 CPU / 15Gi，集群资源配额必须连同 VM/OTel/CH 总预算一起评估。这是小流量候选，不是大规模告警容量承诺。Redis 的 fork/AOF rewrite 峰值和规则查询需实測调参。

PG 同步配置偏向数据完整性：失去所有健康同步备库时写入会等待，不能声称任意故障仍可写；在测试库验证主切换和 CNPG fencing。Redis AOF everysec、异步复制和 Sentinel 并不提供零 RPO，故障可能丢失近期会话/短期状态；`noeviction` 内存满会拒绝写入，需要水位告警。两套依赖都不是跨整个 VKE 的灾备。

这些均是空的新库；当前只存合成测试配置。引入正式规则/用户之前，必须完成新 PG/Redis 的备份、隔离恢复和故障演练，不能等承接正式业务后才补。现有指标/日志历史和原系统备份迁移仍属于后续阶段，本目录没有历史复制动作。

回退只停本次夜莺/新依赖流量，原观测和训练保持原路。不要自动删 PVC、Secret、CNPG CRD、PV 或 namespace；即使 StorageClass 为 Retain，删除 CR/namespace 仍会失去部分恢复所需元数据。需要清理时先登记本次资源及恢复证据，由发布者按明确范围执行。
