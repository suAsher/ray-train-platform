package api

import (
	"context"
	"github.com/gin-gonic/gin"
	"io"
	"net/http/httptest"
	"ray-train-platform-backend/auth"
	ml "ray-train-platform-backend/modellifecycle"
	mr "ray-train-platform-backend/modelregistry"
	"strings"
	"testing"
	"time"
)

type registryStoreFake struct {
	mr.Store
	calls int
}

func (s *registryStoreFake) Get(context.Context, string) (mr.Record, error) {
	s.calls++
	return mr.Record{State: "READY", RegisteredName: "rt-model", RegistryVersion: "2"}, nil
}
func TestModelRegistryReadBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		p          auth.Principal
		want       int
	}{
		{"member", "/api/v1/models/model/versions/version/registry", auth.Principal{Subject: "member", TenantID: "other-team", AuthType: auth.AuthTypeLocal}, 200},
		{"pat", "/api/v1/models/model/versions/version/registry", auth.Principal{Subject: "member", TenantID: "other-team", AuthType: auth.AuthTypePAT}, 403},
		{"hugequery", "/api/v1/models/model/versions/version/registry?unused=" + strings.Repeat("x", 2049), auth.Principal{Subject: "member", TenantID: "team", AuthType: auth.AuthTypeLocal}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &registryStoreFake{}
			h := NewHandler(&fakeJobRepository{}, Options{Models: &modelStoreFake{model: ml.Model{ID: "model", OwnerID: "owner"}}, ModelRegistryLinks: s})
			r := gin.New()
			r.Use(func(c *gin.Context) { c.Set("ray-platform-principal", tc.p); c.Next() })
			h.RegisterModelRegistryRoutes(r.Group("/api/v1"))
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			if w.Code != tc.want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if tc.want != 200 && s.calls != 0 {
				t.Fatal("denied request reached store")
			}
		})
	}
}

type cancellableSnapshotFake struct{ done chan struct{} }

func (s *cancellableSnapshotFake) RequestVersion(context.Context, ml.VersionRequest) (ml.Version, error) {
	return ml.Version{}, nil
}
func (s *cancellableSnapshotFake) Download(ctx context.Context, _ ml.Version, w io.Writer) error {
	defer close(s.done)
	_, err := w.Write(make([]byte, 1<<16))
	return err
}
func TestRegistrySnapshotCloseReleasesBlockedWriter(t *testing.T) {
	s := &cancellableSnapshotFake{done: make(chan struct{})}
	r, err := snapshotReader(context.Background(), s, ml.Version{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("snapshot writer leaked after reader close")
	}
}

type finiteSnapshotFake struct{}

func (finiteSnapshotFake) RequestVersion(context.Context, ml.VersionRequest) (ml.Version, error) {
	return ml.Version{}, nil
}
func (finiteSnapshotFake) Download(_ context.Context, _ ml.Version, w io.Writer) error {
	_, err := io.WriteString(w, "checkpoint")
	return err
}
func TestRegistrySnapshotSuccessfulRead(t *testing.T) {
	r, err := snapshotReader(context.Background(), finiteSnapshotFake{}, ml.Version{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	raw, err := io.ReadAll(r)
	if err != nil || string(raw) != "checkpoint" {
		t.Fatalf("read %q %v", raw, err)
	}
}
