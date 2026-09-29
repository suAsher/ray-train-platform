# 训练故障诊断与命令补全

本页描述候选代码的使用合同。是否已在生产启用，必须分别核对后端、CLI 下载服务、Portal 和训练运行时镜像；源码合入不能代替上线或真实 GPU 验收。

## 统一诊断

```sh
spk-rayjob diagnose job-xxxxxxxxxxxxxxxxxxxxxxxx
spk-rayjob diagnose --output json job-xxxxxxxxxxxxxxxxxxxxxxxx
```

连接参数与其他任务命令一致，参数放在任务 ID 前。诊断与 Portal 任务详情中的诊断面板共同读取 `GET /api/v1/jobs/:id/diagnosis`，沿用 `jobs:read` 和任务可见性授权。原有 `status` 与 `logs` 行为保持兼容。

诊断展示保留日志中识别到的最早异常、发生时间、Pod/容器、附近上下文和后续通信异常。查询在服务端按固定规则筛选并限制数量、上下文和耗时，不把整份训练日志下载到控制面。`coverage` 说明样本、截断和不可用情况；未识别到异常不代表任务健康，也不保证 Loki 仍保留全部历史。

先出现 NaN、OOM 或 Python 异常，之后出现 NCCL peer closed/timeout 时，两者会分别展示。仅看到 NCCL 错误不能认定训练代码或平台是根因。日志时间是收集记录的时间，跨节点缓冲和时钟偏差也可能影响观察顺序。

“训练完成”或 checkpoint 路径只是用户程序输出的线索，不证明模型正确或文件可加载。完成提示后又出现 fatal signal 时，可以提示“可能在启动器退出阶段失败”，但任务的权威状态仍然由现有控制面维护，诊断不会把 `FAILED` 改成 `SUCCEEDED`。阶段和原因分类都应结合原始日志核查。

Portal 诊断由用户点击查询或刷新，不加入每五秒任务轮询。日志服务不可用、旧后端尚无此 API、证据被截断时显示明确说明，原始日志入口仍可使用。

## 运行时收尾范围

候选 launcher 监督自己创建的训练进程。确认某个 Worker 非零退出或启动失败后，保留先观察到的失败码，通知其他 Worker 收尾，并在有限宽限期后强制终止仍存活的受管后代。只处理属于本次监督进程的后代，不使用全节点或全 Pod 的进程匹配清理。

平台不会根据日志中的 `Error`、GPU 利用率或长时间没有进度自动杀任务。若用户 shell 包装脚本一直不退出、也没有向平台返回失败，这一版不能仅凭日志可靠决定其已经失败。容器丢失、监督进程被强制杀死等场景仍需 Kubernetes/Ray 生命周期负责清理。

运行时修复需重建包含 `raytrain-launch` 的相应镜像并让新任务使用新摘要。仅更新后端不能修复旧镜像中已经运行的训练；不重启既有任务、不改写用户源码，也不重新登记未变更的调试镜像。

## Bash / Zsh 补全

先更新到包含补全功能的 CLI。以下命令只向当前 shell 加载补全，是否写入 shell 启动文件由使用者选择。

```bash
# Bash
source <(spk-rayjob completion bash)
```

```zsh
# Zsh（尚未初始化补全时先执行前两句）
autoload -Uz compinit
compinit
source <(spk-rayjob completion zsh)
```

支持命令、子命令、参数名、枚举值、文件路径和当前身份可见的近期任务 ID。Zsh 还可展示任务名称与状态。普通任务命令的参数应放在任务 ID 前，例如 `spk-rayjob logs --follow <TAB>`。

任务补全使用当前连接配置和身份，短超时、失败静默，不持久缓存任务列表，不输出令牌，也不执行提交/停止等写操作。未登录或网络不可达时，静态参数和本地路径仍能补全。

## 交付验证

候选测试在构建机隔离工作区执行，包括共享 API 授权、日志筛选/脱敏/边界、CLI 文本与 JSON、实际 Bash/Zsh 适配、合成 Linux 进程树与 Ray 编排。Portal 使用自己的 `dev` 候选与质量门禁。测试结果、源码 SHA 和尚未完成的生产验收在当次交付记录中分别记录。


