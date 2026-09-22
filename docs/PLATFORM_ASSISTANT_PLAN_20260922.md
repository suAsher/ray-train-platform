# RayTrain 页面助手与多模型路由

更新日期：2026-09-22。助手质量与管理员可见性后端已发布为 **Helm 254**，schema 保持 **56**；源码 `9fcbc2384845ff6938f0129c4b87fc35e52148b5`，镜像 `sha256:3cda7396cb34a8c47fac3919efe131727f616081909b9449e0a34bb2dd7be07d`，2 副本 Ready、零重启。发布前后 6 个训练 Pod 的 UID、节点、重启数和 Ready 均无回退。模型配置仍为 `providers: []`，GPU 与外部 API 均未在共享生产启用。完整网络隔离、训练让卡与本轮真实页面验收仍须分别记录，不以代码或构建通过代替。

## 本轮修正与网络变更窗口

用户反馈：文档检索答非所问、提示过多，闲时推理运行情况不可见。本轮修正检索相关性和完整步骤，增加按问题查询的授权任务/配额事实；模型回答必须直接给出与证据相符的操作，缺少事实时追问，不把片段匹配称为诊断。用户侧使用动态悬浮机器人，来源与模式设置折叠；超级管理员通过「平台管理 → 助手与闲时推理」进入 GPU 占用明细中的实际运行状态卡片。

用户已经确认可安排集群网络变更窗口，要求先提供实施与回滚步骤。具体清单见 [idle GPU 网络变更实施与回滚](PLATFORM_ASSISTANT_NETWORK_CHANGE_20260922.md)。这项确认不表示 CNI 已修改或 GPU 已开启；仍须由具有 VKE 权限的管理员核对受支持开关与维护窗口，完成 MLflow/现有训练流量保护、策略正反例及让卡验收后再启用共享推理。个人 DeepSeek Key 仍仅用于临时验证，不能自动成为所有用户共享的生产 API 配置。

本轮后端证据位于构建机 `/root/raytrain-assistant-validation-20260922`：`quality-final-validated-sha.txt`、`quality-final-backend-tests.jsonl`、`quality-final-vet.log`、`quality-final-race.log`、`release-quality/`。完整 Go 回归 4,396 项通过，34 包通过；真实 PostgreSQL 已配置并通过，外部 MLflow/Ray 专项服务器测试仍按既有条件跳过，不计为本轮现场验收。检索覆盖 43 个已发布问题标题和 12 类自然问法，补齐完整命令章节同分选择与任务失败说明日志授权测试。

真实 DeepSeek 临时测试使用公开/合成证据，经 Go Router 验证相对路径、GPU 配额、Job/Run ID、缺少报错、CUDA OOM 与日志注入五项。第一轮存在过度含糊，第二轮有不必要的资源计算错误，均保留原始记录；最终 `public-quality-deepseek-v3.jsonl` 五项人工核对通过。这仅证明该有限样本集，不承诺任意问题正确，也未把个人测试 Key 配置到共享平台。

新控制器状态接口镜像已构建：`harbor.wellspiking.ai/guofeng.su/raytrain-assistant-idle-controller@sha256:669fd32267ca7773973245e2bca164f1abb9eb408789996c0b83e13bec51d10f`，证据 `quality-controller-build.log`、`quality-controller-image.txt`。尚未替换现有零副本 Deployment；网络窗口时需使用已验证版本并检查后续变更，不能直接照旧摘要启动。下方早期 GPU 记录保留其对应版本和时间点。

### 本轮 Portal dev 发布

本次 Portal 候选 `c0ad297fe9ec2ab662e23a1fdfd2bbac32b6224d` 已推 dev，构建机 `Dockerfile.lint`、dev build、9 项 mock 浏览器回归通过。真实帮助引用采用 `#article/cli-onboarding-v2` 等后端合法单段 ID；错误的 `#article/job/list` 测试样例已修正并保持拒绝，不能因假样例放宽实现。证据为 `portal-c0ad297fe9ec-lint.log`、`portal-c0ad297fe9ec-dev-build.log`、`portal-c0ad297fe9ec-mock-e2e-9case.log`。

