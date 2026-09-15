package helpdocs

import "ray-train-platform-backend/domain"

const modelRegistrationArticleID = "shared-model-registration"
const modelMaintenanceArticleID = "shared-model-maintenance"

// Keep the original source catalog intact. Published documents with these IDs
// take precedence over the built-in guides, including manually edited content.
func withModelLifecycleDocuments(documents []domain.HelpDocument) []domain.HelpDocument {
	out := make([]domain.HelpDocument, 0, len(documents)+9)
	out = append(out, documents...)
	seen := make(map[string]bool, len(documents))
	for _, document := range documents {
		seen[document.ID] = true
	}
	for _, document := range modelLifecycleDocuments() {
		if !seen[document.ID] {
			out = append(out, document)
		}
	}
	return out
}

func modelLifecycleDocuments() []domain.HelpDocument {
	return append([]domain.HelpDocument{
		{ID: modelRegistrationArticleID, Title: "训练权重如何保存为共享模型或同步到功能仓？", Category: "调试与训练结果", SortOrder: 350, Markdown: modelRegistrationGuide, Version: 1, PublishedVersion: 1, UpdatedBy: PlatformSeedActor, Action: "summary"},
		{ID: modelMaintenanceArticleID, Title: "模型谁能看，如何维护与归档？", Category: "调试与训练结果", SortOrder: 360, Markdown: modelMaintenanceGuide, Version: 1, PublishedVersion: 1, UpdatedBy: PlatformSeedActor, Action: "summary"},
		{ID: modelEvaluationStartID, Title: "如何用固定数据版本评估一个模型？", Category: "调试与训练结果", SortOrder: 370, Markdown: modelEvaluationStartGuide, Version: 1, PublishedVersion: 1, UpdatedBy: PlatformSeedActor, Action: "summary"},
		{ID: modelEvaluationResultsID, Title: "评估报告在哪里看，为什么不能比较？", Category: "调试与训练结果", SortOrder: 380, Markdown: modelEvaluationResultsGuide, Version: 1, PublishedVersion: 1, UpdatedBy: PlatformSeedActor, Action: "summary"},
	}, modelReleaseServingDocuments()...)
}

const sharedModelsPublicSection = `### 共享模型入口

「实验中心 → 模型」用于保存和查看跨团队共享的模型与权重版本。先阅读[训练权重如何保存为共享模型或同步到功能仓？](#shared-model-registration)，维护权限和归档操作见[模型谁能看，如何维护与归档？](#shared-model-maintenance)。`

const modelArtifactsSupplement = `### 保存为共享模型版本

需要让其他成员使用某个权重时，可将本人已结束任务中的所选权重保存成共享模型版本。平台仅复制所选文件，原文件和原目录的权限保持不变。操作见[训练权重如何保存为共享模型或同步到功能仓？](#shared-model-registration)；共享范围见[模型谁能看，如何维护与归档？](#shared-model-maintenance)。`

