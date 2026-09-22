# RayTrain 页面助手与多模型路由实施方案

日期：2026-09-22。用户最初同意页面助手首版实施，随后明确「key 暂时无更多额度，先设计吧」。目前停在设计阶段，保留隔离工作树草稿；不继续编码、验证、推送、发布或启动 GPU 服务。新增空闲 GPU 推理要求先完成调度方案，不默认启用抢占。

## 目标与边界

Portal RayTrain 内悬浮可拖拽助手，复用已发布用户说明与当前用户有权读取的任务信息，输出来源、时间和降级状态。新增只读 API，不自动执行 shell、修改训练或读取个人源码。公司模型入口为 https://litellm.westwell-lab.com；未认证 /v1/models 返回 401，根页面、/docs、/ui/、/openapi.json 返回 404，模型名/服务 Key/预算配置尚待安全接入。

## 实施架构

前端只调用同域 /api/v1/assistant。Go API 在现有交互认证之后校验参数、用户和团队，检索公开发布帮助文章，并按明确 jobId 读取授权任务的有限事实。模型 Provider 只接收已裁剪证据，不能调用任意工具。公司 API 与自建 OpenAI-compatible 服务分别配置固定 endpoint/model/Secret；用户不能传 endpoint、key 或任意工具名。响应只呈现纯文本与服务器生成的引用链接。

模式：auto（API 优先，本地备用）、api（仅公司模型，失败退检索）、local（仅自建模型，失败退检索）、docs（不调用模型）。可由管理员设 auto 的首选 Provider。无模型配置、额度耗尽、超时和服务异常都能返回明确标注的文档/状态检索，不冒充生成回答。额度硬限制以 LiteLLM 数据库支持的应用专属虚拟 Key/团队预算为权威，RayTrain 不用进程内 token 计数冒充跨副本月预算。

## 分工与验证任务

- [ ] Provider（backend/assistant/types.go、router.go、openai.go 与 *_test.go）：测试先行覆盖模式顺序、禁跨供应商的显式模式、配额与429区分、无凭据、固定URL/禁止重定向、响应长度、取消超时、纯文档降级。builder先跑新增测试见RED，再实现并验证GREEN。
- [ ] API（backend/api/assistant*.go）：严格JSON/体积限制/限流并发；已发布文章投影；当前身份复用现有任务读取授权；有限脱敏日志；链接白名单与时间；测试匿名/PAT拒绝、跨团队、超长输入、无证据与model故障。
- [ ] 接线（backend/api/jobs.go、backend/main.go、backend/config/assistant.go、config.go、Helm可选env）：默认关闭；Secret只通过secretKeyRef或文件引用，公开caps不含内部endpoint/key；不新增数据库迁移。
- [ ] Portal（src/views/rayTrain/Assistant/、api/assistant.js、layout/index.vue）：精确路由限定、拖拽/键盘/移动端、当前任务绑定、终止请求、会话按subject+tenant隔离；不持久化对话；来源校验；模型与检索状态区分。pureJS行为合同接入Dockerfile.lint。
- [ ] 构建机门禁：Go格式/vet/全部回归、权限集成所需真实PG；Portal完整lint及dev build；独立review；不在本机执行编译测试。
- [ ] 真实模型接入：取得专用Key后仅调用获准模型，验证模型列表、最小无业务数据问答、预算拒绝与fallback；没有凭据不宣称完成。
- [ ] 发布与浏览器：按release先候选后四端同步；Portal只dev；助手开关/模型配置单独验收；真实权限、文档链接、关闭/拖拽与切换团队；不提交GPU训练以验证普通聊天。

## 空闲 GPU 推理：另行实施的调度门槛

现场只读核对：cluster-gpu-queue 为 Kueue v1beta2，withinClusterQueue/reclaimWithinCohort/borrowWithinCohort 均 Never；没有推理专用GPU队列。这不是已具备训练抢占推理能力。

