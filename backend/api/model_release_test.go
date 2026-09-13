package api

import (
	"context"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	mr "ray-train-platform-backend/modelrelease"
	"strings"
	"testing"
)

type releaseAPIFake struct {
	mr.Repository
	release mr.Release
	called  bool
}

func (s *releaseAPIFake) GetRelease(context.Context, string, mr.Actor) (mr.Release, error) {
	return s.release, nil
}
func (s *releaseAPIFake) Decide(context.Context, string, mr.Decision, mr.Actor) (mr.Release, error) {
	s.called = true
	return s.release, nil
}
func releaseAPIRouter(s *releaseAPIFake, p auth.Principal) *gin.Engine {
	h := NewHandler(&fakeJobRepository{}, Options{Models: &modelStoreFake{}, ModelReleases: s})
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("ray-platform-principal", p); c.Next() })
	h.RegisterModelReleaseRoutes(r.Group("/api/v1"))
	return r
}
func TestReleaseDecisionRejectsPATMalformedJSONAndSelf(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		p          auth.Principal
		want       int
	}{
		{"pat", `{"revision":1,"approve":true,"reason":"ok"}`, auth.Principal{Subject: "reviewer", TenantID: "local", AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeJobsWrite}}, 403},
		{"unknown", `{"revision":1,"approve":true,"ownerId":"other"}`, auth.Principal{Subject: "reviewer", TenantID: "local", AuthType: auth.AuthTypeLocal}, 400},
		{"self", `{"revision":1,"approve":true,"reason":"ok"}`, auth.Principal{Subject: "owner", TenantID: "local", AuthType: auth.AuthTypeLocal, Roles: []string{domain.RoleSuperAdmin}}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &releaseAPIFake{release: mr.Release{ApplicantID: "owner", ModelOwnerID: "owner", TenantID: "local", State: mr.Pending}}
			r := releaseAPIRouter(s, tc.p)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/model-releases/release-1/decision", strings.NewReader(tc.body)))
			if w.Code != tc.want || s.called {
				t.Fatalf("status=%d body=%s called=%v", w.Code, w.Body.String(), s.called)
			}
		})
	}
}
func TestReleaseEvidenceFlagsRespectTeam(t *testing.T) {
	r := mr.Release{State: mr.Pending, ApplicantID: "owner", ModelOwnerID: "owner", TenantID: "team-a", DatasetTenantID: "team-a", DatasetVisibility: "TEAM"}
	a := mr.Actor{ID: "reviewer", TenantID: "team-b", TenantAdmin: true}
	got := releaseResponse(r, a)
	if got.CanReview || got.CanReadEvidence || got.CanPublish {
		t.Fatal("cross-team permissions exposed")
	}
	a.TenantID = "team-a"
	got = releaseResponse(r, a)
	if !got.CanReview || !got.CanReadEvidence {
		t.Fatal("same-team review unavailable")
	}
}
