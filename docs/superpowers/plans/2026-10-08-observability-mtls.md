# 新可观测性 mTLS 改造实施计划

> **For agentic workers:** 使用 superpowers:subagent-driven-development，按模块责任执行。源码修改只在本机，测试和构建只在既有构建机；子任务禁止直接部署，统一由根任务审阅后执行。

**Goal:** 在不修改 CNI 和旧系统的条件下，保护并部署新增可观测性数据面。

**Architecture:** 显式回环后端与 Envoy 双向认证代理结合原生 TLS；按角色限制信任域和路径。保持现有 CNI/NetworkPolicy 模式，逐组通过认证、兼容性和作用域门禁后部署。各组本轮部署与基础 canary 验收已完成，不将启用 NetworkPolicy 作为前置条件；业务接管和后续运维门禁单独记录。

**Tech Stack:** 固定版 Envoy 1.39.3、VM Operator 0.75 / VM 1.153、CH/Keeper 26.8、OTel 0.162、N9E 9.1.1、CNPG 1.30.1、Redis 8.8。

设计依据：[mTLS 设计](../specs/2026-10-08-observability-mtls-design.md)。原验证候选 `b9dd3b5` 与原 VM Operator revision 2 保留作比较基线。

**最终检查点（2026-10-08）：新增组件部署与本轮基础 canary 验收完成，临时资源清理和最终旧系统对照完成。** 35 常驻 Pod 全部 Running/Ready/0 restart，21 PVC 全 Bound/670Gi、真实 PV 全 Retain，12 节点 Ready/三项 Pressure False，auxiliaryPods=0。VM `66b5461`、组件 revision 3/Operator 5，182 live/33 RBAC/89 Docker PASS。CH/OTel `a74c9c9` 线上 60 TLS、13 native 拒绝、双 gateway 到双 CH 精确复制及属性治理通过；OTel gRPC Export/SIGKILL/file_storage 恢复另在 Docker 通过。PG 提升实例 2 收敛、双 quorum 流追平/ANY 1。N9E 应用 44、PG 5432 27、依赖 46、webhook 来源 6，共 123 项明确现场检查及另 1 次真实 admission 全 PASS；双 Pod VM vector(1) 通过。业务采集、迁移和持续运维交接不在本轮完成范围。

最终验证候选 `227f5e0` 全量 174 unit PASS/6.844 秒，与上述现场检查分开计数。8000 未知 Operator 证书严格 503/`operator certificate not recognized`，匿名严格 401/`client certificate required`，不泛化接受普通 503；Redis 合法 TLS 后仍需 AUTH，无证书/foreign CA 返回明确 alert。Redis 明文 wire 未作独立肯定结论，reset/timeout 不计 PASS。N9E 唯一 probe/CM/monitor 复制叶 Secret 已按 UID 删除、全部 absent。

最终旧系统对照以 `44ddda1 before-cnpg` 为起点：旧 22 控制器 added/removed/changed 均 0；保留 Pod UID/重启数变化 0，自然新增 11、删除 0；保留 RayJob/RayCluster UID/spec 变化 0、新增 10；ConfigMap 唯一 hash 变化为 `node-onboarding-state`；原平台 `/healthz` HTTP 200。最终证据 `mtls-validation/final-additive-inventory-20261008.json`，此前 post-database 两份快照保留历史。不得与更早 baseline 混算，不概括为所有旧对象零变化或性能零影响。结果依据根任务现场/构建机证据回报冻结。

标准发行镜像使用已核验的固定 digest，优先 Harbor hub；CNPG 专用安全派生镜像已构建推送：`harbor.wellspiking.ai/guofeng.su/cnpg-observability:1.30.1-mtls-c741e72@sha256:53be5d2201b1502aae329b3f64f4b342c897772fb88edde2838e7f7750bc8875`。N9E 派生镜像只回移 go-redis v9.0.2 的三处 caller context 判断，锁定官方源码及八份原嵌入资源、保留两项 build tags，最终仅覆盖原镜像 `/app/n9e`；不升级依赖、不生成生产密钥。已部署 `nightingale-observability:9.1.1-sentinel-01abc76`，digest `sha256:2e544213530f914b961e05d9fbab2ddcbbb995402379496a34815f0d61da6315` 的 raw manifest SHA 核验一致；44 应用现场检查和本轮依赖协议检查通过。PostgreSQL 仅复制同源 manifest `sha256:6b7e6a1267254902840efa9936bbeb0a398a79b5b9cb2e82218e4a92b602e97c`，不重编译；平台后端/Portal 不重构建。