推荐先单卡小模型验证，服务权重缓存至独立受控存储，多个单卡副本优于首版多卡张量并行。只用空闲整卡，不与训练CUDA进程共卡、不修改现有训练Pod/团队配额/专属节点。CPU/内存/网络/存储吞吐同样设限，避免模型加载影响分布式任务。

可回收推理须纳入同一GPU资源账本，建立独立低优先级可借用队列，训练之间原有不抢占语义保持；具体Kueue版本集成/队列cohort/PodSet拓扑需验证后定稿，不能仅给Deployment写低PriorityClass。训练队列有待准入需求时停止接新推理请求，撤销推理资源预留，确认Pod终止和GPU实际释放后准入训练；其他入口及外部工作负载也必须纳入，否则不能保证不挡训练。控制器失联需租约超时后fail-closed停推理。

严格的“任何训练零等待、零性能影响”与共享节点闲时复用不能同时保证；共享模式接受明确且经过验收的回收时延。若要求硬零影响，应使用独立推理硬件或只用API/检索。当前不会为该方案开启现有训练队列抢占。

## 预算与故障语义

LiteLLM预算由其DB/Key配置强制执行，额度周期/时区以服务端返回为准，不能把30d自动称作自然月。预算超限响应与普通429分别记录；401/403是认证配置问题，不说成额度耗尽。auto仅向预先批准的数据处理目标切换；无可用GPU则退检索，不为了回答问题抢训练卡。无需模型的文档模式保持可用。各副本有并发/请求大小/超时上限，但不声称是分布式总预算。

## 验收与剩余项报告规则

必须区分：已实现、builder测试通过、Portal/后端已部署、真实模型通过、空闲GPU回收通过。初版不包含任意写操作、自动修改说明、完整聊天持久化、附件/代码库索引。未来增加action通过服务端参数快照+用户确认+执行前鉴权+幂等控制，不授予通用shell工具。

参考：https://docs.litellm.ai/docs/proxy/users ，https://kueue.sigs.k8s.io/docs/concepts/preemption/ ，https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/ 。

## API 无额度情况下的推荐决策

### 产品模式与路由

用户默认只看到「智能回答」和实际运行来源，不要求用户理解队列或供应商；高级选项才开放文档模式/公司模型/本地模型。管理员设置全站默认策略：

| 策略 | 顺序 | 适用场景 |
| --- | --- | --- |
| 省额度（推荐目标） | 通过质量门槛且 READY 的本地模型 → 有预算的公司模型 → 检索 | 长期常用，利用闲时资源 |
| 质量优先 | 有预算的公司模型 → READY 本地模型 → 检索 | 复杂故障说明、业务要求较高 |
| 本地限定 | READY 本地模型 → 检索 | 数据不允许发给公司网关后的外部供应商 |
| 仅检索 | 已发布文档与受控状态查询 | 零生成费用、服务故障、没有空闲 GPU |

当前没有额度时，公司 Provider 标记 BUDGET_EXHAUSTED，不在每条提问时重试。按明确恢复时间/管理员恢复触发一次探测，不假设每月1日恢复。检索模式展示来源原文和结构化事实，不能称为AI诊断。复杂程度自动升级必须有保守预算限制，首版不让模型无限反思、重试或多模型互审。

生产版本需要共享 Provider 状态（数据库/既有共享存储）及基于LiteLLM的权威预算；当前 Router 草稿的进程内 cooldown 仅用于故障抑制，不足以交付严格跨副本额度控制。数据允许目标集合先由策略过滤，fallback只能在该集合内进行。

### 资源决策与状态机

资源管理属于独立的确定性控制器，不由LLM决定，不在聊天请求中直接创建Pod。

```
DISABLED → WAITING_FOR_IDLE → STARTING → READY → DRAINING → STOPPED
                           ↘ ERROR       ↘ ERROR
```

