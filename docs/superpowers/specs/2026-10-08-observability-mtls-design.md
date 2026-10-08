# 新可观测性组件的 mTLS 隔离设计

日期：2026-10-08。用户已选择“保持 CNI，改造新组件 mTLS 代理”，授权沿此方案继续实现、验证和部署。本文补充 [整体架构](2026-10-08-vke-observability-platform-design.md)，不改变其采集迁移、存储、调度和旧系统保护边界。

## 目标与范围

只修改 `observability-metrics`、`observability-analytics`、`observability-otel`、`observability-alerting` 中本次新增的组件。当前 Cilium `PolicyEnforcement=never` 保持不变，不修改旧 NetworkPolicy，不注入旧业务 Pod，不使用宿主机网络、iptables 或 NET_ADMIN。

需要证明：普通训练 Pod 即使知道 Service/Pod IP，也不能无凭据读取/写入新指标、日志、trace、数据库或协调状态；错误角色的有效证书同样被拒绝。最小健康检查可公开 200/503，不得返回配置、版本清单、指标或管理信息。网络本身仍可达，mTLS 不是防连接洪泛或网络不可达保证。

原来依赖 NetworkPolicy 的清单保留为历史候选，不能直接创建数据面。按指标、分析链路、夜莺三组递进；任一组有旁路或未通过的兼容性测试就留在隔离验证阶段。

## 选型与身份

采用显式 Envoy sidecar，不引入集群级透明 service mesh。已经实际拉取并检查的官方镜像：

```text
harbor.wellspiking.ai/hub/envoyproxy/envoy:v1.39.3@sha256:dd85940439de19a0b6ae8419610363ea0ad351d9a994ea007161c206ec1e1865
```

