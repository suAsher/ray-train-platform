package api

import (
	"context"
	"encoding/hex"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/modellifecycle"
)

type createModelVersionInput struct {
	JobID            string `json:"jobId"`
	Path             string `json:"path"`
	Description      string `json:"description"`
	DatasetID        string `json:"datasetId,omitempty"`
	DatasetVersionID string `json:"datasetVersionId,omitempty"`
}

func (h *Handler) createModelVersion(c *gin.Context) {
	m, ok := h.modelForRequest(c, true)
	if !ok {
		return
	}
	if m.Archived {
		h.modelError(c, modellifecycle.ErrConflict)
		return
	}
	if h.modelSnapshots == nil {
		h.writeError(c, 503, "MODEL_SNAPSHOTS_UNAVAILABLE", "权重快照服务暂不可用")
		return
	}
	var input createModelVersionInput
	if !h.decodeModelJSON(c, &input) {
		return
	}
	relative, err := domain.NormalizeStorageRelativePath(input.Path)
	if err != nil || relative == "" {
		h.writeError(c, 400, "INVALID_ARTIFACT_PATH", "请选择任务输出中的相对文件路径")
		return
	}
	if _, allowed := downloadPolicy(relative); !allowed {
		h.writeError(c, 400, "MODEL_WEIGHT_REQUIRED", "请选择 pth、pt、ckpt、onnx 或 safetensors 权重文件")
		return
	}
	p, _ := auth.PrincipalFromGin(c)
	job, err := h.jobForPrincipal(c.Request.Context(), p, input.JobID)
	if err != nil {
		h.writeError(c, 404, "JOB_NOT_FOUND", "训练任务不存在或不可访问")
		return
	}
	// Shared model management never grants access to a different user's private
	// files, including to administrators. Publishing requires the source owner.
	if job.UserID != p.Subject {
		h.writeError(c, 403, "MODEL_SOURCE_FORBIDDEN", "只能登记本人任务的权重文件")
		return
	}
	switch job.ObservedState {
	case domain.StateSucceeded, domain.StateFailed, domain.StateCanceled, domain.StateTimedOut:
	default:
		h.writeError(c, 409, "MODEL_SOURCE_ACTIVE", "请等待训练任务结束后再登记权重，避免复制仍在写入的文件")
		return
	}
	root, ok := h.jobArtifactRoot(c, p, job)
	if !ok {
		return
	}
	request := modellifecycle.VersionRequest{
		ModelID: m.ID, Description: input.Description, CreatorID: p.Subject, CreatorName: p.Username,
		JobID: job.ID, JobName: job.Spec.Name, FileName: path.Base(relative), SourceRoot: root, RelativePath: relative,
		IdempotencyKey: c.GetHeader("Idempotency-Key"), RuntimeImage: job.Spec.Image,
	}
	if isModelSourceDigest(job.Spec.Source.ArtifactSHA256, 64) {
		request.CodeSHA256 = job.Spec.Source.ArtifactSHA256
	}
	if isModelSourceDigest(job.Spec.Source.Commit, 40) || isModelSourceDigest(job.Spec.Source.Commit, 64) {
		request.CodeCommit = job.Spec.Source.Commit
	}
	if !h.modelDatasetProvenance(c, p, job, input, &request) {
		return
	}
	if h.experiments != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		experiment, queryErr := h.experiments.QueryJobExperiment(ctx, job.TenantID, job.ID)
		cancel()
		if queryErr == nil && experiment.Run != nil {
			request.RunID = experiment.Run.ID
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	version, err := h.modelSnapshots.RequestVersion(ctx, request)
	if h.modelError(c, err) {
		return
	}
	h.writeSuccess(c, http.StatusAccepted, modelVersionResponse{version, true})
}
func isModelSourceDigest(value string, size int) bool {
	if len(value) != size {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func (h *Handler) modelDatasetProvenance(c *gin.Context, p auth.Principal, job *domain.TrainingJob, input createModelVersionInput, out *modellifecycle.VersionRequest) bool {
	source := job.DatasetProvenance
	if source.DatasetVersionID != "" {
		if (input.DatasetID != "" && input.DatasetID != source.DatasetID) || (input.DatasetVersionID != "" && input.DatasetVersionID != source.DatasetVersionID) {
			h.writeError(c, 400, "MODEL_DATASET_CONFLICT", "任务已记录固定数据版本，不能用另一版本覆盖训练来源")
			return false
		}
		out.DatasetID = source.DatasetID
		out.DatasetVersionID = source.DatasetVersionID
		out.DatasetManifestSHA256 = source.ManifestSHA256
		out.DatasetAssociation = "training-record"
		return true
	}
	if input.DatasetID == "" && input.DatasetVersionID == "" {
		return true
	}
	if input.DatasetID == "" || input.DatasetVersionID == "" || h.datasets == nil {
		h.writeError(c, 400, "MODEL_DATASET_REQUIRED", "请选择数据集及其固定版本")
		return false
	}
	dataset, err := h.datasets.GetDataset(c.Request.Context(), p.TenantID, p.HasRole(domain.RoleSuperAdmin), input.DatasetID)
	if err != nil {
		h.writeError(c, 404, "DATASET_NOT_FOUND", "数据集不存在或不可访问")
		return false
	}
	version, err := h.datasets.GetDatasetVersion(c.Request.Context(), p.TenantID, p.HasRole(domain.RoleSuperAdmin), input.DatasetID, input.DatasetVersionID)
	if err != nil {
		h.writeError(c, 404, "DATASET_VERSION_NOT_FOUND", "数据版本不存在或不可访问")
		return false
	}
	if string(version.State) != "READY" || !isModelSourceDigest(version.ManifestSHA256, 64) {
		h.writeError(c, 409, "MODEL_DATASET_NOT_READY", "请选择已完成发布并具有 manifest 摘要的 READY 版本")
		return false
	}
	out.DatasetID = dataset.ID
	out.DatasetName = dataset.Name
	out.DatasetVersionID = version.ID
	out.DatasetManifestSHA256 = version.ManifestSHA256
	out.DatasetAssociation = "user-declared"
	return true
}
func (h *Handler) downloadModelVersion(c *gin.Context) {
	m, ok := h.modelForRequest(c, false)
	if !ok {
		return
	}
	if h.modelSnapshots == nil {
		h.writeError(c, 503, "MODEL_SNAPSHOTS_UNAVAILABLE", "权重快照服务暂不可用")
		return
	}
	version, err := h.models.GetVersion(c.Request.Context(), m.ID, c.Param("versionId"))
	if h.modelError(c, err) {
		return
	}
	if version.State != modellifecycle.Ready {
		h.modelError(c, modellifecycle.ErrNotReady)
		return
	}
	name := path.Base(version.FileName)
	if strings.ContainsAny(name, "\r\n\"") {
		h.modelError(c, modellifecycle.ErrInvalid)
		return
	}
	writeCheckpointDownloadHeaders(c, "application/octet-stream", name, version.SizeBytes)
	c.Header("X-Content-SHA256", version.SHA256)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Minute)
	defer cancel()
	if err := h.modelSnapshots.Download(ctx, version, c.Writer); err != nil && !c.Writer.Written() {
		c.Header("Content-Length", "")
		c.Header("Content-Disposition", "")
		c.Header("Content-Type", "")
		c.Header("X-Content-SHA256", "")
		h.modelError(c, err)
	}
}