Portal dev 实际 Deployment 为 test-dev 集群 `yuanzhu-he/yuanzhu-he-wellspiking-frontend-master-auto-deploy`（名称含 master，但实际为 dev 发布目标）；已更新至本次 c0ad297f 镜像，Pod Ready、零重启，imageID `sha256:1063599816fef6183150264d8c77095675e3af6698c1162f8e72782cb7b66294`。线上入口 HTTP 200，入口资源 `index-BFIStTvx.js` 引用 `FloatingAssistant-BVyshD3k.js`，实际资源包含新版机器人文案、折叠来源和“文档参考 · 当前未使用模型回答”。这证明代码已部署，不代替交互登录后的按钮验收。

随后远端 dev 出现其他维护者的 `0b5d430a81608387ef6cd9f8eb0651efeb744c7f`，只涉及审批模块且包含本次助手候选；未覆盖该更新，未推 Portal master。本次流水线页面的最终状态未通过工具独立读取，实际镜像与 Deployment 已核验。Chrome 工具目前窗口标题与 AX 树不一致、截图不可用，发布后真实提问、引用点击、管理员状态卡片仍待浏览器恢复后补验。

## 一、设计与边界

### 页面与数据范围

- RayTrain 页面悬浮入口可拖拽、键盘移动、关闭和恢复，不新增菜单。用户说明补在现有「各个菜单分别能做什么？」文章内。
- 回答基于已发布使用说明；任务详情可明确附带该任务，复用现有团队和角色权限。按问题需要查询当前团队 GPU 配额、任务资源提交配置、逻辑路径和已授权任务的 MLflow Run 关联；不读取 MLflow 参数、标签、产物或源码。
- 日志默认不读取；可能夹带 Ray 日志尾部的自由文本 StatusMessage 同样需要本次日志授权。每次提问单独勾选后，最多取 30 行、每行 240 字、总节选 2000 字；常见凭据模式脱敏不是完整 DLP。
- 每次独立提问，不发送之前对话，不在服务端保存聊天内容。切换身份、团队或任务时清空或确认上下文；取消后丢弃迟到响应。
- 回答为纯文本，显示来源、文档版本、查询时间、实际模式和降级提示；可点击链接由服务端生成并经前端校验。
- 助手只读：不能提交、停止或修改训练，不能执行命令、查看个人源码或索引用户文件。流式输出、多轮记忆、写操作工具和跨副本月预算不在本次实现内。

### 接口与模型路由

交互登录接口为 `GET /api/v1/assistant/capabilities`、`POST /api/v1/assistant/query`。提问仅接受 `question`、`mode`、`jobId`、`includeLogs`；拒绝重复及未知 JSON 字段，限制 16 KiB 和 4000 字。沿用平台会话权限，不向浏览器下发模型凭据。

支持 OpenAI-compatible Chat Completions 与 Anthropic 原生 Messages；最多配置 4 个固定后端，每个有独立 ID、模型、地址、凭据引用及 API/本地类型。`protocol` 默认 `openai`；`anthropic` 使用独立请求编码与认证头。其他供应商的原生协议需要新增适配器，不能宣称通用支持所有协议。

| 模式 | 行为 |
| --- | --- |
| auto | 按配置优先尝试 API 或本地，再尝试另一类，最终退到文档检索 |
| api | 只尝试配置的 API 模型，失败退文档 |
| local | 只尝试配置的本地服务，失败退文档；不自动申请 GPU |
| docs | 不调用模型，只返回匹配说明和授权任务信息 |

地址与模型由管理员配置，用户不能指定 URL 或工具。外部地址要求 HTTPS；集群内 `.svc.cluster.local` 本地服务可用 HTTP。禁止重定向和继承环境代理。请求体读取上限 5 秒，每次模型尝试最多 12 秒，总请求预算 35 秒；响应上限 128 KiB，输出上限 1500 token。

