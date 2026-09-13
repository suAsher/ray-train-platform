package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	ms "ray-train-platform-backend/modelserving"
)

type servingContractStoreFake struct {
	ms.Store
	contract        ms.Contract
	creates         int
	includeInactive bool
	createErr       error
}

func (s *servingContractStoreFake) GetContract(_ context.Context, id string) (ms.Contract, error) {
	if s.contract.ID != id {
		return ms.Contract{}, ms.ErrNotFound
	}
	return s.contract, nil
}
func (s *servingContractStoreFake) CreateContract(_ context.Context, c ms.Contract) (ms.Contract, error) {
	s.creates++
	s.contract = c
	return c, s.createErr
}
func (s *servingContractStoreFake) ListContracts(_ context.Context, inactive bool) ([]ms.Contract, error) {
	s.includeInactive = inactive
	return []ms.Contract{}, nil
}
func (s *servingContractStoreFake) SetContractActive(_ context.Context, id string, active bool, revision int64) (ms.Contract, error) {
	if id != s.contract.ID {
		return ms.Contract{}, ms.ErrNotFound
	}
	if revision != s.contract.Revision {
		return ms.Contract{}, ms.ErrConflict
	}
	c := s.contract
	c.Active = active
	c.Revision++
	s.contract = c
	return c, nil
}

func servingContractRouter(h *Handler, p auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("ray-platform-principal", p); c.Next() })
	r.GET("/api/v1/model-serving-contracts", h.listModelServingContracts)
	r.POST("/api/v1/model-serving-contracts", h.createModelServingContract)
	r.PATCH("/api/v1/model-serving-contracts/:contractId", h.updateModelServingContract)
	return r
}
func servingContractBody() string {
	return `{"name":"ZIP inference contract","description":"test","imageReference":"` + streamingTestImage + `","sourceArtifactId":"artifact-upload","entryPoint":["python","serve.py"],"inputExample":{"values":[1,2]},"outputDescription":"predictions"}`
}

func TestServingContractFreezesArchiveAndReplaysWithoutDuplicateSnapshot(t *testing.T) {
	h, _, submit, code, source := evaluationArchiveTestSetup()
	store := &servingContractStoreFake{}
	h.modelServing = store
	p := streamingPrincipal()
	p.Roles = []string{domain.RoleSuperAdmin}
	r := servingContractRouter(h, p)
	w := evaluationTestRequest(r, "POST", "/api/v1/model-serving-contracts", servingContractBody())
	if w.Code != 201 || code.published != 1 || store.creates != 1 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	if submit.last.Origin != domain.SubmissionOriginPortal || submit.last.Spec.Source.ArtifactID != source.artifact.ID || store.contract.Code == nil || store.contract.Code.SHA256 != source.artifact.SHA256 {
		t.Fatal("code preflight/freeze bypassed")
	}
	if store.contract.ID == evaluationCodeRequestID(p, "evaluation-request-key") {
		t.Fatal("serving reused evaluator namespace")
	}
	store.contract.InputExample = json.RawMessage(`{ "values": [1, 2] }`)
	w = evaluationTestRequest(r, "POST", "/api/v1/model-serving-contracts", servingContractBody())
	if w.Code != 200 || code.published != 1 || store.creates != 1 {
		t.Fatalf("replay duplicated snapshot %d %s", w.Code, w.Body.String())
	}
	w = evaluationTestRequest(r, "POST", "/api/v1/model-serving-contracts", strings.Replace(servingContractBody(), "predictions", "changed response", 1))
	if w.Code != 409 || code.published != 1 {
		t.Fatalf("definition conflict %d %s", w.Code, w.Body.String())
	}
}

