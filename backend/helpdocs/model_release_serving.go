package helpdocs

import "ray-train-platform-backend/domain"

const (
 modelRegistryArticleID = "model-registry-link"
 modelReleaseArticleID = "model-release-review"
 modelServingCodeArticleID = "model-serving-code"
 modelServingUseArticleID = "model-serving-use"
 modelServingErrorsArticleID = "model-serving-errors"
)
func modelReleaseServingDocuments()[]domain.HelpDocument{
 return []domain.HelpDocument{
  {ID:modelRegistryArticleID,Title:"共享模型如何关联到 MLflow Model Registry？",Category:"MLflow 与 API",SortOrder:450,Markdown:modelRegistryGuide,Version:1,PublishedVersion:1,UpdatedBy:PlatformSeedActor,Action:"summary"},
  {ID:modelReleaseArticleID,Title:"模型如何申请审核、正式发布和回滚？",Category:"调试与训练结果",SortOrder:390,Markdown:modelReleaseGuide,Version:1,PublishedVersion:1,UpdatedBy:PlatformSeedActor,Action:"summary"},
  {ID:modelServingCodeArticleID,Title:"如何准备离线推理代码和匹配的运行方案？",Category:"代码、环境与数据",SortOrder:180,Markdown:modelServingCodeGuide,Version:1,PublishedVersion:1,UpdatedBy:PlatformSeedActor,Action:"summary"},
  {ID:modelServingUseArticleID,Title:"如何启动、调用、停止和切换模型推理服务？",Category:"调试与训练结果",SortOrder:395,Markdown:modelServingUseGuide,Version:1,PublishedVersion:1,UpdatedBy:PlatformSeedActor,Action:"summary"},
  {ID:modelServingErrorsArticleID,Title:"推理服务排队、未就绪或调用失败怎么办？",Category:"常见故障",SortOrder:550,Markdown:modelServingErrorsGuide,Version:1,PublishedVersion:1,UpdatedBy:PlatformSeedActor,Action:"summary"},
 }
}

const modelRegistryGuide = `共享模型版本和 MLflow Model Registry 是两个不同记录。保存权重后，在「实验中心 → 模型」打开 READY 版本，展开“发布审批与 MLflow 注册表”，点击“同步到 MLflow Registry”将该快照登记到原生 Registry。模型所有者和平台 SuperAdmin 可以执行关联，其他成员可以查看已有结果。

### 关联后能看到什么

页面保留注册模型名称、Registry 版本、对应 Run 和 Artifact 来源。打开 MLflow 后可以按该名称查看注册版本，也可以用原生 MLflow SDK 查询。关联过程复制经过大小和 SHA-256 校验的权重，不覆盖原权重，不改写训练 Run 或模型版本来源。任务和 Run ID 仍不要求相等。

等待关联完成后再使用页面返回的确切名称和版本。失败时查看原因并重试同一个版本；不要手工连续创建多个 Registry 版本来碰运气。其他程序通过原生 API 删除或修改 Registry 记录时，平台已有的关联记录不会自动变成一次新的发布审批，需核对实际 Registry 状态。

### 关联不等于部署

这里登记的是权重快照及来源。只有权重文件并不自动具备 MLflow flavor、模型结构、预处理或预测入口，不能因此直接认为它可被 mlflow models serve 加载。业务模型需要匹配的[推理代码和运行方案](#model-serving-code)。

Registry 可用于组织原生模型版本；RayTrain 的[审批、正式发布与回滚](#model-release-review)保留独立的评估证据和审核历史。原生 Registry 的 alias 或 stage 改动不会代替这些审核，也不会自动创建 GPU 服务。

原生 API 接入沿用[MLflow API 指南](#mlflow-api-with-pat)，个人 PAT 选择 mlflow:full。不要把推理服务调用权限和 MLflow 管理权限混在一起。`

const modelReleaseGuide = `发布流程把“选中的权重已经有可靠的评估证据”与“现在把哪一版作为正式版本”分别记录。GPU 服务还需要单独启动。

### 申请与审核

1. 在「实验中心 → 模型」选择未归档模型的 READY 版本，先完成[独立评估](#model-evaluation-start)。用于申请的记录必须执行成功且报告有效，并且对应这个确切版本。
2. 模型所有者或平台 SuperAdmin 发起发布申请，选择评估记录并填写理由。平台固定模型版本、权重 SHA-256、评估 ID 和报告 SHA-256；新评估不会替换已经提交的证据。
3. 由具有权限的其他人审核。审核者必须是申请所属团队的 TenantAdmin 或平台 SuperAdmin，并且有权读取评估证据。申请人和模型所有者都不能自审，SuperAdmin 也不能绕过这条规则。
4. 同意或拒绝都要填写理由。拒绝后原记录保留，可以修正问题后提交新申请；不能把旧拒绝记录改成同意。页面提示记录已变化时先刷新，核对最新状态再操作。

使用 PUBLIC 数据的评估证据可供平台成员查看；TEAM 证据仅所属团队和平台 SuperAdmin 可读。共享权重不会公开 TEAM 评估报告，申请人换到其他团队也不会因此扩大证据范围。

### 正式发布与回滚

审核通过后，模型所有者或平台 SuperAdmin 可以将获批版本设为正式发布版本，并填写发布理由。历史会记录操作人、时间、原版本与目标版本。

需要回滚时，从同一模型已经获批的历史版本选择目标，确认理由后切换正式发布指针。回滚保留新的操作记录，不删除后来版本，不改写评估报告。归档模型不能发起新发布，恢复后再操作。

**审核通过、正式发布和推理服务部署是三个步骤。** 正式版本切换或回滚不会自动重启正在运行的服务，也不会替换服务使用的权重。要切换在线服务版本，请按[服务使用指南](#model-serving-use)停止旧服务、等待资源回收，再启动目标版本；当前切换会中断服务。`

