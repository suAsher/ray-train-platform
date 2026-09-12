# 共享模型目录与权重版本发布验证（2026-09-12）

## 交付范围

本次完成第一阶段：在独立 Portal 的实验中心增加“模型”，与“训练记录 / MLflow API”并列；11 个一级菜单不增加。用户在本人已结束的训练任务产物中选择一个权重，登记为共享模型版本。复制及 SHA-256 校验完成后提供下载。目录与已登记副本对全部平台成员可见，模型所有者及平台管理员可管理；管理权限不允许读取或登记其他人的私人源文件。

支持创建模型、修改名称/说明、版本说明、归档与恢复、全部/本人/归档筛选、不可变来源 Job/Run/代码/镜像/数据 manifest、幂等登记和复制状态。现有原生 MLflow API 与 Registry 权限不变；平台模型目录不会自动注册到 MLflow Registry。

本次没有实现独立评估、审批发布、推理服务、模型推荐别名或对象物理回收；详情中的 READY 只表示权重副本就绪。未填写的历史数据版本仍显示未知，不推断训练来源。具体后续合同见[实施计划](superpowers/plans/2026-09-12-shared-model-lifecycle.md)。

## 版本与生产证据

| 项目 | 证据 |
| --- | --- |
| 后端业务提交 | `77c10a0127b1f3ab4a47bd9f7c9b66351ca81707`；测试通过后四端 main 同步，正式构建目录干净 |
| 后端 tag | `release-20260912-10-77c10a0` |
| 后端 digest | `sha256:4c4fe3d7699c70df9d66a77e92591d633e80f95ac4041d735119dd0b96e01e99` |
| Helm / schema | revision **221** / schema **49** |
| 实际后端 | 两个 Ready Pod，imageID 与上列摘要一致，重启数均 0；`/healthz` 200 |
| Portal dev | `c2f9b48ce2e936e1b3edd6826f16dc2b8c1b8726`；基于 `90d105dd`，推送前确认远端未变化 |
| Portal 实际浏览器资源 | `index-DfCUri3e.js`；已登录生产页出现“训练记录 / 模型 / MLflow API”，模型列表和版本详情正常 |
| Portal CI / Pod imageID | 待独立核实。GitLab 浏览器要求重新单点登录；test-dev Kubernetes API 请求超时。新资源已实际加载不代替 CI / Pod 摘要证据 |

本次只构建 backend，没有重建旧 frontend、CLI、训练/工作区镜像。Helm server dry-run 仅 backend 镜像一行变化。发布前后 4 个活跃 RayJob、4 个 RayCluster、9 个训练 Pod 的完整 UID、状态、容器重启数对比无差异。未更改 local 配额、调度开关、训练资源或用户个人数据归属。

## 迁移备份与验证

schema 48→49 新增模型、版本、审计表，向后兼容。用户明确授权此次完整数据库备份及隔离恢复验证。备份保存在既有构建机的受限发布目录，仅 root 可读；成功恢复到隔离 PostgreSQL，恢复库 schema=48，临时恢复容器随后删除。保留备份、校验和、恢复结果及 Helm values/overlay 供回滚，不把库内容带回本机或提交仓库。

构建机受限证据目录：`/root/raytrain-release-20260912-models`。

- `backup-verify.log`、`backup.sha256`、`restore-verification.txt`、`platform-schema48.dump`。
- `backend-final.log`、`model-core-final.cover`、`backend-build.log`。
- `values-before.yaml`、`backend-overlay.yaml`、`manifest.diff`、`deploy.log`。
- `rayjobs-{before,after}.json`、`rayclusters-{before,after}.json`、`training-pods-{before,after}.json`、`backend-{before,after}.json`、`schema-{before,after}.txt`。
- `portal-lint-final.log`、`portal-build-final.log`、`portal-e2e-final.log` 与浏览器输出目录。

## 候选验证

所有测试、格式检查和编译均在构建机执行。本机仅编辑、审阅和 Git 操作。

- 全量 `go test -timeout=20m -p4 ./...` 通过；模型领域包语句覆盖率 **89.9%**。
- 真实隔离 PostgreSQL：新安装、重复迁移、从 schema48 升级、并发幂等创建、复制租约/CAS/配额约束均通过。
- API 与对象存储：共享读取、所有者/管理员管理、管理员不得登记他人源文件、活动任务拒绝、路径约束、幂等冲突、READY 才可下载、同名同大小源文件替换的 ETag 检测、分片/整体 SHA 校验和复制失败边界通过。
- Portal 官方 `docker/Dockerfile.lint` 门禁（lint / Element Plus / store / RayTrain 合同）与 `pnpm build` 通过。
- Chromium 两份回归 **14 passed**，覆盖模型列表/详情/筛选/编辑/归档、权重登记、READY 数据版本选择、响应中断重试、真实原生下载和既有配额/MLflow/文档。最终运行 `--network none`，下载通过仅环回 HTTP fixture，严格校验下载字节，不访问生产 API。
- 独立规格审阅与代码审阅通过，无剩余 P1/P2 阻断项。

## 生产操作链验收

使用已有 guofeng.su OAuth2 Proxy 会话，未创建 PAT 或新增身份。模型 GET 返回 200。用户另外明确授权在本人已结束的 `fullchain-20260911-03` 任务下新增独立测试文件，完成后归档验收模型并保留记录。

- 来源任务：`job-078243ab019985c842f9f74b`，所有者与当前登录稳定 subject 一致。
- 仅新增 `model-lifecycle-20260912-77c10a0.safetensors`，156 字节，内容为标注验收用途的合成单值张量，不是训练成果。上传前确认同名文件不存在。
- 模型：`6849e5c1-b33b-438f-99f9-831bd568ce39`，名称“平台验收用模型 20260912”。
- 版本：`2cb1bbef-48ee-48a0-88f4-a1ba6bd5a784`，v1；登记 202，后台从 PENDING 到 READY。
- Run 自动关联 `dd5228699de24096b72145f542c9eb2a`，保留原任务代码摘要与运行镜像。原任务没有固定数据版本，页面显示未知。
- 源文件、共享副本和 HTTP 下载均为156字节，SHA-256 均为 `42266484a6afff3107ba1e7560207d24a8ed9146d289d92a41c5858647ae40de`。共享下载响应摘要头一致。
- 相同幂等键重放仍返回原 v1，不新增版本；浏览器实际打开目录、详情、下载按钮并保存版本说明。不存在的模型返回404业务错误，页面显示“模型或版本不存在”和“重新加载”，没有退化成“系统错误”。
- 浏览器归档成功，最终模型 `archived=true`、revision2；v1仍READY、revision4，说明编辑已保存，源文件和共享副本保留。默认目录不再展示验收模型。

此验收没有读取或发布 yihan.she 的私人权重，没有启动训练、删除源数据或对已有 MLflow Run 写入。其他成员共享可见/不可管理和管理员不能发布他人权重已由隔离 API/E2E 测试验证；本次生产会话仅 guofeng.su，没有冒充第二名用户。

## 使用说明与仍待验证项

使用说明保持六分类，独立问题从34篇增加至36篇，新增“如何把训练权重保存成共享模型版本？”及“模型谁能看，如何维护与归档？”。既有问题和兼容接口保留。生产首页已显示6/7/9/6/4/4个问题。

尚待独立核实 Portal CI 与 Pod imageID；本次未进行大体积权重生产压力测试、第二用户生产登录、固定数据版本真实训练后登记、独立评估/审批/服务验收。不可将小文件复制通过扩写为完整模型生命周期已经上线。