func TestServingContractRejectsMemberForeignUploadAndUnknownJSON(t *testing.T) {
	for _, kind := range []string{"member", "foreign", "pending", "unknown", "duplicate", "image-digest", "missing-example"} {
		t.Run(kind, func(t *testing.T) {
			h, _, _, code, source := evaluationArchiveTestSetup()
			store := &servingContractStoreFake{}
			h.modelServing = store
			p := streamingPrincipal()
			p.Roles = []string{domain.RoleSuperAdmin}
			body := servingContractBody()
			switch kind {
			case "member":
				p.Roles = []string{domain.RoleEngineer}
			case "foreign":
				source.artifact.UserID = "another-user"
			case "pending":
				source.artifact.State = domain.SourceArtifactPending
			case "unknown":
				body = strings.Replace(body, `"name":`, `"ownerId":"forged","name":`, 1)
			case "duplicate":
				body = strings.Replace(body, `"name":`, `"name":"duplicate","name":`, 1)
			case "image-digest":
				body = strings.Replace(body, `"name":`, `"imageDigest":"sha256:forged","name":`, 1)
			case "missing-example":
				body = strings.Replace(body, `,"inputExample":{"values":[1,2]}`, "", 1)
			}
			w := evaluationTestRequest(servingContractRouter(h, p), "POST", "/api/v1/model-serving-contracts", body)
			if w.Code < 400 || code.published != 0 || store.creates != 0 {
				t.Fatalf("unsafe %s accepted %d %s", kind, w.Code, w.Body.String())
			}
		})
	}
}

func TestServingContractInactiveListingAndUpdatesRequireAdmin(t *testing.T) {
	h, _, _, _, _ := evaluationArchiveTestSetup()
	store := &servingContractStoreFake{contract: ms.Contract{ID: "contract", Revision: 1, Active: true}}
	h.modelServing = store
	p := streamingPrincipal()
	p.Roles = []string{domain.RoleEngineer}
	r := servingContractRouter(h, p)
	if w := evaluationTestRequest(r, "GET", "/api/v1/model-serving-contracts?includeInactive=true", ""); w.Code != 403 {
		t.Fatalf("member inactive listing %d", w.Code)
	}
	if w := evaluationTestRequest(r, "PATCH", "/api/v1/model-serving-contracts/contract", `{"active":false,"revision":1}`); w.Code != 403 {
		t.Fatalf("member update %d", w.Code)
	}
	p.Roles = []string{domain.RoleSuperAdmin}
	r = servingContractRouter(h, p)
	if w := evaluationTestRequest(r, "GET", "/api/v1/model-serving-contracts?includeInactive=true", ""); w.Code != 200 || !store.includeInactive {
		t.Fatalf("admin inactive listing %d", w.Code)
	}
	if w := evaluationTestRequest(r, "PATCH", "/api/v1/model-serving-contracts/contract", `{"active":false,"revision":1}`); w.Code != 200 || store.contract.Active {
		t.Fatalf("admin update %d", w.Code)
	}
	if w := evaluationTestRequest(r, "PATCH", "/api/v1/model-serving-contracts/contract", `{"active":true,"revision":1}`); w.Code != 409 {
		t.Fatalf("stale update %d", w.Code)
	}
}

func TestServingContractRecoversCommittedResponseAndHidesSnapshotFailure(t *testing.T) {
	h, _, _, code, _ := evaluationArchiveTestSetup()
	store := &servingContractStoreFake{createErr: errors.New("lost database response")}
	h.modelServing = store
	p := streamingPrincipal()
	p.Roles = []string{domain.RoleSuperAdmin}
	r := servingContractRouter(h, p)
	if w := evaluationTestRequest(r, "POST", "/api/v1/model-serving-contracts", servingContractBody()); w.Code != 200 || store.creates != 1 || code.published != 1 {
		t.Fatalf("lost response not reconciled %d %s", w.Code, w.Body.String())
	}
	h, _, _, code, _ = evaluationArchiveTestSetup()
	h.modelServing = &servingContractStoreFake{}
	code.err = errors.New("private TOS credentials")
	if w := evaluationTestRequest(servingContractRouter(h, p), "POST", "/api/v1/model-serving-contracts", servingContractBody()); w.Code != 503 || strings.Contains(w.Body.String(), "private TOS") {
		t.Fatalf("snapshot failure leaked %d %s", w.Code, w.Body.String())
	}
}
