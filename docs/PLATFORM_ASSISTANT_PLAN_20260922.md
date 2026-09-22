# RayTrain 页面助手与多模型路由

日期：2026-09-22。用户已确认开始设计与实现。本文记录候选设计、验证和未完成项；不代表已经发布。DeepSeek 仅是临时连通性测试，不能成为所有用户共用的生产凭据。

## 用户体验

- RayTrain 页面悬浮入口，可拖拽、键盘移动、关闭和恢复，不新增菜单。
- 回答基于已发布使用说明；在任务详情可明确附带该任务，复用现有团队和角色权限。
- 任务日志默认不读取。每次提问单独勾选后，最多取30行、每行240字、总节选2000字；常见凭据模式脱敏不是完整DLP。
- 显示来源链接、文档版本、查询时间、实际回答模式和降级提示。生成回答是纯文本；可点击链接由服务端生成和前端校验。
- 每次独立提问，不把之前对话发送给模型，不在服务端保存聊天内容。切换身份、团队或任务时清空/确认上下文；取消后丢弃迟到响应。
- 只读：不能提交、停止、修改训练、执行命令、查看个人源码或索引用户文件。配额和MLflow专门工具尚未实现，不能把文档解释称为实时工具查询。
- 用户说明补在现有「各个菜单分别能做什么？」文章内，不新增文档树项。

## 接口与多模型

新增交互登录接口：GET /api/v1/assistant/capabilities、POST /api/v1/assistant/query。

提问仅接受 question、mode、jobId、includeLogs；拒绝重复/未知JSON字段，限制16KiB和4000字。沿用平台会话权限，不向浏览器下发供应商Key。现有任务、CLI、MLflow接口保持原有行为。

现支持 OpenAI-compatible Chat Completions 与 Anthropic 原生 Messages 两种协议。最多配置4个固定后端，每个有独立ID、模型、地址、Secret和API/本地类型。LiteLLM、兼容云API、兼容自建Ray Serve可共用适配器；protocol默认openai保持旧配置兼容，anthropic使用独立Messages编码与认证头。其他供应商原生协议需要新增适配器，不能声称通用支持所有协议。

| 模式 | 行为 |
| --- | --- |
| auto | 按配置优先尝试API或本地，再尝试另一类，最终退到文档检索 |
| api | 只尝试配置的API模型，失败退文档 |
| local | 只尝试配置的本地服务，失败退文档；不自动申请GPU |
| docs | 不调用模型，只返回匹配说明和授权任务信息 |

固定地址与模型由管理员配置，用户不能指定URL或工具。HTTPS必需；集群内 `.svc.cluster.local` 本地服务可用HTTP。禁止跟随重定向和继承环境代理。请求体读取上限5秒，每次模型尝试最多12秒，总请求预算35秒；响应上限128KiB，输出上限1500token。

区分额度耗尽、认证失败、普通429和服务不可用。失败后每后端5分钟进程内冷却只用于抑制重试，**不是跨副本月预算**。真正额度硬限制交给LiteLLM/供应商专用Key；没有权威预算接口就不显示虚假的剩余额度。auto配置中的所有目标必须事先获准接收相同范围的数据。

## 默认关闭与影响边界

ASSISTANT_ENABLED默认false：不注册query路由，仅返回关闭状态；Portal隐藏组件。不创建RayService、GPU Pod、队列、配额或存储。不改训练镜像、用户代码、既有任务、数据库schema。

启用后每进程最多8并发、每用户2并发、每用户每分钟8问；独立请求超时、可取消、审计失败退文档。审计复用现有audit_logs，只记录身份、请求ID、模式和日志同意，不记录问题与内容。

新功能会消耗有限的API/数据库/模型资源，不能承诺绝对零影响。发布前必须通过现有全量回归、权限与异常验证；运行后可独立关闭助手。生产发布仍按release skill最小覆盖，不自动启用闲时GPU。