区分额度耗尽、认证失败、普通 429 和服务不可用。每后端失败后 5 分钟的进程内冷却只用于抑制重试，**不是月预算账本**。真正额度硬限制交给供应商或 LiteLLM；没有权威预算接口就不显示剩余额度。`auto` 中所有目标必须事先获准接收同一范围的数据。

页面开关 `ASSISTANT_ENABLED` 默认 false：query 路由不注册，capabilities 返回关闭状态，Portal 隐藏组件。启用后每进程最多 8 并发、每用户 2 并发、每用户每分钟 8 问。审计复用 `audit_logs`，只记录身份、请求 ID、模式和日志同意，不记录问题与内容；审计失败退文档。没有模型时可配置 `enabled: true`、`providers: []` 使用检索模式。

### 后续接入配置示例（当前未启用）

```yaml
assistant:
  enabled: true
  localFirst: false
  providers:
    - id: company
      kind: api
      protocol: openai
      baseURL: https://litellm.westwell-lab.com/v1
      model: <管理员确认的模型名>
      existingSecret: raytrain-assistant-company
      secretKey: api-key
    - id: claude
      kind: api
      protocol: anthropic
      baseURL: https://api.anthropic.com
      model: <该账户实际可用的模型ID>
      existingSecret: raytrain-assistant-anthropic
      secretKey: api-key
    - id: idle-serve
      kind: local
      baseURL: http://assistant-idle-inference.raytrain-assistant-canary.svc.cluster.local:8000/v1
      model: Qwen3-8B-AWQ
      thinkingDisabled: true
```

只引用受限 Secret，不把密钥写进 values、代码、镜像或文档。上例不会创建 Secret、RayService 或 GPU，也不构成开放模型调用的授权；当前实际配置仍为 `providers: []`。个人 DeepSeek key 仅获准临时测试，不用于共享生产后端。早期 `be9e28b` 文档记载一次公开问题 HTTP 200、21 token，但当前会话未持有其原始请求日志或凭据引用；不能替代生产 Router 联调。LiteLLM 无可用额度，Anthropic 尚无真实凭据，均仅协议合同验证。

### 闲时 GPU 策略

采用独立 RayService/RayCluster。Ray Serve 负责推理；确定性控制器负责空闲准入和回收，LLM 没有调度权限。页面与闲时控制器 `config.enabled` 分别开关，默认均关闭；独立资源没有并入生产 Helm 默认安装。

```text
DISABLED → WAITING_FOR_IDLE → STARTING → READY → DRAINING → STOPPED
                           ↘ ERROR       ↘ ERROR
```

- 以 Kubernetes 已存在的 GPU 需求、实际 Pod 分配和 Kueue 预留判断空闲，不能只看 GPU 利用率。平台数据库中尚未创建 RayJob 的排队意图不在观察范围内。
- 首版同时最多一个单卡副本，只借空闲整卡，不与训练共卡，不用 MPS/time-slicing，不重复登记物理容量，不修改训练之间的 Never 抢占策略。
- 出现相关训练需求后：摘流、有限排空或取消请求、删除自有服务、等待 Worker 和 Kueue 预留释放。Actor 缩至 0 不等于释放 GPU；不允许未经预算的双集群 GPU 滚动升级。
- 连续空闲 10 分钟才启动，启动超时 10 分钟，最长寿命 1 小时，排空 15 秒、Pod 终止 15 秒。首版显式启用后只预热一个副本，尚未实现按聊天需求自动启停。
- 只操作本次专用资源，不取消用户训练，不改训练镜像、用户源码、存储归属或团队配额。共享节点仍可能产生 CPU、网络、存储和功耗竞争，不能承诺训练零等待或零性能影响。

## 二、当前实现与现场状态

### 控制面与资源控制器

代码位于 `backend/assistant`、`backend/api/assistant*`、`backend/assistantidle`、`backend/cmd/assistant-inference-controller`、`images/assistant-serve` 和 `deploy/assistant-idle`。Portal 位于独立仓库，不使用本仓库旧前端发布。

