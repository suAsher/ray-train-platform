# 集成身份、实验授权与受控产物发布验收

2026-09-12。本轮按用户明确授权实现并发布独立集成身份、实验级 grant 和独立 Run 受控产物；生产验收发现的 SDK 结束接口回归也已修复、发布并复验。用户随后明确完整共享 MLflow 才是主要接入需求，本文记录已完成的可选受限通道，不将它作为全部 MLflow 的替代。

## 版本与发布状态

| 对象 | 版本与证据 |
| --- | --- |
| 后端首发提交 | `0088e9f9` |
| 后端首发版本 | `release-20260912-04-0088e9f9`；Helm **215**；schema **48** |
| 后端首发摘要 | `sha256:edb0cf2506dc5a7219ea835c8c6d4bfd05eca62a016a242f8d25cfb50c41b26f` |
| SDK 热修复候选 | `80dd1642a274b664e532a9c559dee16a8f93d927`；真实路由回归 RED 与修复后全量 PostgreSQL 验证 GREEN |
| SDK 热修复发布 | `release-20260912-05-80dd1642`；Helm **216**；摘要 `sha256:89da0ab78374d8fe76a908428acddaedbb5d5e9c04383df2bb0553dfe4e6691b`；两个后端 Pod 的 imageID 一致、Ready、重启数 0 |
| Portal dev | 独立仓库 `wellspiking/frontend/wellspiking-frontend`，提交 `8113e99d8e1f0754bd830d027624a95cdfae1686` |
| Portal CI/CD | pipeline **33857**，jobs **89793 / 89794 / 89795** 均 success；部署日志 Helm **1043** |
| Portal 发布镜像 | `harbor.wellspiking.ai/wellspiking/frontend/wellspiking-frontend-dev`，摘要 `sha256:ddbbe925cd5812a7161d9962d98302ddbfe077148c68b81a50df31f9a8bc7e08` |
| Portal 实际 Pod imageID | **尚未取得**：目标 infer Kubernetes API 网络不可达；其他可访问配置没有对应 Pod。CI 构建摘要、部署日志和浏览器页面证据不能替代目标 Pod imageID |

热修复不新增数据库迁移，schema 仍为 48。构建仅涉及变更组件；未把业务代码放入训练镜像，也未修改训练任务、调度或个人数据归属。

## 本轮交付范围

- **独立集成身份**：交互式所有者管理机器身份、短期凭据和撤销；机器不能自建身份、发令牌或给自己授权。身份绑定用户与团队，不通过调用方 Header 选择归属。
- **实验级实时授权**：集成权限由 scope、有效身份/团队与目标实验 grant 共同约束。元数据读写与产物读写分开；`artifacts:write` 需要产物读权限，不自动授予实验元数据写权限。当前粒度为实验，不是单个 Run。
- **受控 Run 产物**：平台 REST 初始化、固定分片上传、断点续传、分片及整文件 SHA256 校验、不可变 READY 和受权下载。仅 RUNNING 可初始化/上传/完成；PENDING 可在 Run 结束或过期后由有权调用方取消。
- **Portal 与使用说明**：沿用 11 个一级菜单，在实验中心增加集成接入与外部 Run 产物展示；37 篇帮助中补充身份、凭据、实验授权及文件步骤，没有把新能力拆成额外一级菜单。
- **交付资料**：新增标准库产物客户端、续传 receipt、专用 OpenAPI 与接口合同，更新[接口交付单](MLFLOW_PARTNER_HANDOFF.md)和[平台下一步计划](PLATFORM_NEXT_PLAN_20260912.md)。

产物单文件范围为大于 0 且不超过 20 GiB；固定 8 MiB 分片，编号从 1 开始。所有者逻辑预算 100 GiB、最多 16 个 PENDING 会话、有效期 24 小时，过期未取消仍占预算。单片请求读取上限 2 分钟，完整校验与下载上限 15 分钟。READY 不覆盖、不给删除接口，不执行或反序列化模型文件，不迁移用户个人数据。此通道与原生 MLflow 共享目录分开，不是 `mlflow.log_artifact()` 的代理。

## 构建机候选验证

本机仅编辑与审阅；编译、测试、格式化和构建均在构建机完成。

