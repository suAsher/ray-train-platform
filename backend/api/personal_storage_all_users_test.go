package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/personalstorage"
)

func TestPersonalStorageAllUsersThirdInteractiveOwnerCanOnlyUseOwnStorage(t *testing.T) {
	thirdRoot, aliceRoot, bobRoot := t.TempDir(), t.TempDir(), t.TempDir()
	for root, name := range map[string]string{thirdRoot: "charlie-private.txt", aliceRoot: "alice-private.txt", bobRoot: "bob-private.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	service := &recordingPersonalService{
		view: personalstorage.View{State: "READY", BindingID: "binding-charlie", PublicKey: "ssh-ed25519 charlie-public", Username: "charlie", Generation: 1},
		stores: map[string]personalstorage.Store{
			"stable-charlie": &personalHTTPStore{root: thirdRoot},
			"stable-alice":   &personalHTTPStore{root: aliceRoot},
			"stable-bob":     &personalHTTPStore{root: bobRoot},
		},
	}
	principal := auth.Principal{Subject: "stable-charlie", Username: "charlie", TenantID: "team-c", AuthType: auth.AuthTypeOIDC}
	// An empty deployment list is supplied after startup validates explicit
	// all-user authorization (or the separate upstream isolation attestation).
	h := NewHandler(&fakeJobRepository{}, Options{PersonalStorage: service})
	router := personalStorageTestRouter(h, &principal)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-RayTrain-Personal-Storage", "1")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	for _, route := range []string{"/api/v1/personal-storage/wellspiking", "/api/v1/personal-storage/wellspiking/verify"} {
		w := request(http.MethodPost, route, `{}`)
		if w.Code != http.StatusOK || service.owner.ID != "stable-charlie" || service.owner.Username != "charlie" {
			t.Fatalf("third interactive owner lost self-service access: %d %s", w.Code, w.Body)
		}
	}
	if service.enrolled != 1 || service.verified != 1 {
		t.Fatal("third owner did not enroll and verify itself")
	}
	for _, team := range []string{"team-c", "team-other"} {
		principal.TenantID = team
		w := request(http.MethodGet, "/api/v1/personal-storage/wellspiking", "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":true`) || service.owner.ID != "stable-charlie" {
			t.Fatal("all-user mode lost stable owner across team changes")
		}
		w = request(http.MethodGet, "/api/v1/data-spaces/wellspiking-personal/entries?ownerID=stable-alice", "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "charlie-private.txt") || strings.Contains(w.Body.String(), "alice-private.txt") || strings.Contains(w.Body.String(), "bob-private.txt") || service.owner.ID != "stable-charlie" {
			t.Fatalf("all-user scope expanded personal owner selection: %d %s", w.Code, w.Body)
		}
	}
	for _, body := range []string{`{"ownerID":"stable-bob"}`, `{"allUsersEnabled":true}`, `{"testOwnerIDs":[]}`} {
		w := request(http.MethodPost, "/api/v1/personal-storage/wellspiking", body)
		if w.Code != http.StatusBadRequest || service.enrolled != 1 {
			t.Fatalf("request changed owner or deployment scope: %d %s", w.Code, w.Body)
		}
	}
	principal.AuthType = auth.AuthTypePAT
	principal.Roles = []string{domain.RoleSuperAdmin}
	beforeOwner, beforeOpened := service.owner, service.opened
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/personal-storage/wellspiking"},
		{http.MethodPost, "/api/v1/personal-storage/wellspiking"},
		{http.MethodPost, "/api/v1/personal-storage/wellspiking/verify"},
		{http.MethodGet, "/api/v1/data-spaces/wellspiking-personal/entries"},
	} {
		w := request(tc.method, tc.path, `{}`)
		if w.Code != http.StatusForbidden {
			t.Fatalf("all-user mode admitted administrative PAT: %s %d", tc.path, w.Code)
		}
	}
	if service.enrolled != 1 || service.verified != 1 || service.owner != beforeOwner || service.opened != beforeOpened {
		t.Fatal("rejected machine identity caused personal storage effects")
	}
}
