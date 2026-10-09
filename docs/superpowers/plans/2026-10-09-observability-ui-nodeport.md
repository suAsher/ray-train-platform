# 可观测性 UI 临时 NodePort 交付计划

用户要求说明架构、部署文件、Chart/values、资源账目与维护方法，并将可配置 UI 临时通过 NodePort 开放，后续切换 Ingress。已确认使用公司内网/VPN和节点内网 IP。

## 交付边界

- 提供 Grafana 与夜莺浏览器入口，保持产品原有登录。新建独立小型 Envoy 网关，不修改或重启原 Grafana、夜莺、Alloy、Loki、训练任务。
- 网关的浏览器端使用 cert-manager 签发的专用 serverAuth 证书；夜莺上游使用现有 `n9e-ui-gateway` clientAuth 身份，校验 CA 和精确服务器 URI。
- 浏览器证书只允许 `172.28.2.65`、`172.28.1.232`，专门签发策略不能扩展已有内部身份权限。公开 CA 可以导出，私钥不离开 Secret。
- 临时端口为夜莺 31700、Grafana 31701；全局只读清单确认未占用。NodePort Cluster 模式不提供源 IP ACL，不更改 CNI 或云安全组，不申请公网入口。
- Grafana 原有两副本各为 SQLite/emptyDir。本次用独立后端 Service 和入口 ClientIP affinity 减少会话漂移；不能视为共享数据库或持久化修复。
- 不开放裸 VM 写接口、数据库、Envoy admin 或无认证采集器诊断端口；当前指标查询使用 Grafana/夜莺，Grafana 的 Loki/ClickHouse 日志分析数据源后续单独接入。

## 顺序及验证

1. 只读盘点 Helm/清单归属、资源 requests/limits/实际用量、PVC、UI 认证及存量对象 UID/spec/restarts。
2. 先提交 UI 入口合同测试，在构建机隔离候选验证 RED；实现独立 `next/ui-access` Chart。
3. 在构建机验证完整基础设施测试、Helm lint/render、固定 Envoy 实际配置加载；审阅精确新增对象和 server-side dry-run。
4. 仅安装新 UI release，等待新 Certificate Ready、网关就绪；使用受信 CA 验证两个节点/两个入口的 HTTPS、名称、登录页、静态文件、未认证拒绝、伪造身份头拒绝、metrics 拒绝和可完成的原生登录/查询流程。
5. 对比原训练/监控/数据面对象，记录并区分用户并发任务变化；确认没有临时探针残留。
6. 回填 `OPERATIONS.md` 和 `DEPLOYMENT_RUNBOOK.md` 的实际版本、资源、入口、证书信任步骤、已验证范围与限制。

## 后续 Ingress

复用网关作为受控 backend，申请域名证书，配置外部 URL 和 WebSocket，保持夜莺内部 mTLS。验证新域名的登录、静态资源、API、证书和原权限后再将本 Chart Service 改为 ClusterIP 收回 NodePort。CA 根轮换、Grafana 共享数据库、真实通知渠道和 OTel 业务接入分别维护，不借入口发布一并变更。

也可在统一入口确认能承担夜莺上游 mTLS 后，验证其客户端身份、服务器身份校验和原生登录，再移除临时代理。当前 ALB 的这项能力尚未确认。

## 回退

回退只卸载本次 UI release/关闭本次 NodePort；不删除原产品 Service、Secret、PVC 或 namespace。新浏览器证书与它自身的签发策略属于此 release，内部既有 `obs-mtls-n9e-ui-gateway` 不在删除范围。Helm 回退不替代数据库备份。

## 完成记录

2026-10-09 已安装 `nightingale/observability-ui` revision 1，部署源码 `12a9866`。331 项合同测试及 33 项现场检查通过，2536 个存量保护对象无差异；夜莺新入口的真人验证码登录仍待用户完成。实际资源、访问地址与证据见 [运维手册](../../../ops/observability/next/OPERATIONS.md) 和 [部署记录](../../../ops/observability/next/DEPLOYMENT_RUNBOOK.md)。
