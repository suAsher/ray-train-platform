package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
	ms "ray-train-platform-backend/modelserving"
)

type createModelServingContractInput struct {
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	ImageReference    string          `json:"imageReference"`
	SourceArtifactID  string          `json:"sourceArtifactId"`
	EntryPoint        []string        `json:"entryPoint"`
	InputExample      json.RawMessage `json:"inputExample"`
	OutputDescription string          `json:"outputDescription"`
}

func (h *Handler) createModelServingContract(c *gin.Context) {
	p := actorPrincipal(c)
	if !p.HasRole(domain.RoleSuperAdmin) {
		h.writeError(c, 403, "SERVING_CONTRACT_ADMIN_REQUIRED", "仅平台管理员可以登记推理方案")
		return
	}
	var input createModelServingContractInput
	if !h.decodeEvaluationJSON(c, &input, map[string]bool{"name": true, "description": true, "imageReference": true, "sourceArtifactId": true, "entryPoint": true, "inputExample": true, "outputDescription": true}) {
		return
	}
	if h.modelServing == nil || h.modelEvaluationSubmission == nil {
		h.servingError(c, ms.ErrUnavailable)
		return
	}
	key, ok := h.modelRequestKey(c)
	if !ok {
		return
	}
	if !evaluationIdentifier(input.SourceArtifactID) || len(input.InputExample) == 0 || len(input.InputExample) > ms.MaxInputExampleBytes {
		h.servingError(c, ms.ErrInvalid)
		return
	}
	release, ok := h.acquireEvaluationCodeOperation(c)
	if !ok {
		return
	}
	defer release()
	artifact, ok := h.evaluationCodeSource(c, input.SourceArtifactID)
	if !ok {
		return
	}
	spec := domain.JobSpec{Name: "serving-contract-validation", Image: input.ImageReference, Source: domain.CodeSource{Type: "workspace-archive", ArtifactID: artifact.ID}, Entrypoint: domain.Entrypoint{Command: append([]string{}, input.EntryPoint...)}, TrainingEngine: domain.TrainingEngineRayTrain, RayVersion: domain.RayVersionCanary, Resources: evaluationDefaultResources()}
	result, err := h.modelEvaluationSubmission.Preflight(c.Request.Context(), SubmissionInput{Principal: p, Spec: spec, Origin: domain.SubmissionOriginPortal})
	if err != nil {
		h.writeSubmissionError(c, p, err)
		return
	}
	digest, ok := extractImageSHA256Digest(result.Image)
	if !ok {
		h.servingError(c, ms.ErrInvalid)
		return
	}
	id := servingContractRequestID(p, key)
	var example bytes.Buffer
	if err := json.Compact(&example, input.InputExample); err != nil {
		h.servingError(c, ms.ErrInvalid)
		return
	}
	contract := ms.Contract{ID: id, Name: strings.TrimSpace(input.Name), Description: input.Description, OwnerID: p.Subject, OwnerName: p.Username, TenantID: p.TenantID, Revision: 1, Active: true, ImageReference: result.Image, ImageDigest: "sha256:" + digest, Code: &me.CodeSnapshot{ID: id, SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes, Format: "zip"}, EntryPoint: append([]string{}, input.EntryPoint...), InputExample: append(json.RawMessage(nil), example.Bytes()...), OutputDescription: input.OutputDescription, CreatedAt: time.Now().UTC()}
	if h.servingError(c, ms.ValidateContract(contract)) {
		return
	}
	h.freezeContractCode(c, contract, *artifact)
}

func servingContractRequestID(p auth.Principal, key string) string {
	sum := sha256.Sum256([]byte("model-serving-contract\x00" + p.TenantID + "\x00" + p.Subject + "\x00" + key))
	return hex.EncodeToString(sum[:16])
}

func sameServingContractDefinition(a, b ms.Contract) bool {
	return a.ID == b.ID && a.OwnerID == b.OwnerID && a.TenantID == b.TenantID && a.Name == b.Name && a.Description == b.Description && a.ImageReference == b.ImageReference && a.ImageDigest == b.ImageDigest && reflect.DeepEqual(a.Code, b.Code) && reflect.DeepEqual(a.EntryPoint, b.EntryPoint) && sameServingInputExample(a.InputExample, b.InputExample) && a.OutputDescription == b.OutputDescription
}