- WAITING_FOR_IDLE：查看全部平台提交入口、Kueue待准入/资源预留、实际GPU Pod、工作区/评估/推理资源需求及专属节点约束；只看DCGM利用率不充分。
- STARTING：租约与资源登记先完成；仅申请可回收整卡。资源状态变化/训练需求出现时，即使权重仍在加载也应停止本次启动。
- READY：模型健康、规定输出/中文检索任务质量验证通过后才被路由选中；运行中持续校验租约、限额、等待训练及节点压力。
- DRAINING：先从路由摘除、停止接新请求；允许有上限的当前请求排空，超过期限取消推理请求。随后停止自有Pod并释放Kueue预留，不以scale=0回执直接宣称释放显存。
- STOPPED：确认自有Pod终止与GPU分配/预留释放，才报告可重新准入。等待训练消失、达到最短空闲窗口后再启动，避免频繁冷热切换。
- 控制器不可用/租约过期/集群状态未知：不新增推理；运行推理按预先设计的fail-closed租约收尾。其他用户任务不作为回收对象。

建议首轮参数（不是已验证承诺）：最多1个单卡副本、连续空闲10分钟才启动、推理摘流后至多15秒请求排空、30秒Pod优雅终止；训练新增准入延迟目标需端到端实测后确定，不能把15+30秒直接当硬上界。驱动/存储卡住时有告警与人工处理，不杀未知GPU进程。

### 训练优先的正确隔离

1. 不开启训练之间的抢占。当前全局Never策略不能直接改成LowerPriority来容纳助手，因为可能扩大到现有训练。
2. 推理进入独立低优先级、可借用、可撤销的GPU资源域，GPU名义保留为0的设计需按安装版本验证；训练保留既有配额/优先关系。
3. 一旦采用cohort借用/回收，要明确账本里的CPU、内存、GPU与ResourceFlavor/节点集合完全一致。不能在同一物理池额外声明一份GPU容量。
4. 为多机训练保留可用的节点组合。单卡模型优先放到已有碎片节点，避免拆散整节点；训练入队时仍必须重新评估拓扑并回收，不能仅凭总空闲卡数决策。
5. 所有GPU消费者必须进入统一需求视图，包括交互调试、独立评估、模型服务及其他提交入口。未纳入的外部任务意味着无法给“训练永不受阻”保证。
6. 首版不使用GPU time-slicing、MPS或与训练共享同一GPU。不同GPU同节点仍可能争用CPU、内存带宽、网卡、磁盘与功耗，需独立限制并监测。
7. 推理和模型预热使用独立受控缓存；不扫描训练数据，不把训练文件/代码送给模型，不更改原Base镜像。

### 模型与推理引擎选择门槛

先确认目标闲时节点实际GPU显存、架构、驱动和可获取模型许可证。以能单卡部署的中文指令模型候选进行评测，再固定模型权重摘要、量化格式、服务镜像与最大上下文。具体名称/量化/并发现在不写死。服务提供OpenAI-compatible接口；vLLM类服务或其他引擎按该模型的兼容矩阵选，不承诺所有GPU/量化均支持。

不是空闲卡越多就启动越多实例：按实际排队请求与目标延迟扩容，设置最大副本/GPU数；常见说明命中可以只检索。先单卡，再评估多个单卡副本；首版不使用跨节点张量并行模型占用完整训练拓扑。

### 必须经过的资源验收

使用专用测试资源，绝不取消真实用户训练。先离线/隔离模拟队列并发，再在批准窗口真实验证：

- 空闲时模型启动、热身、回答；无额度时明确走本地或检索。
- 单卡与多节点整组训练进入时，推理摘流、取消/排空、Pod退出、GPU分配释放、训练完整准入；记录各阶段耗时与UID。
- 训练已经运行时启停推理，比较固定负载的step时间、P95数据等待、CPU/内存/磁盘/网络及GPU压力；不能只看“训练仍RUNNING”。
- CPU/内存不足、节点NotReady、驱动异常、镜像拉取失败、权重加载中出现训练、控制器重启/断联等失败路径。
- 训练任务重复进入/退出不造成Pod重启风暴；取消聊天不取消训练；API仍无额度时不反复付费探测。
- 所有来源和降级状态可在UI解释；真实响应与检索引用通过相同的隐私/越权测试。

### 下一步所需信息