| 检查 | 结果与边界 |
| --- | --- |
| 后端全量验证 | 全量 Go 包通过；使用真实 PostgreSQL 验证新装、schema 46 升级、重复迁移与并发行为 |
| 产物并发与覆盖率 | race 检查通过；产物相关覆盖率 **82.4%** |
| integrations 覆盖率 | **100%** |
| auth 包覆盖率 | 整体 **72.1%**；未达到 80%，不宣称全仓或所有包覆盖率达到 80% |
| Python 示例客户端 | **17/17** mock unittest 通过：续传先读状态、仅缺失片上传、网络不确定不盲重试、receipt 0600 无 PAT、下载 SHA256/大小校验、不覆盖已有文件、错误隔离与重定向拒绝 |
| OpenAPI | `openapi-spec-validator 0.7.2` 校验通过 |
| Portal lint 与构建 | 完整 Dockerfile lint/合同检查及 dev 构建通过；候选构建排除 `.env` 文件 |
| Portal E2E | **7/7 Playwright** 通过；这是候选自动化证据，不能替代生产身份与网络联调 |
| SDK 热修复回归 | 真实人类/机器授权路由复现 `end_time` 回归为 RED；修复后全量 PostgreSQL 验证 GREEN；上线后 SDK 指定结束时间与读回一致，终态拒写 409 |

## 数据库备份与训练保护

用户专门授权了本轮 schema 46 全库备份。受限备份和发布证据保存在构建机 `/root/raytrain-release-20260912-integrations/`。已完成 `restore-schema46` 隔离恢复验证，验证后的临时数据库已删除；未把备份内容、数据库凭据或个人数据写入报告。

首发及 SDK 热修复最终核查中，**5 个 RayJob、5 个 RayCluster、11 个训练 Pod** 的 UID、状态与容器重启数相对发布前逐项比较均为空 diff。配额读数为 **24 / 24 / 0**，未修改 `local` 24 卡配额。最终快照为发布目录中的 `jobs-sdk-final.json`、`clusters-sdk-final.json`、`pods-sdk-final.json`、`backend-sdk-final.json`。

## 真实生产联调

本轮使用 Portal 同源 API 访问真实生产后端和存储。用户明确授权了三枚有效期一天的集成令牌，用于本次专用资源验收；令牌仅保存在浏览器内存、请求同源生产接口，完成后撤销并清空明文。这里验证的是两个真实集成身份之间的授权隔离，不等于两个不同人类用户或不同团队的完整验收。

| 资源 | 标识与结果 |
| --- | --- |
| writer 集成身份 | `97b79ff83588952f1ab163a38b77ee99` |
| isolated 集成身份 | `4df8cbc86e554acf6c8e214f2c1de35e` |
| 平台 Experiment ID | `bf0b8f783a3798466e5f855018b6f8cc`；上游 MLflow Experiment ID `8` |
| 平台 Run ID | `53807742bdb0e6966af93f27590a08ab`；上游 MLflow Run ID `637a7b14bf8e4626b926e3c0a69b9a4a` |
| Run 结束结果 | 已通过 REST 结束为 `FINISHED`；SDK 带 end_time 的生产问题见下节，不能用 REST 成功替代该项 |
| Artifact ID | `6432ec83650e3d2e3b5de1ee564575bc` |
| 文件 | `acceptance-checkpoint.bin`，**8,388,641 字节**；状态 **READY** |
| 完整 SHA256 | `1f39bce438af4337ff56fb0c537586e9f7a58bfbe122ba87f7c5563784cececc` |

### 产物读写与下载

1. 第一片上传与同片同摘要重试均返回 **200**；相同编号的冲突内容返回 **409**。
2. 第二片上传成功；complete 与重复 complete 均返回 **200**，产物为 READY。
3. 只读凭据下载返回 **200**；实查长度与完整 SHA256 一致，并核对 `Content-Disposition: attachment`、`Cache-Control: no-store`、`X-Content-Type-Options: nosniff`。
4. 已取消两个专用 PENDING 上传；未删除 READY 产物或既有个人数据。
5. Run 结束后尝试初始化产物返回 **409**，未重新打开 Run。

### 权限与撤销