## 1. 共用身份与门禁（根任务）

文件责任：`ops/observability/next/mtls/`、`tests/test_mtls_credentials.py`、`tests/test_mtls_contract.py`、`verify_scope.py` 的适用扩展；不修改旧 credential bootstrap 的既有 15 个 Secret 合同。

- [x] 核对真实 Envoy 镜像工具、版本及 digest。
- [x] 根据组件角色/DNS 矩阵实现凭据工具：默认只列名称；显式创建仅作用新身份 Secret；部分成功时停止，不覆盖旧值；保留可恢复 CA。
- [x] 完成 role/DNS、跨 namespace、重复身份、旧 Secret 名、空 SAN、信任域、日志泄密、非法输入等合同；纳入当前构建机静态回归。
- [x] 创建 26 个新身份与 3 个附加 Secret；持久 CA 路径 `/root/raytrain-observability-pki-20261008`，不输出或提交私钥/完整 Secret。
- [ ] 完成证书持续运维交接：当前工具仅首次创建，叶证书 90 天、CA 1095 天，无自动续期/到期通知；按部署手册的续签与分批滚动流程验证，跨 CA 双信任轮换及受限 CA 私钥备份恢复尚待演练。CNPG 原生 CA 独立由 CNPG 管理，不混作本工具证书。
- [x] 完成本轮 VM、CH/OTel、N9E/依赖的真实认证、角色与失败关闭检查，按各自现场/Docker 协议矩阵记录范围；Redis 明文 reset/timeout 不作肯定结论。生产 CA 私钥只留受限持久 builder 目录，仅叶证书私钥进入对应身份 Secret。
- [x] 生成 Pod 的监听/原生端口与通道正负向检查按各组矩阵完成；不泛化为尚未接入业务链路的全覆盖证明。

## 2. VM 指标组（VM worker）

文件责任：`victoriametrics/` 全部候选、`tests/test_vm_mtls.py`；不修改其他组件或共用凭据工具。

- [x] 实现 RPC/HTTP 回环、insert/select 身份、self-scrape 路径、VMAuth tenant/token/reloader/probe 保留合同测试。
- [x] 实现 proxy ConfigMap/sidecar/卷、Operator CR 参数与身份挂载，固定三 storage 出站；配置 selectNode 和 AM gossip 原生 mTLS。
- [x] Docker 完整 88 项验证通过，覆盖该运行脚本的 VM/AM 检查；无宿主端口、不借用旧库。保留早期 72 项核心结果与失败日志为历史证据，不替代 VKE 验收。
- [x] VM Operator revision 5、`raytrain-next` revision 3 已部署；17 个组件 Pod 滚动完成。
- [x] 实现并纳入 `1165276` 合同：Auth/AM 显式 `vm-auth-reloader` / `vm-am-reloader`，各只 get/list/watch 精确配置 Secret，拒绝额外宽绑定。
- [x] Auth/AM 全部 Pod 已切换窄 SA；最终 RBAC 33 PASS，含实际 name-fieldselector LIST/WATCH、禁止未筛选 list/其他 Secret key；四个旧 Role/Binding 已由根任务按 owner/UID 核对删除。证据：`mtls-validation/vm-rbac-final-20261008.json`、`mtls-validation/1165276-render/removed-generated-rbac.json`。
- [x] VM 真实 Docker 89 PASS，暂停后健康失败检测 10.042 秒，小于 20 秒门限；证据 `mtls-validation/fa75f6e-vm-runtime.json`。
- [x] 修复旧 proxy 无 checksum 不会自动 reload：补每角色 podMetadata config SHA；`66b5461` 全量 153 PASS。13 个对象的精确差异仅含 8 个 ConfigMap 的 idle health/no_traffic 5s 与 7 个角色的 config SHA，随后完成滚动。
- [x] 滚动后的 VKE 现场 182 项复验 PASS，证据 `mtls-validation/66b5461-render/vm-final-live-result.json`；前一 `vm-live-20261008/result-full.json` 保留历史范围。
- [x] 最后临时 probe UID 前缀 `5d9f88dd…` 已 Succeeded，并以 UID precondition 删除成功；回执 `mtls-validation/66b5461-render/final-probe-deleted.json`。
- [ ] 后续生产采集交接、容量和恢复门禁单独执行；不能将当前自监控/canary 通过写成全部业务接管完成。

