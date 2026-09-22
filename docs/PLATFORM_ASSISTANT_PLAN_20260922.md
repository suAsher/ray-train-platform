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
      baseURL: http://assistant-serve-svc.ray-train-platform.svc.cluster.local:8000/v1
      model: <已验收的自建模型名>
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
- 模型权重走内部受控缓存；按GPU显存、架构、许可证和中文问答质量选择固定权重摘要与镜像。当前未选定或下载生产模型。
- 按实际请求扩容，不因卡空闲就全部占满。建议先连续空闲10分钟才启动，摘流至多15秒排空、Pod终止30秒；这些是待验证参数，不是训练准入延时SLA。

严格“训练零新增等待、零性能影响”需要独立硬件/API/检索。共享池必须接受并实测回收时间，不能承诺绝对零影响。

验收必须包含：单卡和多节点训练到达时完整回收；模型加载中到达训练；控制器断联；节点/驱动/存储异常；重复冷启动；固定业务训练step时间、P95数据等待与各项资源压力。只能操作本次专用资源，不取消用户训练。GPU池范围、可接受新增等待和模型候选需在实际启用前明确。

## 验证记录与剩余项

基线：后端四端5eb7b629215b38fc6dc83f4f00a5429fdad12c48；Portal dev 752e45b1676bd0e67d31c615b6bc4ee3e40fe91b。隔离工作树在 /tmp/rtp-assistant-backend-20260922 与 /tmp/rtp-assistant-portal-20260922，未推送或部署。

- Router新增测试已在实现前确认RED；安全边界新增测试也确认修复前失败。后端代码候选43e062b在构建机通过gofmt、go vet ./...、go test -p 1 -count=1 -timeout=20m ./...（真实隔离PostgreSQL）、assistant与API助手相关race测试；assistant模块覆盖率95.1%。
- Portal完整lint/合同、dev三阶段构建通过；真实Chromium使用当前组件和模拟API通过6项交互测试（开关、回答/引用、逐次日志同意、取消迟到响应、拖拽、切换任务）。这是隔离浏览器验收，不是生产登录验收。
- Portal检测到dev并行更新de2d5ecf（数据集列表可读性），已无冲突合入候选922de112，整合后完整lint、合同与dev构建再次通过。
- Helm测试：默认关闭时渲染清单与基线完全一致；开启测试后只有后端assistant环境变量和Secret引用变化。
- 真实DeepSeek最小连通性：models HTTP200且指定模型可用；一次不含业务数据的问答HTTP200/OK，总21token。仅证明外部接口可调用，不代表页面端到端或生产凭据已配置。
- LiteLLM模型列表无Key为401；暂无专用有预算Key，不能声称公司接口联调完成。
- GPU推理部署、资源回收、真实业务问答质量、流式输出、多轮记忆、跨副本预算状态与写操作均未包含在首版交付。
- 生产后端、Portal、数据库schema、配额、调度和运行中训练本轮未改动。后端四端收尾复核仍为5eb7b629；Portal远端dev为他人新增的de2d5ecf。本次未推送。后续发布需重新核对四端及最小差异。
- 构建机证据目录：/root/raytrain-assistant-validation-20260922（backend-tests-verified.log、security-red.log、security-truncated-red.log、helm-verify.log、portal-integrated-lint.log、portal-integrated-dev.log）。浏览器harness：/tmp/rtp-assistant-browser-harness-20260922/env_publish_review_final_20260922110836。
- 测试环境差异：PostgreSQL测试串行执行避免跨包共享迁移锁互相干扰；最终工具镜像sha256:b048b8f45eff4125e54b738117b0e1c54b30eaa133e566fd8d55777ebe4f41bf与规定Go基础镜像层完全一致，仅附加bash/git/gcc/jq。Portal的Docker Hub syntax下载不可达，临时验证Dockerfile仅去掉首行syntax指令，其余三阶段步骤原样执行；仓库Dockerfile未改动。

