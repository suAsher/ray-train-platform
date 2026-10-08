# CNPG 1.30.1 专用认证适配设计

状态：IMPLEMENTED_UNVERIFIED；已交付锁定官方源码的补丁、Dockerfile 与 Go 协议回归，等待隔离 builder 的源码级 RED/GREEN 和真实新库验收。尚未构建或推送派生镜像，未修改集群。原候选 `b9dd3b5` 保留。源码、补丁及基础镜像均由 `upstream-lock.json` 固定；不使用未核验的示例 SHA。

## 范围与交付

只为新的 `observability-alerting/n9e-postgres` 使用派生 Operator。保留上游 v1.30.1、官方 PostgreSQL 17 镜像和现有 CNPG CRD。独立名称、namespace watch 和 webhook namespaceSelector 不放宽。旧平台数据库、其他 Operator 和训练 Pod 均不进入改造范围。

计划交付：`upstream-lock.json`（tag、实际 commit、来源归档 SHA256）、一个受审阅补丁、固定基础镜像的 Dockerfile、可注入上游包的 Go 协议回归。先在未补丁上游执行回归证明 RED，再应用补丁验证 GREEN；普通配置文件检查不能替代真实 TLS 握手或新库 HA 测试。

## 8000：详细状态沿用已有 Operator 身份

修改 `pkg/management/postgres/webserver/remote.go` 的 `NewRemoteWebServer`：`/pg/status` 纳入 `withOperatorAuth`；其余数据/管理路径继续使用该 guard。仅精确健康路径保留免证书，保留 TLS 1.3 + `RequestClientCert`，不改成全端口强制客户端证书而破坏 kubelet。

健康例外必须包括 `/failsafe`：它是实例之间隔离探测的无副作用入口，并不携带 Operator 证书。健康响应只保留状态码与固定最小文本，错误细节进入日志；不得返回 PostgreSQL 状态、配置或指标。不得以路径前缀、反向代理头或任何客户端声明绕过身份校验。

`withOperatorAuth` 比较客户端叶证书公钥 SHA256 与 Cluster.status.operatorCertificateFingerprint；复用这套已存在的身份，不替换为观测证书 URI。Operator 本身调用链已有证书：`reconcileCluster` 先登记指纹，然后 `NewTLSConfigForContext{ClientCert:r.OperatorClientCert}` → `GetStatusFromInstances` → `rawInstanceStatusRequest` → `common.NewHTTPClient`。首次启动和 Operator 换主时有指纹传播窗口；status 的 retry predicate 需显式复用 `IsTransientAuthError` 处理 503，401 不重试为匿名。

上游源码：

- [remote 路由及既有 guard](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.1/pkg/management/postgres/webserver/remote.go)
- [controller 指纹登记与证书调用](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.1/internal/controller/cluster_controller.go)
- [指纹切换](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.1/internal/controller/cluster_operator_cert.go)
- [status 重试](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.1/pkg/management/postgres/webserver/client/remote/instance.go)
- [实例间健康探测](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.1/pkg/management/postgres/webserver/probes/pinger.go)

兼容性变化必须公布：原版 `kubectl cnpg status` 走 API server ProxyGet，Pod 内 `instance status` 也没有 Operator 私钥，实例详情请求将被拒绝。普通 port-forward 或执行原命令不能补上证书。先使用授权 Kubernetes API 读取 Cluster 条件；专门的安全诊断通道另行审阅，不能为了 CLI 恢复匿名详细状态。

## 9187：仅监控身份可抓取指标

在 `pkg/management/postgres/webserver/metricserver/metrics.go` 的 `New` 接入独立认证 helper：

1. 通过现有 Cluster.spec.env 读取 `OBS_METRICS_CLIENT_CA_FILE` 与 `OBS_METRICS_ALLOWED_CLIENT_URIS`。CA 使用现有 projectedVolumeTemplate 挂载 `/projected/obs-metrics/ca.crt`；不扩展 CRD。
2. 保留现有 `GetServerCertificate` 以使用 CNPG 服务端证书；要求 `MetricsPortTLS=true`、TLS 1.3、`RequireAndVerifyClientCert`。
3. Go 标准证书链/有效期/clientAuth 校验通过后，由 `VerifyConnection` 精确匹配监控 URI；不接受空列表、通配符、其他 namespace 或仅同 CA 的错误角色。
4. TLS 关闭、CA 缺失/损坏、身份缺失均失败关闭，不能退回未认证指标。CA 轮换方式必须有合同；若先采用启动加载，轮换后需要受控滚动重启实例管理器，不能宣称 Secret 更新就自动生效。