const modelRegistrationGuide = `在 Portal「实验中心 → 模型」查看共享模型。模型用于组织同一用途的权重；每个版本保存一次所选权重文件的独立快照及其来源。保存前确认这个文件适合向平台全部成员共享。

### 从训练产物登记

1. 打开本人训练任务的详情，在训练产物中选择一个权重文件。任务必须已经结束：成功、失败、取消或超时均可，运行中的任务需等待结束。失败任务留下的权重是否有效，需要自行判断。
2. 点击“登记模型版本”，创建模型或选择自己有权维护的现有模型，填写模型名称、模型说明和版本说明。归档模型需先恢复才能新增版本。
3. 确认所选文件和来源，点击“登记并复制”；提交后可通过“查看模型版本”检查进度。支持 .pth、.pt、.ckpt、.onnx、.safetensors；一次版本只保存一个所选文件。后续有新权重时登记新版本。

登记只复制所选文件，保留原文件，不共享原目录或其中的其他文件。复制完成前请保留来源文件并避免继续修改它。登记到他人的模型需要模型维护权限；即使有维护权限，也只能选择本人任务的权重作为来源。

### 等待复制并核对下载

快照按 8 MiB 分片复制，单个权重上限为 20 GiB。大文件需要等待复制和校验完成。

| 状态 | 含义与下一步 |
| --- | --- |
| PENDING | 已接收登记，等待复制 |
| COPYING | 正在复制并计算文件摘要，稍后刷新查看 |
| READY | 快照已完成，可以下载，并核对文件大小和 SHA-256 |
| FAILED | 快照失败，查看页面原因，确认来源文件仍存在且可读后，回本人任务重新登记 |

只有 READY 版本可使用“下载权重副本”。SHA-256 用来确认下载文件与保存的快照一致；它不代表模型精度已经通过评估。可在本地用 sha256sum（Linux）或 shasum -a 256（macOS）计算摘要，与版本详情的 SHA-256 比较。

### 数据集和来源如何记录

已有训练数据版本记录会沿用到模型版本，不能覆盖为另一个版本。历史任务没有记录时显示“未知 / 未登记”，不会从文件名或目录猜测数据集。

如果确实知道历史权重使用的数据，可在登记时选择自己有权访问的 READY 数据集版本；该关联标为“用户补充”，与训练时自动记录的来源区分。它不会改写历史任务，也不能证明训练当时使用的就是这个版本。无法确认时保留未知。

版本保留任务、可用的关联 Run、代码和数据等来源信息；缺失信息保持缺失。有关文件与来源的维护边界，见[模型谁能看，如何维护与归档？](#shared-model-maintenance)。

### 如何同步到功能仓

入口统一为「我的训练任务 → 本人任务详情 → 训练产物 → 同步到功能仓」。同步到的是[正式功能仓](https://spiking.wellspiking.ai/modelF/function)，无需选择环境。根据任务状态，按下面两条流程操作。

| 你想做什么 | 何时配置 | 点击哪个按钮 | 何时上传 |
| --- | --- | --- | --- |
| 手动同步：训练结束后挑选已有文件 | 任务已结束 | 开始同步 | 提交后后台开始处理 |
| 自动同步：预先指定训练成功后要上传的文件 | 任务运行中 | 保存自动同步 | 仅本任务成功结束后开始处理 |

自动同步需要为每个任务主动保存配置，不会默认上传所有训练产物。不需要先手工登记共享模型，也不需要先申请模型发布。

### 同步前需要准备什么

- **本人任务与目标权限**：当前团队下的本人训练任务；公司账号已加入功能仓对应团队，并拥有目标仓写权限。功能仓与其中的目标模型需预先创建，列表为空或不能选择时联系功能仓负责人。
- **MLflow 来源**：训练需记录可验证的关联 Run。可在任务详情或「实验中心 → 训练记录」检查关联；接入方法见[如何向 MLflow 记录训练参数和指标？](#mlflow-framework-metrics)。最终只能解析到一个确定的来源 Run，多个候选不能由平台猜选。
- **同步文件**：将需要同步的文件写入本任务输出目录，并确认可以共享。不限文件后缀：模型文件、config、yaml/yml、JSON、Python 配置、说明文件及无后缀文件均可选择；每次 1–8 个非空文件，单文件不超过 20 GiB，不支持目录或通配符。同一次不能选同名文件，即使位于不同子目录。

只需使用公司 SSO 登录页面，无需另建 PAT、复制令牌或填写接口地址。功能仓权限与训练平台角色分别管理。

### 手动同步：训练结束后选择文件

1. 打开本人已结束任务的「训练产物」。成功、失败、取消或超时任务均可；失败任务留下的 checkpoint 是否有效，由你确认。
2. 在要同步的文件行点击“同步到功能仓”，路径会带入表单；也可在下方同步区域点击“新建同步”，手动填写文件路径。
3. 依次选择“功能仓”和“目标模型”，填写“版本名称”，例如 epoch-12；检查“文件路径”，多个文件每行一个，可将模型与配置一起同步。
4. 确认页面显示“任务已结束”，点击“开始同步”。提交后在同一页面的同步记录中查看进度，必要时点击“刷新记录”；无需先下载到本机再上传。
5. 记录显示“同步成功”后，核对文件和三个来源 ID，点击“打开功能仓”，查看所选目标模型下的新版本及文件。

例如训练输出中已有 checkpoints/epoch_12.pth，选择该文件同步成 epoch-12；后续还想同步 checkpoints/best.pth，可以“新建同步”另建 best 版本。每次主动新建都允许生成新版本，不会覆盖之前同步的版本。

### 自动同步：运行中配置，成功后上传

1. 正常提交训练后，趁任务仍在运行，打开该任务「训练产物」下的“新建同步”。自动同步配置在任务详情中保存。
2. 选择“功能仓”“目标模型”，填写“版本名称”和预计生成的“文件路径”，例如 checkpoints/best.pth。此时文件可以尚未生成，但训练结束时必须确实存在。
3. 确认页面显示训练成功后自动同步，点击“保存自动同步”。看到保存成功提示及同步记录，才表示配置已保存；只填表单或点击“收起”不会保存。
4. 可关闭浏览器。平台等待本任务成功结束，再复制、校验、上传指定文件，并登记功能仓版本。它只处理你填写的文件，不会自动寻找“最优权重”或持续同步每个 checkpoint。
5. 训练结束后回到同步记录查看结果。“同步成功”后点击“打开功能仓”；如果显示“等待重新授权”，按下面的故障处理登录并继续，无需重新训练。

若任务失败、取消或超时，这份自动配置不会上传。需要保留其 checkpoint 时，在已结束任务中主动“新建同步”，走手动流程。若打开表单时训练已经结束、按钮显示“开始同步”，此时进行的是手动同步。

### 文件路径怎么填写

“文件路径”相对于本任务输出目录，不是电脑上的文件路径，也不是完整挂载路径。假设代码把文件写到 PLATFORM_OUTPUT_PATH 下的 checkpoints/best.pth，表单填写 checkpoints/best.pth；直接写在输出目录根下的 model.pt，就填写 model.pt。不要填写变量名 PLATFORM_OUTPUT_PATH、目录、*.pth 或 /mnt/data/ 开头的完整路径。

例如同一次同步填写 checkpoints/best.pth、configs/train.yaml 和 README（每行一个），三份文件会登记到同一个功能仓版本并关联相同的三个 ID。配套配置必须也位于本任务输出目录；只有源码包里存在、未写入输出目录的配置不会自动被带上。平台只复制文件，不执行 Python 配置，也不解析或转换 YAML。

自动配置前先确认训练代码真正保存的位置和文件名；例如代码只生成 epoch_12.pth，就不能期望平台自动生成 best.pth。修改已保存配置中的文件、版本名或目标时，需要新建同步；不再需要的旧配置可在按钮可用时点击“取消同步”，避免两份配置都触发。

### 文件如何到达功能仓，怎样才算成功

两条流程使用同一条后台处理链路：**任务结束并满足同步条件 → 核对任务与 Run → 创建共享文件副本并校验 → 上传功能仓 → 创建目标版本 → 查询并核对文件和三个 ID → 同步成功**。

浏览器负责选择和发起，文件由训练平台后端从文件副本传给功能仓，不占用额外训练 GPU。大文件使用分片上传，需要等待处理。复制完成前请保留原文件并避免修改。平台自动生成的共享模型副本可在「实验中心 → 模型」找到；副本对平台成员共享，原文件和原目录权限不变，目标仓仍按自身权限控制访问。

只有文件上传成功还不够；必须完成版本登记与来源读回核对，记录才显示“同步成功”。这表示文件和来源已入功能仓，不代表模型已评估、已转换、已进入发布仓或已启动推理服务，这些操作需分别进行。

### 三个 ID 是什么，在哪里确认

| 同步记录 | 功能仓接口字段 | 含义 |
| --- | --- | --- |
| Job ID | jobId | 训练平台任务 ID |
| Run ID | runId | 本任务确定关联的 MLflow Run ID |
| Experiment ID | experimentId | 该 Run 所属实验 ID |

三者自动关联，无需手填，也不要求相同。同步记录同时显示“功能仓版本 ID”，用于确认这一次上传创建的目标版本。同一个 Job / Run 可以有多次同步，每次选择不同文件或创建不同版本。

当前在训练平台的同步记录中核对三个 ID；功能仓后端已保存并返回这些字段，但其“模型查看”页面尚未展示三个 ID。不要因目标页面没显示而认为未关联。

历史任务没有 Run，或存在多个 Run 无法确定来源时，不能完成要求三个 ID 的同步。不会自动补建或随意绑定最近的 Run；请按 MLflow 接入说明准备后续训练。历史权重仍可按本文前半部分登记为共享模型，缺失来源保持缺失。

### 同步失败或授权过期怎么办

| 页面状态或问题 | 你需要做什么 |
| --- | --- |
| 等待训练成功及文件就绪 | 先查看训练状态；成功结束后还需等待副本复制，避免反复新建同步 |
| 排队同步 / 上传文件中 / 登记功能仓版本中 | 等待并刷新原记录，不重复点击新建；大文件处理时间更长 |
| 等待重新授权 | 重新通过公司 SSO 登录，在原记录点击“重新授权并继续”；长时间训练可能遇到授权过期，无需重新训练或手工提供令牌 |
| 功能仓为空、目标模型为空或无写权限 | 检查目标仓和模型是否已创建，必要时加载更多功能仓；请负责人确认团队成员及写权限后重新加载 |
| 同步失败 | 先核对原文件是否存在、路径是否正确、MLflow 来源是否明确、目标权限是否有效；修正后点击“重试同步”。需要更换文件或共享快照已失败时，回任务“新建同步”生成新副本 |
| 创建结果待确认 | 功能仓可能已创建版本，先“打开功能仓”核对；此状态不会自动重复创建，也没有普通重试按钮。无法确认时提供任务 ID、同步记录和目标版本信息联系支持，避免盲目新建 |

“重试同步”用于继续原记录；“新建同步”代表主动创建另一份记录与版本。可在按钮可用时“取消同步”，取消不会停止训练；进入版本登记阶段后不能承诺撤销外部已经发生的创建。归档平台模型或取消同步不会自动删除功能仓中的文件或版本。`