现场为 KubeRay 1.6.2、Kueue 0.19.0。实际暂停字段是 `spec.rayClusterConfig.suspend`，RayService integration 已配置，不需要升级 CRD。控制器只创建 suspended 服务，由 Kueue 准入，不主动解除暂停，不写 ClusterQueue 或团队配额。

`7a48ffb` 之后加入观察缓存与硬节点排除，具体边界如下：

- 观察 Nodes、Pods、RayJobs、RayClusters、Workloads，使用 List+Watch，约每 20 秒刷新；新鲜度从 List 开始计算，最多 35 秒。任一流未同步、断开或过期即拒绝观察并关闭 gate；单纯保持连接不会延长有效期。缓存去除命令、环境值和非必需元数据，且不用于回写 Kubernetes。
- gate 的最多 3 秒有效期与上述 35 秒观察上限是不同层。观察失败时关闭 gate 并尝试回收自有服务；独立 reaper 在 leader 租约过期或超时后收尾。API 不可达、缓存传播及 GC 延迟意味着 60 秒释放仍是目标，不是保证。
- List 使用独立有界预算；实际写操作、单对象读取及 Lease 继续使用原身份和短超时。controller 配置 1 GiB 内存上限及 `GOMEMLIMIT=700MiB`，仍需观察真实资源规模下的峰值。
- 资源计算包含 Pod requests/limits、init/sidecar 并行峰值。未准入 Workload、相关 GPU Pending/Unknown/未绑定 Pod、未就绪 Workload、过渡态 RayJob 触发让卡；正常运行的训练不阻止其他整卡空闲使用。
- 已准入 Workload 只有在当前 RayJob 名称、namespace、UID 归属匹配，且全部 GPU podSet 的硬节点约束排除所有助手候选节点时才可忽略；未准入需求仍阻断。以 RayCluster 为 controller owner 的 Pending GPU Pod 也仅在硬约束完全排除候选节点时忽略，不依靠标签或 namespace 猜测归属。
- 固定 namespace、实例名和标签，删除使用 UID 前置条件。自有 RayCluster/GPU Pod/Workload 未消失前不重新计空闲窗口；不删除其他实例资源。

Worker 为 1 GPU/4 CPU/16 GiB；Head 为 CPU 容器，请求 500m/4 GiB、上限 2 CPU/8 GiB，排除专属团队节点。`ed341fe` 固定 Head object store 为 256 MiB（`268435456` 字节）、Worker 为 512 MiB（`536870912` 字节），避免随容器内存自动扩张。Head readiness 使用 HTTP `52365/api/healthz`，Worker readiness 使用 `52365/api/local_raylet_healthz`，两端 liveness 使用 `52365/api/healthz`；不依赖 shell/wget、Worker HTTP proxy 或 gate。Serve 应用是否就绪仍由 RayService Ready 约束。

KubeRay 1.6.2 会把 RayService 自带的 Serve service selector 固定为 `ray.io/cluster=<cluster>` 和 `ray.io/serve=true`，且会忽略用户自定义 selector。现场确认 HeadOnly 下 worker 也可能带 `ray.io/serve=true` 并进入 ready endpoints，但 worker 没有 8000 proxy。因此 `4a270926` 追加独立 `assistant-idle-inference` ClusterIP Service，只按 `app.kubernetes.io/instance`、`app.kubernetes.io/component=assistant-idle`、`raytrain.wellspiking.ai/assistant-role=head` 选择 Head，后端入口不得指向 KubeRay-owned Serve service。

NetworkPolicy 清单仍保留默认拒绝和端口最小化设计：Ray 通信限专用 namespace，后端只访问 head 8000；KubeRay 访问 8265 的同一个 peer 必须同时匹配 operator namespace 和 Pod 标签。真实现场 NetworkPolicy 当前未通过：`network-policy-audit.json`、`network-policy-audit.md` 显示 Cello/Cilium `PolicyEnforcement=never`，Head/Worker 共享 `securityidentity=1228`，所以策略不产生预期隔离。不能使用节点 local 规则、临时 iptables 或手工拦截作为放行依据；整改计划见 `network-policy-remediation-plan.md`。

