package api

import (
	"bufio"
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kfake "k8s.io/client-go/kubernetes/fake"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	platformk8s "ray-train-platform-backend/k8s"
	"ray-train-platform-backend/personalstorage"
	"strings"
	"testing"
	"time"
)

func TestPersonalStorageOwnerUsesStableIdentityNotTeam(t *testing.T) {
	first, err := personalStorageOwner(auth.Principal{Subject: "platform-user-1", Username: "guofeng.su", TenantID: "team-a", AuthType: auth.AuthTypeOIDC})
	if err != nil {
		t.Fatal(err)
	}
	second, err := personalStorageOwner(auth.Principal{Subject: "platform-user-1", Username: "guofeng.su", TenantID: "team-b", AuthType: auth.AuthTypeOAuth2Proxy})
	if err != nil || first != second || first.ID != "platform-user-1" {
		t.Fatalf("team must not select personal storage: %#v %#v %v", first, second, err)
	}
}
func TestPersonalStorageRejectsMachineAndMissingIdentity(t *testing.T) {
	for _, principal := range []auth.Principal{{Subject: "alice", Username: "alice", AuthType: auth.AuthTypePAT}, {Username: "alice", AuthType: auth.AuthTypeOIDC}, {Subject: "alice", AuthType: auth.AuthTypeOIDC}} {
		if _, err := personalStorageOwner(principal); err == nil {
			t.Fatalf("must reject invalid owner: %#v", principal)
		}
	}
}

type recordingPersonalService struct {
	owner     personalstorage.Owner
	enrolled  int
	verified  int
	opened    int
	view      personalstorage.View
	store     personalstorage.Store
	stores    map[string]personalstorage.Store
	reference personalstorage.MountReference
}