const modelServingCodeGuide = `推理方案固定能够加载某种模型的代码、依赖镜像、入口和输入输出约定，页面中称为“推理契约”。普通用户准备代码和样例后，请具有登记权限的维护者在「实验中心 → 推理服务 → 推理契约」登记方案。共享方案由平台 SuperAdmin 登记和启停；本篇只说明提交哪些内容，不要求用户操作集群。

### 准备模型适配代码

先从[项目推理示例目录](https://gitlab.wellspiking.ai/guofeng.su/ray-train-platform/-/tree/main/examples/model-serving)取得 serving_sdk.py。可以在本机或构建机准备源码，训练节点不需要访问 Git 或外网。业务模型另外提供 model_adapter.py，实现真实的权重加载、预处理、预测和结果转换；依赖必须已存在于选定镜像中。

下面的 serve.py 调用你自己实现的 load_model 与 predict。它们的输入输出要与方案登记的 JSON 样例一致；平台不会猜测 .pth、.onnx 或 .safetensors 的模型结构：

` + "```python\nimport tempfile\nfrom pathlib import Path\nfrom serving_sdk import ServingClient\nfrom model_adapter import load_model, predict\n\ndef main():\n    client = ServingClient.from_environment()\n    with tempfile.TemporaryDirectory(prefix='model-serving-') as directory:\n        weights = client.download_model(Path(directory) / 'weights.bin')\n        model = load_model(weights)\n        # predict 返回可序列化的 JSON 对象或数组。\n        # 真正加载成功后才开始提供健康检查和推理接口。\n        client.serve(lambda payload: predict(model, payload))\n\nif __name__ == '__main__':\n    main()\n```" + `

SDK 使用平台注入的任务专属凭据下载本次固定权重，校验大小与 SHA-256，再提供 model-serving-http/v1 的健康检查和 JSON 推理端点。代码里无需个人 PAT，也不要输出任务凭据或读取无关私人目录。

### 打包与登记

源码 ZIP 根目录至少包含 serve.py、serving_sdk.py、model_adapter.py 及适配器需要的其他源码；不要多套一层目录导致入口找不到。ZIP 不超过 64 MiB，排除权重、数据集、密钥、.git 和本地虚拟环境。

向维护者提供：源码 ZIP、可用的已登记运行镜像、入口 python serve.py、一份真实且无敏感信息的输入 JSON，以及输出字段的说明。由维护者上传 ZIP 并登记不可变方案。代码通过内网取得，不打进训练镜像；修改代码需要上传新 ZIP 并登记新方案，历史服务的代码和权重不会被覆盖。

输入和输出各不超过 1 MiB；输入使用 application/json。SDK 一次处理一个推理请求，适配器应在请求超时内返回 JSON，不能把长期批处理伪装成同步推理。

示例目录中的 smoke_adapter.py 只做文件校验与协议回显，不执行真实模型预测。它用于验证通路，不证明模型精度、预处理正确或业务上线条件已经满足。`