专用验收 namespace `raytrain-assistant-canary` 已准备公开模型缓存并启用 controller/reaper。首轮 Head 在 4 GiB 上限下多次 OOMKilled；KubeRay 默认 Worker readiness 依赖镜像没有的 wget，并检查 HeadOnly 模式下不存在的 Worker Serve proxy。修复后第三轮 RayService 在现场 UTC `08:01:53` 创建，`08:03:47` Ready，Head/Worker 均 0 restart。缓存卷的节点/zone 约束必须与允许节点一致，不假定所有节点都有权重。

### 固定推理运行时

使用 **Ray 2.58 / vLLM 0.29.0+cu129 / torch 2.13+cu129 / Python 3.12**，底座摘要 `sha256:3e10e8189823e0f7ae4620c271bcdaaf64127ec7d0edc351591a508498b7684a`。旧 Ray 2.43/vLLM 0.8.5 因扫描报告 193 条/29 包，已拒绝放行。

固定公开模型 Qwen3-8B-AWQ；单卡、2 并发、输入输出合计 8192 token、输出最多 1500 token、显存利用率 0.85。服务端关闭 thinking、工具和跨请求 prefix cache；只读本地模型目录，`trust_remote_code=False`，不从训练节点在线下载。使用真实 tokenizer 精确计数，只裁剪证据，不静默丢弃用户问题。HTTP 断连、超时或 gate 失效均需取消在途请求；Ray 健康检查只检查 engine，对外 `/healthz` 同时检查 gate。

底座 NCCL 2.30.7 与 torch 声明的 2.29.7 不符，使用正常 resolver 修正，保留其余原生 CUDA 组合；不使用 `--no-deps` 或改包 metadata。已适配 vLLM 新参数和 Transformers 5 的 `return_dict=False`，构建检查真实 Ray 入口、引擎参数及非 root 导入。

集群 RTX4090D 节点存在 550.127.05 与 550.144.03 驱动。本次引擎验收在 **172.28.1.229、550.144.03 原生驱动**完成。底座可选 `cuda-compat-12-9` 曾被 NVIDIA hook 写入动态库缓存，造成 GeForce 加载不支持的 forward driver；候选仅移除此包并刷新镜像缓存，保留 CUDA 12.9 运行库。`5431fe1` 将 `NVIDIA_REQUIRE_CUDA` 限定为 `cuda>=12.4,driver>=550.127.05,driver<551`，不设置 `NVIDIA_DISABLE_REQUIRE`，不升级宿主驱动。该范围是设备准入约束，不代表其中所有节点均已验收；PTX/JIT 和新驱动功能仍须通过实际路径验证。

### 版本与发布状态

| 项目 | 当前状态 |
| --- | --- |
| 后端源码 main | 本地 main、GitHub、内部 GitLab、正式构建目录已同步到 `4a270926c4b3c26568c6490de309bba43a0d5a98` |
| 生产后端镜像 | Helm **253**；`release-20260922-01-d04026c`，摘要 `sha256:b4a9bbb43d612546f11f6be35c810de5f5444cfe5e917d2af38fb56fcf7780d1`；两副本 Ready/0 restart，`healthz=200`；schema 56 |
| Serve 镜像 | 源码 `5431fe1`；Harbor 摘要 `sha256:4bf3b53bed22ea0219eb0f33c483d729d740f5d18573ea0caeb15f83094b7ce3` |
| Controller 镜像 | 源码 `d04026c`，摘要 `sha256:be02ac1b95a997f7609e52fe7444ca1a67c358bcae78c5e6f8442201882e83ad`；controller/reaper 均已缩至 0 |
| Portal dev | 实际 dev 为 `67bcfa`，他人后续改动未触碰 assistant；`34488`、`34503` 通过。另有人合入 master `dbfba3bb`（CI `34505`），本次只推 dev，不改 master |
| RayService 现场 | 第三轮 `08:01:53` 创建、`08:03:47` Ready，Head/Worker 0 restart；真实 HTTP、Go Router、Head-only Service 均通过 |

