# 夜莺增量建设候选

此目录只拥有 `observability-alerting` 中的新夜莺及专用依赖。现有平台 PostgreSQL、Prometheus、Alertmanager、Grafana、Loki、Alloy、训练任务及入口均不修改。当前是待构建机验证的候选，文件存在不代表部署或验收完成。

2026-10-08 用户选择保留当前 CNI，并进一步选择为新增组件改造 mTLS 代理；没有部署夜莺或其数据库到 VKE。原候选 `b9dd3b5` 已在构建机内部 Docker 网络通过真实初始化与登录验证：PG UTF8/TLS verify-full、首次 seed 与重复 seed 拒绝、47→57 表迁移、随机 root 密码哈希、保持验证码启用的取题→答题→登录→认证 profile。单 PG、单 Redis 和单 Sentinel 的三 DNS 别名仅验证客户端配置与初始化，不能作为三副本 HA 或 VKE 验收。临时容器、网络、假凭据与数据已清理；证据为构建机 `runtime-n9e-20261008/{result.json,final-evidence-checks.json,evidence.md}`。

新组件可以使用 CPU 或 GPU 节点的 CPU/内存；不请求 GPU，不添加通用 toleration，不驱逐训练。统一使用 `raytrain-observability-additive`（负优先级、`preemptionPolicy: Never`）；显式排除当前高内存 `172.28.1.222` 和 algorithm 专属 `172.28.3.32`。其余节点由调度器选择，多个副本必须分散到不同 hostname。资源不足时保留 Pending，不降低训练保障。

## 固定候选与前提

| 组件 | 候选 | 作用与约束 |
| --- | --- | --- |
| Nightingale | `9.1.1`，既有 Harbor `hub` 代理，已固定摘要 | 2 个 center/alert 实例，共用专用 PG/Redis；关闭 EmbeddedTSDB、Ibex、匿名查询、Agent 写入口、A2A/MCP |
| CloudNativePG | chart `0.29.1`，基于 operator `1.30.1` 的专用认证派生镜像 | 2 个控制器、仅 watch 本 namespace；必须通过 `cnpg-hardening/` 的真实协议测试并锁 digest，不能部署原版代替 |
| PostgreSQL | `17.11-202608170816-standard-trixie`，清单已固定官方摘要 | 新 `n9e-postgres`，1 主 2 备；一份同步备库确认，另一份备用；不借用平台库 |
| Redis/Sentinel | DandyDeveloper `redis-ha` chart `4.39.0`，Redis `8.8.0-alpine`，已固定摘要 | 3 个 Redis+Sentinel Pod，1 主 2 备、Sentinel quorum=2、AOF；无 HAProxy |
| 存储 | `ebs-ssd-observability-retain` | 由父层创建，Retain、WFFC、可扩容；PG/Redis 各 3×20Gi |

CNPG 1.30 系列官方支持 Kubernetes 1.36。Redis chart 使用稳定的 Kubernetes API，但这不等于本次 VKE/CSI 已验收；必须完成 Helm 渲染、server-side dry-run、Secret/TLS、Sentinel 故障恢复和数据库初始化验证。不可用旧 EOL 版本、单副本或现有数据库悄悄替代。