| 操作 | 真实响应 |
| --- | --- |
| 另一集成身份无 grant 访问测试资源 | **404** |
| 集成令牌调用训练 jobs 能力 | **403** |
| 只读令牌写入 | **403** |
| 给 isolated 授予实验 read 后读取 Run | **200** |
| 仅实验 read grant 时访问产物/写入 | **404 / 404** |
| 再授予 artifacts:read 后读取产物 | **200** |
| 撤销 grant 后再访问 | **404** |
| isolated 与只读两枚令牌撤销后验证 | 均 **401**；isolated 身份也已撤销 |
| writer 读写令牌 | SDK 复验后撤销 **200**，再次访问 **401**；writer 身份撤销 **200**。三枚令牌和两个身份全部撤销 |

正式 API 域名的跨源令牌传递额外复验被自动审批拒绝，令牌未传出。**不声称本轮新集成令牌已通过 canonical 域名直接访问验收**；以上 Portal 同源生产验收仍是实际结果。对接应用所在机器的正式域名 DNS/TLS/443 与令牌路径需要按获准网络途径另行验收，不能通过关闭证书校验或扩大凭据传递绕过。

## 生产发现与 SDK 热修复

首发生产联调发现：新增授权 wrapper 未透传 `FinishRunAt`，因此官方 SDK `set_terminated` 携带 `end_time` 时返回 **503**。底层服务已有实现不能证明 wrapper 路由可用；REST finish 成功也不能覆盖 SDK 协议验收。

已增加经过真实注册路由、分别使用人类 PAT 与集成机器身份的 RED 回归。修复提交 `80dd1642a274b664e532a9c559dee16a8f93d927` 透传授权检查及 `FinishRunAt`，全量 PostgreSQL 测试 GREEN。`release-20260912-05-80dd1642` 已发布：原 Run 同结束时间重试返回 200；新专用平台 Run `8a97d20cc517295eab390784752ed792`（上游 `a0deb7b0da524e7981d696625700192d`）经 SDK 结束返回 200，读回 FINISHED、end_time **1789192623197** 与传入值一致，终态追加指标返回 409。最终令牌撤销请求 ID `eefd4264384a04c71b2cd21c4077992f`，撤销后 401 请求 ID `3de108106aa3acdeca0c013282e61e86`，身份撤销请求 ID `b5512599c96c13866d6866f0b6b625c4`。

## 浏览器实查

登录 Portal 已核实：**11 个一级菜单、实验中心 4 个页签、37 篇帮助**，帮助内容包含新的集成身份/实验授权与受控产物章节。已看到真实外部 Run、READY 文件及下载链接。页面可见和链接存在与上述真实 API/下载结果分别保留证据，不能单凭 UI 按钮认定后端功能可用。

Portal 目标 Pod imageID 尚未取得，保留为明确缺口。不得用旁边 master checkout、本仓库 `frontend/`、其他集群 Pod 或历史镜像代替独立 dev 仓库此次部署证据。

## 尚未完成与下一步

1. 用户后续要求完整共享 MLflow 接入并合并普通用户帮助，另行记录后续发布；本页记录的受限 SDK 热修复和撤销已完成。
2. 取得 Portal infer 目标 Pod 的实际 imageID；对接方运行机器按获准通道完成正式域名访问验收。当前同源生产验证不能代替目标机器的网络条件。
3. 不可变**模型候选**尚未实现；当前 READY 产物只是可信文件底座。下一批固定来源 Run、产物摘要、代码/数据/运行时版本和清单，之后做固定输入的独立评估，再接 Registry 版本、审批、Serving 与回滚。原生 MLflow 共享 UI 仍不能证明审批不可绕过。
4. 完整指标历史导出、后台创建对账、分布式限流与运维恢复仍按后续批次推进；文件分片续传不等于全 MLflow SDK 兼容。
5. 后台日志发现历史资源出现 `CLEANED → RETIRING` 约束错误，已列为下一步只读诊断对象。本轮未为此改变调度、reconciler 或训练状态，不能将新功能验收解释为这个历史问题已修复。
6. 团队专属节点、TAS/闲时抢占、IDC 连接器不在本轮变更范围，不能把设计或开关存在当作上线验收。

接口定义见[集成身份与产物合同](MLFLOW_INTEGRATION_ARTIFACT_API.md)、[OpenAPI](api/mlflow-integration-artifacts.openapi.json)和[客户端示例](../examples/mlflow_integration/README.md)。正式对接仍需明确调用方、负责人、允许的实验与能力、到期日和秘密交付渠道；验收临时凭据不能作为长期接入凭据。
