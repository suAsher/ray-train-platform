package helpdocs

import "ray-train-platform-backend/domain"

const modelRegistrationArticleID = "shared-model-registration"
const modelMaintenanceArticleID = "shared-model-maintenance"

// Keep the original source catalog intact. Published documents with these IDs
// take precedence over the built-in guides, including manually edited content.
func withModelLifecycleDocuments(documents []domain.HelpDocument) []domain.HelpDocument {
	out := make([]domain.HelpDocument, 0, len(documents)+4)
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
	return []domain.HelpDocument{
		{ID: modelRegistrationArticleID, Title: "如何把训练权重保存成共享模型版本？", Category: "调试与训练结果", SortOrder: 350, Markdown: modelRegistrationGuide, Version: 1, PublishedVersion: 1, UpdatedBy: PlatformSeedActor, Action: "summary"},
		{ID: modelMaintenanceArticleID, Title: "模型谁能看，如何维护与归档？", Category: "调试与训练结果", SortOrder: 360, Markdown: modelMaintenanceGuide, Version: 1, PublishedVersion: 1, UpdatedBy: PlatformSeedActor, Action: "summary"},
		{ID: modelEvaluationStartID, Title: "如何用固定数据版本评估一个模型？", Category: "调试与训练结果", SortOrder: 370, Markdown: modelEvaluationStartGuide, Version: 1, PublishedVersion: 1, UpdatedBy: PlatformSeedActor, Action: "summary"},
		{ID: modelEvaluationResultsID, Title: "评估报告在哪里看，为什么不能比较？", Category: "调试与训练结果", SortOrder: 380, Markdown: modelEvaluationResultsGuide, Version: 1, PublishedVersion: 1, UpdatedBy: PlatformSeedActor, Action: "summary"},
	}
}

const sharedModelsPublicSection = `### 共享模型入口

「实验中心 → 模型」用于保存和查看跨团队共享的模型与权重版本。先阅读[如何把训练权重保存成共享模型版本？](#shared-model-registration)，维护权限和归档操作见[模型谁能看，如何维护与归档？](#shared-model-maintenance)。`

const modelArtifactsSupplement = `### 保存为共享模型版本

需要让其他成员使用某个权重时，可将本人已结束任务中的所选权重保存成共享模型版本。平台仅复制所选文件，原文件和原目录的权限保持不变。操作见[如何把训练权重保存成共享模型版本？](#shared-model-registration)；共享范围见[模型谁能看，如何维护与归档？](#shared-model-maintenance)。`

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

版本保留任务、可用的关联 Run、代码和数据等来源信息；缺失信息保持缺失。有关文件与来源的维护边界，见[模型谁能看，如何维护与归档？](#shared-model-maintenance)。`

const modelMaintenanceGuide = `「实验中心 → 模型」向平台全部成员开放跨团队查看。成员可以浏览模型和版本，并下载 READY 权重快照；全员可读不代表全员可修改。可用“全部 / 我创建的”筛选模型，再通过“查看详情”打开版本列表。

### 谁可以维护

模型创建者和平台 SuperAdmin 可以修改模型名称、模型说明、版本说明，并归档或恢复模型。普通成员只能维护自己创建的模型。版本登记也需要模型维护权限，且来源权重必须来自本人已结束的训练任务。

模型版本的权重文件和来源信息不可变。需要更换权重时新增版本，不覆盖已保存的版本；修改说明也不会改写任务、Run、代码版本或数据集来源。

### 归档与恢复

暂时不再使用的模型可以归档，并通过“查看已归档”找到后恢复。归档用于整理目录，保留模型、版本和快照；归档期间不能新增版本。恢复后可以继续登记新版本。

### 共享快照与来源权限

共享的是模型目录、版本记录和所选文件的快照。来源任务和 Run 仍受各自原有权限控制，个人原目录不会随模型共享。其他成员能够下载快照，不代表能够打开你的任务详情、浏览原始产物目录或修改来源实验。

### 与 MLflow 和后续流程的关系

RayTrain 的共享模型目录不会自动写入 MLflow Model Registry。现有原生 MLflow 页面、SDK 和 Registry 继续按原有方式使用，见[如何调用 MLflow API 查询实验与 Run？](#mlflow-api-with-pat)。

READY 只表示快照可用，不表示评估通过。需要评估时见[如何用固定数据版本评估一个模型？](#model-evaluation-start)。审批与 Serving 尚未上线；评估完成也不表示获准发布或推理服务已经部署。`