生产发布证据：`/root/raytrain-release-20260922-assistant/docs-only-final-preflight/`。`safe-summary.json` 证明 dry-run 仅改变后端镜像和 3 个助手变量；`post-release-summary.json`、`post-release-training-comparison.json`、`post-release-backend-metadata.json`、`post-release-log-scan.json`、`post-release-schema.json` 分别保存最终核验。4 个活跃 RayJob、3 个 RayCluster、8 个训练 Pod 的 UID、状态、Ready 和重启数均与发布前一致；5919 行后端日志未匹配 panic/fatal/迁移错误，不等同于完整性能无影响证明。

使用说明已随后端在「开始使用与账号 → 各个菜单分别能做什么？ → 页面助手怎么用？」发布。Chrome 发布前可正常读取 Portal 使用说明；发布后浏览器连接超时、原生 AX 停留在旧菜单且截图不可用，用户重新打开后仍未恢复。没有提取浏览器凭据或伪造 SSO；因此当前不能称真实登录提问、引用跳转、拖动、取消已完成线上验收。

## 三、验证证据与适用范围

所有编译、测试、依赖安装和镜像构建均在既有构建机执行，本机只编辑、审阅与 `git diff --check`。证据根目录为 `/root/raytrain-assistant-validation-20260922`。

| 范围 | 已有证据 | 结论边界 |
| --- | --- | --- |
| 后端与控制器 | `d04026c` 完整 Go 回归 4265 项通过、0 失败、9 项外部服务测试跳过，34 个包通过；57 项真实隔离 PostgreSQL 测试全部通过、无跳过；race、vet、变更文件格式检查通过。`4a270926` head-only Service 候选完整 Go 回归通过，日志为 `headservice-final-go-test-raytrain-go-test.log`；vet 通过 | 9 项需外部 MLflow、站点运行时或 Ray 服务的测试仍跳过；全仓 gofmt 仍有两个既有非本次文件未格式化，变更 Go 文件格式通过；不替代生产会话验收 |
| Portal | dev `67bcfa6c` Deployment 1/1；`34488`、`34503` 通过 | 他人 master `dbfba3bb`（CI `34505`） 不属于本轮；本次只推 dev，不碰 master；仍需真实登录到模型响应的完整链路验收 |
| 推理运行时 | 27 项单测、真实 Ray/FastAPI ASGI、取消/撤流、vLLM 参数、非 root 只读入口通过；真实 tokenizer 中文、长证据、超长问题 3 项通过 | 隔离 ASGI 测试不能代替现场 HTTP、路由及 gate 撤流；安全扫描告警仍保留 |
| 真实 GPU 引擎 | 172.28.1.229 的 RTX4090D、550.144.03 原生驱动通过 CUDA 矩阵运算、完整 Qwen3-8B-AWQ 加载和单、双请求；冷加载 115.32 秒，17 token 短问答单请求 0.3201 秒、双请求 0.3286 秒 | 不是业务吞吐基准；不据此放行其他节点或所有驱动组合 |
| RayService HTTP 与 Router | `real-rayservice-http-acceptance.json`、`real-router-rayservice-acceptance.json` 均 PASS，覆盖真实 HTTP、8K 裁剪、2 并发、取消后槽恢复和 Go Router；Head-only Service sole-head 已验 | HTTP/Router 通过不代表 NetworkPolicy 已隔离；KubeRay-owned Serve service 不能作为 backend URL |
| 训练让卡 | `training-reclaim-r2/` 的创建、gate、删除时间和 `reclaimed-ray-task-cuda.log` 分别记录：测试训练 `08:29:13` 创建、`08:29:14` gate closed、`08:29:28` RayService/RayCluster/Workload 删除；约 `08:31` worker 获得 GPU，Ray remote CUDA 在 RTX4090D 上完成，`sum=1032636.75` PASS | 完整 RayJob 最终 FAIL，原因是临时测试 manifest 缺 dashboard port 且 heredoc 与 KubeRay entrypoint 拼接不兼容；不能称 RayJob SUCCEEDED。该失败不否定 GPU 释放和 CUDA 执行证据 |
| NetworkPolicy | `network-policy-audit.json`、`network-policy-audit.md`、`network-policy-remediation-plan.md` 已记录失败原因和整改计划 | Cello/Cilium 当前 `PolicyEnforcement=never` 且 Head/Worker 同 security identity，NetworkPolicy 不放行。禁止使用 local 节点规则绕过；整改前只能声明 Service 路由正确，不能声明 CNI 隔离正确 |
| 公开模型 | 10 个文件与 Qwen 官方 ModelScope API 的文件 revision/SHA256 一致；非 root、只读、断网检查通过；两份 safetensors 的 903 个索引 tensor 均存在 | ModelScope 文件 revision 不冒称 HuggingFace 同一 commit；仍需保留集群缓存校验与具体挂载证据 |
| 独立复审 | `5431fe1` 相对 `be9e28b` 的缓存、硬节点排除与 native driver 约束未发现新增可证实 P1/P2 训练破坏、鉴权绕过或泄密阻断 | 代码审阅不替代现场故障注入、CNI 策略整改和回收测试 |

