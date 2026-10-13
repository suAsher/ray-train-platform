# 临时 UI 入口：Nightingale 与 Grafana

本目录是独立 Helm chart，release `observability-ui`、namespace `nightingale`。它只新增入口，不升级或重启原 Grafana、夜莺、Alloy、Loki、训练任务。当前是否已安装、实际 revision 和验收结果以 [部署记录](../DEPLOYMENT_RUNBOOK.md) 为准。

## 当前入口：独立 Grafana

2026-10-13 **12:04:38 北京时间**，`nightingale/observability-ui` **revision 2** 已启用 `grafana.independent=true`，31701 指向独立 `grafana` namespace 的两副本 Grafana 和专用三实例 PostgreSQL。沿用两个现有代理和所有 NodePort，没有新建一组入口 Pod。

两个节点的 TLS、原生登录 / Secure HttpOnly cookie、4 类数据源、生产看板、VM 与 ClickHouse 真实查询、正常 Origin WebSocket 101、错误 Origin 403、未认证/伪造身份 401 和 `/metrics` 404 均通过；31700 夜莺登录入口仍返回 200，未配置正式通知。完整证据为构建机 `ui-cutover-acceptance.json`，见[部署记录](../DEPLOYMENT_RUNBOOK.md#2026-10-13-后端-http-遥测与独立-grafana-ha)。

新 Grafana 使用 **`grafana/grafana-admin`** 的独立原生账号，**没有自动迁入旧 SQLite 的账号、手工看板或 UI 配置**。旧实例原样保留，专门的回退说明在下文。新数据库共享、主备切换与无网络环境的备份恢复已通过；长期备份 / WAL 归档仍是独立运维事项。

## 入口和边界

| 用途 | 主地址 | 备用地址 | 原应用认证 |
| --- | --- | --- | --- |
| 夜莺：数据源、告警规则、联系人和通知渠道 | `https://172.28.2.65:31700/` | `https://172.28.1.232:31700/` | 夜莺原有账号、验证码与权限 |
| Grafana：看板、Explore、数据源 | `https://172.28.2.65:31701/` | `https://172.28.1.232:31701/` | **独立 Grafana 原生账号**，不是平台 SSO 或旧 SQLite 账号 |

两个地址均要求公司内网/VPN 可路由到该节点，首次使用前先完成下节的 CA 信任。使用固定主地址登录；换 IP 后浏览器 cookie 不会自动共享。

```text
浏览器 ──HTTPS:31700──> UI Envoy ×2 ──mTLS:17443──> 夜莺 ×2 ──只读 VM 查询
浏览器 ──HTTPS:31701──> 同一 UI Envoy ──mTLS:8443──> 独立 Grafana ×2 ──TLS──> 专用 PostgreSQL ×3
```

Gateway 使用原应用登录，保留 Authorization、Cookie、Origin；清除外部伪造的认证代理身份头，只设置 HTTPS 协议头。支持 Grafana Live WebSocket。响应中的每条 `Set-Cookie` 独立保留原属性并补 `Secure`，不会按逗号拆分 `Expires`。不记录请求访问日志、URL 或 token。不提供管理端口，阻止 `/metrics` 及其子路径从浏览器入口透传。

Grafana 上游为 `https://grafana.grafana.svc:8443`，网关呈现现有 `obs-mtls-n9e-ui-gateway` 叶证书并核验 `spiffe://observability.raytrain.local/grafana/grafana-server`。Grafana 共享数据库与稳定加密密钥，不依靠 ClientIP 粘连保存配置。原 Prometheus/Loki 数据源保留原 HTTP 访问边界；不要把 UI 上游 mTLS 说成所有老链路均已改为 TLS。

NodePort 的 `externalTrafficPolicy: Cluster` 使无网关 Pod 的节点也能转发，可能发生 SNAT。两个 IP SAN 只用于证书验证，**不是入口防火墙**；NodePort 可能在所有节点接口上开放。当前 VKE NetworkPolicy 执行关闭，chart 不把它当作访问限制。应在云安全组/路由中把相应端口的可达范围限定为公司内网/VPN，不开放公网；本 chart 不修改 CNI 或安全组。

## 首次使用：信任公开 CA

浏览器使用独立 `nightingale/obs-ui-browser-server` Certificate，仅 `server auth`；绝不向用户分发 `obs-mtls-n9e-ui-gateway` 的客户端证书/私钥。server Certificate 的精确 IP SAN、独立 URI、90 天有效期/提前 30 天续期由本 chart 声明。cert-manager 的严格 Policy 仅允许 `nightingale` namespace 与 `obs-mtls-mesh` issuer，并只向 cert-manager controller 授予该 Policy 的 `use` 权限。

管理员在构建机导出 **公开 CA 文件**，不是完整 Secret：

```bash
umask 077
VKE_CONTEXT='cd9q3b7rf9aidnnbd0je0@81533555-kd9q3q0frjmp21sk5lklg'
kubectl --context="$VKE_CONTEXT" -n nightingale get secret obs-ui-browser-server \
  -o jsonpath='{.data.ca\.crt}' | base64 -d > observability-ui-ca.crt
openssl x509 -in observability-ui-ca.crt -noout -subject -issuer -dates -fingerprint -sha256
```

通过已有受信渠道向运维用户交付 `observability-ui-ca.crt` 并核对 SHA-256 指纹；在公司策略允许的浏览器/操作系统证书管理界面导入受信 CA，然后验证页面证书的 SAN 与访问 IP、有效期均匹配。不要绕过证书警告后输入密码，也不要复制 `tls.key`、CA 私钥或整份 Secret。导入此 CA 意味着信任该 CA 签发的证书，宜由公司统一证书管理工具分发并管理撤销。

CLI 检查可使用 `curl --cacert observability-ui-ca.crt https://172.28.2.65:31700/`，不要使用 `-k` 作为成功证据。夜莺账号保持原样；Grafana 使用本轮独立实例的账号。网关不代用户登录、不关闭验证码；管理员凭据仅通过既有受控方式交付。

授权管理员可在构建机的私人终端读取初始登录信息，不将终端结果发入对话或文档：

```bash
# 夜莺初始用户名 root；如果已在 UI 修改密码，以修改后的密码为准。
kubectl --context="$VKE_CONTEXT" -n nightingale get secret n9e-bootstrap-admin \
  -o jsonpath='{.data.password}' | base64 -d
# 当前独立 Grafana 用户名、密码；这不是平台统一认证账号。
kubectl --context="$VKE_CONTEXT" -n grafana get secret grafana-admin \
  -o jsonpath='{.data.admin-user}' | base64 -d
kubectl --context="$VKE_CONTEXT" -n grafana get secret grafana-admin \
  -o jsonpath='{.data.admin-password}' | base64 -d
```

以上命令面向当前新实例。只读取 `admin-user` / `admin-password`，不要输出其 `secret-key`；不向对话、截图或工单粘贴密码。首次登录后当前密码以 Grafana 数据库为准，初始化 Secret 不是密码变更的实时副本。

修改初始化 Secret 不会重置已存在的夜莺用户；不要为重置密码重跑 bootstrap。需要长期保存的夜莺配置位于专用 PostgreSQL，当前 Grafana 配置位于专用 PostgreSQL，维护与备份见 [grafana/README.md](../grafana/README.md)。

## 文件与配置维护

| 文件 | 管理内容 |
| --- | --- |
| `Chart.yaml` | 自有 chart 版本 |
| `values.yaml` | 固定 Envoy digest、副本与资源、NodePort、节点 IP SAN |
| `templates/certificate.yaml` | 新浏览器 Certificate、精确申请 Policy 与专用 use RBAC |
| `templates/configmap.yaml` | HTTPS、上游 mTLS、SDS、请求头边界、Secure cookie |
| `templates/deployment.yaml` | 2 副本网关、低优先级与资源边界、PDB |
| `templates/services.yaml` | 两个临时 NodePort 与 Grafana 粘性后端 Service |
| `../tests/test_ui_access.py` | 真实 Helm render 安全与迁移合同 |

新增 2 个 Pod，合计 requests **0.1 CPU / 128 MiB**，limits **0.6 CPU / 256 MiB**，不申请 GPU、不新增 PVC。滚动发布最多额外 1 个临时 Pod。使用原 `raytrain-observability-additive` 低优先级、不抢占现有任务；排除 `172.28.1.222` 与 algorithm 的 `172.28.3.32`，无额外 GPU/专属节点 toleration。

依赖：现有 `nightingale`、`monitoring`、`grafana` namespace，cert-manager/approver-policy、`obs-mtls-mesh` ClusterIssuer、Ready 的 `obs-mtls-n9e-ui-gateway` client Certificate，以及两套现有服务。chart 不拥有或重建这个 client Certificate、不挂载 VM token、CA 私钥或 ServiceAccount token。

### 安装或升级

遵循仓库 release skill：本机只编辑/审阅；commit + Git bundle 到构建机隔离 worktree 后测试。以下命令在**已验证候选目录**执行，显式使用 VKE context，不能从历史副本发布：

```bash
VKE_CONTEXT='cd9q3b7rf9aidnnbd0je0@81533555-kd9q3q0frjmp21sk5lklg'
UI_CHART='ops/observability/next/ui-access'
python3 -m unittest discover -s ops/observability/next/tests -p 'test_ui_access.py' -v
helm template observability-ui "$UI_CHART" -n nightingale > ui-candidate.yaml
kubectl --context="$VKE_CONTEXT" apply --dry-run=server -f ui-candidate.yaml
```

发布前还需以固定 Envoy 镜像执行 `--mode validate` 和无生产密钥的 TLS/多 cookie 夹具验证，审阅完整渲染差异、NodePort 无冲突、节点/SAN 正确、namespace 配额与可调度资源足够。记录原训练 Pod/RayJob/RayCluster 的 UID、重启数和状态作为前后对照。已有 release 升级先以管理员只读方式保存 values 和必要资源备份到权限 `0700` 目录，不导出包含私密内容的整份 manifest。

首次安装：

```bash
helm upgrade --install observability-ui "$UI_CHART" --kube-context "$VKE_CONTEXT" \
  -n nightingale --atomic --wait --timeout 10m
kubectl --context="$VKE_CONTEXT" -n nightingale wait --for=condition=Ready \
  certificate/obs-ui-browser-server --timeout=120s
kubectl --context="$VKE_CONTEXT" -n nightingale rollout status deployment/observability-ui --timeout=180s
```

已有 release 的小改动：使用 `--reuse-values -f <审阅后的最小覆盖>`，先 `--dry-run=server --hide-secret` 再正式升级。不要对本 chart 额外套用 VM/CNPG 的 post-renderer；此 chart 已直接声明自己的 filesystem SDS。

验收两个节点、两个端口的正确证书及原登录页；验证未登录管理 API 仍拒绝、伪造身份头不能绕过登录、`/metrics` 不可访问、验证码与静态文件正常。登录后的正向功能测试使用真实获准账号；仅登录页 200 或未登录 401 不算 UI 功能全部验收。对比现有训练对象，清理本次测试资源。

### 证书与日常巡检

```bash
helm --kube-context="$VKE_CONTEXT" -n nightingale list
kubectl --context="$VKE_CONTEXT" -n nightingale get deploy observability-ui
kubectl --context="$VKE_CONTEXT" -n nightingale get svc nightingale-ui grafana-ui
kubectl --context="$VKE_CONTEXT" -n nightingale get certificate obs-ui-browser-server obs-mtls-n9e-ui-gateway
kubectl --context="$VKE_CONTEXT" -n nightingale top pod -l app.kubernetes.io/name=observability-ui
```

Certificate 的两份叶证书 Secret 均整目录只读挂载，Envoy filesystem SDS 监视 kubelet 原子投射更新；不用 `subPath`，不用为了正常续期重启原应用。改变 bootstrap/路由配置会通过 ConfigMap checksum 滚动更新网关。信任根改变仍是独立审阅的 CA 轮换工程，需要双根过渡、客户端信任更新与负向身份测试；支持 SDS 更新不等于可以随意替换根 CA。

改节点地址时，在 values 的 `certificate.ipAddresses` 中同步更新精确列表，检查实际节点、路由与安全组后经渲染/策略/证书验证发布。证书 SAN 列表变更会触发 cert-manager 更新；待新证书生效后再使用新 IP。切换到仅 ClusterIP 不涉及修改训练或原应用。

### 回退到保留的旧 Grafana

新旧账号、UI 配置和数据库分别保留，回退不等于迁移。先保存受限的当前 release 配置并审阅 server-side dry-run，最小覆盖如下：

```yaml
grafana:
  independent: false
```

这将 31701 定向回 `monitoring/grafana-ui-backend:80`，上游为旧集群内 HTTP，旧登录凭据引用 **`monitoring/grafana-admin`**。旧两副本各自 SQLite + emptyDir，ClientIP 粘连不是持久化或共享 HA；仅 UI 保存的旧配置仍受其原有丢失风险影响。回退不把新 PG 用户或看板复制进 SQLite，**不删除新 Grafana、PG、Certificate、PVC 或 PV**。

恢复新入口时设 `grafana.independent: true`，继续保留其原生登录与 mTLS。Chart 默认 false 是保护新安装的安全默认值；当前生产 release 实际为 true，后续更新必须保留这个运行配置。

### 后续改 Ingress

1. 确定两个独立 DNS 名称，复用各应用根路径 `/`，不要直接发明子路径。更新 Grafana `root_url`/夜莺对外 URL 的变更需要单独保存原配置并检查登录、CSRF 与 WebSocket。
2. Ingress 浏览器端使用正常受信域名证书（由 cert-manager 管理），后端继续指向同一 `nightingale-ui:443` / `grafana-ui:443` gateway Service。Ingress 到 gateway 的 HTTPS 校验需要一张包含 Service DNS 的独立、严格受限 server Certificate 及信任 CA；当前证书只有节点 IP SAN，不能直接用 Service DNS 验证，也不能用关闭验证代替迁移。
3. 完成 Ingress 正向/负向认证与证书测试后，用最小覆盖 `service.type: ClusterIP` 升级本 chart。模板会移除两个 NodePort 字段和 `externalTrafficPolicy`；确认节点上旧端口不可达后关闭对应安全组规则。
4. 保留原应用登录和夜莺上游 mTLS。如果后续采用统一认证，由独立变更定义账号/角色映射，不能靠注入代理身份头跳过原认证。

上面是保留专用代理的迁移路径。另一条路径是由统一 Ingress/API 网关直接承担上游 mTLS，验证同样的客户端身份、CA、服务器身份、转发头和原生登录后直接接夜莺；独立 Grafana 同样要求指定客户端身份和上游 mTLS；仅旧回退目标保留普通 HTTP 上游。此时可撤掉这两个临时代理，不需要永久在统一网关后再叠一层。现有火山 ALB 的前端双向认证配置不能作为已经支持上游客户端证书的证据，整合前需核实。

入口回滚使用 `helm rollback observability-ui <已验收revision> -n nightingale --kube-context "$VKE_CONTEXT" --wait`。只需暂停外部入口时优先改为 ClusterIP。完整移除此 release 不应删除既有夜莺/Grafana、原 client Certificate、数据库或存储；删除 Helm 所属新对象前核对所有权，证书 Secret 的保留/回收依 cert-manager owner-reference 配置单独确认，不盲删同 namespace 资源。

实现参考：[Envoy v1.39.3 Lua HeaderMap API](https://github.com/envoyproxy/envoy/blob/v1.39.3/docs/root/configuration/http/http_filters/lua_filter.rst)、[Envoy SDS 轮换](https://www.envoyproxy.io/docs/envoy/latest/configuration/security/secret.html)。
