# 调试环境保存为训练镜像

日期：2026-09-21。**候选已实现并通过部分验证，尚未上线；完整集群发布和 GPU 训练验收待完成。**

## 用户操作

1. 保留现有 Base 训练镜像和默认选项，新增可选的 Base 配套调试镜像。
2. 用户启动本人工作区，在 `/opt/raytrain/environment` 中安装、调试依赖。新增镜像默认使用 `https://mirrors.ivolces.com/pypi/simple/`；不修改节点的 pip、APT 或 Docker 配置。
3. 点击“保存训练环境”，填写名称、说明、本人/当前团队可用范围。
4. 使用自己的 Harbor 用户名和个人 CLI Secret 授权，选择有 push 权限的现有项目及仓库。公司 Harbor 使用 OIDC，CLI Secret 不是公司 SSO 密码或平台 PAT。
5. 平台在集群运行临时 CPU 构建任务，用固定版本依赖重建训练环境，推送到用户选择的 Harbor 仓库。
6. 实际拉取检查通过后显示 READY，用户点击“使用此环境创建训练任务”，继续提交源码、数据和训练资源。

平台范围与 Harbor 项目可见性是独立权限。普通用户不能因为拉取机器人具有全项目权限而使用未获平台授权的个人镜像。不会自动创建 Harbor 项目、替换旧镜像或启动训练。

## 当前范围与明确限制

- 保存受管 Python 环境中可由固定包源取得的 wheel 依赖；同时固定实际文件指纹、wheel SHA256、基底和最终镜像摘要。
- 原 Base 的 Python、Ray、PyTorch、CUDA 和已装基础包不能通过此入口替换；其他核心环境需要准备并登记匹配基底。
- 不支持任意容器 rootfs commit、APT/Conda 系统变更、editable 或未登记文件。训练源码、数据、权重、工作区及凭据不进入镜像。
- 离线 wheel 可用于调试，但保存时仍要求固定源能提供内容一致的 wheel。**私有或仅本地 wheel 的上传与材料托管尚未实现**。
- 调试依赖位于本次 Pod 可写层，停止/重建前先保存到 READY；工作区文件快照不能代替环境版本。
- 捕获在排队获得执行名额后进行；排队和捕获时不要继续安装依赖。前后双重比对检测变化，重试使用已冻结材料。
- READY 表示 CPU、完整性与实际拉取检查通过，不等于用户业务 GPU 训练通过。多节点验证单独记录。

## 已选实现

Rootless BuildKit 在现有标准安全策略下实测因内核权限限制失败。因此采用“受管依赖层 + 可信 OCI 组装”，不放宽 seccomp、不使用特权、不挂宿主容器 socket，也不访问调试容器的整个根文件系统。

| 阶段 | 执行与凭据边界 |
| --- | --- |
| QUEUED / CAPTURING | 校验工作区 owner、实际镜像摘要、Pod UID/RayCluster 归属；运行固定绝对路径捕获程序，读取有界依赖 manifest |
| BUILDING | 固定 Prepare 镜像下载准确 wheel，离线安装、校验指纹和 CPU 兼容性，导出仅允许目录的依赖层；不挂 Harbor 凭据 |
| VALIDATING | 可信 assembler 独立 Pod 读取拉取机器人，拉取固定 Base、检查 tar 和层摘要、生成 OCI；不执行或解包用户 tar |
| PUSHING | 可信 publisher 独立 Pod 才挂用户短期推送凭据；限定固定 Harbor、项目、repository、tag、artifact digest |
| VERIFYING_PULL | 用现有 imagePullSecrets 拉取新 digest；无凭据文件卷，执行固定绝对路径校验 |
| READY | 事务登记环境版本和个人/团队镜像目录，收尾本次 Job、PVC 与凭据副本 |

辅助状态包括 FAILED、AWAITING_AUTH、CANCEL_REQUESTED、CANCELED。完成取消必须确认本次 Pod 已停止；已推到 Harbor 的镜像不会自动删除。

每个构建请求 1 CPU/2 GiB、上限 4 CPU/8 GiB、0 GPU、30 分钟超时；每操作独立 60 GiB PVC。执行并发全局 2、本人 1；暂存操作上限全局 8、本人 3；授权材料上限全局 50、本人 5。失败材料最长保留 24 小时。仅使用 production 共享池，不占用团队专属节点，也不调整 GPU 配额。

用户构建由集群承载；平台源码测试和平台辅助镜像构建仍在既有运维构建机。这是两类不同工作。

## 认证与数据

