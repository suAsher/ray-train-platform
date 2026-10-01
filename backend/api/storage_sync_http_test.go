package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	ss "ray-train-platform-backend/storagesync"
)

type syncHTTPRepo struct {
	plans    map[string]ss.Plan
	runs     map[string]ss.Run
	previews map[string]ss.Preview
}
type syncHTTPTx struct{ *syncHTTPRepo }

func (r *syncHTTPRepo) Transact(_ context.Context, f func(ss.Tx) error) error {
	return f(&syncHTTPTx{r})
}
func (r *syncHTTPRepo) GetPlan(_ context.Context, id string) (ss.Plan, error) {
	return (&syncHTTPTx{r}).GetPlan(id)
}
func (r *syncHTTPRepo) ListPlans(context.Context) ([]ss.Plan, error) {
	return (&syncHTTPTx{r}).ListPlans()
}
func (r *syncHTTPRepo) GetRun(_ context.Context, id string) (ss.Run, error) {
	return (&syncHTTPTx{r}).GetRun(id)
}
func (r *syncHTTPRepo) ListRuns(_ context.Context, id string) ([]ss.Run, error) {
	return (&syncHTTPTx{r}).ListRuns(id)
}
func (r *syncHTTPRepo) GetPreview(_ context.Context, id string) (ss.Preview, error) {
	return (&syncHTTPTx{r}).GetPreview(id)
}
func (r *syncHTTPRepo) ListRunFiles(context.Context, string, string, int) (ss.FilePage, error) {
	return ss.FilePage{Items: []ss.FileResult{}}, nil
}
func (r *syncHTTPTx) GetPlan(id string) (ss.Plan, error) {
	p, ok := r.plans[id]
	if !ok {
		return p, ss.ErrNotFound
	}
	return p, nil
}
func (r *syncHTTPTx) ListPlans() ([]ss.Plan, error) {
	out := []ss.Plan{}
	for _, p := range r.plans {
		out = append(out, p)
	}
	return out, nil
}
func (r *syncHTTPTx) PutPlan(p ss.Plan) error { r.plans[p.ID] = p; return nil }
func (r *syncHTTPTx) GetRun(id string) (ss.Run, error) {
	p, ok := r.runs[id]
	if !ok {
		return p, ss.ErrNotFound
	}
	return p, nil
}
func (r *syncHTTPTx) ListRuns(id string) ([]ss.Run, error) {
	out := []ss.Run{}
	for _, p := range r.runs {
		if id == "" || p.PlanID == id {
			out = append(out, p)
		}
	}
	return out, nil
}
func (r *syncHTTPTx) PutRun(p ss.Run) error { r.runs[p.ID] = p; return nil }
func (r *syncHTTPTx) GetPreview(id string) (ss.Preview, error) {
	p, ok := r.previews[id]
	if !ok {
		return p, ss.ErrNotFound
	}
	return p, nil
}
func (r *syncHTTPTx) ListPreviews() ([]ss.Preview, error) {
	out := []ss.Preview{}
	for _, p := range r.previews {
		out = append(out, p)
	}
	return out, nil
}
func (r *syncHTTPTx) PutPreview(p ss.Preview) error                            { r.previews[p.ID] = p; return nil }
func (r *syncHTTPTx) AcquireLocks(string, int, []ss.PathLock) error            { return nil }
func (r *syncHTTPTx) ReleaseLocks(string) error                                { return nil }
func (r *syncHTTPTx) PutFileResults(string, int, int64, []ss.FileResult) error { return nil }
func syncHTTPFixture(t *testing.T) (*gin.Engine, *syncHTTPRepo, *StorageSyncHandler) {
	t.Helper()
	resolver, _ := syncResolverFixture()
	repo := &syncHTTPRepo{plans: map[string]ss.Plan{}, runs: map[string]ss.Run{}, previews: map[string]ss.Preview{}}
	manager := ss.NewManager(repo, nil, resolver, ss.Options{})
	h := NewStorageSyncHandler(manager, resolver, nil, []byte("test-secret-key"))
	router := gin.New()
	public := router.Group("/api/v1", func(c *gin.Context) {
		p := auth.Principal{Subject: "admin", TenantID: "local", Roles: []string{"SuperAdmin"}, AuthType: auth.AuthTypeOAuth2Proxy}
		c.Request = c.Request.WithContext(auth.SetPrincipalContext(c.Request.Context(), p))
		c.Next()
	})
	h.RegisterManagementRoutes(public)
	h.RegisterInternalRoutes(router.Group("/api/v1/internal"))
	return router, repo, h
}
func syncHTTPCall(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "request-one")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}
func syncHTTPConfig() ss.Config {
	return ss.Config{Mode: "INCREMENTAL", ConflictPolicy: "UPDATE", Verification: "METADATA", Concurrency: 1, Schedule: ss.Schedule{Kind: "MANUAL", Timezone: "Asia/Shanghai"}, Mappings: []ss.Mapping{{Source: ss.Location{SpaceID: "idc-spk-hybrid", RelativePath: "small"}, Destination: ss.Location{SpaceID: "my-files", RelativePath: "acceptance"}, Layout: "CONTENTS"}}}
}
func TestStorageSyncHTTPKeepsOtherAdminsPersonalRecordsPrivate(t *testing.T) {
	router, repo, _ := syncHTTPFixture(t)
	config := syncHTTPConfig()
	repo.plans["personal"] = ss.Plan{ID: "personal", CreatedBy: "other", Config: config}
	repo.runs["private"] = ss.Run{ID: "private", PlanID: "personal", RequestedBy: "other", Config: config}
	repo.previews["browse"] = ss.Preview{ID: "browse", Kind: "BROWSE", Actor: "other", BrowseEntries: []ss.BrowseEntry{{Name: "private-file", RelativePath: "private-file", Kind: "FILE"}}}
	repo.previews["preview"] = ss.Preview{ID: "preview", Kind: "PREVIEW", Actor: "other", Config: config}
	for _, path := range []string{"/plans", "/runs"} {
		w := syncHTTPCall(router, "GET", "/api/v1/admin/storage-sync"+path, "")
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var response struct {
			Data struct {
				Items []json.RawMessage `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Data.Items) != 0 {
			t.Errorf("%s exposed private records: %s", path, w.Body.String())
		}
	}
	for _, route := range []struct{ method, path, body string }{{"GET", "/runs/private", ""}, {"GET", "/runs/private/files", ""}, {"GET", "/browse-requests/browse", ""}, {"GET", "/previews/preview", ""}, {"POST", "/previews", `{"planId":"personal","configRevision":1}`}} {
		w := syncHTTPCall(router, route.method, "/api/v1/admin/storage-sync"+route.path, route.body)
		if w.Code != 404 {
			t.Errorf("private route %s status=%d %s", route.path, w.Code, w.Body.String())
		}
	}
}
func TestStorageSyncHTTPAuthorizedPreviewAndStrictPayload(t *testing.T) {
	router, repo, _ := syncHTTPFixture(t)
	body, _ := json.Marshal(map[string]any{"name": "copy", "config": syncHTTPConfig()})
	created := syncHTTPCall(router, "POST", "/api/v1/admin/storage-sync/plans", string(body))
	if created.Code != 201 {
		t.Fatal(created.Body.String())
	}
	var response struct {
		Data ss.Plan `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	p := response.Data
	if p.Enabled {
		t.Fatal("save automatically enabled schedule")
	}
	preview := syncHTTPCall(router, "POST", "/api/v1/admin/storage-sync/previews", `{"planId":"`+p.ID+`","configRevision":1}`)
	if preview.Code != 202 {
		t.Fatal(preview.Body.String())
	}
	for _, malformed := range []string{`{"name":"x","bucket":"foreign"}`, `{} {}`, strings.Repeat("x", (1<<20)+1)} {
		w := syncHTTPCall(router, "POST", "/api/v1/admin/storage-sync/plans", malformed)
		if w.Code != 400 {
			t.Errorf("invalid payload status=%d", w.Code)
		}
	}
	if len(repo.plans) != 1 {
		t.Fatal("invalid body created a plan")
	}
}
func TestStorageSyncHTTPInternalClaimAndStaleScope(t *testing.T) {
	router, repo, h := syncHTTPFixture(t)
	now := time.Now()
	repo.runs["ssr-test"] = ss.Run{ID: "ssr-test", State: "RUNNING", Phase: "PREVIEW", Attempt: 1, Generation: 1, RequestedBy: "admin", Config: syncHTTPConfig(), CreatedAt: now}
	request := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/internal/storage-sync/run/ssr-test/1/1/claim", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	body := `{"runId":"ssr-test","attempt":1,"generation":1,"workerId":"pod-1"}`
	if w := request("invalid", body); w.Code != 401 {
		t.Fatal(w.Body.String())
	}
	token := StorageSyncWorkerToken(h.key, "run", "ssr-test", 1, 1)
	if w := request(token, body); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := request(token, strings.ReplaceAll(body, "pod-1", "pod-2")); w.Code != 409 {
		t.Fatal("duplicate worker claimed", w.Body.String())
	}
}