该版本 2026-10-06 发布，包含安全修复。镜像有 `sh`、`sed`、`envoy`，没有 `curl/wget`，探针不得依赖后两者。[官方发布](https://github.com/envoyproxy/envoy/releases/tag/v1.39.3)

- Envoy 入站必须 `require_client_certificate=true`，验证专用 CA 和精确角色 URI SAN。身份格式为 `spiffe://observability.raytrain.local/<namespace>/<role>`；这是本项目静态证书身份约定，不宣称部署了 SPIRE 自动身份认证。
- Envoy 出站同时提交客户端证书并验证对端 CA、精确服务端身份。原生 TLS 客户端可使用受控 DNS SAN + 明确 serverName，不把“证书包含 URI”误认为原生软件已按 URI 授权。
- Redis、Alertmanager gossip 等只支持 CA 层鉴权的原生协议，使用独立角色信任域；不让整个观测 CA 的所有证书都获得 peer 权限。原有密码/ACL/VMAuth token 继续承担应用权限。
- 新身份 Secret 命名 `obs-mtls-<role>`，包含 `tls.crt`、`tls.key`、`ca.crt`，默认挂载 `/etc/obs-mtls`；不覆盖已有 15 个组件 Secret，不共享所有组件的同一私钥。初期同角色 HA 副本使用同角色身份。
- 证书包含必要 DNS SAN、serverAuth/clientAuth，限制有效期；私钥不进入仓库、命令行和日志。签发/轮换流程必须保留可恢复的 CA 管理路径，不能继续使用生成后立即销毁 CA 的一次性办法承担长期身份维护。

## 代理监听与授权

原生无认证监听必须关闭，或绑定 `127.0.0.1`。代理独立绑定 Pod IP，或使用不冲突的独立外部端口。仅删除 Service 端口不能满足要求；必须扫描实际 Pod IP，涵盖 IPv4/IPv6 默认绑定行为。

出站代理只监听回环，固定目标 Service/StatefulSet DNS，不做任意目的地址的开放代理。无 iptables 截获。必须核对 Operator 自动生成的地址、配置重载、缓存失效广播、peer 发现及探针，不能只改主数据请求。

HTTP 代理在需要共用端口时按角色和路径授权：监控身份只获得 GET `/metrics`，不能借同一 TLS 通道调用管理、删除、快照或 reload。管理身份只发给受控验收/初始化程序。Envoy admin 只绑定回环或不启用，不对 Service/Pod IP 暴露。

sidecar 强制非 root、只读根文件系统、drop ALL、禁止提权；显式 CPU/内存上限、单 worker、有限连接/缓冲。运行配置临时目录用受限 emptyDir，Pod IP 仅来自 Downward API。新增资源预算纳入各 namespace 配额，沿用低优先级、不抢占训练规则。

## 指标组

1. VMStorage 的 HTTP、8400/8401 原生 RPC 改为回环；Envoy 外部 RPC 只允许 insert/select 对应角色。
2. VMInsert/VMSelect 的 storageNode 通过三个固定回环出站代理分别连接三个 storage；同时处理 selectNode 缓存通知路径。使用 Operator 0.75 的 extraArgs 覆盖默认参数，验收生成的真实 argv。
3. VMAuth 后端请求通过固定本地出站代理；入口同时校验客户端身份与原有固定 tenant1001 读写 token。
4. Agent、Alert 的 scrape/remote write/query 使用正确 TLS 客户端配置；self-scrape 按组件校验服务端 DNS 身份。
5. Alertmanager gossip 优先原生独立信任域 mTLS，必须证明关闭明文/UDP 回退、三 peer 收敛及恢复；若锁版无法做到，就不创建 AM 数据面，先修候选。
6. Operator/reloader 的管理与指标也逐项盘点，健康端点只允许最小状态例外。

## ClickHouse、Keeper 与 OTel 组

CH 的 SQL、复制和 Keeper 客户端保留可用的原生 TLS 客户端能力，入站代理按 SQL/管理、replica、Keeper client、Raft 分角色授权。Keeper 客户端证书不能因此获得 Raft peer 权限。后端监听回环，代理绑定 Pod IP；稳定副本 DNS 和原生复制协议不随意改为 localhost。

Altinity 0.27.4 的 SQL HTTP 客户端没有客户端证书选项，需显式的仅 Operator Pod 内出站路由；如采用每副本 hostAliases，必须为代理上游另设不被覆盖的别名 Service，固定两副本映射并对扩容合同做负向检查。不能使用不生效的 HTTP_PROXY 假设。

OTel OTLP、metrics、health 分开；OTLP 保留客户端身份限制，向 CH 提交独立 ingest 身份，SQL 最小权限保留。数据字段、持久队列、trace 辅助表/MV 和两副本复制仍须真实验收。

## 夜莺与依赖组

Redis/Sentinel 优先原生 mTLS：明文 port=0，TLS 6379/26379、replication TLS、client certificate 必需、独立 CA、AUTH/ACL；公告稳定 DNS，N9E 验证每次发现的主库名称。保持 Sentinel 自动发现，不改成固定单机地址。

N9E HTTP 绑定回环，Envoy 对 UI/API 和 metrics 分角色保护；登录密码、验证码和 Redis 会话继续启用。数据库仍使用专用新 PG，不借用平台库。

锁版 CNPG 1.30.1 的 instance manager `/pg/status` 和 9187 metrics 有默认匿名入口，stock 镜像没有全部关闭/回环配置。不能把它们当最小健康例外，也不能谎称 sidecar 自动封住直连。此组先验证最小原生认证适配方案：保留原 operator 公钥校验并覆盖非健康 status 路径，为 metrics 增加认证/回环能力；必须有上游源码依据、定向回归、受控派生镜像和独立升级说明。该适配未通过前不发布 CNPG/N9E 组，不放宽已有隔离目标。

## 验收与发布

先在构建机内部 Docker 网络验证固定镜像和临时证书，再在 VKE 专用 canary Pod 验证。证据不包含 Secret 值。每组至少覆盖：

| 输入/故障 | 预期 |
| --- | --- |
| 明文、无证书、无关 CA、过期证书 | 数据/管理通道拒绝 |
| 同 CA 错误角色证书 | 非授权端口或路径拒绝 |
| 合法角色但错误 token/密码/ACL | 应用层拒绝 |
| 正确角色与权限 | 正向端到端通过 |
| Service/Pod IP/旧原生端口直连 | 不存在可绕过的数据或管理路径 |
| sidecar 或证书不可用 | 失败关闭，不能切回明文 |
| peer 重启、证书更新 | 合法流量恢复；不串用旧身份或丢失错误统计 |
| 部署前后旧系统对照 | 原训练 UID/重启、控制器与配置摘要不因本次改变 |

没有业务流量迁移，本轮回退仅作用新增组件。保留新卷、凭据、证据和版本，不自动删除云盘或旧对象。
