# 新夜莺线上验收协议

仅针对 `observability-alerting` 新建的夜莺、CNPG 和 Redis；由父发布流程执行 Kubernetes 写操作。现有训练、平台 PostgreSQL、旧可观测性、真实告警通知不在范围内。此文档是执行协议，不代表已在线上通过。

## 已有证据与探针边界

候选 `01abc769e4ce164cbe1c21ef6174ba53db432927` 的派生 Nightingale 已在隔离 Docker 通过72项检查，包括真 PG 客户端证书、三 Redis/Sentinel、自动切主、故障前后验证码登录/profile，以及原 HTML/主 JS 哈希保持。证据为 builder `mtls-validation/01abc76-n9e-derived/runtime/`。其中 PG 是单实例，不能作为 CNPG 高可用证据；没有实际 VM 查询。

当前 N9E 与 CNPG 的 Envoy 配置没有 active `health_checks` 或 health-only listener，因此不存在已配置间隔被 `no_traffic_interval=60s` 覆盖的问题。应用原生探针与 sidecar TCP readiness 共同决定 PodReady；TCP 仅验证代理监听，不证明 TLS、授权或后端链路。保持本轮探针配置，以下真实请求验收补充功能证据。

## 阶段一：只读部署与依赖门槛

父流程先记录对象 UID、imageID、节点、重启数和时间；同时沿用全局活跃训练前后对照。所有观察只选择明确的新 release/Deployment/Cluster 名，不能用全 namespace 删除或重启。

| 检查 | 必须满足 |
| --- | --- |
| 镜像与身份 | CNPG operator image 与 `OPERATOR_IMAGE_NAME` 同一审阅摘要；PG/Redis/N9E 与本轮锁版一致；只登记公开证书指纹/有效期，不输出 Secret |
| PG | `n9e-postgres` Cluster Ready、三实例 Ready、不同 hostname、当前主与目标主相同；主库 `pg_stat_replication` 精确两条 `streaming`。本候选实际采用 `synchronous_standby_names=ANY 1 (...)`，因此两副本 `sync_state=quorum` 是正确结果，并须核实 flush/replay 追平；不能机械要求字面 `sync`。两个备库 `pg_is_in_recovery()` 为真 |
| Redis | 三 Pod Ready、不同 hostname；三个 Sentinel `CKQUORUM n9e` 成功，均发现同一主和精确两个健康副本；副本连到当前主且没有进行中的全量同步 |
| 存储/预算 | PG/Redis 独立 Retain PVC 均 Bound，未绑定现有平台盘；低优先级、不抢占、不请求 GPU、无额外 toleration |
| 控制器 | 仅 watch 新 namespace；两固定 webhook 的 RBAC resourceNames/namespaceSelector/Namespaced scope 正确；新 CR server-side dry-run 实际经过 admission |

PG 备库仍在拉镜像、join 失败、同步备库不齐时，不进行切主，也不把“主库可连”当作三副本通过。先恢复依赖到上述门槛。`primaryUpdateStrategy=supervised` 下因镜像源更新出现 `Waiting for user action` 时，先完成受控 switchover，使 Cluster Ready，再启动夜莺初始化。

CNPG 1.30.1 候选须省略空的 `affinity.tolerations`，不要写 `[]`：PodSpec 注解序列化会省略空数组，比较函数却以 `reflect.DeepEqual` 区分 nil 与空切片，导致不断滚动最后副本。也不要删除初始化中的 replica Join Job 来重试拉取；保留 PVC 时它不会自动重建。若已经发生，应核对 Cluster/PVC UID，并用同锁版源码的 `specs.JoinReplicaInstance` 生成准确恢复 Job、完成服务端 dry-run 后再创建，不能手动把 PVC 标成 ready。

## 阶段二：初始安装与权限检查

新库 seed 只执行一次：读取一次性 Job 完成状态及数据库事实，不在线上重复 seed。夜莺先单实例执行迁移，确认目标表可查询、日志无迁移失败、root 密码已哈希（只输出布尔值）；再按 HA patch 扩到两实例并验证两个不同实例的 engine 心跳。

验证 Job/Pod 使用临时专用名称和 owner label、`automountServiceAccountToken: false`、无宿主端口/hostNetwork/特权/GPU、固定镜像及 CPU/内存上限。客户端凭据从本轮新 Secret 只读挂载，在进程内读取；不得把密码/token 写进 argv、日志或结果。仅输出检查名、布尔值、公开对象标识及内容哈希。探针客户端不持 Kubernetes 权限。

| 入口 | 正向 | 负向 |
| --- | --- | --- |
| N9E 17443 | UI 身份能读首页与认证 API；vmagent 身份只读 GET `/metrics` | 无证书握手失败、其他角色403；UI 不能读 metrics；vmagent 不能访问 UI 或 POST metrics |
| N9E 17000 | 仅 Pod 内回环 `/ping` | 从另一个 Pod 访问目标 PodIP 失败 |
| CNPG operator 18443/8080 | vmagent 经18443读指标 | 原8080 PodIP不可访问；无证书/错误角色被拒绝；配置/管理路径不暴露 |
| PG instance 9187 | vmagent，验证 CNPG server CA 及真实证书 DNS | 无证书、独立 CA 不符或同 mesh CA 错误 URI 被拒绝 |
| PG instance 8000 | Operator 仍可协调/读状态；精确最小健康例外 | 无 Operator 身份不能读详细 status；不导出 Operator 私钥给测试 Pod |
| CNPG webhook 9443 | 新 CR admission dry-run 成功 | 普通测试 Pod 或伪造 XFF 不能进入 admission；`/readyz` 仅最小状态，不作为 admission 成功依据 |
| PG 5432 | 原生 verify-full + `CN=n9e` + SCRAM；`n9e`/`postgres` 库允许 | 无客户端证书、错误 CN、错误密码、其他库拒绝；仍无 superuser/createdb |
| Redis 6379 / Sentinel 26379 | 独立 Redis CA、真实发现 DNS、TLS1.3、正确 AUTH | 明文、无证书、mesh CA 证书拒绝；合法证书无 AUTH 仍返回 NOAUTH |

