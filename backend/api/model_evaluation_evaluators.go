package api

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
)

type createModelEvaluatorInput struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	ImageReference   string   `json:"imageReference"`
	ImageDigest      string   `json:"imageDigest"`
	SourceArtifactID string   `json:"sourceArtifactId"`
	GitURL           string   `json:"gitUrl"`
	GitCommit        string   `json:"gitCommit"`
	EntryPoint       []string `json:"entryPoint"`
	SchemaVersion    string   `json:"schemaVersion"`
	Protocol         string   `json:"protocol"`
}

func (h *Handler) createModelEvaluator(c *gin.Context) {
	p := actorPrincipal(c)
	if !p.HasRole(domain.RoleSuperAdmin) {
		h.writeError(c, 403, "EVALUATOR_ADMIN_REQUIRED", "仅平台管理员可以登记评估方案")
		return
	}
	var input createModelEvaluatorInput
	if !h.decodeEvaluationJSON(c, &input, evaluatorRequestFields) {
		return
	}
	if h.modelEvaluationSubmission == nil {
		h.modelEvaluationError(c, me.ErrNotReady)
		return
	}
	if input.Protocol == "" {
		input.Protocol = me.Protocol
	}
	if input.SourceArtifactID != "" && (input.GitURL != "" || input.GitCommit != "") {
		h.modelEvaluationError(c, me.ErrInvalid)
		return
	}
	var artifact *domain.SourceArtifact
	origin := domain.SubmissionOriginAPI
	source := domain.CodeSource{Type: "git", URL: input.GitURL, Commit: input.GitCommit}
	if input.SourceArtifactID != "" {
		release, ok := h.acquireEvaluationCodeOperation(c)
		if !ok {
			return
		}
		defer release()
		var okSource bool
		artifact, okSource = h.evaluationCodeSource(c, input.SourceArtifactID)
		if !okSource {
			return
		}
		origin = domain.SubmissionOriginPortal
		source = domain.CodeSource{Type: "workspace-archive", ArtifactID: artifact.ID}
	}
	// Catalogue and JobSpec validation is read-only. Uploaded source uses the
	// existing interactive archive origin; generic API permissions stay narrow.
	spec := domain.JobSpec{Name: "evaluator-validation", Image: input.ImageReference, Source: source, Entrypoint: domain.Entrypoint{Command: append([]string{}, input.EntryPoint...)}, TrainingEngine: domain.TrainingEngineRayTrain, RayVersion: domain.RayVersionCanary, Resources: evaluationDefaultResources()}
	result, err := h.modelEvaluationSubmission.Preflight(c.Request.Context(), SubmissionInput{Principal: p, Spec: spec, Origin: origin})
	if err != nil {
		h.writeSubmissionError(c, p, err)
		return
	}
	digest, ok := extractImageSHA256Digest(result.Image)
	if !ok {
		h.modelEvaluationError(c, me.ErrInvalid)
		return
	}
	digest = "sha256:" + digest
	if input.ImageDigest != "" && input.ImageDigest != digest {
		h.writeError(c, 409, "EVALUATOR_IMAGE_CHANGED", "登记镜像的实际摘要与所填摘要不一致")
		return
	}
	id, err := newEvaluationID()
	if h.modelEvaluationError(c, err) {
		return
	}
	if artifact != nil {
		key, ok := h.modelRequestKey(c)
		if !ok {
			return
		}
		id = evaluationCodeRequestID(p, key)
	}
	evaluator := me.Evaluator{ID: id, Name: strings.TrimSpace(input.Name), Description: input.Description, OwnerID: p.Subject, OwnerName: p.Username, TenantID: p.TenantID, Revision: 1, Active: true, ImageReference: result.Image, ImageDigest: digest, GitURL: input.GitURL, GitCommit: input.GitCommit, EntryPoint: append([]string{}, input.EntryPoint...), SchemaVersion: input.SchemaVersion, Protocol: input.Protocol, CreatedAt: time.Now().UTC()}
	if artifact != nil {
		evaluator.Code = &me.CodeSnapshot{ID: id, SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes, Format: "zip"}
	}
	if h.modelEvaluationError(c, me.ValidateEvaluator(evaluator)) {
		return
	}
	if artifact != nil {
		h.publishModelEvaluatorCode(c, evaluator, *artifact)
		return
	}
	evaluator, err = h.modelEvaluations.CreateEvaluator(c.Request.Context(), evaluator)
	if h.modelEvaluationError(c, err) {
		return
	}
	h.writeSuccess(c, 201, evaluator)
}
func (h *Handler) updateModelEvaluator(c *gin.Context) {
	if !actorPrincipal(c).HasRole(domain.RoleSuperAdmin) {
		h.writeError(c, 403, "EVALUATOR_ADMIN_REQUIRED", "仅平台管理员可以启停评估方案")
		return
	}
	var input struct {
		Active   *bool `json:"active"`
		Revision int64 `json:"revision"`
	}
	if !h.decodeEvaluationJSON(c, &input, map[string]bool{"active": true, "revision": true}) {
		return
	}
	if input.Active == nil || input.Revision < 1 {
		h.modelEvaluationError(c, me.ErrInvalid)
		return
	}
	evaluator, err := h.modelEvaluations.SetEvaluatorActive(c.Request.Context(), c.Param("evaluatorId"), *input.Active, input.Revision)
	if h.modelEvaluationError(c, err) {
		return
	}
	h.writeSuccess(c, 200, evaluator)
}