## 3. CH/Keeper/OTel 组（CH worker，根任务整合）

文件责任：`clickhouse/`、`otel/`、`tests/test_ch_mtls.py`、`tests/test_otel_mtls.py`。

- [x] 完成 Keeper client/Raft 身份隔离、CH ingest/query/admin 权限、旧原生端口关闭、Operator 出站解析与扩容缺路由失败合同。
- [x] 实现回环 listener、按 Pod IP 绑定的代理、native TLS outbound、必要 alias Service/hostAliases，以及仅健康例外的探针。
- [x] 完成隔离三 Keeper 运行验证。
- [x] `b145e23` Docker 运行脚本全部 PASS，两个副本的 log/trace/lookup 各 1，health 200→503→200；HTTPS advertised host 修复保留证书 DNS 校验。
- [x] 独立 `7cd5e01` 全量 170 unit PASS，真实 gRPC Export 与 SIGKILL/file_storage 恢复在 Docker PASS；每个 CH 副本各 12 log+12 trace，精确 IDs 无丢失/重复，sender 不重试、应用 PID 已换/proxy anchor 不变、queue/inflight 全 0、cleanup verified。证据 `mtls-validation/7cd5e01-otel-recovery/result.json`；保留早期 `1052a65` 11 RED 历史，此结果不是线上故障注入证明。
- [x] 现场发现 CH Operator watch regex 回退 all，越界 watch 被 RBAC 拦截；候选 `b4e8b1d` 改为 literal namespace，并关闭默认 scrape annotations。
- [x] 更正监听误判：额外 `listen_host[1]` 也是 `127.0.0.1`、index 2 不存在，没有 wildcard 旁路，没有 XML 修改；当时为核查保持零副本的历史过程保留。Keeper 已 3 Ready/0 restart，实际 socket 白名单通过。
- [x] CH 已 2 Ready/0 restart、两个 100Gi PVC；新 canary 每副本 7 表，复制 2/2、无积压。
- [x] `a74c9c9` 移除与 Operator 生成对象重复的两个手写 PDB；删除前有备份且用 UID/RV 约束，不继续保留双份手写对象。
- [x] CH/OTel 线上范围完成：8 Pod Ready/0 restart、7 PVC/300Gi，CH 双副本各 7 表且复制正常；60 strict TLS PASS、13 direct Pod IP native ConnectionRefused，无 IPv6 监听。
- [x] 两 gateway 各 1 log+1 span 在双 CH 精确 runID/lookup 一致，非白名单属性丢弃、服务字段按策略覆盖、队列 0；5 临时 Pod+1 CM 按 UID/RV 删除并确认 absent，无额外 Secret；schema Job Complete、TTL 86400 秒。
- [x] 上述线上证据位于 `mtls-validation/b4e8b1d-ch-operator-fix/` 的 final-live-state、tls-live-matrix、native-bypass-live、otel-live-replication、temporary-probes-absent 五份 JSON；与独立 Docker 恢复证据分开记录。
- [ ] 后续生产负载、业务接入和适用故障场景仍独立验收，不将本轮 canary 或 Docker 恢复泛化为线上所有 HA 场景通过。

## 4. 夜莺/PG/Redis 组（N9E worker）

文件责任：`nightingale/`、`tests/test_n9e_mtls.py`；如确需 CNPG 派生镜像，先提交具体 patch 与源码锁定设计，统一新增到本组件目录，不修改平台后端构建链。

