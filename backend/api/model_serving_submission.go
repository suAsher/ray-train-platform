package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/modellifecycle"
	mr "ray-train-platform-backend/modelrelease"
	ms "ray-train-platform-backend/modelserving"
	"time"
)

type createModelServiceInput struct {
	Name       string           `json:"name"`
	ReleaseID  string           `json:"releaseId"`
	ContractID string           `json:"contractId"`
	Resources  domain.Resources `json:"resources"`
	TTLSeconds int64            `json:"ttlSeconds"`
}

func servingActor(p auth.Principal) mr.Actor {
	return mr.Actor{ID: p.Subject, Name: p.Username, TenantID: p.TenantID, SuperAdmin: p.HasRole(domain.RoleSuperAdmin), TenantAdmin: p.HasRole(domain.RoleTenantAdmin)}
}
func (h *Handler) serviceInput(c *gin.Context) (createModelServiceInput, bool) {
	var input createModelServiceInput
	if !h.decodeEvaluationJSON(c, &input, map[string]bool{"name": true, "releaseId": true, "contractId": true, "resources": true, "ttlSeconds": true}) {
		return input, false
	}
	if !evaluationIdentifier(input.ReleaseID) || !evaluationIdentifier(input.ContractID) || input.TTLSeconds < 3600 || input.TTLSeconds > 604800 || len(input.Name) < 1 || len(input.Name) > 200 || input.Resources.WorkerReplicas != 1 || input.Resources.GPUsPerWorker != 1 {
		h.servingError(c, ms.ErrInvalid)
		return input, false
	}
	return input, true
}
func (h *Handler) freezeModelService(c *gin.Context, input createModelServiceInput, key string) (ms.Deployment, bool) {
	var d ms.Deployment
	ctx := c.Request.Context()
	p := actorPrincipal(c)
	release, err := h.modelReleases.GetRelease(ctx, input.ReleaseID, servingActor(p))
	if err != nil || release.State != mr.Approved {
		h.servingError(c, ms.ErrNotReady)
		return d, false
	}
	m, err := h.models.GetModel(ctx, release.ModelID)
	if h.modelError(c, err) {
		return d, false
	}
	if !canManageModel(p, m) {
		h.servingError(c, ms.ErrUnauthorized)
		return d, false
	}
	v, err := h.models.GetVersion(ctx, m.ID, release.VersionID)
	if h.modelError(c, err) {
		return d, false
	}
	contract, err := h.modelServing.GetContract(ctx, input.ContractID)
	if h.servingError(c, err) {
		return d, false
	}
	if m.Archived || v.State != modellifecycle.Ready || v.SHA256 != release.ModelSHA256 || !contract.Active || contract.Code == nil {
		h.servingError(c, ms.ErrNotReady)
		return d, false
	}
	id, err := newEvaluationID()
	if h.servingError(c, err) {
		return d, false
	}
	jobID, err := newJobID()
	if h.servingError(c, err) {
		return d, false
	}
	raw, err := json.Marshal(input)
	if h.servingError(c, err) {
		return d, false
	}
	hash := sha256.Sum256(raw)
	now := time.Now().UTC()
	d = ms.Deployment{ID: id, Name: input.Name, ReleaseID: release.ID, ModelID: m.ID, VersionID: v.ID, ModelSHA256: v.SHA256, ModelSizeBytes: v.SizeBytes, FileName: v.FileName, OwnerID: p.Subject, OwnerName: p.Username, TenantID: p.TenantID, Contract: contract, Resources: input.Resources, JobID: jobID, State: ms.Creating, Revision: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Duration(input.TTLSeconds) * time.Second), IdempotencyKey: key, RequestSHA256: hex.EncodeToString(hash[:])}
	d.JobSpec = domain.JobSpec{Name: "serving-" + id, Image: contract.ImageReference, Source: domain.CodeSource{Type: "serving-archive", ArtifactID: contract.Code.ID, ArtifactSHA256: contract.Code.SHA256}, Entrypoint: domain.Entrypoint{Command: append([]string{}, contract.EntryPoint...)}, TrainingEngine: domain.TrainingEngineRayTrain, RayVersion: domain.RayVersionCanary, DataMode: domain.DataModeMount, Resources: input.Resources, Output: domain.DataLocation{Space: domain.DataSpaceMyRuns, RelativePath: "services/" + id}, TimeoutSeconds: input.TTLSeconds}
	if h.modelEvaluationSubmission == nil {
		h.servingError(c, ms.ErrUnavailable)
		return d, false
	}
	result, err := h.modelEvaluationSubmission.Preflight(ctx, servingSubmissionInput(p, d))
	if err != nil {
		h.writeSubmissionError(c, p, err)
		return d, false
	}
	digest, ok := extractImageSHA256Digest(result.Image)
	if !ok || "sha256:"+digest != contract.ImageDigest {
		h.servingError(c, ms.ErrNotReady)
		return d, false
	}
	d.JobSpec.Image = result.Image
	return d, true
}
func servingSubmissionInput(p auth.Principal, d ms.Deployment) SubmissionInput {
	return SubmissionInput{Principal: p, Spec: d.JobSpec, Origin: domain.SubmissionOriginServing, IdempotencyKey: "model-serving:" + d.ID, ExternalSubmissionID: d.ID, ReservedJobID: d.JobID, ExpectedImageDigest: d.Contract.ImageDigest}
}
func (h *Handler) preflightModelService(c *gin.Context) {
	input, ok := h.serviceInput(c)
	if !ok {
		return
	}
	d, ok := h.freezeModelService(c, input, "preflight-validation")
	if !ok {
		return
	}
	var quota *domain.TenantQuota
	if h.quota != nil {
		q, err := h.quota.TenantGPUQuota(c.Request.Context(), actorPrincipal(c).TenantID)
		if h.servingError(c, err) {
			return
		}
		quota = &q
	}
	h.writeSuccess(c, 200, gin.H{"deployment": servingResponse(actorPrincipal(c), d), "requestedGpus": d.Resources.GPUsPerWorker, "quota": quota})
}
func (h *Handler) createModelService(c *gin.Context) {
	input, ok := h.serviceInput(c)
	if !ok {
		return
	}
	key, ok := h.modelRequestKey(c)
	if !ok {
		return
	}
	p := actorPrincipal(c)
	ctx := c.Request.Context()
	raw, err := json.Marshal(input)
	if h.servingError(c, err) {
		return
	}
	hash := sha256.Sum256(raw)
	d, err := h.modelServing.FindDeploymentRequest(ctx, p.TenantID, p.Subject, key)
	if errors.Is(err, ms.ErrNotFound) {
		d, ok = h.freezeModelService(c, input, key)
		if !ok {
			return
		}
		d, _, err = h.modelServing.ReserveDeployment(ctx, d)
	}
	if h.servingError(c, err) {
		return
	}
	if d.RequestSHA256 != hex.EncodeToString(hash[:]) {
		h.servingError(c, ms.ErrConflict)
		return
	}
	if d.State != ms.Creating {
		h.writeSuccess(c, 200, servingResponse(p, d))
		return
	}
	if h.modelEvaluationSubmission == nil {
		h.servingError(c, ms.ErrUnavailable)
		return
	}
	job, err := h.modelEvaluationSubmission.Submit(ctx, servingSubmissionInput(p, d))
	if err != nil {
		recovered, getErr := h.repository.Get(ctx, d.TenantID, d.JobID)
		if getErr == nil && servingJobMatches(recovered, d) {
			job = recovered
			err = nil
		}
	}
	if err != nil {
		failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		_ = h.modelServing.FailDeploymentSubmission(failCtx, d.ID)
		cancel()
		h.writeSubmissionError(c, p, err)
		return
	}
	if !servingJobMatches(job, d) {
		h.servingError(c, ms.ErrConflict)
		return
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if h.servingError(c, h.modelServing.MarkDeploymentSubmitted(commitCtx, d.ID, d.JobID)) {
		return
	}
	d.State = ms.Submitted
	h.writeSuccess(c, 202, servingResponse(p, d))
}
func servingJobMatches(job *domain.TrainingJob, d ms.Deployment) bool {
	return job != nil && job.ID == d.JobID && job.UserID == d.OwnerID && job.TenantID == d.TenantID && job.SubmissionOrigin == domain.SubmissionOriginServing && job.ExternalSubmissionID == d.ID && job.Spec.Source == d.JobSpec.Source && job.Spec.Image == d.JobSpec.Image
}
