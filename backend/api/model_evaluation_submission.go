package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
	"ray-train-platform-backend/modellifecycle"
	"ray-train-platform-backend/repositories"
)

func newEvaluationID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
func evaluationDefaultResources() domain.Resources {
	return domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 4, MemoryPerWorker: "16Gi"}
}
func (h *Handler) evaluationRequest(c *gin.Context, preflight bool) (me.Request, bool) {
	var request me.Request
	if !h.decodeEvaluationJSON(c, &request, evaluationRequestFields) {
		return request, false
	}
	if preflight {
		request.IdempotencyKey = "preflight-validation"
	} else {
		key, ok := h.modelRequestKey(c)
		if !ok {
			return request, false
		}
		request.IdempotencyKey = key
	}
	if request.Resources == (domain.Resources{}) {
		request.Resources = evaluationDefaultResources()
	}
	if len(request.Sites) != 0 {
		h.writeError(c, 400, "EVALUATION_SITES_UNSUPPORTED", "当前评估仅支持验证集或测试集的全部场地，请清空场地筛选")
		return request, false
	}
	if request.DatasetVersionID == "latest" {
		h.writeError(c, 400, "EVALUATION_FIXED_DATASET_REQUIRED", "请选择具体的 READY 数据版本，评估不接受 latest")
		return request, false
	}
	config, _, err := me.CanonicalConfig(request.Config)
	if h.modelEvaluationError(c, err) {
		return request, false
	}
	request.Config = config
	request.Sites = []string{}
	if h.modelEvaluationError(c, me.ValidateRequest(request)) {
		return request, false
	}
	return request, true
}
func evaluationRequestHash(request me.Request) (string, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (h *Handler) freezeModelEvaluation(c *gin.Context, request me.Request) (me.Evaluation, bool) {
	var result me.Evaluation
	if h.models == nil || h.datasets == nil || h.modelEvaluationSubmission == nil {
		h.writeError(c, 503, "EVALUATION_DEPENDENCIES_UNAVAILABLE", "模型、数据或提交服务暂不可用")
		return result, false
	}
	ctx := c.Request.Context()
	p := actorPrincipal(c)
	model, err := h.models.GetModel(ctx, request.ModelID)
	if h.modelError(c, err) {
		return result, false
	}
	version, err := h.models.GetVersion(ctx, request.ModelID, request.VersionID)
	if h.modelError(c, err) {
		return result, false
	}
	if model.Archived || model.ID != request.ModelID || version.ModelID != model.ID || version.ID != request.VersionID || version.State != modellifecycle.Ready {
		h.modelEvaluationError(c, me.ErrNotReady)
		return result, false
	}
	evaluator, err := h.modelEvaluations.GetEvaluator(ctx, request.EvaluatorID)
	if h.modelEvaluationError(c, err) {
		return result, false
	}
	if !evaluator.Active || evaluator.ID != request.EvaluatorID {
		h.modelEvaluationError(c, me.ErrNotReady)
		return result, false
	}
	dataset, err := h.datasets.GetDataset(ctx, p.TenantID, p.HasRole(domain.RoleSuperAdmin), request.DatasetID)
	if err != nil {
		h.writeError(c, 404, "DATASET_NOT_FOUND", "数据集不存在或不可访问")
		return result, false
	}
	dataVersion, err := h.datasets.GetDatasetVersion(ctx, p.TenantID, p.HasRole(domain.RoleSuperAdmin), request.DatasetID, request.DatasetVersionID)
	if err != nil {
		h.writeError(c, 404, "DATASET_VERSION_NOT_FOUND", "数据版本不存在或不可访问")
		return result, false
	}
	if dataset.ID != request.DatasetID || dataVersion.DatasetID != dataset.ID || dataVersion.ID != request.DatasetVersionID || dataVersion.State != domain.DatasetVersionReady || dataVersion.SchemaVersion != evaluator.SchemaVersion || dataset.SchemaVersion != evaluator.SchemaVersion {
		h.modelEvaluationError(c, me.ErrNotReady)
		return result, false
	}
	count := dataVersion.ValSamples
	if request.Split == "test" {
		count = dataVersion.TestSamples
	}
	if count < 1 {
		h.writeError(c, 409, "EVALUATION_EMPTY_SPLIT", "所选验证集或测试集没有样本，请选择非空划分")
		return result, false
	}
	id, err := newEvaluationID()
	if h.modelEvaluationError(c, err) {
		return result, false
	}
	jobID, err := newJobID()
	if h.modelEvaluationError(c, err) {
		return result, false
	}
	config, configHash, err := me.CanonicalConfig(request.Config)
	if h.modelEvaluationError(c, err) {
		return result, false
	}
	result = me.Evaluation{ID: id, ModelID: model.ID, VersionID: version.ID, ModelSHA256: version.SHA256, FileName: version.FileName, Dataset: me.DatasetSnapshot{ID: dataset.ID, VersionID: dataVersion.ID, ManifestSHA256: dataVersion.ManifestSHA256, SchemaVersion: dataVersion.SchemaVersion, Split: request.Split, Sites: []string{}, SampleCount: count, Visibility: string(dataset.Visibility), TenantID: dataset.OwnerTenantID}, Evaluator: evaluator, Config: config, ConfigSHA256: configHash, Resources: request.Resources, OwnerID: p.Subject, OwnerName: p.Username, TenantID: p.TenantID, JobID: jobID, State: me.Creating, ReportState: me.ReportPending, Revision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), IdempotencyKey: request.IdempotencyKey}
	if !me.CanRead(result, p.TenantID, p.HasRole(domain.RoleSuperAdmin)) {
		h.modelEvaluationError(c, me.ErrNotFound)
		return me.Evaluation{}, false
	}
	if h.modelEvaluationError(c, me.ValidateEvaluation(result)) {
		return result, false
	}
	result.JobSpec = domain.JobSpec{Name: "evaluation-" + id, Image: evaluator.ImageReference, Source: evaluationCodeJobSource(evaluator), Entrypoint: domain.Entrypoint{Command: append([]string{}, evaluator.EntryPoint...)}, TrainingEngine: domain.TrainingEngineRayTrain, RayVersion: domain.RayVersionCanary, DataMode: domain.DataModeStreaming, DatasetRef: domain.DatasetReference{Dataset: dataset.ID, Version: dataVersion.ID}, CachePolicy: domain.DatasetCachePolicyAuto, Resources: request.Resources, Output: domain.DataLocation{Space: domain.DataSpaceMyRuns, RelativePath: "evaluations/" + id}, TimeoutSeconds: 24 * 60 * 60}
	preflight, err := h.modelEvaluationSubmission.Preflight(ctx, evaluationSubmissionInput(p, result))
	if err != nil {
		h.writeSubmissionError(c, p, err)
		return result, false
	}
	actual, ok := extractImageSHA256Digest(preflight.Image)
	if !ok || "sha256:"+actual != evaluator.ImageDigest {
		h.writeError(c, 409, "EVALUATOR_IMAGE_CHANGED", "评估镜像摘要已变更，请联系管理员登记新方案")
		return result, false
	}
	result.JobSpec.Image = preflight.Image
	result.RequestSHA256, err = evaluationRequestHash(request)
	if h.modelEvaluationError(c, err) {
		return result, false
	}
	return result, true
}
func evaluationSubmissionInput(p auth.Principal, e me.Evaluation) SubmissionInput {
	return SubmissionInput{Principal: p, Spec: e.JobSpec, Origin: domain.SubmissionOriginEvaluation, IdempotencyKey: "model-evaluation:" + e.ID, ExternalSubmissionID: e.ID, ReservedJobID: e.JobID, ExpectedImageDigest: e.Evaluator.ImageDigest, ExpectedDatasetManifestSHA256: e.Dataset.ManifestSHA256}
}
func (h *Handler) preflightModelEvaluation(c *gin.Context) {
	request, ok := h.evaluationRequest(c, true)
	if !ok {
		return
	}
	e, ok := h.freezeModelEvaluation(c, request)
	if !ok {
		return
	}
	var quota *domain.TenantQuota
	if h.quota != nil {
		value, err := h.quota.TenantGPUQuota(c.Request.Context(), actorPrincipal(c).TenantID)
		if err != nil {
			h.writeError(c, 503, "EVALUATION_QUOTA_UNAVAILABLE", "暂时无法查询当前团队配额，请稍后重试")
			return
		}
		quota = &value
	}
	h.writeSuccess(c, 200, gin.H{"evaluation": e, "jobSpec": e.JobSpec, "requestedGpus": e.Resources.WorkerReplicas * e.Resources.GPUsPerWorker, "quota": quota})
}
func (h *Handler) createModelEvaluation(c *gin.Context) {
	request, ok := h.evaluationRequest(c, false)
	if !ok {
		return
	}
	p := actorPrincipal(c)
	ctx := c.Request.Context()
	hash, err := evaluationRequestHash(request)
	if h.modelEvaluationError(c, err) {
		return
	}
	e, err := h.modelEvaluations.FindEvaluationRequest(ctx, p.TenantID, p.Subject, request.IdempotencyKey)
	if err == nil {
		if e.RequestSHA256 != hash {
			h.modelEvaluationError(c, me.ErrConflict)
			return
		}
		if !me.CanRead(e, p.TenantID, p.HasRole(domain.RoleSuperAdmin)) {
			h.modelEvaluationError(c, me.ErrNotFound)
			return
		}
		if e.State != me.Creating {
			h.writeSuccess(c, 200, evaluationResponse(p, e))
			return
		}
	} else if errors.Is(err, me.ErrNotFound) {
		var ok bool
		e, ok = h.freezeModelEvaluation(c, request)
		if !ok {
			return
		}
		e, _, err = h.modelEvaluations.ReserveEvaluation(ctx, e)
		if h.modelEvaluationError(c, err) {
			return
		}
		if e.State != me.Creating {
			h.writeSuccess(c, 200, evaluationResponse(p, e))
			return
		}
	} else {
		h.modelEvaluationError(c, err)
		return
	}
	if h.modelEvaluationSubmission == nil {
		h.modelEvaluationError(c, me.ErrNotReady)
		return
	}
	if h.submitReservedModelEvaluation(c, p, e) {
		e.State = me.Submitted
		h.writeSuccess(c, 202, evaluationResponse(p, e))
	}
}
func (h *Handler) submitReservedModelEvaluation(c *gin.Context, p auth.Principal, e me.Evaluation) bool {
	// A replay uses its persisted reservation. Neither a new evaluator revision
	// nor a concurrent HTTP retry can allocate another job or output directory.
	job, err := h.modelEvaluationSubmission.Submit(c.Request.Context(), evaluationSubmissionInput(p, e))
	if err != nil {
		var conflict *repositories.IdempotencyConflictError
		if errors.As(err, &conflict) && conflict.JobID != e.JobID {
			h.modelEvaluationError(c, me.ErrConflict)
			return false
		}
		recovered, getErr := h.repository.Get(c.Request.Context(), e.TenantID, e.JobID)
		if getErr == nil && evaluationJobMatches(recovered, e) {
			job = recovered
			err = nil
		}
	}
	if err != nil {
		h.writeSubmissionError(c, p, err)
		return false
	}
	if !evaluationJobMatches(job, e) {
		h.modelEvaluationError(c, me.ErrConflict)
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 10*time.Second)
	defer cancel()
	if err := h.modelEvaluations.MarkEvaluationSubmitted(ctx, e.ID, e.JobID); h.modelEvaluationError(c, err) {
		return false
	}
	return true
}
func evaluationJobMatches(job *domain.TrainingJob, e me.Evaluation) bool {
	return job != nil && job.ID == e.JobID && job.UserID == e.OwnerID && job.TenantID == e.TenantID && job.SubmissionOrigin == domain.SubmissionOriginEvaluation && job.ExternalSubmissionID == e.ID && evaluationCodeSourceMatches(job.Spec.Source, e.Evaluator) && strings.TrimSpace(job.Spec.Image) == strings.TrimSpace(e.JobSpec.Image)
}
