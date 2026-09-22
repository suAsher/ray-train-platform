# RayTrain 页面助手与多模型路由实施方案

日期：2026-09-22。用户已同意页面助手首版实施；新增空闲 GPU 推理要求先完成调度方案，不默认启用抢占或启动 GPU 服务。

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