func (s *recordingPersonalService) View(_ context.Context, o personalstorage.Owner) (personalstorage.View, error) {
	s.owner = o
	return s.view, nil
}
func (s *recordingPersonalService) Enroll(_ context.Context, o personalstorage.Owner) (personalstorage.View, error) {
	s.owner = o
	s.enrolled++
	return s.view, nil
}
func (s *recordingPersonalService) Verify(_ context.Context, o personalstorage.Owner) (personalstorage.View, error) {
	s.owner = o
	s.verified++
	return s.view, nil
}
func (s *recordingPersonalService) OpenStore(_ context.Context, o personalstorage.Owner) (personalstorage.Store, error) {
	s.owner = o
	s.opened++
	if s.stores != nil {
		return s.stores[o.ID], nil
	}
	return s.store, nil
}
func (s *recordingPersonalService) MountReference(context.Context, personalstorage.Owner) (personalstorage.MountReference, error) {
	if s.reference.BindingID != "" {
		return s.reference, nil
	}
	return personalstorage.MountReference{}, personalstorage.ErrNotReady
}
func personalStorageTestRouter(h *Handler, p *auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if p != nil {
			c.Set("ray-platform-principal", *p)
		}
		c.Next()
	})
	h.RegisterPersonalStorageRoutes(r.Group("/api/v1"))
	h.RegisterDataSpaceRoutes(r.Group("/api/v1"))
	return r
}
func TestPersonalStorageRoutesRejectPATAndRequestedOwner(t *testing.T) {
	service := &recordingPersonalService{view: personalstorage.View{BindingID: "binding", State: "PENDING", PublicKey: "ssh-ed25519 public-only", Username: "alice"}}
	for _, body := range []string{`{"ownerID":"bob"}`, `{"username":"bob"}`, `{"host":"attacker"}`, `{"root":"/users"}`} {
		principal := auth.Principal{Subject: "stable-alice", Username: "alice", TenantID: "team-a", AuthType: auth.AuthTypeOIDC, Roles: []string{domain.RoleSuperAdmin}}
		h := NewHandler(&fakeJobRepository{}, Options{PersonalStorage: service})
		r := personalStorageTestRouter(h, &principal)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/personal-storage/wellspiking", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-RayTrain-Personal-Storage", "1")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest || service.enrolled != 0 {
			t.Fatalf("requested owner/account/config accepted: %d %s", w.Code, w.Body)
		}
	}
	principal := auth.Principal{Subject: "stable-alice", Username: "alice", TenantID: "team-a", AuthType: auth.AuthTypePAT, Roles: []string{domain.RoleSuperAdmin}}
	h := NewHandler(&fakeJobRepository{}, Options{PersonalStorage: service})
	r := personalStorageTestRouter(h, &principal)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/personal-storage/wellspiking", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("PAT must not enroll/browse personal storage: %d", w.Code)
	}
}
func TestPersonalStorageEnrollmentAndStatusExposeOnlyOwnPublicMetadata(t *testing.T) {
	service := &recordingPersonalService{view: personalstorage.View{BindingID: "private-binding", OwnerID: "stable-alice", State: "PENDING", PublicKey: "ssh-ed25519 public-only", Username: "alice", Generation: 1}}
	principal := auth.Principal{Subject: "stable-alice", Username: "alice", TenantID: "team-a", AuthType: auth.AuthTypeOAuth2Proxy}
	repo := &fakeJobRepository{}
	h := NewHandler(repo, Options{PersonalStorage: service})
	r := personalStorageTestRouter(h, &principal)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/personal-storage/wellspiking", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-RayTrain-Personal-Storage", "1")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || service.owner.ID != principal.Subject || service.owner.Username != "alice" || service.enrolled != 1 {
		t.Fatalf("wrong personal enrollment %d %#v", w.Code, service)
	}
	body := w.Body.String()
	for _, forbidden := range []string{"ownerID", "bindingID", "SecretName", "privateKey", "private-binding"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("private internal metadata leaked: %s", body)
		}
	}
	identities := repo.identity
	principal.TenantID = "team-b"
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/personal-storage/wellspiking", nil))
	if w.Code != http.StatusOK || service.owner.ID != "stable-alice" || repo.identity != identities || service.enrolled != 1 {
		t.Fatal("status must not provision or change personal binding across teams")
	}
}
func TestPersonalStorageDisabledAndUnauthenticated(t *testing.T) {
	h := NewHandler(&fakeJobRepository{}, Options{})
	r := personalStorageTestRouter(h, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/personal-storage/wellspiking", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous accepted %d", w.Code)
	}
	p := auth.Principal{Subject: "alice", Username: "alice", AuthType: auth.AuthTypeOIDC}
	r = personalStorageTestRouter(h, &p)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/personal-storage/wellspiking", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("disabled status must be explicit: %s", w.Body)
	}
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/personal-storage/wellspiking", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-RayTrain-Personal-Storage", "1")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled enrollment accepted %d", w.Code)
	}
}
func TestPersonalStorageWorkspaceUnboundAndPendingBehavior(t *testing.T) {
	p := auth.Principal{Subject: "alice", Username: "alice", AuthType: auth.AuthTypeOIDC, TenantID: "team-a"}
	service := &recordingPersonalService{view: personalstorage.View{State: "UNBOUND"}}
	h := NewHandler(&fakeJobRepository{}, Options{PersonalStorage: service})
	if claim, err := h.resolveWorkspacePersonalStorage(context.Background(), p); err != nil || claim != "" {
		t.Fatalf("unconnected existing behavior changed %q %v", claim, err)
	}
	service.view = personalstorage.View{State: "PENDING", BindingID: "alice-binding"}
	if _, err := h.resolveWorkspacePersonalStorage(context.Background(), p); err == nil {
		t.Fatal("bound pending storage must block fallback")
	}
}

type personalHTTPStore struct{ root string }

func (s *personalHTTPStore) List(_ context.Context, p string) ([]personalstorage.Entry, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, p))
	if err != nil {
		return nil, err
	}
	out := []personalstorage.Entry{}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, personalstorage.Entry{Name: e.Name(), Size: info.Size(), Mode: info.Mode(), ModTime: info.ModTime()})
	}
	return out, nil
}
func (s *personalHTTPStore) Stat(_ context.Context, p string) (personalstorage.Entry, error) {
	info, err := os.Stat(filepath.Join(s.root, p))
	if err != nil {
		return personalstorage.Entry{}, err
	}
	return personalstorage.Entry{Name: info.Name(), Size: info.Size(), Mode: info.Mode(), ModTime: info.ModTime()}, nil
}
func (s *personalHTTPStore) Open(_ context.Context, p string, flags int) (personalstorage.File, error) {
	return os.OpenFile(filepath.Join(s.root, p), flags, 0600)
}
func (s *personalHTTPStore) Mkdir(_ context.Context, p string) error {
	return os.Mkdir(filepath.Join(s.root, p), 0700)
}
func (s *personalHTTPStore) Remove(_ context.Context, p string) error {
	return os.Remove(filepath.Join(s.root, p))
}
func (s *personalHTTPStore) Rename(_ context.Context, a, b string) error {
	return os.Rename(filepath.Join(s.root, a), filepath.Join(s.root, b))
}
func (s *personalHTTPStore) Close() error { return nil }