## 配置示例（非生产配置）

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
      model: <该账户实际可用的Claude模型ID>
      existingSecret: raytrain-assistant-anthropic
      secretKey: api-key
    - id: idle-serve
      kind: local
      baseURL: http://assistant-idle-serve-svc.raytrain-assistant-idle.svc.cluster.local:8000/v1
      model: Qwen3-8B-AWQ
```

这仅展示配置形态，RayService与Secret并未因此创建。凭据只引用既有Secret；不要写在values、代码、镜像或文档。没有模型时可使用 enabled:true、providers:[] 的检索模式。DeepSeek可作为独立临时测试配置，扩展字段thinkingDisabled仅给明确支持该扩展的后端启用。

## 闲时GPU：独立实施与验收

只读现场核对：KubeRay v1.6.2、Kueue v0.19.0，已安装RayService CRD，`kubectl get rayservices.ray.io -A` 当时无资源。现有cluster-gpu-queue三个抢占策略均Never，没有推理专用队列。不能仅创建RayService就宣称训练能优先回收GPU。

采用独立RayService与RayCluster，不复用训练集群。Ray Serve负责请求与模型副本，独立确定性资源控制器负责空闲准入和回收，LLM没有调度权限。

```text
DISABLED → WAITING_FOR_IDLE → STARTING → READY → DRAINING → STOPPED
                           ↘ ERROR       ↘ ERROR