`4a270926` 的 head-only Service 证据包括 `headservice-final-bundle-verify.log`、`headservice-final-fetch.log`、`headservice-final-worktree.log`、`headservice-final-vet.log`、`headservice-final-gofmt.log`、`headservice-final-gofmt-changed.log` 和 `headservice-final-go-test-raytrain-go-test.log`。此前 `ed341fe` 证据为 `rayservice-probes-final-summary.json`、`rayservice-probes-final-validated-sha.txt`、`rayservice-probes-final-backend-tests.jsonl`、`rayservice-probes-final-postgres-version.log`、`rayservice-probes-final-race-coverage.log`、`rayservice-probes-final-vet.log` 和 `rayservice-probes-final-format.log`；race 覆盖率为 assistantidle 84.0%、命令入口 16.1%，不将包级回归写成入口全覆盖。探针修复 RED/GREEN 为 `render-probes-red.log`、`render-probes-green.log`，新 Controller 构建记录为 `controller-probes-build.log`。

公开模型的构建机缓存为 `/root/raytrain-assistant-model-20260922/Qwen3-8B-AWQ`，manifest 保留许可证、来源和校验信息。早期阶段日志包括 `backend-tests-modern-final.log`、`idle-modern-final-tests.log`、`backend-tests-anthropic.log`、`portal-integrated-lint.log`、`portal-integrated-dev.log`、`helm-anthropic-verify.log`、`runtime-audited-build-third.log`、`runtime-audited-tokenizer.log`、`runtime-audited-model-check.log` 和 `model-cache-download.log`，不冒称均对应当前 HEAD。GPU、镜像及现场证据按源码 SHA、镜像摘要及采样时间关联留存；保留失败和 RED 原始记录。

### 安全扫描结论

`runtime-audited-audit.json` 的原始退出码为 1，仍报 Accelerate、setuptools 两条，另有 deep-ep、flashinfer-cubin、flashinfer-jit-cache、python-apt 四项不在 PyPI 审计覆盖内。**不宣称扫描 clean 或镜像无漏洞**，Python 包扫描也不等于 OS/CUDA 完整安全验证。

- httplib2 已在 `/usr/local` 安装 0.32.0，并核对 root/UID1000 的实际导入；apt 旧文件和 metadata 保留，不能把 Python 路径修复写成系统包告警消除。
- Accelerate 1.14 checkpoint 路径告警暂无修复；当前 Qwen3 原生 safetensors loader 不调用受影响两函数。配置/索引读取前限制目录、文件类型和大小，权重路径反例已验证；这是限定运行路径的适用性判断。
- setuptools 的修复版本 83 与 vLLM `<81` 约束冲突，不能强装；被报的 macOS 源分发打包路径不用于当前 Ubuntu 推理运行。仍保留告警和依赖约束记录。

删除镜像内可选兼容包后的最终产物仍需保留完整包清单及复扫关联记录；引擎成功运行不改变上述安全结论。