官方参考：[KubeRay与Kueue](https://docs.ray.io/en/latest/cluster/kubernetes/k8s-ecosystem/kueue.html)、[Serve伸缩](https://docs.ray.io/en/latest/serve/autoscaling-guide.html)、[LiteLLM虚拟Key](https://docs.litellm.ai/docs/proxy/virtual_keys)、[Kubernetes抢占](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/)。


## 4090D 本地推理决策与 Anthropic 补充（2026-09-22）

用户授权继续推进并由平台决定首轮模型规模。只读DCGM核对7个GPU节点均为NVIDIA GeForce RTX 4090 D，每卡FB_FREE+FB_USED约24209–24210MiB；这是显存总量，不是可申请余量。现有队列没有cohort、三个抢占策略均Never，无RayService。

首候选固定为官方 Qwen/Qwen3-8B-AWQ（4-bit、Apache-2.0），独立RayService内单卡vLLM，最多1个GPU Worker/模型副本，张量并行1，max_model_len=8192、max_num_seqs=2、GPU内存利用上限初值0.8，关闭thinking并仅返回最终回答。权重须固定revision和摘要、从内网只读缓存获取；环境镜像单独固定摘要，不改变训练Base。

8B 4-bit的纯权重量级约4GB，实际还包含未量化参数、量化元数据、KV cache、activation与运行时，因此不能把4GB当运行显存。24GB适合作为该规模、有限上下文/并发的验证目标；容量是工程估算，不是已测吞吐或质量承诺。14B可以后续作质量对照；首轮不采用32B/70B、多卡张量并行或把所有闲卡常驻占满。Anthropic云模型不能下载成4090本地权重；二者通过统一助手接口各自路由。

优先级决策：训练优先；共享池先以“额外准入等待不超过60秒”为验收目标，不当作保证或对现有队列的变更授权。未通过回收验收前GPU模式保持关闭，超限后停用闲时推理并保留API/文档模式；绝不通过取消真实训练来腾卡。模型质量测试先用无业务敏感信息的平台公开帮助问题，覆盖正确引用、无证据拒答和提示注入；不能以回答一次OK替代此测试。

Anthropic原生接入使用/v1/messages、anthropic-version=2023-06-01及独立x-api-key，不发送OpenAI Bearer或重复系统消息。只解析text block，不执行tool_use、不返回thinking；401、明确额度错误、429与529分别按既有认证/额度/限流/不可用语义降级。没有Anthropic专用Key，原生接口目前以合同测试验证，不能报告真实Claude调用通过。

参考：[Anthropic Messages](https://platform.claude.com/docs/en/api/messages/create)、[API错误](https://platform.claude.com/docs/en/api/errors)、[Qwen3-8B-AWQ官方模型卡](https://huggingface.co/Qwen/Qwen3-8B-AWQ)、[vLLM量化兼容表](https://docs.vllm.ai/en/stable/features/quantization/)。


最新准入核对补充：kueue-manager-config的integrations.frameworks已经包含ray.io/rayjob、ray.io/rayservice和ray.io/raycluster；因此不是“集群未安装原生集成”，而是平台尚无助手服务的完整资源生命周期/主动回收实现。部署前按安装版本验证原生工作负载与资源预留的准确关系，优先让推理原生进入既有Kueue资源账本，并由专用控制器主动撤销自身推理，保持训练之间Never语义不变。拒绝采用手工把训练ClusterQueue nominalQuota减1再加1的方案：这会与现有容量同步竞争，也违反不擅自调整配额的边界。GPU总量与原local团队配额均不因助手改写。


8K是输入与输出合计token窗口，不是8000汉字。实际Serve包装层必须按该模型tokenizer计数，预留最多1500输出token，超出时优先裁剪低相关文档及日志节选并向用户标注；不能只靠字符长度估算，也不能静默丢掉用户问题。若仍不能容纳，应明确拒绝该模型请求并走已批准的备用路径。首轮需测试长中文问题与日志，避免“显存够但上下文超限”。本地Worker的初始CPU/内存预算为8 CPU/32Gi（待完整资源渲染与准入验证），仅共享训练池，排除团队专属节点；Head与缓存/网络开销也必须记账。

Anthropic候选验证：db567f3加构建机gofmt差异通过go vet、完整go test -p 1 -count=1 ./...（真实隔离PostgreSQL）、assistant及API助手race；模块覆盖率95.9%。原生协议RED在仅OpenAI实现上确认；GREEN覆盖认证头隔离、请求格式、默认协议兼容、text-only响应、预算/普通400区分、混协议降级、重定向不转发凭据。Helm关闭时与原基线manifest相同，开启Anthropic时仅助手环境配置和Secret引用变化。独立审查无P1/P2。证据：/root/raytrain-assistant-validation-20260922/anthropic-red.log、backend-tests-anthropic.log、helm-anthropic-verify.log。

本轮只新增后端原生协议适配、配置和设计，Portal组件未改，无需重跑未受影响的前端构建。本轮未推送/部署、未创建RayService、未下载权重、未占GPU；临时PostgreSQL与测试网络已由脚本清理。后续仍需真实Claude凭据联调、本地模型质量/内存/吞吐验收及完整Kueue主动回收验证，不能把协议合同通过报告成这些项目完成。
