# 新可观测性 mTLS 改造实施计划

> **For agentic workers:** 使用 superpowers:subagent-driven-development，按模块责任执行。源码修改只在本机，测试和构建只在既有构建机；子任务禁止直接部署，统一由根任务审阅后执行。

**Goal:** 在不修改 CNI 和旧系统的条件下，保护并部署新增可观测性数据面。

**Architecture:** 显式回环后端与 Envoy 双向认证代理结合原生 TLS；按角色限制信任域和路径。先指标组，再 CH/OTel，最后解决 CNPG 原生入口后部署夜莺组。

**Tech Stack:** 固定版 Envoy 1.39.3、VM Operator 0.75 / VM 1.153、CH/Keeper 26.8、OTel 0.162、N9E 9.1.1、CNPG 1.30.1、Redis 8.8。

设计依据：[mTLS 设计](../specs/2026-10-08-observability-mtls-design.md)。原验证候选 `b9dd3b5` 与当前 Operator revision2 保留作比较基线。

## 1. 共用身份与门禁（根任务）

文件责任：`ops/observability/next/mtls/`、`tests/test_mtls_credentials.py`、`tests/test_mtls_contract.py`、`verify_scope.py` 的适用扩展；不修改旧 credential bootstrap 的既有 15 个 Secret 合同。

- [x] 核对真实 Envoy 镜像工具、版本及 digest。
- [ ] 根据组件角色/DNS 矩阵增加凭据工具：默认只列名称；显式创建仅作用新身份 Secret；部分成功时停止，不覆盖旧值；签发与轮换保留可恢复 CA。
- [ ] 先写反例：无 role/DNS、跨 namespace、重复身份、旧 Secret 名、空 SAN、过宽信任域、日志泄密、非法输入都失败；在构建机确认 RED 后实现。
- [ ] 用真实 OpenSSL/Envoy 验证合法、错误角色、无证书、错误 CA、过期证书和失败关闭；CA 私钥和叶证书私钥只进入受限临时目录/Secret。
- [ ] 审查所有生成 Pod 的监听/原生端口/外部端口清单，并为每条通道指定正向与负向测试。

## 2. VM 指标组（VM worker）

文件责任：`victoriametrics/` 全部候选、`tests/test_vm_mtls.py`；不修改其他组件或共用凭据工具。

- [ ] 为 RPC/HTTP 回环、只允许 insert/select 身份、self-scrape 路径、VMAuth tenant/token/reloader/probe 保留写失败合同测试。
- [ ] 实现 proxy ConfigMap/sidecar/卷、Operator CR 参数与身份挂载；固定三 storage 出站；处理 selectNode 与 AM gossip 原生 mTLS。
- [ ] 构建机真实渲染/CRD/schema/Envoy validate；检查 Operator 生成 Pod argv，不能只看输入 CR。
- [ ] 隔离 Docker 网络运行至少三个 storage 和三个 AM 的实际协议验证，含错误角色和 peer 恢复；无宿主端口、不借用旧库。
- [ ] 根任务审阅完整 server dry-run/所有权差异后部署该组；VKE 合成写查/自监控/告警/旁路拒绝通过才记录验收。

## 3. CH/Keeper/OTel 组（CH worker，根任务整合）

文件责任：`clickhouse/`、`otel/`、`tests/test_ch_mtls.py`、`tests/test_otel_mtls.py`。

- [ ] 先写 Keeper client/Raft 身份隔离、CH ingest/query/admin 权限、旧原生端口关闭、Operator 出站解析与扩容缺路由失败的合同测试。
- [ ] 实现回环 listener、按 Pod IP 绑定的代理、native TLS outbound、必要 alias Service/hostAliases，以及仅健康例外的探针。
- [ ] 固定镜像的 Envoy/config/schema 验证和两个 CH/三个 Keeper 的真实复制测试；schema/MV/OTel 写入按最小权限实际运行。
- [ ] 根任务审阅后部署；用专用 canary 验日志/trace、两副本、队列恢复、错角色和裸 Pod IP 拒绝。

## 4. 夜莺/PG/Redis 组（N9E worker）

文件责任：`nightingale/`、`tests/test_n9e_mtls.py`；如确需 CNPG 派生镜像，先提交具体 patch 与源码锁定设计，统一新增到本组件目录，不修改平台后端构建链。

- [ ] 先写 Redis/Sentinel 明文关闭、TLS hostname announce、N9E 校验开关与 metrics/UI 身份合同测试。
- [ ] 原生 mTLS Redis、N9E 回环代理、专用 Secret 与配置渲染适配。
- [ ] CNPG 端口补丁先验证既有 operator 客户端证书覆盖正常 `/pg/status`，再为匿名敏感接口增加拒绝回归；不能简单要求 kubelet 提交证书造成全库不健康。
- [ ] 构建机实际初始化/验证码登录回归，增加真实三副本 Redis/Sentinel 与 PG 切换；禁止用 DNS 别名的单 Sentinel 代替 HA 验收。
- [ ] 根任务只在全部身份门禁通过后部署该组，核对初始化、HA、登录与 VM 只读数据源/无真实通知告警。

## 5. 集成交付（根任务）

- [ ] 按模块完成独立安全审阅、负向测试和准确资源预算；用户/其他任务变更保留。
- [ ] 受审阅文件复制到已附加 worktree，以明确 commit 生成 Git bundle，送既有构建机隔离候选验证；不改正式 main checkout，不推未验证版本。
- [ ] 每次应用前核对 VKE context、原系统基线与精确新资源所有权；不递归 apply 整个候选目录。
- [ ] 记录应用/未应用、Helm revision、镜像 imageID、证书到期、PVC/拓扑、测试和恢复边界；更新原 runbook，不把单模块成功写成全体系完成。

构建机统一验证入口（候选确定后使用该目录实际路径）：

```bash
python3 -m unittest discover -s "$obs_candidate/ops/observability/next/tests" -v
python3 "$obs_candidate/ops/observability/next/verify_scope.py" "$obs_evidence/rendered.yaml"
```

`verify_scope.py` 的集群对象白名单必须来自逐项审阅，不可为消除错误自动扩展。Envoy 使用真实锁定镜像 `--mode validate`，证书与协议矩阵另外运行；静态测试不能代替运行验证。