func TestPersonalStorageHTTPFilesRemainOwnedAcrossAdminAndTeamSwitch(t *testing.T) {
	aliceRoot, bobRoot := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(aliceRoot, "alice.txt"), []byte("alice-private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bobRoot, "bob.txt"), []byte("bob-private"), 0600); err != nil {
		t.Fatal(err)
	}
	service := &recordingPersonalService{view: personalstorage.View{State: "READY", BindingID: "binding-a"}, stores: map[string]personalstorage.Store{"alice": &personalHTTPStore{root: aliceRoot}, "bob": &personalHTTPStore{root: bobRoot}}}
	principal := auth.Principal{Subject: "alice", Username: "alice", TenantID: "team-a", AuthType: auth.AuthTypeOIDC, Roles: []string{domain.RoleSuperAdmin}}
	h := NewHandler(&fakeJobRepository{}, Options{PersonalStorage: service, PersonalStorageMaxUploadBytes: 32})
	r := personalStorageTestRouter(h, &principal)
	request := func(method, url, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-RayTrain-Personal-Storage", "1")
		w := httptest.NewRecorder()
		req = req.WithContext(context.WithValue(req.Context(), personalStorageHTTPControllerKey{}, http.NewResponseController(&personalDeadlineRecorder{ResponseRecorder: w})))
		r.ServeHTTP(w, req)
		return w
	}
	w := request(http.MethodGet, "/api/v1/data-spaces/wellspiking-personal/entries?ownerID=bob", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "alice.txt") || strings.Contains(w.Body.String(), "bob.txt") {
		t.Fatalf("administrator selected another owner %d %s", w.Code, w.Body)
	}
	principal.TenantID = "team-b"
	w = request(http.MethodPut, "/api/v1/data-spaces/wellspiking-personal/content?path=upload.txt", "written-as-alice")
	if w.Code != http.StatusOK {
		t.Fatalf("personal upload failed %d %s", w.Code, w.Body)
	}
	if data, err := os.ReadFile(filepath.Join(aliceRoot, "upload.txt")); err != nil || string(data) != "written-as-alice" {
		t.Fatalf("upload lost stable owner %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(bobRoot, "upload.txt")); !os.IsNotExist(err) {
		t.Fatal("upload crossed into another user's root")
	}
	w = request(http.MethodPost, "/api/v1/data-spaces/wellspiking-personal/folders", `{"path":"newdir"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("mkdir failed %d %s", w.Code, w.Body)
	}
	for _, badPath := range []string{"../bob", "/bob", ".shared", ".spiking-group-shared"} {
		w = request(http.MethodPost, "/api/v1/data-spaces/wellspiking-personal/folders", `{"path":"`+badPath+`"}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("unsafe path accepted %q %d", badPath, w.Code)
		}
	}
	w = request(http.MethodPut, "/api/v1/data-spaces/wellspiking-personal/content?path=too-large.txt", strings.Repeat("x", 33))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatal("oversize personal upload accepted")
	}
	principal.Subject = "bob"
	principal.Username = "bob"
	w = request(http.MethodGet, "/api/v1/data-spaces/wellspiking-personal/entries", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "alice.txt") || !strings.Contains(w.Body.String(), "bob.txt") {
		t.Fatalf("bob can browse alice files: %s", w.Body)
	}
	for _, route := range []string{"download?path=bob.txt", "video?path=bob.txt"} {
		w = request(http.MethodGet, "/api/v1/data-spaces/wellspiking-personal/"+route, "")
		if w.Code == http.StatusOK {
			t.Fatalf("unsupported provider operation allowed %q", route)
		}
	}
}