- 交互会话才能管理发布授权，不使用 PAT 或演示身份绕过交互权限。
- 校验 Harbor 返回的实际 repository push grant；token HTTP 200 或能看到项目不等于可写。
- 固定 HTTPS Harbor origin，验证 challenge、重定向及上传地址，防止凭据被送到其他主机。
- 凭据按 owner、团队、账号、目标、操作和到期时间用独立用途 AES-GCM 加密；数据库保存引用和元数据，Secret 材料登记在持久账本中供故障后清理。
- 推送短期明文 Secret 仅供 publisher；依赖构建/用户工作区不挂此 Secret。
- 终态先确认工作负载已停止，再删除凭据/PVC；不确定数据库写入结果时回读确认，未知状态由有界 TTL 清理。
- 清除平台副本不等于撤销 Harbor 原 CLI Secret，不宣称擦除历史备份。

新增迁移 0055 为授权材料、构建操作与环境版本；0056 为镜像 owner/visibility。旧目录保持原团队/全局可见范围和默认选项，已有任务引用不变。个人目录创建后不能直接回滚到不认识该 ACL 的旧后端；先禁用新构建并按兼容方案处理，Helm 回滚也不会回退 schema。

## 后端接口

全部位于 `/api/v1`：

| 路由 | 用途 |
| --- | --- |
| GET /environment-build-capabilities | 功能开关与支持的基底/调试镜像 |
| POST /registry-authorizations | 创建本人短期授权 |
| GET /registry-authorizations/:id/projects | 查询项目 |
| POST /registry-authorizations/:id/check-target | 校验准确目标 push 权限 |
| DELETE /registry-authorizations/:id | 撤销平台授权副本 |
| POST /workspaces/:id/environment-builds | 本人工作区保存操作 |
| GET /environment-builds；GET /environment-builds/:id | 本人进度、固定错误提示和校验结果 |
| POST /environment-builds/:id/retry | 同一冻结材料重试，可提供新 authorizationId |
| POST /environment-builds/:id/cancel | 停止本次构建并收尾 |
| GET /environment-versions | 本人及获准团队版本 |

请求限时、体积与字段校验、owner/团队鉴权和速率限制均在后端。API/UI 显示脱敏阶段和固定错误，不开放任意原始 Pod 日志。

## 发布与验收状态

- 基线四端源码：`7288e52650bad1adfd90493ae255784deb2a534d`；本轮尚未推送 main。
- 当前生产仍为 backend `release-20260920-03-7288e52`、Helm 245、schema 54。
- 后端候选 `b5c98c9` 与构建机实际格式化测试树逐字一致；完整 Go vet/Go 回归及真实隔离 PostgreSQL 通过。补齐授权失效、清理重试和 OCI 组装等用例后，跨包聚合语句覆盖率为 83.29%（environmentbuild 84.67%、registryauth 82.03%）。
- 运行时候选 `1763b7c` 27 项 Python 测试、两个 Dockerfile 构建、断网 capture/verify、编辑器及 CPU Ray Actor 通过。实际 22 包捕获→火山源下载→哈希锁定离线重建→内容/CPU 重验→导出层通过；可信 OCI assembler 从真实固定 Base 组装通过，未发布用户镜像。
- 验收层 98,058,240 字节，SHA256 `1fec9533197864926fe9ad3c4c29dc3a13f5fa1c9bf229bed7622b5f8209b5b8`；OCI 摘要 `sha256:4adf14ab2b86ee96637c158aae40bf0e9436895c3cba62cf531d7512f5a94791`。CPU 检查不能代替 GPU 验收。
- 固定 Base 的 Conda 元数据存在未打包条目；将“缺失状态”纳入不可变基底指纹，新增 wheel 仍严格拒绝缺文件。
- 火山索引在集群 Pod 可达。jupyter_server 2.17.0 的镜像文件与索引/官方哈希不一致，保持拒绝；固定 2.14.2 已核对哈希和 ZIP 并构建通过。
- Portal 候选 `4894636bc2c1cb7e09e8413c831f929bd352e0d9` 已非破坏性合并同事最新 dev `ff6a99eb`，精确归档、完整 lint、合同和 dev build 再次通过，尚未推送 dev。
- 平台使用说明的“自定义环境”文章已加入操作、CLI Secret、失败重试和边界，随后端发布生效；未上线前用户还看不到新内容。
- 完整真实闭环、用户凭据推送、机器人拉取、目录使用、单卡训练和 UI 验收仍待完成。验收目标为用户授权的 `guofeng.su` 身份、`public` 项目专用仓库。
- schema 54→56 备份脚本已准备；生产备份需本次明确授权。上线前审查完整 Helm server-side diff，并记录存量任务 UID/重启数连续性。
- 构建机证据：`/tmp/rtp-env-verify5-evidence-20260921/`、`/tmp/rtp-env-verify5-coverage-20260921.log`、`/tmp/rtp-env-layer-evidence-1763b7c/`、`/tmp/rtp-env-assembler-acceptance-20260921.log`、`/tmp/rtp-portal-env-{lint,dev}-20260921.log`。这些是候选验证，不是生产交付凭据。

源码与日志中的敏感值不得进入本记录。最终发布后在本节补齐最终 SHA、组件 digest、Helm/schema、真实任务及清理证据；不得将候选通过当作上线验收。