- [x] 完成 Redis/Sentinel 明文关闭、TLS hostname announce、N9E 校验开关与 metrics/UI 身份合同。
- [x] 实现原生 mTLS Redis、N9E 回环代理、专用 Secret 与配置渲染适配。
- [x] CNPG 官方源码锁定、最小补丁、72 协议叶测试、22 上游 specs 和 vet 通过；新认证 helper 覆盖率 94.05%，不泛化为整个 CNPG 或整个观测栈覆盖率。
- [x] CNPG 派生 Operator/instance-manager 镜像构建推送并锁 digest；8000 复用公钥 pin，9187 严格 vmagent 身份，9443 使用真实 API server 来源例外；健康保留最小响应。
- [x] CNPG 26 个对象 absent/scope/server dry-run 后安装 revision 1，两 Pod 各 2/2 Ready；Pod-IP metrics 正向/负向、原生 8080 关闭、非 API webhook 拒绝、7 项 RBAC 与真实 PG admission dry-run 通过。
- [x] 创建三实例新 VKE `n9e-postgres` CR，三实例最终 Ready；最终全栈 21 PVC 全 Bound/670Gi、实际 PV 全 Retain，数量与容量来自最终库存而非目标推断。
- [x] Docker 新单 PG 客户端认证与初始验证码登录通过；真实三 Redis/Sentinel 选主和剩余副本同步通过。
- [x] go-redis v9.0.2 三处 `ctx.Err()` 最小补丁通过真实 TLS RED/GREEN，23 项 N9E 合同通过；caller 取消仍返回，单 Sentinel TLS 超时可继续，错误 CA/DNS 仍拒绝。
- [x] `01abc76` 派生镜像已构建推送 Harbor，raw manifest SHA 核验一致；72 项 Docker 检查通过，包括原 HTML/引用 JS 与同一 N9E 进程故障前后真实验证码/登录。没有关闭验证码；此结果不构成两实例 N9E 或 VKE PG HA 证据。
- [x] VKE Redis ECR 拉取超时后，核验 index 和 linux/amd64 manifest 一致再改 hub；三副本各 3/3 Ready、重启 0。
- [x] Redis 使用相同 manifest 升级至 revision 3 `deployed`，三副本各 3/3 Ready、重启 0；日志 `mtls-validation/5e51aa4-render/redis-ready-upgrade.log` 保留此前等待超时后的恢复过程。
- [x] Redis 本轮现场 TLS/AUTH/无证书与 foreign CA 明确 alert 验收通过；明文 wire 未作独立肯定结论，不将 reset/timeout 当 PASS。
- [ ] Redis 其他现场故障恢复场景另行验收；Pod Ready、Helm `deployed` 或 Docker 选主结果不等于全部 VKE HA。
- [x] PG Harbor mirror 与 GHCR 同源 manifest 校验成功，证据 `mtls-validation/postgres-mirror-verification.json`；根任务只 patch imageName，随后删除旧 join Job、保留 PVC。
- [x] 明确记录上述删除导致的阻塞：CNPG 1.30.1 对 orphan initializing replica PVC 不像 primary 一样自动重建 Job。使用官方 JoinReplicaInstance 恢复，helper SHA 前缀 `31aced22…`、新 Job UID 前缀 `f4ca…`；实例 2 加入成功，Operator 已自动创建实例 3 join，恢复回执 `mtls-validation/n9e-pg-join-recovery-20261008/join-job-created.json`。
- [x] PG 三实例 Ready、两个 quorum 复制流追平后，根任务以官方四字段及 UID/RV 乐观 status merge 请求 promote 实例 2；提交请求时没有提前记录 PASS。
- [x] 后续提升收敛检查 PASS：current=target=2、Healthy/ClusterReady=True；三 Pod 不同节点 Ready、重启 0；实例 1/3 recovery=true，两 streaming/quorum 流 flush=replay，ANY 1。quorum 不等于每个副本字面 `sync`。
- [ ] 数据库备份恢复、实例 metrics/状态与 N9E 应用故障恢复按适用检查另行验收；不把单次提升收敛泛化为全部 HA 场景通过。
- [x] N9E dependency-gate 已保存，PG 收敛后完成 bootstrap/迁移和双 Pod 部署，双 Pod Ready/0 restart；最初 42 live probe PASS，含原 HTML/JS、登录/JWT cross-Pod 与三个 Redis/Sentinel 检查，保留阶段结果。
- [x] 唯一 canary 数据源 `vke-additive-canary-20261008`（ID 1/plugin 1）verification=verified，经受保护夜莺代理查询 vector(1) PASS。前两次 metadata data/error 与 auth dat/err envelope 断言不一致，未 upsert，属于验收脚本问题而非服务故障；保留排障记录。
- [x] 双 Pod 完整应用 probe 重跑 44 PASS、not_tested=[]，含 VM query vector(1)；证据 `mtls-validation/a74c9c9-n9e-live/full-n9e-live-result.json`。该字段只描述应用 probe 自身覆盖范围。
- [x] `14ffc79` 全量 174 unit PASS；PG 5432 线上严格协议矩阵 27 PASS，证据 `mtls-validation/a74c9c9-n9e-live/pg-native-v2-result.json`。
- [x] `227f5e0` 全量 174 unit PASS/6.844 秒；最终依赖 46 live PASS，8000 unknownOperator 严格 503/`operator certificate not recognized`，匿名 401/`client certificate required`、合法 ready 200，未放宽普通 503。保留旧 probe 误期 401 的历史；服务配置无需改变。
- [x] 两 Operator webhook 来源 6 PASS，ready 200、普通 Pod/伪造 headers 403；真实 API server admission PASS。证据分别为 `mtls-validation/a74c9c9-n9e-live/dependency-final-live-result.json`、`webhook-source-result.json`、`pg-admission-server-dryrun.json`。
- [x] N9E 唯一临时 probe、CM、monitor 复制叶 Secret 已按 UID 删除且全部 absent；`mtls-validation/a74c9c9-n9e-live/cleanup.json` confirmed=true。
- [ ] 应用故障恢复与无真实通知的告警触发/恢复按适用范围另验，不提前标整组或整体完成。