```

- 所有GPU需求进入同一资源账本，包括训练、调试、评估与其他推理。只看GPU利用率不足以证明可借用。
- 独立低优先级、可撤销推理资源域；不能修改原训练之间的Never语义，不能把同一物理GPU容量重复登记到两个队列。
- 首次最多一个单卡副本，只用空闲整卡，不与训练共卡，不用MPS/time-slicing。兼顾多机训练完整节点组合、CPU/网络/磁盘和节点功耗竞争。
- 训练出现需求：摘除推理路由→有限排空/取消聊天→停止自有Worker Pod→确认GPU分配及Kueue预留释放→训练准入。控制器失联或状态不明时停止新增推理，租约失败需按设计收尾。
- Serve Actor缩至0不等于Worker Pod退出，更不等于Kueue预留释放。须防止RayService/Autoscaler在让卡期间重建Worker。
- 更新RayService可能创建双集群；首版禁止未经预算的双份GPU滚动升级。
- 模型权重走内部受控缓存；按GPU显存、架构、许可证和中文问答质量选择固定权重摘要与镜像。公开候选权重已在构建机下载并逐文件校验SHA256；尚未放入集群模型PVC或进行GPU验收。
- 首版显式启用后只预热一个副本，不因卡空闲就全部占满；按需求自动启停尚未实现。先连续空闲10分钟才启动，摘流至多15秒排空、Pod终止15秒；这些是待验证参数，不是训练准入延时SLA。

严格“训练零新增等待、零性能影响”需要独立硬件/API/检索。共享池必须接受并实测回收时间，不能承诺绝对零影响。

验收必须包含：单卡和多节点训练到达时完整回收；模型加载中到达训练；控制器断联；节点/驱动/存储异常；重复冷启动；固定业务训练step时间、P95数据等待与各项资源压力。只能操作本次专用资源，不取消用户训练。GPU池范围、可接受新增等待和模型候选需在实际启用前明确。

## 当前实现与部署边界

实现位于 `backend/assistant`、`backend/api/assistant*`、`backend/assistantidle`、独立 `cmd/assistant-inference-controller`、`images/assistant-serve` 和 `deploy/assistant-idle`。Portal候选在独立仓库，不构建本仓库旧前端。两个开关分别是页面 `ASSISTANT_ENABLED` 与闲时控制器 `config.enabled`，均默认关闭。独立RayService清单没有并入生产Helm默认资源。

只读核对Kueue0.19实际源码与安装CRD：暂停字段是 **`spec.rayClusterConfig.suspend`**，不是顶层 `spec.suspend`。RayService integration已配置，不需要据此升级CRD。控制器只创建suspended服务，由Kueue准入，不主动解除暂停，不写ClusterQueue或团队配额。

- 计算Pod requests/limits及init/sidecar并行峰值。GPU Pending/Unknown/未绑定Pod、未准入或未就绪Workload、过渡态RayJob触发让卡；运行中正常训练不阻止其他整卡空闲使用。RayJob使用真实字段 `status.jobDeploymentStatus`。
- 独立namespace、固定实例名与标签、UID删除条件，最多1个GPU。等待自有RayCluster/GPU Pod/Workload消失后才重新计空闲窗口；不删除不属于本实例的资源。
- 连续空闲10分钟启动，启动超时10分钟、最长寿命1小时。gate最多3秒有效，撤流排空15秒、Pod终止15秒；独立reaper在leader租约过期或超时后回收自有服务。API不可达与GC延迟不能保证60秒释放。
- 只观察已进入Kubernetes的需求，看不到平台数据库中尚未创建RayJob的排队意图。必须实测真实训练到达，不能承诺零等待。
- Worker为1GPU/4CPU/16Gi，Head为CPU容器。排除专属团队节点，不改训练镜像、用户源码、存储归属、local配额和训练之间的Never策略。
- NetworkPolicy默认拒绝，Ray通信限专属namespace，后端只访问head8000；KubeRay8265要求同一个peer同时匹配operator namespace及现网Pod标签。该设计尚需集群实际连通/拒绝验证。
- 镜像拉取只引用本namespace已准备的Secret名，控制器不读取或创建凭据。ModelPVC也需单独准备；现网 `ebs-ssd` 为WaitForFirstConsumer，RWO及zone/node affinity可能限制运行节点，不能假装所有节点均有模型缓存。

## 4090D与推理镜像选择

只读监控确认RTX4090D约24GB，driver550.127.05。首轮选择官方Qwen/Qwen3-8B-AWQ、单卡、2并发、8K输入输出合计、最多1500输出、显存利用率0.85，禁thinking/工具/跨请求prefix cache。8B 4-bit纯参数估算约4GB，实际下载权重约6.10GB，运行另需KV cache与激活，必须实测显存与吞吐。暂不采用多卡或让全部闲卡常驻。

现候选为 **Ray2.58 / vLLM0.29.0+cu129 / torch2.13+cu129 / Python3.12**，底座固定 `sha256:3e10e8189823e0f7ae4620c271bcdaaf64127ec7d0edc351591a508498b7684a`。旧Ray2.43/vLLM0.8.5虽曾通过CPU门禁，审计193条/29包，已拒绝放行。NVIDIA允许CUDA12.x minor compatibility但PTX/JIT及新驱动功能仍可能失败；不使用CUDA13默认镜像或面向特定专业卡的forward compatibility，也不升级训练节点驱动。

新底座pipcheck发现NCCL2.30.7与torch声明2.29.7不符，正常resolver修正后保留其余原生CUDA组合；不使用no-deps或改包metadata。构建显式验证依赖、真实Ray入口、vLLM参数、非root导入。vLLM已删除swap_space/disable_log_requests，适配为cpu_offload_gb=0/enable_log_requests=False；Transformers5需要显式return_dict=False才能取得token列表，真实分词器测试已经捕获并验证修复。

公开模型缓存：构建机 `/root/raytrain-assistant-model-20260922/Qwen3-8B-AWQ`，10个文件均与Qwen官方ModelScope API公布的文件revision/SHA256一致，含许可证、配置、tokenizer、索引与两份safetensors，manifest记录完整来源。原HuggingFace revision元数据曾核对，但下载403；不能将ModelScope文件revision冒称为HuggingFace同一个commit。运行只读内部目录、trust_remote_code=False、不从训练节点下载外网文件。

## 验证记录

日期2026-09-22，所有编译/测试/镜像构建在既有构建机，本机只编辑与审阅。证据目录 `/root/raytrain-assistant-validation-20260922`。

| 范围 | 证据与结果 | 尚不能证明 |
| --- | --- | --- |
| 后端/API/Anthropic | go vet、完整Go回归、真实隔离PostgreSQL、助手race通过；assistant覆盖95.9%；最终a589c3f完整Go/PG再次通过 | 未发布、未用真实ClaudeKey调用 |
| 闲时控制器 | 生命周期/资源账本/重启回收、renderer、NetworkPolicy同peer、pullsecret合同与race通过；assistantidle覆盖81.0% | 命令入口整体覆盖12.9%，租约循环与真实GPU回收待验收 |
| Portal | 922de112已合并远端dev的de2d5ecf；完整lint/合同/dev构建及真实Chromium组件+模拟API6项通过 | 非生产SSO端到端验收 |
| 默认关闭 | Helm与基线清单一致；开启协议配置仅新增助手环境与Secret引用 | 不代表已上线 |
| 推理运行时 | 27单测、真实Ray/FastAPI ASGI、取消/撤流、vLLM真实参数检查、非root只读入口通过；真实tokenizer中文/长证据/超长问题3项通过 | 未加载GPU权重、未测模型质量与训练干扰 |
| 模型与依赖 | 固定公开模型文件校验完成；旧镜像拒绝；新镜像扫描和适用性逐项审阅 | Python包审计不等于OS/CUDA完整镜像安全验证 |
| 真实外部接口 | 临时DeepSeek一次非业务问题HTTP200，21token；LiteLLM无专用Key返回401 | 个人Key不配置为共享生产凭据，未完成公司模型服务接入 |

关键日志：`backend-tests-modern-final.log`、`idle-modern-final-tests.log`、`backend-tests-anthropic.log`、`portal-integrated-lint.log`、`portal-integrated-dev.log`、`helm-anthropic-verify.log`、`runtime-audited-build-third.log`、`runtime-audited-tokenizer.log`、`runtime-audited-model-check.log`、`model-cache-download.log`。RED日志保留协议、prefix cache、网络范围、pullsecret缺项及新依赖API不兼容的原始失败证据，不删测试绕过。

## 安全审计与剩余验证

新核心栈第一次扫描仅3条：httplib2已在/usr/local路径安装0.32.0并验证root/UID1000实际导入，保留apt旧文件及metadata，系统包告警不伪报消除；Accelerate1.14 checkpoint路径告警暂无修复，当前Qwen3原生safetensors loader源码不调用受影响两函数，配置/索引读取前拒绝非普通文件并限定目录/大小，权重路径反例通过；setuptools83与vLLM的<81约束冲突，不能强装，其macOS源分发打包路径不用于当前Ubuntu运行环境。4个包（deep-ep、flashinfer-cubin、flashinfer-jit-cache、python-apt）不在PyPI审计覆盖内，不能算自动通过。最终镜像仍需保留完整复扫报告及GPU验收，不能称为无漏洞镜像。

必须完成后才能启用共享池GPU：

1. 专用namespace/模型缓存/只拉取凭据和实际网络策略准备，server-side dry-run审阅，仅操作本次资源。
2. 同时最多1GPU、1小时上限，固定模型加载、中文帮助问答质量、2并发/8K/OOM/取消/冷启动与驱动PTX路径验证。
3. 专用测试需求触发完整GPU及Kueue预留释放，包含模型加载中、控制器失联和节点异常；不取消真实用户训练。60秒是目标，不是已验收保证。
4. 按当前授权同步后端四端和Portal dev；最小发布仅更新必要组件。生产后端与GPU模式分别开关，文档模式不需要模型Key。

流式输出、多轮记忆、跨副本月预算、写操作工具不在当前实现；不要让用户误以为助手能自动改任务或精确显示公司剩余额度。

最终复扫 `runtime-audited-audit.json` 仍报2条（Accelerate、setuptools），4项不覆盖；httplib2实际Python版本0.32.0不再命中。原始退出码为1，不将适用性判断写成扫描通过。最终Runtime源码858e54a的27单测、真实ASGI、引擎参数及完整模型目录检查均通过；这仍不代表GPU或完整镜像安全验收。

## 版本与现场状态

代码候选 `858e54abb1d98b41e3782909c2a1744c63b77368`；Go代码与通过完整PG回归的a589c3f相同，之后仅修改独立Python运行时/测试和文档。最终本地Docker候选摘要（尚未推Harbor）：Serve `sha256:bb4c2c583401ecac110158d4c7331dd8ce38520d04f89a2be25b7d9b03704b1f`，Controller `sha256:033ec8b544850da469a11d59047bf726eec57d81a8ca0fb95bca8189e3007a04`。这不是线上镜像摘要；推仓后需重新取registry权威digest。完整模型目录在最终非root/只读/断网容器通过检查，2个safetensors分片的903个索引tensor均存在，仅验证header，不代表已执行GPU计算。

本轮后端main四端重新核对仍为 `5eb7b629215b38fc6dc83f4f00a5429fdad12c48`；Portal远端dev为 `de2d5ecf08fa18524417ae6b1deb458b19afa54a`，候选 `922de11218c0b764a825ca2cf05804659bc9b691`。生产Helm252，后端镜像摘要 `892d7f968604bcd6ffe1a4b6163855e2013acbf258e601fb8c0556852d9555b5`；无RayService。此次未推送、未部署、未创建GPU Pod或修改生产资源；隔离工作树与最终镜像/模型缓存保留供后续验收。临时PostgreSQL和网络已清理。

本轮3个被替代的干净构建工作树、4个旧候选镜像标签和传输bundle已清理。最终构建工作树 `/tmp/rtp-assistant-audit-final`、2个最终镜像、公开模型缓存及全部证据保留。未删除之前存在的其他容器或未知文件。

## 下一次放行的明确范围

当前只完成隔离候选，尚未执行本次main推送、Portal发布或集群资源创建。下一步建议确认以下范围后按依赖顺序执行；远端或代码变化时重新验证，失败不扩大范围：

- 将已验证后端候选同步本地main、GitHub、内部GitLab和正式构建目录；Portal只发布dev。后端只构建backend，另推送已验证的助手Serve/Controller独立镜像到既有平台Harbor项目，不重建训练Base、CLI或旧前端。
- 在独立验收namespace准备专用模型PVC（优先现有NVMe local storage class，约16Gi，仅公开权重）、引用既有只拉取凭据、独立ServiceAccount/RBAC/NetworkPolicy/低优先级及LocalQueue。原ClusterQueue、团队配额及原训练调度设置不变。
- 同时最多1张空闲4090D，Worker1GPU/4CPU/16Gi，加必要CPU Head与控制器；单次验收上限1小时。先满足连续空闲和无训练待准入条件，只用公开帮助文本，不上传用户日志到外部模型、不创建个人PAT。
- 验证加载、问答、取消、专用需求到达时撤流及资源回收、控制器故障回收；异常仅停止本次实例。结束后关闭本次GPU服务/控制器并核对GPU及Kueue预留释放，保留公开缓存与证据。
- 页面助手与GPU独立放行。后端部署前审阅server-side dry-run并核对存量训练UID/重启数；无专用模型Key时可先用文档模式。临时个人DeepSeekKey不配置为共享生产后端，未通过驱动/回收验收不常驻启用GPU模式。

参考：[Kueue0.19实际源码](https://github.com/kubernetes-sigs/kueue/blob/v0.19.0/pkg/controller/jobs/rayservice/rayservice_controller.go)、[KubeRay1.6.2结构](https://github.com/ray-project/kuberay/blob/v1.6.2/ray-operator/apis/ray/v1/rayservice_types.go)、[Anthropic Messages](https://platform.claude.com/docs/en/api/messages/create)、[Anthropic错误](https://platform.claude.com/docs/en/api/errors)、[vLLM0.29发行](https://github.com/vllm-project/vllm/releases/tag/v0.29.0)、[CUDA兼容边界](https://docs.nvidia.com/deploy/cuda-compatibility/minor-version-compatibility.html)、[Qwen官方镜像](https://modelscope.cn/models/Qwen/Qwen3-8B-AWQ)、[LiteLLM预算](https://docs.litellm.ai/docs/proxy/virtual_keys)。