直接测试 PodIP 时，客户端仍使用证书覆盖的服务 DNS 作为 SNI/hostname 校验；不能因为直连 IP 就设置跳过 TLS 验证。CNPG 8000 现有 CLI status 不带 Operator 私钥，不用它作为授权正向；通过实际 reconcile、Cluster 条件和 SQL 交叉验证。

## 阶段三：双实例业务与 VM 链路

1. 两个 Nightingale Pod 分别通过受控 UI mTLS 客户端读取 `/` 与实际引用 `/assets/index-0720a85a.js`。复用 `frontend_probe.py` 的原内容锁，并保持固定 DNS 校验。不能只检查 HTTP200；SPA fallback 不能冒充 JS。
2. 保持验证码开启：`GET /api/n9e/auth/ifshowcaptcha`，`POST /api/n9e/auth/captcha`。仅在这个新 Redis 中只读该题答案作为白盒 oracle；先用错误答案证明拒绝，再取新题正确登录。密码只读取本次 bootstrap Secret，不重设账号。
3. 从 Pod A 登录得到的临时 JWT，分别访问 A/B 的 `/api/n9e/self/profile`，必须均为新环境的 `root`；反向再验一次。检查两副本会话/JWT、公共依赖和迁移一致性。日志只保留布尔结果。
4. 父流程通过 `POST /api/n9e/datasource/upsert` 创建唯一 `vke-additive-canary` 数据源。`plugin_type=prometheus`，`category=timeseries`，`cluster_name=vke-additive-canary`，`http.url=http://127.0.0.1:18427`；`http.headers.Authorization` 在进程内由 `n9e-vm-read.token` 构造。不启用 `force_save` 绕过连通性验证，不把 token 写入非敏感 ConfigMap。
5. 从两个 N9E 实例分别通过认证的 `GET /api/n9e/proxy/<本次数据源ID>/api/v1/query?query=vector(1)` 查询，要求 Prometheus success、vector 值1。这才覆盖 N9E → 同 Pod Envoy18427 → VMAuth mTLS8427 → 新租户1001。`POST /api/n9e/datasource/query` 在该版本只筛选数据源ID，不能作为 PromQL 成功证据。
6. 仅创建专用 canary 规则 `vector(1) > 0`，持续60秒，通知渠道/联系人/回调/自愈均为空。观察正常 firing，再改本次规则为 `vector(0) > 0` 验证 resolved，随后暂停。保留本次数据源/规则ID，不能修改或导入旧告警规则。

实际接口依据固定 Nightingale 源码 `center/router/router.go`、`router_datasource.go`、`models/datasource.go`，构建证据目录保留其原文件。数据源 plugin ID、settings 和 API payload 必须按实际新库的 plugin list 与固定模型核实，不能照搬其他版本的ID。数据源/规则创建是父流程的显式写入步骤，读验证脚本不自动创建业务配置。

## 阶段四：新组件 HA 验收

每次只注入一个故障，先有持续的登录/profile与合成读写观察。禁止直接停止宿主节点、驱逐训练、删除 namespace/PVC、批量暂停整个依赖。

1. **PG 计划切换**：先要求三个实例全部健康和复制追平，父流程用固定 v1.30.1 `kubectl cnpg promote n9e-postgres <已核对的备库> -n observability-alerting`。该上游命令只读 Cluster/Pod 并乐观锁更新 Cluster.status，未调用已加固的8000 status。记录旧/新主、rw Service endpoint、两条流复制与同步状态恢复；同一 N9E 进程应恢复真实登录/profile。这是受控 switchover，不能写成非计划故障恢复。
2. **PG 自动故障恢复**：如要声明这一能力，父流程另行选择新集群内受控单主故障方案，核对 CNPG fencing 与恢复条件；只通过计划 promote 不能替代。不得并行故障 PG 与 Redis。
3. **Redis**：三个 Sentinel 完整发现两副本是硬前提。父流程对确认的新主成员执行受控单成员中断，记录 Service DNS仍在、两个幸存 Sentinel 自动选举、实际新主 role、剩余副本同步、N9E故障后验证码/登录/profile。不得同时下线 quorum，多次短重启不能冒充持续故障。若只显式 `SENTINEL FAILOVER`，报告计划切换；不能冒充不可用故障检测。
4. **N9E**：验证双实例跨Pod会话后，仅对本次 Deployment 的一个实例执行受控滚动替换，另一实例继续 query/canary；检查两个不同 engine 实例的心跳和恢复，没有把两个副本写成两个互不协调的引擎集群。

故障注入和清理由父流程负责并使用具体 UID/owner；验证脚本只采样，不自行 kill/patch/delete。每个阶段不通过即停止下一阶段、保存脱敏证据，保留原观测/训练路径。成功后只清理本次临时 probe，保留新依赖与留存盘；不自动删除证书或审计记录。

证据按阶段记录 `candidate SHA / image digest / 对象UID / 时间 / 检查名 / true或false`。必须区分 Docker隔离验证、线上静态核对、真实业务、计划切换和自动恢复，不能互相替代。

参考：[CNPG v1.30.1 promote 实现](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.1/internal/cmd/plugin/promote/promote.go)、[N9E v9.1.1 路由](https://github.com/ccfos/nightingale/blob/v9.1.1/center/router/router.go)、[数据源模型](https://github.com/ccfos/nightingale/blob/v9.1.1/models/datasource.go)。