### 2026-09-29 候选交付记录

- 后端、CLI、运行时最终受测代码：`a75483df1882fa8241ec7663850587867fda7f12`，分支 `codex/training-diagnostics`。之后的交付记录提交只改文档。
- Portal 最终受测代码：`7bad129025b9f57050cab8d14d94286ce4161833`，同名候选分支，基于当时远端 `dev` 的 `2410eea74c0c895f1aa9c43bd73ed9890b761cc7`。
- 后端候选目录：`/Users/ashersu/.codex/worktrees/training-diagnostics/ray-train-platform`。
- Portal 候选目录：`/Users/ashersu/Desktop/西井/wellspiking-frontend-diagnostics-20260929`。原 Portal checkout 保留。
- 未推送任何分支、未发布业务镜像、未部署、未重新登记调试镜像。原后端 checkout 与正式构建目录仍为 `4a80b6ce5871a643b3123d26ca4bc670ba813d5b`；原 checkout 的未提交交接文档及 `output/environment-acceptance-20260921/source.zip` 保留。

构建机证据目录为 `/tmp/rtp-diagnostics-evidence-20260929`。以下均为本次隔离测试结果，不是线上检查：

| 检查 | 结果与证据 |
| --- | --- |
| Go 格式与静态检查 | 最终代码 `gofmt` 检查、`go vet ./...` 通过；`final-go-postgres-a75483d.log` |
| Go 完整回归 | 临时 PostgreSQL 16 实例下 `go test -p 1 -timeout=20m -count=1 -coverprofile=/coverage/go.out ./...` 通过；`final-go-postgres-serial-a75483d.log` |
| 新增 Go 模块覆盖率 | 458/529 条语句，86.6%；全仓存量加新增合计 74.5%，未达到全仓 80% 目标，不将新增覆盖率当成全仓覆盖率 |
| Python 进程与调度合同 | 20 项测试通过；`runtime-green2.log`；受测运行时代码与最终候选相同 |
| 真实 Ray 单 Worker | 专用 CPU 容器中保留原退出码 23；`ray-cpu-integration.log` |
| 真实 Ray 多 Worker | 单容器内 head 加两个逻辑 Ray 节点，使用 CPU 和逻辑 GPU 资源；一个 torchrun Worker 失败后约 8.87 秒返回失败码 1，忽略 TERM 的另一 Worker 被清理；`ray-distributed-integration.log` |
| Portal 门禁 | 最终 Portal 的 `docker/Dockerfile.lint` 全部既有门禁及新增诊断合同通过，包含 Vue SSR 转义、请求过期结果和错误处理检查；`portal-7bad1290-lint.log` |
| Portal 构建 | `pnpm build` 通过；`portal-7bad1290-build.log`。仍有模块类型与 CSS 压缩警告，未作为新诊断功能的修复范围 |
| 独立审阅 | 修复大量 checkpoint 输出挤掉完成证据、部分查询失败丢弃已取得证据两项问题；修复前测试失败，修复后回归通过，复审无剩余具体问题 |

首轮带覆盖率的并行全套测试遇到已有 `TestPostgresAdvisoryLockMutualExclusionAndRelease` 失败：多个测试包使用同一临时数据库，迁移共用数据库级 advisory lock，互斥测试首次 try-lock 会受其他包迁移影响。保持业务代码和该测试不变，改用 `-p 1` 隔离跨包竞争后全套通过；原失败日志保留。临时 PostgreSQL 容器与网络已清理。

本次未做真实多物理节点 GPU 训练、生产登录浏览器验收或历史用户任务重跑。隔离 Ray 集成只能证明该测试条件下的退出码与清理行为，不能当作生产 GPU 验收。后续如发布，应分别更新后端与 `spk-rayjob` 下载服务、Portal，以及需要此次 launcher 修复的运行时镜像，并单独记录发布摘要与真实验收；发布前需再次核对远端基线。
