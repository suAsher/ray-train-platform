package api

import (
	"context"
	"encoding/base64"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	me "ray-train-platform-backend/modelevaluation"
	ml "ray-train-platform-backend/modellifecycle"
	ms "ray-train-platform-backend/modelserving"
	"strings"
	"testing"
	"time"
)

type servingInternalFake struct {
	ms.Store
	deployment ms.Deployment
	err        error
	calls      int
}

func (s *servingInternalFake) AuthorizeServingJobToken(context.Context, string, []byte, time.Time) (ms.Deployment, error) {
	s.calls++
	return s.deployment, s.err
}
func TestServingInternalDownloadsAndTokenIsolation(t *testing.T) {
	job := "job-0123456789abcdef01234567"
	snapshot := me.CodeSnapshot{ID: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64), SizeBytes: 3, Format: "zip"}
	base := ms.Deployment{ID: "deployment", JobID: job, State: ms.Submitted, ExpiresAt: time.Now().Add(time.Hour), ModelID: "model", VersionID: "version", ModelSHA256: strings.Repeat("c", 64), ModelSizeBytes: 10, FileName: "model.pt", Contract: ms.Contract{Code: &snapshot}}
	for _, tc := range []struct {
		name, endpoint, token string
		mutate                func(*ms.Deployment)
		err                   error
		want                  int
	}{
		{name: "code", endpoint: "code", token: "valid", want: 200},
		{name: "model", endpoint: "model", token: "valid", want: 200},
		{name: "no token", endpoint: "code", want: 401},
		{name: "invalid token", endpoint: "code", token: "bad", want: 401},
		{name: "wrong token", endpoint: "code", token: "valid", err: ms.ErrUnauthorized, want: 401},
		{name: "cross job", endpoint: "code", token: "valid", mutate: func(d *ms.Deployment) { d.JobID = "job-aaaaaaaaaaaaaaaaaaaaaaaa" }, want: 404},
		{name: "stopped", endpoint: "code", token: "valid", mutate: func(d *ms.Deployment) { d.State = ms.Stopped }, want: 403},
		{name: "expired", endpoint: "code", token: "valid", mutate: func(d *ms.Deployment) { d.ExpiresAt = time.Now().Add(-time.Minute) }, want: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := base
			if tc.mutate != nil {
				tc.mutate(&d)
			}
			s := &servingInternalFake{deployment: d, err: tc.err}
			code := &evaluationCodeStoreFake{content: "zip", size: 3}
			h := NewHandler(&fakeJobRepository{}, Options{ModelServing: s, EvaluationCode: code, Models: &modelStoreFake{version: ml.Version{ID: "version", ModelID: "model", State: ml.Ready, SHA256: base.ModelSHA256, SizeBytes: 10, FileName: "model.pt"}}, ModelSnapshots: finiteSnapshotFake{}})
			r := gin.New()
			h.RegisterModelServingInternalRoutes(r.Group("/api/v1/internal"))
			req := httptest.NewRequest("GET", "/api/v1/internal/jobs/"+job+"/model-serving/"+tc.endpoint, nil)
			if tc.token != "" {
				token := tc.token
				if token == "valid" {
					token = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
				}
				req.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if tc.want != 200 && code.opened != 0 {
				t.Fatal("denied token opened snapshot")
			}
			if tc.want == 200 {
				expected := "zip"
				if tc.endpoint == "model" {
					expected = "checkpoint"
				}
				if w.Body.String() != expected {
					t.Fatalf("body %q", w.Body.String())
				}
			}
		})
	}
}