拟定监控身份：`spiffe://observability.raytrain.local/observability-metrics/vmagent`。CA Secret 为 `obs-mtls-cnpg-metrics-ca`，只投射公开 `ca.crt`；监控私钥只在授权抓取端。vmagent 针对各 Pod 抓取时使用证书覆盖的实际服务端 DNS 名，不能跳过服务端校验。

源码：[metrics TLS 构造](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.1/pkg/management/postgres/webserver/metricserver/metrics.go)、[已有 env 与 projected volume API](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.1/api/v1/cluster_types.go)。

## 9443：明确的 Kubernetes 控制面协议例外

原生 admission webhook 是服务端 TLS，API server 不会因为 webhook.caBundle 存在就提供客户端证书。不把它写成 mTLS。

在 Operator webhook HTTP handler 加来源校验：除精确 `/readyz` 最小健康响应外，只有真实 `Request.RemoteAddr` 中的 `172.28.2.64`、`172.28.5.50` 可以进入 admission handler。使用 `net.SplitHostPort` 与 `net/netip` 解析并规范 IPv4-mapped IPv6；不读 X-Forwarded-For、Forwarded、X-Real-IP 或用户提供的 HTTP 头。允许 IP 通过 `OBS_WEBHOOK_API_SERVER_IPS` 固定；空值、CIDR、主机名、端口、多余地址或解析失败均拒绝启动，连接来源不符返回 403。

真实 apiserver endpoint/SNAT 行为仍须在新 Operator canary 验证；来源变化应失败关闭并明确更新允许列表，不能自动扩大到节点 CIDR。此协议例外只覆盖 admission webhook，不授权访问 metrics、详细 status 或数据库。

Operator 指标仍可用现成 `--metrics-bind-address=127.0.0.1:8080` 隐藏，再由专用 mTLS Envoy 代理公开 GET `/metrics`。Envoy admin 只在回环/Unix socket。不能把 ServiceMonitor 关闭误认为底层监听关闭。

## 构建与镜像注入

chart 的 Deployment image 和 `OPERATOR_IMAGE_NAME` 必须同时指向同一派生镜像 digest。`createBootstrapContainer` 使用该镜像运行 `/manager bootstrap /controller/manager`，将派生管理器注入 PostgreSQL 容器；`Cluster.spec.imageName` 继续使用原锁版 PostgreSQL 17 镜像。数据库 5432 保持原生 TLS/客户端证书和角色授权，不需要回环代理。

源码：[chart 双镜像引用](https://github.com/cloudnative-pg/charts/blob/cloudnative-pg-v0.29.1/charts/cloudnative-pg/templates/deployment.yaml)、[bootstrap 容器](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.1/pkg/specs/containers.go)、[管理器复制](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.1/internal/cmd/manager/bootstrap/cmd.go)。

## 必需回归与发布闸门

- 8000：无证书 401、错误/过期或未登记身份拒绝、合法 Operator 可读 status；最小健康无需证书且不泄露内部信息；首次 pin、Operator 换主、503 窗口及恢复；不允许匿名回退。
- 9187：真实 TLS 握手矩阵覆盖无证书、错误 CA、正确 CA 错误 URI、过期证书、正确身份；配置缺失、TLS 关闭和 CA 轮换失败均拒绝开放指标。
- 9443：两个真实 IP 正向；同网段其他 IP、伪造转发头、非法 RemoteAddr、IPv4-mapped IPv6、空配置负向；健康只固定最小响应。
- 上游受影响包的现有回归与补丁新增回归均通过；不得删原用例以获得通过。
- 最终 Helm 渲染核对两个镜像引用、watch namespace、RBAC/webhook 边界和真实生成 Pod argv；三副本新 PG 的首次初始化、复制、故障切换及 N9E 登录要重新验收。

本设计不宣称能阻止任意网络连接或出站流量；认证用于保护新服务入口，旧 CNI 策略保持不变。