参考固定版本：[Nightingale 发布](https://github.com/ccfos/nightingale/releases/tag/v9.1.1)、[官方镜像构建](https://github.com/ccfos/nightingale/blob/v9.1.1/.goreleaser.yaml)、[CNPG 支持矩阵](https://cloudnative-pg.io/docs/1.30/supported_releases/)、[CNPG chart](https://github.com/cloudnative-pg/charts/tree/cloudnative-pg-v0.29.1/charts/cloudnative-pg)、[Redis chart](https://github.com/DandyDeveloper/charts/tree/redis-ha-4.39.0/charts/redis-ha)。

已核验基础镜像摘要见 `versions.yaml`。新增 CNPG 派生镜像构建、真实协议测试及 digest 锁定前，`10-cnpg-values.yaml` 保持 `BUILD_REQUIRED`，post-renderer 会拒绝发布；不能把上游基础摘要填入替代。构建机直连 Docker Hub 超时，因此夜莺使用既有 Harbor hub 代理；代理多架构 index 的 amd64 子摘要与既有华为代理一致，真实镜像 `n9e -version` 为 `v9.1.1-4e056150142b2e1ed6966c9317a9e08442c399f7`。部署前仍需验证节点访问这些注册表的权限；需要 pull Secret 时由父层安全配置，不能输出 Docker 认证配置。

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
| `obs-mtls-nightingale` / `obs-mtls-cnpg-operator` | `tls.crt`、`tls.key`、`ca.crt` | mesh CA 签发，分别用于 N9E 入/出代理和 CNPG 指标代理 |
| `obs-mtls-n9e-ui-gateway` | 同上，仅 clientAuth | 受控管理员客户端；不授予训练用户或普通 Pod |
| `obs-mtls-redis-ha-peer` / `obs-mtls-nightingale-redis` | 同上，独立 Redis CA | HA Redis/Sentinel 同组服务及其唯一应用客户端，继续要求密码 |
| `obs-mtls-n9e-postgres-client` / `obs-mtls-n9e-postgres-bootstrap` | 同上，独立 PostgreSQL client CA，CN=`n9e` | 分离常驻应用与一次性初始化证书 |
| `obs-mtls-n9e-postgres-replication` | 同上，CN=`streaming_replica` | 仅新 PG 复制身份 |
| `obs-mtls-postgres-client-ca` / `obs-mtls-cnpg-metrics-ca` | 只含公开 `ca.crt` | 分别验证 PG 原生客户端、9187 的 mesh 监控客户端；绝不向 Pod 发 CA 私钥 |

24 字符是当前上游 `InitRoot` 的兼容约束：它将长度大于 31 的值视作已哈希，不能直接放 64 字符随机明文。初始化前已有随机密码，首次单实例启动将它哈希；不使用上游默认密码。首次成功后不重跑 seed，不用修改此 Secret 来“重置”既有账号。[初始化实现](https://github.com/ccfos/nightingale/blob/v9.1.1/models/user.go)

Nightingale 该版文件加载不进行环境变量插值，因此 `config/render-config.py` 在 init container 中读 Secret，编码为 TOML 后写入内存 `emptyDir`。主容器不挂 DB/Redis 原始 Secret，仅只读挂载生成配置；Pod/exec 权限仍需限制。配置变更需受控更新新组件 Pod。

数据库 DSN 必须采用该版支持的 `host=… user=n9e dbname=n9e password=…` 形式，不使用 PostgreSQL URI：上游预检自行按空格解析 `dbname`，URI 会被错误解析并可能把密码带入错误日志。配置渲染器限定随机数据库凭据字符集并保持固定用户名，发布前用假凭据验证失败日志不含密码。[上游数据库预检](https://github.com/ccfos/nightingale/blob/v9.1.1/pkg/ormx/ormx.go)

该版启动预检会临时用 `dbname=postgres` 查询 `pg_database`，即使 `n9e` 库已经存在也必须连接。因此 HBA 为同一 `n9e` 身份额外开放专用数据库实例的 `postgres` 维护库，仍强制 clientcert verify-full + SCRAM；不开放所有数据库、不赋予 SUPERUSER/CREATEDB。隔离验收同时检查此预检成功、无证书失败、`template1` 被拒绝。

## 安装顺序与构建机门禁

所有命令在已审阅候选的构建机隔离目录执行。先记录现有活跃训练 UID/节点/重启数和原观测 release 状态；不要对本目录 `kubectl apply -R`，其中 values、patch、bootstrap 并非可同时应用。

1. 父层建立 namespace、低优先级 PriorityClass、独立 Retain StorageClass 和 Secret；检查至少 3 个可调度节点、EBS 数量及资源余量。确认 hostname 标签确实为清单中的 IP。
2. 先只读检查 `*.postgresql.cnpg.io` CRD、CNPG webhook 固定名称和现有 CNPG release/CR。已有资源时暂停本次新安装、核对所有权，不能接管或重写。
3. 构建机拉取固定 chart 和镜像，记录 archive checksum、manifest digest；先完成 CNPG 派生补丁 Go 回归与构建，再锁真实摘要、审阅渲染及 server-side dry-run。CNPG 使用 `scope-cnpg-webhooks.py` 为所有 webhook 加本 namespace selector 和 `scope: Namespaced`，并向固定 `n9e-cnpg` Deployment 注入受限 Envoy；同时输出 `n9e-cnpg-mtls` ConfigMap、`n9e-cnpg-metrics` Service。它还验证 operator 与 bootstrap 注入的 `OPERATOR_IMAGE_NAME` 一致。Redis 渲染加 `--skip-tests`，并用 `scope-redis.py` 显式化 subject namespace。每次安装/升级都使用各自相同的 post-renderer；不要调用 Redis `helm test`。
4. 保持 CNI 现状，`45-networkpolicy.yaml` 仅作为待启用的附加防护，不是当前认证边界。先由根凭据工具根据 `mtls-identities.json` 和 `--include-n9e-ca-projections` 签发新身份；再部署 CNPG/Redis 和 `20-postgres.yaml`。等待新 PG 3/3、Redis 3/3 和 3 个 Sentinel 健康。验证 PG CA/DNS、clientcert+SCRAM，Redis/Sentinel 无证书拒绝且带证书仍需 AUTH，真实 Sentinel 主切换后 N9E 能按新 DNS 重连。
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
  --from-file="$obs_candidate/config/envoy.yaml" \
  --dry-run=client -o yaml > runtime-configmap.yaml
```

只在审阅后应用生成的非敏感 ConfigMap。seed Job 使用 `psql --single-transaction`，不使用 shell 展开拼接 SQL，不打印环境变量。它不运行 MySQL seed，也不执行现有平台库迁移。

## 只读 VM 与合成验收

- N9E 数据源 URL：`http://127.0.0.1:18427`，由同 Pod Envoy 以 `nightingale` 证书连接 VMAuth 的 8427 mTLS 入口，精确验证 `observability-metrics/vmauth` URI；只放行 Prometheus 只读 API，该 VMAuth 身份只映射新 tenant `1001`。
- HTTP Header 使用 `Authorization: Bearer <n9e-vm-read.token>`，通过受控 UI/API 创建到新夜莺专用数据库；token 不写进本目录、命令行或输出。
- 数据源仅关联 `vke-additive-canary` engine，PromQL 先测 `vector(1)`；再创建专用 canary `vector(1) > 0`，60 秒持续条件，无通知规则、联系人、回调和自愈。验收记录新夜莺事件后暂停该规则。
- 先验证 UI/API 查询正常，再核对 event 的 firing/resolved。canary 转为 `vector(0) > 0` 验证恢复；改变仅限本次 canary，不修改真实规则。
- N9E 原生 17000 仅回环监听；Service 为 17443 mTLS。管理员即使用 port-forward，也必须通过持有 `n9e-ui-gateway` 证书的受控客户端/本地网关，保留夜莺原有验证码、密码与 JWT。无证书不能访问登录页，证书也不能代替夜莺登录。初期不创建公共 Ingress、不连接 Portal SSO。
- 新环境没有配置真实通知规则、联系人、Webhook 或自愈。CNI 当前不执行策略，mTLS 不拦截出站流量，不能承诺网络层禁止公网通知；`45-networkpolicy.yaml` 只是将来实施防护的候选。

## 入口保护与轮换

| 入口 | 保护方式 | 允许身份 |
| --- | --- | --- |
| N9E 17443 `/metrics` | Envoy mTLS + 精确路径/GET RBAC | `observability-metrics/vmagent` |
| N9E 17443 UI/API | Envoy mTLS + 原生登录/JWT | `observability-alerting/n9e-ui-gateway` |
| CNPG operator 18443 `/metrics` | Envoy mTLS；原始 8080 仅回环 | `observability-metrics/vmagent` |
| CNPG instance 9187 | 派生管理器原生 mTLS + 精确 URI | `observability-metrics/vmagent`，server CA 仍为 CNPG CA |
| CNPG instance 8000 详细 status/管理 | 已有 Operator 公钥 pin | 仅当前 Operator；精确最小健康路径例外 |
| CNPG webhook 9443 | server TLS + 真实 RemoteAddr 精确白名单 | 两个已核验 apiserver IP；`/readyz` 最小健康例外，明确不称作 mTLS |
| PostgreSQL 5432 | 原生 TLS + clientcert verify-full + SCRAM | CN=`n9e`；复制继续专用 CN=`streaming_replica` |
| Redis 6379 / Sentinel 26379 | 原生 mTLS + 独立 Redis CA + AUTH | HA peer 与 N9E Redis client；`port: 0` 关闭明文 |

所有 URI 前缀为 `spiffe://observability.raytrain.local/`。Redis 原生不校验 URI，专用 CA 限制签发范围，N9E 验证真实发现的 DNS，不能设置 `InsecureSkipVerify`。Redis HA 三个 Pod 共用 peer Secret，这是按集群角色授权，不是每 Pod 独立密钥。CNPG native PG 按证书 CN 映射角色，也不把 URI 当数据库用户名。

证书 Secret 引用不意味着进程自动热加载。本候选 N9E、Envoy、Redis 的 CA/leaf 轮换先更新信任链并受控滚动新组件；CNPG 9187 派生认证 helper 的加载语义见 `cnpg-hardening/DESIGN.md`。轮换前后都必须验证合法身份仍可用、旧证书按预期失效，不能删除 CA 私钥后才发现无法续期。Webhook API server 来源变化失败关闭，确认真实 RemoteAddr 后审阅更新，不能自动放宽到整个节点 CIDR。

CNPG 补丁会使原版 `kubectl cnpg status` 和 Pod 内 `instance status` 的详细查询被拒绝，因为它们没有 Operator 私钥。通过 Kubernetes API 查看 Cluster 条件，不用 port-forward 绕过认证；更完整的安全诊断入口另行审阅。

## 可重复隔离运行验收

`verify-runtime.py` 只在授权构建机、审阅后的 Git bundle 候选运行。它不使用 kubectl、不读生产凭据、不推拉镜像；四个固定摘要镜像必须已在 builder 缓存。输入 SQL 仍由 `prepare-seed.py` 校验固定官方 blob：

```bash
python3 "$obs_candidate/verify-runtime.py" \
  --upstream-sql /root/raytrain-observability-additive-20261008T065709Z/validation-n9e/upstream.sql \
  --output /root/raytrain-observability-additive-20261008T065709Z/n9e-mtls-runtime-<候选SHA>-<唯一时点>
```

输出路径必须不存在。脚本使用唯一 `obs-n9e-test-*` 容器/网络名称和 owner label、内部网络、零宿主端口、每容器最多 1 CPU/1Gi、临时数据与随机凭据。退出时仅清理自己的容器/网络/私有目录，保留脱敏 `result.json` 与日志，清理失败也判失败。

覆盖：实际候选配置 renderer、两个 Envoy 配置、N9E native Pod-IP 旁路拒绝、无证书/错误角色/错误方法拒绝、三 Redis+三 Sentinel 原生 mTLS、证书后仍需 AUTH、DNS 主发现、冻结本次主节点 Redis/Sentinel 进程后的自动切换、两个幸存 Sentinel 的新主一致性及实际主从角色、切换前后 N9E 正常验证码登录/profile、真实 PG clientcert+SCRAM、无证书/错 CN 拒绝、seed 防重复、迁移和随机 root 哈希。验证码先取真实图片，在本次新 Redis 中只读该题答案作为自动化白盒 oracle，错答案先拒绝，再新题正确答案通过；不关闭验证码、不改数据或声称视觉 UI E2E。

故障注入使用本次主成员容器的 `docker pause`，不手动发起 Sentinel failover。锁版 chart 的三个 announce Service 是独立 ClusterIP，设置 `publishNotReadyAddresses: true`；Pod 不可用时其 DNS 仍存在。因此脚本在冻结前后核验同一 DNS 地址，避免 `docker stop` 删除 Docker alias 后额外引入 DNS 故障。DNS 延迟会使 Sentinel 进入 TILT，集群 DNS 的可用性仍是线上 HA 前提；此脚本不声称覆盖 DNS 故障。三个 Sentinel 的 password-only `requirepass` 来自同一 Secret，Redis 会将它用于 Sentinel 之间认证，`sentinel auth-pass n9e` 则是访问 Redis 数据节点的独立密码。[Redis 官方 Sentinel 说明](https://redis.io/docs/latest/operate/oss_and_stack/management/sentinel/)

PG 使用新单实例和临时 CNPG-server CA 类比，服务端 CA 与 postgres-client CA 分离；客户端仍按真实 hostname/verify-full 校验，不能用自己的 client CA 代替 server CA。该脚本不证明 CNPG 真实签发、8000/9187/9443 补丁行为或三副本 PG HA；这些由独立 Go 协议回归与新 VKE 组件验收完成。

## 资源与 HA 限制

最终常驻 requests 合计约 **3.86 CPU、7.72Gi 内存、120Gi EBS**，包含 2 个 N9E 和 2 个 CNPG Operator 的 Envoy；另需启动/滚动副本余量。limits 合计约 17.2 CPU / 15.5Gi，集群资源配额必须连同 VM/OTel/CH 总预算一起评估。这是小流量候选，不是大规模告警容量承诺。Redis 的 fork/AOF rewrite 峰值和规则查询需实测调参。

PG 同步配置偏向数据完整性：失去所有健康同步备库时写入会等待，不能声称任意故障仍可写；在测试库验证主切换和 CNPG fencing。Redis AOF everysec、异步复制和 Sentinel 并不提供零 RPO，故障可能丢失近期会话/短期状态；`noeviction` 内存满会拒绝写入，需要水位告警。两套依赖都不是跨整个 VKE 的灾备。

这些均是空的新库；当前只存合成测试配置。引入正式规则/用户之前，必须完成新 PG/Redis 的备份、隔离恢复和故障演练，不能等承接正式业务后才补。现有指标/日志历史和原系统备份迁移仍属于后续阶段，本目录没有历史复制动作。

回退只停本次夜莺/新依赖流量，原观测和训练保持原路。不要自动删 PVC、Secret、CNPG CRD、PV 或 namespace；即使 StorageClass 为 Retain，删除 CR/namespace 仍会失去部分恢复所需元数据。需要清理时先登记本次资源及恢复证据，由发布者按明确范围执行。