不再要求当前无额度Key。后续真实实施前需要：允许用于助手的GPU池/节点范围、对训练新增准入等待的可接受上限、可用模型权重与许可证或获准候选选择、公司Key恢复时的模型列表与预算恢复策略。若“零新增等待”是硬约束，则明确选择专用推理资源/API/检索，不在共享训练池实现保证做不到的承诺。

## 本轮草稿状态（非验收结果）

- 本地后端隔离分支codex/platform-assistant-20260922：d3351f3仅提交设计/类型与RED测试，未推远端；Provider和API草稿尚未整合验证。
- Portal隔离工作树 /tmp/rtp-assistant-portal-20260922：悬浮组件、状态工具与合同草稿未提交、未测试。
- builder /tmp/rtp-assistant-red-20260922：新增Router测试已确认实现前RED（缺少实现符号）；没有GREEN/完整回归，不能报告功能完成。
- 生产后端、Portal部署、配额、Kueue策略和运行中训练均未修改；没有调用收费模型或创建GPU工作负载。

## 后续授权补充：RayService 与 DeepSeek 最小验证

用户随后明确建议 Ray Serve/RayService 闲时推理，并提供临时 DeepSeek 凭据，授权测试 api.deepseek.com 的 deepseek-flash。仅本节覆盖前文「没有调用收费模型」的当时状态；没有据此部署页面助手或修改GPU调度。

- 构建机 `kubectl get rayservices.ray.io -A`：No resources found；CRD已有v1、v1alpha1。
- 控制器镜像只读核对：KubeRay v1.6.2、Kueue v0.19.0；feature gate与RayService弹性准入尚未验收。
- DeepSeek `/models`：HTTP200，确认 deepseek-flash 可用。
- `/chat/completions`：一次不含业务数据的“Reply with exactly OK”请求，关闭思考，最多64输出token，HTTP200、实际回答OK；prompt20/completion1/total21token。单次耗时0.75秒，不作为性能SLA。
- 凭据通过不回显输入只进入进程内存，不写脚本、文件、仓库、镜像、平台Secret或文档，不复用到其他站点。临时脚本在验证后删除。
- 此次只证明用户指定API可调用；页面端到端、权限问答、真实日志模型处理、故障降级和本地GPU推理仍未验收。临时个人Key不自动成为全平台生产凭据。

### RayService 架构选择

本地推理采用独立RayService，由KubeRay管理独立RayCluster；不挂入任一训练任务的RayCluster，不与正在训练的Worker复用进程。Ray Serve负责请求分发、模型副本健康和并发；单卡LLM执行引擎按权重选择。助手后端对外部API和内部Serve使用相同Provider抽象。

区分三层伸缩：

1. Serve副本/Actor伸缩至0，释放Ray逻辑资源与模型进程；并不证明GPU Worker Pod已经退出。
2. Ray/KubeRay worker伸缩或受控集群回收，确认GPU Pod和资源请求真正消失。
3. Kueue解除对应资源预留，训练工作负载再获准入。不能只在Ray内部看见空闲GPU就报告Kubernetes已释放。

训练到来时不等待普通的“聊天请求少了才缩容”：控制器先摘除Provider，再阻止Serve因新请求重新唤醒；经受控回收释放自有GPU Worker与队列预留。健康自愈、自动扩容和训练退让必须共享同一运行意图，防止删掉Worker后RayService立即补回。RayService滚动升级可能短暂存在新旧RayCluster，因此禁用隐含双份GPU占用，升级方案要计入资源上限或采用可接受中断的受控重建。

RayService+Kueue的具体弹性集成必须按已安装KubeRay/Kueue版本及feature gate核对，不能直接照搬latest文档。官方当前说明由RayService把队列元数据传播给其RayCluster，Kueue准入底层RayCluster；部分弹性能力仍有版本/实验feature要求。

来源：https://docs.ray.io/en/latest/cluster/kubernetes/k8s-ecosystem/kueue.html ，https://docs.ray.io/en/latest/serve/autoscaling-guide.html ，https://api-docs.deepseek.com/zh-cn/ 。
