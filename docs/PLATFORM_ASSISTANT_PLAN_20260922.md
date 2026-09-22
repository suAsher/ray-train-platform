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

首版采用 OpenAI-compatible Chat Completions 协议。最多配置4个固定后端，每个有独立ID、模型、地址、Secret和API/本地类型。LiteLLM、兼容云API、兼容自建Ray Serve可共用适配器；供应商原生非兼容协议需要新增适配器，不能声称通用支持所有协议。

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
      baseURL: https://litellm.westwell-lab.com/v1
      model: <管理员确认的模型名>
      existingSecret: raytrain-assistant-company
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

- Router新增测试已在实现前确认RED；后续GREEN、全量回归和浏览器验收证据见本轮交付记录。
- Helm测试：默认关闭时渲染清单与基线完全一致；开启测试后只有后端assistant环境变量和Secret引用变化。
- 真实DeepSeek最小连通性：models HTTP200且指定模型可用；一次不含业务数据的问答HTTP200/OK，总21token。仅证明外部接口可调用，不代表页面端到端或生产凭据已配置。
- LiteLLM模型列表无Key为401；暂无专用有预算Key，不能声称公司接口联调完成。
- GPU推理部署、资源回收、真实业务问答质量、流式输出、多轮记忆、跨副本预算状态与写操作均未包含在首版交付。
- 生产后端、Portal、数据库schema、配额、调度和运行中训练本轮未改动。后续发布需重新核对四端及最小差异。

官方参考：[KubeRay与Kueue](https://docs.ray.io/en/latest/cluster/kubernetes/k8s-ecosystem/kueue.html)、[Serve伸缩](https://docs.ray.io/en/latest/serve/autoscaling-guide.html)、[LiteLLM虚拟Key](https://docs.litellm.ai/docs/proxy/virtual_keys)、[Kubernetes抢占](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/)。
