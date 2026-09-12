package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/modellifecycle"
)

type modelSnapshotsFake struct {
	request     *modellifecycle.VersionRequest
	downloaded  bool
	downloadErr error
}

func (s *modelSnapshotsFake) RequestVersion(_ context.Context, r modellifecycle.VersionRequest) (modellifecycle.Version, error) {
	s.request = &r
	return modellifecycle.Version{ID: "version-1", ModelID: r.ModelID, State: modellifecycle.Pending, FileName: r.FileName, SourceRoot: r.SourceRoot, RelativePath: r.RelativePath}, nil
}
func (s *modelSnapshotsFake) Download(_ context.Context, _ modellifecycle.Version, w io.Writer) error {
	s.downloaded = true
	if s.downloadErr != nil {
		return s.downloadErr
	}
	_, err := w.Write([]byte("weights"))
	return err
}
func TestModelVersionPublicationRejectsForeignAndActiveSources(t *testing.T) {
	for _, tc := range []struct {
		name, owner string
		state       domain.State
		status      int
	}{
		{"foreign", "other", domain.StateSucceeded, 403}, {"active", "alice", domain.StateRunning, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := artifactJob("job-a", "local")
			job.UserID = tc.owner
			job.ObservedState = tc.state
			store := &modelStoreFake{model: modellifecycle.Model{ID: "model-1", OwnerID: "alice"}}
			snapshots := &modelSnapshotsFake{}
			h := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{job}}, Options{Models: store, ModelSnapshots: snapshots})
			r := modelRouter(h, auth.Principal{Subject: "alice", TenantID: "local", AuthType: auth.AuthTypeLocal})
			req := httptest.NewRequest("POST", "/api/v1/models/model-1/versions", strings.NewReader(`{"jobId":"job-a","path":"best.pth"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "new-version")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.status || snapshots.request != nil {
				t.Fatalf("unsafe source accepted: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
func TestModelVersionPublicationUsesServerRootAndHidesPrivatePaths(t *testing.T) {
	job := artifactJob("job-a", "local")
	job.UserID = "alice"
	job.ObservedState = domain.StateSucceeded
	job.Spec.Source.URI = "https://secret:password@internal/private"
	assets := &fakeStorageAssetStore{assets: []domain.StorageAsset{{ID: "outputs", Name: "outputs", Kind: domain.StorageAssetOutput, Provider: domain.StorageProviderTOS, ClaimName: "tos-outputs", RootPrefix: "platform/local/outputs", BrowseEnabled: true}}}
	store := &modelStoreFake{model: modellifecycle.Model{ID: "model-1", OwnerID: "alice"}}
	snapshots := &modelSnapshotsFake{}
	h := NewHandler(&fakeJobRepository{jobs: []domain.TrainingJob{job}}, Options{Models: store, ModelSnapshots: snapshots, StorageAssets: assets})
	r := modelRouter(h, auth.Principal{Subject: "alice", TenantID: "local", AuthType: auth.AuthTypeLocal})
	req := httptest.NewRequest("POST", "/api/v1/models/model-1/versions", strings.NewReader(`{"jobId":"job-a","path":"checkpoints/epoch-1.pth"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "new-version")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if snapshots.request == nil || snapshots.request.SourceRoot != "platform/local/outputs/runs/job-a" || snapshots.request.RelativePath != "checkpoints/epoch-1.pth" || snapshots.request.DatasetAssociation != "" {
		t.Fatalf("wrong request: %+v", snapshots.request)
	}
	for _, private := range []string{"platform/local", "checkpoints/", "password", "secret", "sourceRoot", "relativePath"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatalf("leaked %s", private)
		}
	}
}
func TestModelVersionPublicationRejectsPathsAndUnapprovedExtensions(t *testing.T) {
	for _, file := range []string{"../best.pth", "/best.pth", "config.py", "log.txt", ""} {
		store := &modelStoreFake{model: modellifecycle.Model{ID: "model-1", OwnerID: "alice"}}
		snapshots := &modelSnapshotsFake{}
		h := NewHandler(&fakeJobRepository{}, Options{Models: store, ModelSnapshots: snapshots})
		r := modelRouter(h, auth.Principal{Subject: "alice", TenantID: "local", AuthType: auth.AuthTypeLocal})
		req := httptest.NewRequest("POST", "/api/v1/models/model-1/versions", strings.NewReader(`{"jobId":"job-a","path":"`+file+`"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 400 || snapshots.request != nil {
			t.Fatalf("path %q accepted: %d", file, w.Code)
		}
	}
}

func TestSuperAdminCannotPublishAnotherUsersPrivateWeights(t *testing.T) {
 job:=artifactJob("foreign-job","other-team");job.UserID="alice";job.ObservedState=domain.StateSucceeded
 snapshots:=&modelSnapshotsFake{}
 store:=&modelStoreFake{model:modellifecycle.Model{ID:"model-1",OwnerID:"someone-else"}}
 h:=NewHandler(&fakeJobRepository{jobs:[]domain.TrainingJob{job}},Options{Models:store,ModelSnapshots:snapshots})
 r:=modelRouter(h,auth.Principal{Subject:"admin",TenantID:"local",AuthType:auth.AuthTypeLocal,Roles:[]string{domain.RoleSuperAdmin}})
 req:=httptest.NewRequest("POST","/api/v1/models/model-1/versions",strings.NewReader(`{"jobId":"foreign-job","path":"weights.pth"}`));req.Header.Set("Idempotency-Key","explicit-request");req.Header.Set("Content-Type","application/json")
 w:=httptest.NewRecorder();r.ServeHTTP(w,req)
 if w.Code!=403||snapshots.request!=nil{t.Fatalf("admin source publication status %d, request %+v",w.Code,snapshots.request)}
}
func TestSharedModelDownloadsOnlyReadyAcrossTenants(t *testing.T){
 for _,state:=range []string{modellifecycle.Pending,modellifecycle.Copying,modellifecycle.Failed,modellifecycle.Ready}{
  t.Run(state,func(t *testing.T){
   snapshots:=&modelSnapshotsFake{}
   store:=&modelStoreFake{model:modellifecycle.Model{ID:"model-1",OwnerID:"alice",TenantID:"other"},version:modellifecycle.Version{ID:"version-1",ModelID:"model-1",State:state,SizeBytes:7,FileName:"weights.pth",SHA256:strings.Repeat("a",64)}}
   h:=NewHandler(&fakeJobRepository{},Options{Models:store,ModelSnapshots:snapshots})
   r:=modelRouter(h,auth.Principal{Subject:"bob",TenantID:"local",AuthType:auth.AuthTypeOIDC})
   w:=httptest.NewRecorder();r.ServeHTTP(w,httptest.NewRequest("GET","/api/v1/models/model-1/versions/version-1/download",nil))
   if state==modellifecycle.Ready {
    if w.Code!=200||!snapshots.downloaded||w.Body.String()!="weights"||w.Header().Get("X-Content-SHA256")!=store.version.SHA256{t.Fatalf("ready download: %d %s",w.Code,w.Body.String())}
   }else if w.Code!=409||snapshots.downloaded||w.Header().Get("Content-Disposition")!=""{t.Fatalf("nonready downloadable: %d %s",w.Code,w.Body.String())}
  })
 }
}
func TestSharedModelCorruptionBeforeStreamingReturnsServiceError(t *testing.T){
 snapshots:=&modelSnapshotsFake{downloadErr:modellifecycle.ErrInvalid}
 store:=&modelStoreFake{model:modellifecycle.Model{ID:"model-1"},version:modellifecycle.Version{State:modellifecycle.Ready,SizeBytes:7,FileName:"weights.pth"}}
 h:=NewHandler(&fakeJobRepository{},Options{Models:store,ModelSnapshots:snapshots})
 r:=modelRouter(h,auth.Principal{Subject:"bob",TenantID:"local",AuthType:auth.AuthTypeLocal})
 w:=httptest.NewRecorder();r.ServeHTTP(w,httptest.NewRequest("GET","/api/v1/models/model-1/versions/version-1/download",nil))
 if w.Code!=503||w.Header().Get("Content-Length")!=""||w.Header().Get("Content-Disposition")!=""||!strings.Contains(w.Body.String(),"MODEL_DOWNLOAD_FAILED"){t.Fatalf("bad corruption response %d %s",w.Code,w.Body.String())}
}