## 5. 集成交付（根任务）

- [x] 本轮各模块完成独立安全审阅、负向检查和实际资源清点；保留用户/其他任务变更。
- [x] 已通过明确 commit/Git bundle 在既有构建机隔离候选验证；未改正式 main checkout。后续修复仍逐候选执行相同流程。
- [x] 线上 CH/OTel 候选 `a74c9c9` 的 159 unit PASS、独立恢复候选 `7cd5e01` 全量 170 unit PASS分别记录；VM/N9E Docker 与现场结果各保留范围，OTel 早期 11 RED 为历史。不得将独立测试脚本候选标为所有线上组件的部署版本。
- [x] 最终依赖验证候选 `227f5e0` 全量 174 unit PASS/6.844 秒；N9E 应用 44、PG 27、依赖 46、webhook 6，共 123 明确现场检查与另 1 次真实 admission 均通过，不与 unit 数量混算。
- [x] 本轮应用均按 VKE context、原系统基线与精确新资源所有权执行；后续仍不递归 apply 整个候选目录。
- [x] 完成以 `44ddda1 before-cnpg` 为起点的本次只读对照：旧 22 控制器全不变、保留 Pod UID/重启和保留 RayJob/RayCluster UID/spec 全不变；自然新增 Pod 11、任务 10，ConfigMap 唯一 hash 变化为 node-onboarding-state，12 节点 Ready。
- [x] 最终库存确认 35 常驻 Pod 全 Running/Ready/0 restart、0 GPU/负 priority/Never/无 hostNetwork/不在禁用节点；21 PVC Bound/670Gi/PV Retain、12 节点 Ready/三项 Pressure False、auxiliaryPods=0；原平台 healthz 200，旧系统对照与同一 before-cnpg 起点一致。
- [x] 冻结两份本轮文档，记录组件 release、镜像、实际 PVC/拓扑、检查范围、历史问题与清理证据；不宣称业务迁移或所有故障场景完成。
- [ ] 后续门禁：业务采集、告警规则 firing→resolved、真实通知、SSO、历史迁移、全套备份恢复、自动续期/到期通知及证书轮换演练；正式业务交接前完成受限 CA 私钥备份恢复。

构建机统一验证入口（候选确定后使用该目录实际路径）：

```bash
python3 -m unittest discover -s "$obs_candidate/ops/observability/next/tests" -v
python3 "$obs_candidate/ops/observability/next/verify_scope.py" "$obs_evidence/rendered.yaml"
```

`verify_scope.py` 的集群对象白名单必须来自逐项审阅，不可为消除错误自动扩展。Envoy 使用真实锁定镜像 `--mode validate`，证书与协议矩阵另外运行；静态测试不能代替运行验证。

现场操作、镜像摘要、证据目录和未完成项统一记录在 [部署手册](../../../ops/observability/next/DEPLOYMENT_RUNBOOK.md)。所有部署写入由根任务集中执行；运行组未通过时保留门禁，不能把 Docker 隔离测试写成 VKE HA 或旧系统无影响的证明。