func TestPersonalStorageCSRFRejectedBeforeEffects(t *testing.T) {
	service := &recordingPersonalService{view: personalstorage.View{State: "READY"}}
	p := auth.Principal{Subject: "alice", Username: "alice", TenantID: "team-a", AuthType: auth.AuthTypeOAuth2Proxy}
	h := NewHandler(&fakeJobRepository{}, Options{PersonalStorage: service})
	r := personalStorageTestRouter(h, &p)
	for _, route := range []string{"/personal-storage/wellspiking", "/personal-storage/wellspiking/verify", "/data-spaces/wellspiking-personal/folders", "/data-spaces/wellspiking-personal/uploads"} {
		for _, crossSite := range []bool{false, true} {
			req := httptest.NewRequest(http.MethodPost, "/api/v1"+route, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			if crossSite {
				req.Header.Set("X-RayTrain-Personal-Storage", "1")
				req.Header.Set("Sec-Fetch-Site", "cross-site")
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden || service.enrolled != 0 || service.verified != 0 || service.opened != 0 {
				t.Fatalf("untrusted mutation caused effects %s %d", route, w.Code)
			}
		}
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/data-spaces/wellspiking-personal/content?path=x", strings.NewReader("x"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || service.opened != 0 {
		t.Fatal("simple raw upload reached remote store")
	}
}

func TestPersonalStorageFirstWorkspacePreparesNamespaceBeforeMount(t *testing.T) {
	service := &recordingPersonalService{view: personalstorage.View{State: "READY", BindingID: "binding-a"}, reference: personalstorage.MountReference{OwnerID: "alice", BindingID: "binding-a", Username: "alice", Generation: 1, SecretName: "personal-a"}}
	client := kfake.NewSimpleClientset()
	repo := &fakeJobRepository{}
	h := NewHandler(repo, Options{PersonalStorage: service, Kubernetes: platformk8s.NewClientFromInterfaces(nil, client), PlatformNamespace: "platform", PersonalStorageCSIImage: "registry/csi@sha256:" + strings.Repeat("a", 64)})
	p := auth.Principal{Subject: "alice", Username: "alice", TenantID: "new-team", AuthType: auth.AuthTypeOIDC}
	if _, err := h.resolveWorkspacePersonalStorage(context.Background(), p); err == nil {
		t.Fatal("driver is missing and must fail closed")
	}
	if _, err := client.CoreV1().Namespaces().Get(context.Background(), "tenant-new-team", metav1.GetOptions{}); err != nil {
		t.Fatalf("new tenant namespace was not prepared before storage: %v", err)
	}
	if repo.identity != 1 {
		t.Fatal("stable user must persist before mount")
	}
}
func TestPersonalStorageExistingWorkspaceIsNotModifiedOrProvisioned(t *testing.T) {
	service := &recordingPersonalService{view: personalstorage.View{State: "PENDING", BindingID: "binding-a"}}
	workspace := domain.DevWorkspace{ID: "ws-existing", TenantID: "team-a", UserID: "alice", State: domain.WorkspaceRunning}
	store := &fakeWorkspaceStore{workspace: workspace}
	client := kfake.NewSimpleClientset()
	h := NewHandler(&fakeJobRepository{}, Options{PersonalStorage: service, Workspaces: store, Kubernetes: platformk8s.NewClientFromInterfaces(nil, client)})
	principal := auth.Principal{Subject: "alice", Username: "alice", TenantID: "team-a", AuthType: auth.AuthTypeOIDC, Roles: []string{domain.RoleEngineer}}
	r := personalStorageTestRouter(h, &principal)
	h.RegisterWorkspaceRoutes(r.Group("/api/v1"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dev-workspaces", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ws-existing") || store.workspace != workspace || service.owner.ID != "" || len(client.Actions()) != 0 {
		t.Fatalf("existing workspace changed %d %s %#v", w.Code, w.Body, client.Actions())
	}
}

type personalDeadlineRecorder struct{ *httptest.ResponseRecorder }

func (w *personalDeadlineRecorder) SetReadDeadline(time.Time) error { return nil }

func TestPersonalStorageSlowUploadHasTransportDeadlineAndCleansStage(t *testing.T) {
	root := t.TempDir()
	service := &recordingPersonalService{store: &personalHTTPStore{root: root}}
	p := auth.Principal{Subject: "alice", Username: "alice", TenantID: "team-a", AuthType: auth.AuthTypeOIDC}
	h := NewHandler(&fakeJobRepository{}, Options{PersonalStorage: service})
	h.personalStorageUploadTimeout = 100 * time.Millisecond
	router := personalStorageTestRouter(h, &p)
	router.GET("/ordinary-stream", func(c *gin.Context) { time.Sleep(200 * time.Millisecond); c.String(http.StatusOK, "unaffected") })
	server := httptest.NewServer(PersonalStorageHTTPHandler(router))
	defer server.Close()
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "PUT /api/v1/data-spaces/wellspiking-personal/content?path=slow.txt HTTP/1.1\r\nHost: %s\r\nContent-Type: application/octet-stream\r\nX-RayTrain-Personal-Storage: 1\r\nContent-Length: 4\r\nConnection: close\r\n\r\na", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("slow upload did not terminate with a bounded response: %v", err)
	}
	response.Body.Close()
	if response.StatusCode == http.StatusOK {
		t.Fatal("partial slow upload was published")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed upload left staging or final data: %v %v", entries, err)
	}
	response, err = http.Get(server.URL + "/ordinary-stream")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("personal upload deadline affected another route")
	}
}