func sameServingInputExample(a, b json.RawMessage) bool {
	decode := func(raw json.RawMessage) (any, error) {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		err := decoder.Decode(&value)
		return value, err
	}
	left, leftErr := decode(a)
	right, rightErr := decode(b)
	return leftErr == nil && rightErr == nil && reflect.DeepEqual(left, right)
}

func (h *Handler) freezeContractCode(c *gin.Context, contract ms.Contract, artifact domain.SourceArtifact) {
	existing, err := h.modelServing.GetContract(c.Request.Context(), contract.ID)
	if err == nil {
		if !sameServingContractDefinition(existing, contract) {
			h.servingError(c, ms.ErrConflict)
			return
		}
		h.writeSuccess(c, 200, existing)
		return
	}
	if !errors.Is(err, ms.ErrNotFound) {
		h.servingError(c, err)
		return
	}
	snapshot, err := h.evaluationCode.Publish(c.Request.Context(), contract.Code.ID, artifact)
	if err != nil {
		h.writeError(c, 503, "SERVING_CODE_UNAVAILABLE", "推理代码快照保存失败，请保留请求键重试")
		return
	}
	if snapshot != *contract.Code {
		h.writeError(c, 503, "SERVING_CODE_SNAPSHOT_MISMATCH", "推理代码快照与上传校验记录不一致，请保留请求键重试")
		return
	}
	created, err := h.modelServing.CreateContract(c.Request.Context(), contract)
	if err != nil {
		// A failed response may follow a successful DB commit. Preserve the
		// immutable snapshot and reconcile; never remove a possibly linked file.
		existing, readErr := h.modelServing.GetContract(c.Request.Context(), contract.ID)
		if readErr == nil && sameServingContractDefinition(existing, contract) {
			h.writeSuccess(c, 200, existing)
			return
		}
		h.servingError(c, err)
		return
	}
	h.writeSuccess(c, 201, created)
}

func (h *Handler) listModelServingContracts(c *gin.Context) {
	canManage := actorPrincipal(c).HasRole(domain.RoleSuperAdmin)
	includeInactive := false
	if raw, present := c.GetQuery("includeInactive"); present {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			h.servingError(c, ms.ErrInvalid)
			return
		}
		includeInactive = value
	}
	if includeInactive && !canManage {
		h.writeError(c, 403, "SERVING_CONTRACT_ADMIN_REQUIRED", "仅平台管理员可以查看已停用推理方案")
		return
	}
	if h.modelServing == nil {
		h.servingError(c, ms.ErrUnavailable)
		return
	}
	items, err := h.modelServing.ListContracts(c.Request.Context(), includeInactive)
	if h.servingError(c, err) {
		return
	}
	if items == nil {
		items = []ms.Contract{}
	}
	h.writeSuccess(c, 200, gin.H{"items": items, "canManage": canManage})
}

func (h *Handler) updateModelServingContract(c *gin.Context) {
	if !actorPrincipal(c).HasRole(domain.RoleSuperAdmin) {
		h.writeError(c, 403, "SERVING_CONTRACT_ADMIN_REQUIRED", "仅平台管理员可以启停推理方案")
		return
	}
	var input struct {
		Active   *bool `json:"active"`
		Revision int64 `json:"revision"`
	}
	if !h.decodeEvaluationJSON(c, &input, map[string]bool{"active": true, "revision": true}) {
		return
	}
	if input.Active == nil || input.Revision < 1 || !evaluationIdentifier(c.Param("contractId")) {
		h.servingError(c, ms.ErrInvalid)
		return
	}
	if h.modelServing == nil {
		h.servingError(c, ms.ErrUnavailable)
		return
	}
	contract, err := h.modelServing.SetContractActive(c.Request.Context(), c.Param("contractId"), *input.Active, input.Revision)
	if h.servingError(c, err) {
		return
	}
	h.writeSuccess(c, 200, contract)
}
