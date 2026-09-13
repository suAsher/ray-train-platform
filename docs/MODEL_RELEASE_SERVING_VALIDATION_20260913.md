# 模型审批、Registry 与推理服务发布记录

## 状态（持续验收中）

后端与 Portal 已部署；独立评估成功报告、生产非自审、实际 Worker 推理和停止/重建尚未完成，不能报告整个阶段验收完成。

| 项目 | 已验证事实 |
|---|---|
| 后端业务代码 | 镜像发布时本地、GitHub、内部 GitLab、正式构建目录均为 `0ad8dbad268173f2e53855ff3e2b7e655f203299`；本文随后以仅文档提交同步四端，不重建镜像 |
| 部署配置代码 | `b8567f41763c1f3166edfa9efce8fd025c489446`：仅增加代码快照临时卷；无镜像重建或数据库迁移 |
| 后端版本 | `release-20260913-02-0ad8dba` |
| 后端摘要 | `sha256:d45f8caf881f0dff5abeb226aac3f19f4d21e48f982b00ea7c5c5ba1facc52f9` |
| 源码分发摘要 | `sha256:2623c825f23b810f2e0e3df724a13cdcf5ef9aad6c95af7b1cfafe259c47d348` |
| Helm / schema | 226 / 52 |
| Portal dev | `525de83b118270d66392f204662a704c584583b3`；[流水线 33893](https://gitlab.wellspiking.ai/wellspiking/frontend/wellspiking-frontend/-/pipelines/33893) 全部成功 |
| Portal 部署 | 作业 89862，Helm revision 1052；登录页面加载 `index-DNXMIjj-.js` |
| 副本 | 两个后端 Pod Ready，重启 0，实际 imageID 与后端候选摘要相同 |
| 存量训练保护 | 4 个活跃 RayJob、4 个 RayCluster、10 个训练 Pod 的 UID、状态、重启数在发布前后逐项相同 |

首发 revision 224 仅构建 backend 和 source-materializer；随后 revision 225 的 Registry 跳转修复仅重建 backend。没有构建训练镜像、修改 local 的 24 卡配额、改变调度或迁移个人数据。首发 Helm server dry-run 只有 backend image 和 SOURCE_MATERIALIZER_IMAGE 两行变化；跳转修复仅 backend image 一行变化。

## 验证与修复

- 构建机最终后端候选全量 `go test -count=1 -timeout=20m -p 1 ./...` 通过，实际连接内部 Docker 网络中的 PostgreSQL 与生产同版本 MLflow。包级并发会争用既有全局迁移锁测试，改为串行后保留全部断言通过。
- schema 新装、升级、重复迁移、不可变快照、事务并发及幂等由真实 PostgreSQL 验证；没有把跳过结果当通过。
- 21 项源码分发 Python 测试、6 项 Serving SDK 测试通过。
- 独立审阅修复了提交失败预留槽泄漏、任务 ID 约束、共享发布状态可见性、健康检查忽略 ready/protocol 等问题。
- Portal 额外修复普通用户误请求管理员停用方案、服务任务错误显示训练续训/重提操作的问题；最终候选 `525de83b118270d66392f204662a704c584583b3` 的官方 lint、生产编译及 38 项浏览器回归（7 项服务、27 项评估/模型/实验、4 项上传）均通过，已推 dev，流水线 33893 全部成功。

登录浏览器实际打开 `/raytrain/rayTrain/experiments?tab=services`：保留原 11 个一级菜单，实验中心显示训练记录、模型、评估、推理服务、MLflow API 五个页签；实际加载服务列表并明确尚无实例，不把空页面当作已完成推理验收。

构建机测试日志：`rtp-serving-go-final.log`、`rtp-serving-python.log`、`rtp-serving-portal-lint-final.log`、`rtp-serving-portal-e2e-final.log`、`rtp-serving-portal-build-final.log`，已保存到受限发布目录 `/root/raytrain-release-20260913-serving`；隔离测试 PostgreSQL/MLflow 容器及内部 Docker 网络已删除。

生产 `/help/articles` 返回 43 篇，新增五篇问题文档均有完整正文；原有问题和已发布自定义正文保留。Portal Kubernetes 管理 API `test-k8s.westwell-research.com:6443` 本次仍超时，暂未独立读取前端 Pod imageID，不能以 CI 或页面证据替代。

## Registry Run 跳转修复

浏览器点击“查看同步 Run”时发现旧跳转仅查训练任务，专用 Registry Run 被错误报告为不存在。`0ad8dba` 增加持久化 READY 关联查询，允许交互式成员查看共享模型 Run；严格校验源 URI 与 Run ID，保留原训练 Run 权限以及 PAT/integration 边界。

新增测试先在构建机验证 RED，再进行针对性回归、独立权限审阅及候选全量 Go + 真实 PostgreSQL/MLflow 测试，全部通过。日志 `registry-hotfix-tests.log`、构建、dry-run、发布及训练前后快照保存在同一受限发布目录。两个新后端副本 Ready / 重启 0 / imageID 等于上表摘要，healthz 200，schema 52，4/4/10 个既有训练资源逐项未变。

登录浏览器实际点击“查看同步 Run”，成功打开实验 11、Run `5d1e6349585b4d048bd04adf4dea059d`，页面显示 Finished、对应 Registered Model v1；不是仅凭 API 200 判断。

## 生产 Registry 证据

只操作既有本人合成验收模型 `6849e5c1-b33b-438f-99f9-831bd568ce39` 的版本 `2cb1bbef-48ee-48a0-88f4-a1ba6bd5a784`，未修改他人模型或既有训练 Run。

- 同步 POST 返回 202，随后 GET 返回 READY。
- 原生 Registered Model：`raytrain-model-6849e5c1-b33b-438f-99f9-831bd568ce39`，版本 `1`。
- 专用复制 Run：`5d1e6349585b4d048bd04adf4dea059d`；原训练 Run `dd5228699de24096b72145f542c9eb2a` 仍保留为来源。
- 实际 Artifact URI：`mlflow-artifacts:/11/5d1e6349585b4d048bd04adf4dea059d/artifacts/checkpoint/42266484a6afff3107ba1e7560207d24a8ed9146d289d92a41c5858647ae40de.safetensors`。
- 内网读回 SHA-256 为 `42266484a6afff3107ba1e7560207d24a8ed9146d289d92a41c5858647ae40de`，与既有 156 字节合成权重相同。
- 再次同步返回 200、相同版本及 Run；没有重复注册。
- 模型恢复归档，revision 6；复制和登记记录保留。

浏览器直取 `/raytrain/mlflow/...` 未持有 MLflow Dashboard 会话而返回 401，不把它当作下载成功；读回验证通过后端 Pod 的既有内网访问完成，仅输出合成文件摘要。

## 备份和授权边界

用户已单独授权本次 schema 50→52 全库备份及隔离恢复。构建机 `/root/raytrain-release-20260913-serving` 权限 700，备份文件 600。备份恢复到 `--network none` 的临时 PostgreSQL，schema 50 与表结构验证成功，随后恢复容器删除；备份及校验和保留，不带回本机或提交仓库。

用户另授权一个临时 local 审核身份、一枚本人 1 天 `models:invoke` PAT、最多顺序三次单 Worker / 1 GPU / 4 CPU / 16 Gi / 1 小时上限的专用推理验收（同时最多一个），结束立即撤销身份和令牌、停止实例、停用方案并归档模型。当前尚未创建这些身份、令牌和实例。

用户已在浏览器手动选择 `rtp-internal-evaluation-smoke.zip`，实际上传和不可变评估方案登记均成功，原文件选择阻塞已解除。推理协议示例包已准备在本机 `/tmp/rtp-serving-protocol-smoke-20260913.zip`，SHA-256 `53e2457455a9ad93e8a91bf0762669b9c1b4d44b93f90cd842dcb577f78672ae`；只验证服务协议，不代表业务模型精度。

## 内网代码快照生产故障与修复

用户手动选包后，实际上传成功（4209 字节 / `4236b519a6044acfdc14fe0fdef54bf898e68e3c164bf4f45eae519531f41e3e`），但创建评估方案返回 503。实机定位为只读根文件系统下 `os.CreateTemp` 默认写 `/tmp` 失败。普通上传使用显式独立目录，所以此前上传与单元测试没有覆盖这项部署缺失。该路径也供推理代码快照使用。

`b8567f4` 为后端增加独立 1Gi `code-snapshot-spool` emptyDir、`TMPDIR=/var/lib/ray-platform/code-snapshots` 和 `fsGroup:65532`，保留只读根、非 root、原 3Gi 上传卷。没有改训练卷、调度、配额、镜像或 schema。新增合同测试在构建机先 RED 后 GREEN；全量 Go 回归通过（本次无数据库变更，未重建 PostgreSQL 测试环境），Helm 使用备份生产 values lint/render 通过，独立审阅无阻断。默认 values 的占位域名导致首次 render 拒绝，不算通过；改用生产 values 后才继续发布。

候选四端同步后，server dry-run 精确为上述 8 行新增配置；revision226 发布成功。两个后端 Ready / 重启0 / 原 imageID，healthz200/schema52。发布前后4个活跃RayJob、4个RayCluster、10个训练Pod的UID/状态/重启数逐项未变。实机临时目录可写，方案成功创建后临时文件数为0。

浏览器使用原幂等请求重试“创建方案”成功，方案 `1c5c2c99e0d4ba4106e36637ac4d89b4` 的代码SHA与上传包一致、Git字段为空、入口为 `python smoke_evaluator.py`。真实 preflight200 固定本人156字节权重、公有数据版本 `version-a26436a221f5f4b851863a3bde884d26` / val / 1620来源样本，未读取样本。浏览器“发起评估”按钮能打开完整表单。

**当前阻塞变为GPU配额**：2026-09-13 12:30前后实时 quota 为 local limit24/used24/available0，页面明确显示可用0卡并禁用提交。没有创建评估任务或推理实例，也未签发临时身份/PAT；没有改动配额或停止他人任务。暂停验收时方案已停用（revision2），本人验收模型重新归档（revision8），源码快照与审计保留。后续至少释放1卡后，在既有授权范围恢复方案和验收模型，继续评估、非自审与推理链路。

新增证据位于构建机受限发布目录：`code-spool-tests.log`、`code-spool-helm.log`、`code-spool-rendered.yaml`、`code-spool-manifest.diff`、`code-spool-deploy.log` 及前后资源快照。

## 剩余验收

- [x] Portal 最终候选门禁、CI、线上资产，以及实验中心五个页签与 Registry Run 跳转浏览器验收。
- [ ] Portal Pod imageID 独立核验（集群管理 API 不可达）；生产评估/推理页面操作验收见下项。
- [ ] 内网 ZIP 独立评估成功且报告 VALID。
- [ ] 本人不能自审，临时授权审核者完成审批，发布与历史回滚有真实记录。
- [ ] 实际 Worker 就绪、PAT 调用得到真实 HTTP 结果，停止后计算资源回收；历史版本重新部署。
- [ ] 全部临时资源及凭据按授权收尾，记录最终版本和结果。

已有隔离测试与 Registry 生产证据不能替代上述剩余环节。存在活动推理/评估任务时，不能直接回滚到不支持这些任务来源的旧后端；先按授权停止本次任务并确认回收。schema 51/52 为新增表，不执行破坏性逆向 SQL。