const modelMaintenanceGuide = `「实验中心 → 模型」向平台全部成员开放跨团队查看。成员可以浏览模型和版本，并下载 READY 权重快照；全员可读不代表全员可修改。可用“全部 / 我创建的”筛选模型，再通过“查看详情”打开版本列表。

### 谁可以维护

模型创建者和平台 SuperAdmin 可以修改模型名称、模型说明、版本说明，并归档或恢复模型。普通成员只能维护自己创建的模型。版本登记也需要模型维护权限，且来源权重必须来自本人已结束的训练任务。

模型版本的权重文件和来源信息不可变。需要更换权重时新增版本，不覆盖已保存的版本；修改说明也不会改写任务、Run、代码版本或数据集来源。

### 归档与恢复

暂时不再使用的模型可以归档，并通过“查看已归档”找到后恢复。归档用于整理目录，保留模型、版本和快照；归档期间不能新增版本。恢复后可以继续登记新版本。

### 共享快照与来源权限

共享的是模型目录、版本记录和所选文件的快照。来源任务和 Run 仍受各自原有权限控制，个人原目录不会随模型共享。其他成员能够下载快照，不代表能够打开你的任务详情、浏览原始产物目录或修改来源实验。

### 与 MLflow 和后续流程的关系

RayTrain 的共享模型目录不会在保存快照时自动写入 MLflow Model Registry。需要关联时，在 READY 版本操作中主动[关联 MLflow Model Registry](#model-registry-link)，核对返回的注册名称与版本。现有原生 MLflow 页面、SDK 和 Registry 继续按原有方式使用，见[如何调用 MLflow API 查询实验与 Run？](#mlflow-api-with-pat)。

READY 只表示快照可用，不表示评估通过。需要独立评估时见[如何用固定数据版本评估一个模型？](#model-evaluation-start)。后续[审批、正式发布与回滚](#model-release-review)和[Serving 推理服务](#model-serving-use)分别操作；评估完成不表示获准发布或推理服务已经部署。`
