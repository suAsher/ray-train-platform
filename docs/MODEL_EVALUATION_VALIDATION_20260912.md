# 独立模型评估发布与验证记录

第二阶段代码与页面已发布；生产专用评估因训练节点下载 GitHub 源码超时而失败，尚未通过生产报告成功回传验收。设计见[实施计划](superpowers/plans/2026-09-12-independent-model-evaluation.md)，前一阶段见[共享模型验证](MODEL_LIFECYCLE_VALIDATION_20260912.md)。

## 版本与发布证据

| 项目 | 观测 |
| --- | --- |
| 发布前后端四端 | `4fd302153e57a6ab48c1d0158cf0e6d3769427a1` |
| 发布业务代码四端 | `31e8cdf1bb88b7c650ba5728ba441e869c696b83`；本地与正式构建目录均干净 |
| 后端镜像 | `release-20260912-11-31e8cdf` |
| 后端摘要 | `sha256:0795495f8a5b601350a26fd0816889c1e63d1bf7a97ec9103480adf981a48f14` |
| Helm / schema | revision 222 / schema 50，原为 221 / 49 |
| 后端实际运行 | 两副本 Ready，重启数0，Pod imageID与候选摘要一致，healthz 200 |
| Portal dev 首次发布 | `d82ec905dcb44335ef557c504ad2b0c4cf28f133` |
| Portal CI | [33880](https://gitlab.wellspiking.ai/wellspiking/frontend/wellspiking-frontend/-/pipelines/33880)，lint、docker-dev、helm-deploy-dev均通过 |
| Portal 部署日志 | job 89837，release `yuanzhu-he-wellspiking-frontend-master`，namespace `yuanzhu-he`，revision 1048，deployed |
| Portal 页面资产 | `index-BKf82OJr.js`，已登录浏览器可见四个页签和真实评估记录 |

Portal 是独立仓库，CI历史 release 名包含 master 不代表本次代码来自 master；流水线实际 checkout 为 dev 的 d82ec905。当前无法连接 test-dev Kubernetes API，未独立读取 Portal Pod imageID，不以 CI 或页面资产替代这一证据。后续文档提交不改变业务镜像。

本次只构建 backend；Helm server dry-run 与现有 manifest 的差异只有后端镜像一行。既有3个非终态 RayJob、其关联1个训练 RayCluster和2个训练 Pod，在滚动发布前后及验收任务回收后 UID、状态及重启数一致。训练镜像、调度开关、节点策略和24卡配额均未修改。

## 实现范围

- 共享模型版本固定权重 SHA；评估另行固定 READY 数据版本、val/test、评估方案代码提交、镜像和参数摘要。第一版单 Worker 节点、1–8 GPU、全部场地；不允许 latest、空划分或隐式回退。
- 预检与幂等提交复用既有 GPU 配额、任务仓库和 outbox；先预留固定 Job ID。取消与创建使用同一事务锁防止取消后再创建任务。
- 任务专属凭据授权固定权重下载与报告回传，不注入个人 PAT。配置从数据库读回后重新规范化，避免 PostgreSQL JSONB 格式变化破坏摘要。
- 有效不可变报告和任务成功退出共同决定评估成功。缺失报告、失败任务不会展示成功结果；同口径成功结果才允许比较。
- PUBLIC 数据评估结果全平台成员可见；TEAM 数据结果由数据所属团队权限决定。模型共享不改变源数据权限。
- 可信评估运行参数跳过训练集预加载，保留固定数据 manifest 与挂载；普通训练渲染保持原样。评估程序自行读取固定 val/test。
- 实验中心新增“评估”；版本化数据集补充 train/val/test 数量、固定版本定位及无可用版本提示。使用说明仍为六分类、每个问题独立文档，共38篇。
- 外部 MLflow SDK 使用 `https://raytrain.wellspiking.ai/api/v1/mlflow-native` 作为 Tracking URI，REST 追加 `/api/2.0/mlflow/...`。平台训练保留注入的连接配置，不替换为个人 PAT 网关。此入口覆盖已验证的 Tracking/Artifacts/Registry，不等于所有未来 MLflow 产品接口均受支持。

### 验收后页面小修

Portal `6258338d1f0143f601c456b9d9e56f6663ae9ba1` 增加“查看运行任务”和失败日志提示、列表权重文件名，将“实际样本数”改为“数据版本样本数”。不扩大任务访问权限。构建机官方门禁、生产编译及23个隔离Chromium测试通过，独立审阅无P1/P2；已推dev，[流水线33881](https://gitlab.wellspiking.ai/wellspiking/frontend/wellspiking-frontend/-/pipelines/33881)通过，部署作业89840成功，Portal Helm revision1049。登录页面实际加载 `index-CzldrNzD.js`，评估详情显示新样本数文案及正确的 `/job/detail/job-a2d979777890a3ce42c03b97` 链接。

## 构建机测试

所有测试、格式化、编译和构建均在构建机隔离目录执行，本机只编辑、审阅与 Git 操作。

| 验证 | 结果 |
| --- | --- |
| 最终后端31e8cdf，完整 `go test -timeout=20m ./...` | 通过；配置真实隔离 PostgreSQL，未以 skip 充当通过 |
| 评估领域覆盖率 | 83.5% |
| PostgreSQL | 全新安装、49→50、重复迁移、并发配额、幂等、创建/取消竞态、报告不可变及 JSONB 摘要通过 |
| 权限与运行时 | 跨任务令牌拒绝、过期、严格报告、普通训练兼容通过 |
| Python SDK | 9个测试通过 |
| Portal d82ec905 | 官方 Dockerfile.lint 全部门禁、生产编译通过 |
| Chromium | 22个隔离 E2E通过：评估8、模型5、体验9；容器无网络，原生下载仅本地HTTP fixture |
| 独立审阅 | 修复数据版本加载重试与 TEAM 报告说明后，无未解决P1/P2 |
| 依赖审计 | 内部 Nexus audit 返回400 `ERR_PNPM_AUDIT_BAD_RESPONSE`，未完成；本次未修改依赖 |

保留早期 RED 证据：帮助新增问题缺失、k8s可信运行参数尚未实现时的合同失败；这些不冒充最终测试结果。

## 备份与专用生产验收

用户单独授权 schema49 全库备份和隔离恢复。备份保存在既有构建机 root 限定目录 `/root/raytrain-release-20260912-evaluations`，无网络 PostgreSQL 恢复、schema及元数据数量验证通过，临时恢复容器已删除，备份保留用于回滚。不在仓库保存数据库或凭据。

用户随后授权一项专用1 Worker、1 GPU、4 CPU、16Gi协议验收；固定本人156字节合成权重及公开 val 版本，仅检查权重下载/SHA与报告，不读取数据样本或计算模型准确率。

| 项目 | 记录 |
| --- | --- |
| 评估 ID | `2b28fdd1090bd023d486863f1a10049f` |
| 实际 Job ID | `job-a2d979777890a3ce42c03b97` |
| 方案 | `b91beab08b5ae2b068fe013868dc313d`，平台协议验收20260912（不代表模型精度） |
| 模型 / 版本 | `6849e5c1-b33b-438f-99f9-831bd568ce39` / `2cb1bbef-48ee-48a0-88f4-a1ba6bd5a784` |
| 权重 SHA | `42266484a6afff3107ba1e7560207d24a8ed9146d289d92a41c5858647ae40de` |
| 数据版本 | `version-a26436a221f5f4b851863a3bde884d26`，val，manifest记载1620样本，不代表实际处理数量 |
| 创建 / 终态 UTC | 2026-09-12 14:34:23 / 14:39:38 |
| 最终结果 | FAILED / MISSING；评估脚本未启动，无有效报告 |

提交时前端流水线尚未反映到页面，使用既有浏览器登录会话调用平台公开评估 API，预检200、提交202；并未通过新表单创建生产任务。随后新页面正确显示真实失败记录、固定来源、停用方案、报告缺失，比较与报告下载禁用。新表单完整操作链由隔离 E2E 验证。

失败日志：`fatal: unable to access 'https://github.com/suAsher/ray-train-platform.git/': Operation too slow. Less than 1024 bytes/sec transferred the last 60 seconds`；随后 `source materialization failed: Git fetch failed or exceeded 180 seconds`。后端此前能解析 GitHub commit，不代表训练节点能下载仓库。未绕过源码校验、修改网络或把代码装进镜像。

发现失败后发起取消时任务已自动终态，API拒绝再次取消；未重复提交任务。方案已停用（revision2），模型重新归档（revision4）。RayJob/RayCluster/训练Pod自动回收，local 配额恢复24总额、8已用、16可用；未创建个人PAT，未修改既有训练或他人文件。

## 尚未完成与下一步

1. 生产成功链路仍需补验：先提供训练节点可访问的固定源码来源，再单独安排一次专用计算验收。本次单任务授权已执行完毕，不能自动追加任务。
2. 业务模型评估方案尚未登记，当前唯一协议验收方案已停用。任意 pth/safetensors 不能自动运行；需要模型团队提供加载代码、数据适配和指标定义。未声称完成 BEVFusion 精度评估。
3. Portal Pod实际imageID仍因 test-dev API网络不可达而未独立验证。生产“查看数据版本”曾被自动审批拒绝；补充DOM及源码证据确认仅GET和路由跳转后获准执行，成功定位固定数据版本，页面显示 train 15,228 / val 1,620 / test 0，没有提交新任务。
4. 第三阶段模型审批、推理契约、服务创建/就绪、真实请求与回滚仍未实现。IDC连接器、团队节点池、TAS/抢占也不属于本次发布。

回滚边界：schema50是新增表，普通旧训练兼容；活动评估依赖新后端的可信参数渲染，存在活动评估时不得直接回滚到阶段一后端。不得运行破坏性逆向 SQL。