const modelServingUseGuide = `在[实验中心 → 推理服务](https://spiking-dev.wellspiking.ai/raytrain/rayTrain/experiments?tab=services)查看服务，或从模型已获批的版本开始创建。所有平台成员可查看共享服务；创建需要模型维护权限，停止需要服务创建者或平台 SuperAdmin 权限。模型批准记录的 TEAM 评估证据仍受原有权限保护。

### 启动一个服务

1. 选择已获批、未归档模型的 READY 权重版本和匹配的已启用推理方案。单独上传权重或关联 Registry 还不够。
2. 填写名称、CPU、内存及存续时间。当前固定 1 Worker、1 GPU，存续时间为 1 小时至 7 天；到期会停止并回收资源。
3. 先预检固定来源与资源，再确认创建。预检不会占用 GPU；创建后进入实际提交与排队流程。服务占用当前团队计算配额，不会绕过训练队列。
4. 等待服务 READY，并执行一次符合方案输入约定的实际调用。健康检查只证明对应版本的服务可响应，不替代业务输出核对。

### 在页面实际调用

服务就绪后，在调用区域填入该方案的 JSON 输入并提交，查看实际返回。不要将未知字段或大型文件直接粘贴进去。一次请求与响应各不超过 1 MiB；需要图片、点云或其他复杂输入时，由方案维护者明确可接受的编码和大小。

如果需要在登录后的 Portal 同域页面中集成调用，使用当前会话和带 /raytrain 前缀的地址；下面需要替换真实服务 ID 和输入结构：

` + "```javascript\nconst serviceId = 'REPLACE_SERVICE_ID';\nconst response = await fetch(\n  `/raytrain/api/v1/model-services/${encodeURIComponent(serviceId)}/invocations`,\n  {\n    method: 'POST',\n    credentials: 'same-origin',\n    headers: { 'Content-Type': 'application/json' },\n    body: JSON.stringify({ inputs: 'REPLACE_WITH_CONTRACT_INPUT' }),\n  },\n);\nconst result = await response.json();\nif (!response.ok) throw new Error(result.error?.message || `HTTP ${response.status}`);\nconsole.log(result);\n```" + `

成功返回的是模型的原始 JSON，不保证带平台的 data 包装；应按推理方案的输出字段读取。

### 自己的程序如何调用

在「账户与安全 → 个人访问令牌」自行创建“模型推理调用”用途、包含 models:invoke 的 PAT。令牌用于调用共享推理服务；它不会获得模型审批、修改、服务启停或 MLflow 全局读写权限，已有 token 也不会自动新增这个 scope。

将令牌安全传入环境变量 RAYTRAIN_PAT；不要写进源码或发给其他人。程序使用生产域名的标准 API 地址，不加 Portal 的 /raytrain 前缀：

` + "```bash\ncurl --fail-with-body --silent --show-error \\\n  -X POST 'https://raytrain.wellspiking.ai/api/v1/model-services/REPLACE_SERVICE_ID/invocations' \\\n  -H \"Authorization: Bearer ${RAYTRAIN_PAT}\" \\\n  -H 'Content-Type: application/json' \\\n  --data-binary @request.json\n```" + `

request.json 必须使用该方案的真实输入结构。服务 ID 与 MLflow Run ID、平台 Job ID 都不同；不要把 MLflow Tracking URI 当作此推理接口。MLflow SDK 接入见[MLflow API](#mlflow-api-with-pat)。

### 停止与切换版本

不用时主动点击停止，等待状态确认停止并完成资源回收。到期或失败时也要核对状态；仅关闭浏览器不会停止服务。

当前同一模型仅允许一个未回收服务。切换采用先停后启：停止旧服务、等待回收、选择目标获批版本与方案、重新启动并实际调用验证。这会中断服务，而且新服务 ID 和调用地址会改变；调用方需要更新地址。正式发布指针变化不会自动更新正在运行的服务。`

const modelServingErrorsGuide = `先到「实验中心 → 推理服务」记录服务 ID、状态、模型版本、方案和错误信息。有权查看底层任务时再打开任务日志。其他成员可调用共享服务，不表示可以读取服务创建者的私人日志或输出目录。

| 现象 | 检查与处理 |
| --- | --- |
| 已创建但一直排队 | 查看团队 GPU 配额、任务准入与节点资源；服务和训练共用配额，不会因网页关闭而释放 |
| 模型已有未回收服务 | 先停止该模型现有服务并等待回收，再创建下一条；不要反复点击创建 |
| 代码包或入口错误 | 确认 ZIP 根目录、入口文件和镜像依赖；更新需要新方案 |
| 权重下载或 SHA-256 校验失败 | 核对服务固定的 READY 权重版本、文件大小与保存状态；不要跳过摘要校验继续加载 |
| 一直未就绪 | 检查模型实际加载、端口与方案协议；大模型加载需要时间，应用应加载成功后再提供健康响应 |
| 401 / 403 | 检查会话、令牌到期、当前团队及 models:invoke scope；管理操作还需要各自角色权限 |
| 413 / 415 | 输入过大或类型不符；请求必须是最多 1 MiB 的 application/json |
| 429 | 请求过于频繁或并发已满，遵循 Retry-After，稍后重试 |
| 模型错误、无响应或超时 | 核对输入与方案约定、GPU 显存和适配器异常；同步调用约 30 秒超时，缩小请求或调整业务实现 |
| 响应无效 | 推理代码必须返回最多 1 MiB 的有效 JSON，不支持返回网页、文件流或无限日志 |
| 服务已停止或到期 | 旧调用地址不能继续使用；需要重新创建并更新调用方的服务 ID |

健康检查会核对服务身份与权重 SHA-256，但“健康 / READY”不代表模型精度达标。文件校验、模型实际加载、实际推理输出和独立评估指标是不同证据。请用自己明确预期的样本核对输出，再决定是否提供给业务使用。

如果创建请求遇到暂时网络错误，保留原页面和请求重试，先查看是否已经存在服务记录。不要通过不断重新开窗重复提交。资源异常时仅停止本人的目标服务；不要取消其他人的训练来腾卡。`