## 四、待完成与放行顺序

1. **补齐真实页面验收。** 恢复 Chrome 自动化连接后，在真实交互会话验证文档提问、来源链接、浮窗移动/关闭/恢复、无匹配结果、只读任务上下文和权限拒绝。当前发布模式仅检索，不能宣传为已提供大模型生成回答。
2. **独立审阅集群隔离变更，尚不执行。** 先与 VKE 管理方确认托管 Cello/vpc-cni 的 NetworkPolicy 开关及回滚方式，不直接修改 CNI DaemonSet 或宿主机防火墙。现有 5 条 MLflow 策略目前也未执行，全局开启会同时激活，需逐条核对 backend、训练 ingest、MLflow、PostgreSQL、DNS 和存储流向。完整实施与回滚计划见 [网络变更清单](PLATFORM_ASSISTANT_NETWORK_CHANGE_20260922.md)。
3. **先修助手身份标签，再安排受控网络窗口。** 当前 Cilium 只将筛选后的标签计入安全身份，Head/Worker 的 `assistant-role` 不参与身份计算。优先仅给助手模板使用现有受支持的 `cilium-policy-role` 等标签，区分 Head、Worker、Controller、Reaper，并同步精确策略；先证明身份不同，不扩大整个集群的标签集合、不修改现有训练 Pod。托管开关的实际支持和滚动影响需管理方确认。
4. **隔离、DB 待启动 GPU 需求保护与回滚验证后才重开 GPU。** DB demand 保护尚待实现，详见网络清单，不可跳过该闸门。 覆盖同节点/跨节点正反访问：后端仅到 Head 8000、operator 到 8265、Ray 内部和 gate/DNS/API 白名单；无关 Pod 和后端访问管理端口须被拒绝。同时检查新建及已建立的 MLflow/训练连接。异常按已审阅的 VKE 配置回滚；不能保证已中断连接自动恢复。GPU 仍保持关闭，不以特权 iptables 或仅加代理替代隔离。
5. **完整训练及干扰补验。** 修正专用 RayJob 的 dashboard 端口和 entrypoint 引用形式，重新取得 SUCCEEDED；补在途推理/加载中撤流、多节点训练需求、节点异常和固定业务负载性能对比。已有 1 秒关门、15 秒删除服务只是本次观察，不是全场景 SLA。

**本次收尾已完成：** controller、reaper 均为 0 副本；专用 namespace 内 Pod/RayJob/RayCluster/RayService/Workload 数量全部为 0，`requests.nvidia.com/gpu` 已用 0。公开模型缓存和受限证据保留，未移动或删除用户文件，未更改配额或 CNI。

参考：[Cilium 策略执行模式](https://docs.cilium.io/en/stable/security/policy/intro/)、[Cilium 安全身份标签](https://docs.cilium.io/en/stable/operations/performance/scalability/identity-relevant-labels/)、[Kueue 0.19 实际源码](https://github.com/kubernetes-sigs/kueue/blob/v0.19.0/pkg/controller/jobs/rayservice/rayservice_controller.go)、[KubeRay 1.6.2 结构](https://github.com/ray-project/kuberay/blob/v1.6.2/ray-operator/apis/ray/v1/rayservice_types.go)、[KubeRay 1.6.2 Service 构造](https://github.com/ray-project/kuberay/blob/v1.6.2/ray-operator/controllers/ray/common/service.go)、[Anthropic Messages](https://platform.claude.com/docs/en/api/messages/create)、[Anthropic 错误](https://platform.claude.com/docs/en/api/errors)、[vLLM 0.29 发行](https://github.com/vllm-project/vllm/releases/tag/v0.29.0)、[CUDA 兼容边界](https://docs.nvidia.com/deploy/cuda-compatibility/minor-version-compatibility.html)、[Qwen 官方模型](https://modelscope.cn/models/Qwen/Qwen3-8B-AWQ)、[LiteLLM 预算](https://docs.litellm.ai/docs/proxy/virtual_keys)。
