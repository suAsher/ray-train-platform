package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
)

type fakeDataSpaceObjectStore struct {
	listRoot     string
	listPath     string
	folderRoot   string
	folderPath   string
	presignRoot  string
	presignPath  string
	presignType  string
	presignSize  int64
	presignErr   error
	downloadRoot string
	downloadPath string
	putRoot      string
	putPath      string
	putType      string
	putSize      int64
	putContent   string
	putErr       error
	entries      objectstore.DataEntryPage
	readContent  string
	readInfo     objectstore.ArtifactRead
	multipartID  string
	partBody     string
	completed    []objectstore.MultipartPart
	aborted      bool
}

func (store *fakeDataSpaceObjectStore) ListDataEntries(_ context.Context, root, relativePath, _ string, _ int) (objectstore.DataEntryPage, error) {
	store.listRoot, store.listPath = root, relativePath
	return store.entries, nil
}

func (store *fakeDataSpaceObjectStore) PresignDataPut(_ context.Context, root, relativePath, contentType string, sizeBytes int64, _ time.Duration) (objectstore.PresignedPut, error) {
	store.presignRoot, store.presignPath, store.presignType, store.presignSize = root, relativePath, contentType, sizeBytes
	if store.presignErr != nil {
		return objectstore.PresignedPut{}, store.presignErr
	}
	return objectstore.PresignedPut{URL: "https://upload.example.invalid/signed", RequiredHeaders: map[string]string{"Content-Type": contentType}, ContentLength: sizeBytes, ExpiresAt: time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)}, nil
}

func (store *fakeDataSpaceObjectStore) ReadData(_ context.Context, root, relativePath string) (objectstore.ArtifactRead, error) {
	store.downloadRoot, store.downloadPath = root, relativePath
	result := store.readInfo
	result.Content = io.NopCloser(strings.NewReader(store.readContent))
	return result, nil
}

func (store *fakeDataSpaceObjectStore) PutData(_ context.Context, root, relativePath, contentType string, sizeBytes int64, body io.Reader) error {
	store.putRoot, store.putPath, store.putType, store.putSize = root, relativePath, contentType, sizeBytes
	contents, err := io.ReadAll(body)
	store.putContent = string(contents)
	if store.putErr != nil {
		return store.putErr
	}
	return err
}

func (store *fakeDataSpaceObjectStore) CreateDataDirectory(_ context.Context, root, relativePath string) error {
	store.folderRoot, store.folderPath = root, relativePath
	return nil
}

func (store *fakeDataSpaceObjectStore) CreateDataMultipart(_ context.Context, _, _, _ string) (string, error) {
	if store.multipartID == "" {
		store.multipartID = "provider-secret"
	}
	return store.multipartID, nil
}

func (store *fakeDataSpaceObjectStore) UploadDataPart(_ context.Context, _, _, _ string, _ int, _ int64, body io.Reader) (string, error) {
	contents, err := io.ReadAll(body)
	store.partBody = string(contents)
	return "etag-part", err
}

func (store *fakeDataSpaceObjectStore) CompleteDataMultipart(_ context.Context, _, _, _ string, parts []objectstore.MultipartPart) error {
	store.completed = append([]objectstore.MultipartPart(nil), parts...)
	return nil
}

func (store *fakeDataSpaceObjectStore) AbortDataMultipart(_ context.Context, _, _, _ string) error {
	store.aborted = true
	return nil
}

func TestDataSpaceEntriesAreScopedAndDoNotRevealStorageLayout(t *testing.T) {
	store := &fakeDataSpaceObjectStore{entries: objectstore.DataEntryPage{Entries: []objectstore.DataEntry{{Name: "dataset.csv", Type: objectstore.DataEntryFile, SizeBytes: 128}}}}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: store})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces/my-files/entries", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if store.listRoot != "ray-train/tenants/tenant-a/users/user-a/files/" || store.listPath != "" {
		t.Fatalf("unsafe list root=%q path=%q", store.listRoot, store.listPath)
	}
	for _, forbidden := range []string{"ray-train/", "claimName", "bucket"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("entry response leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestDataSpaceWritesRejectReadonlyRootsAndPresignOnlyWritablePersonalFiles(t *testing.T) {
	store := &fakeDataSpaceObjectStore{}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: store})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	readonly := httptest.NewRecorder()
	readonlyRequest := httptest.NewRequest(http.MethodPost, "/api/v1/data-spaces/team-shared/folders", strings.NewReader(`{"path":"new-data"}`))
	readonlyRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(readonly, readonlyRequest)
	if readonly.Code != http.StatusForbidden || store.folderRoot != "" {
		t.Fatalf("readonly folder creation must be rejected: status=%d root=%q body=%s", readonly.Code, store.folderRoot, readonly.Body.String())
	}

	upload := httptest.NewRecorder()
	uploadRequest := httptest.NewRequest(http.MethodPost, "/api/v1/data-spaces/my-files/uploads", strings.NewReader(`{"path":"data/train.csv","contentType":"text/csv","sizeBytes":1234}`))
	uploadRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(upload, uploadRequest)
	// The ticket must point back at the platform and name only the caller's own
	// space; it must never carry an object-store address.
	if upload.Code != http.StatusCreated || !strings.Contains(upload.Body.String(), `"url":"/api/v1/data-spaces/my-files/content?path=data%2Ftrain.csv"`) {
		t.Fatalf("unsafe upload authorization: status=%d body=%s", upload.Code, upload.Body.String())
	}
	if strings.Contains(upload.Body.String(), "http") {
		t.Fatalf("upload ticket must not expose an object-store URL: %s", upload.Body.String())
	}
}

func TestDataSpaceWritesRejectRolelessPrincipalOnPersonalRoots(t *testing.T) {
	store := &fakeDataSpaceObjectStore{}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: store})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", AuthType: auth.AuthTypeLocal})

	folder := postDataSpaceOperation(router, "/api/v1/data-spaces/my-files/folders", `{"path":"not-allowed"}`)
	if folder.Code != http.StatusForbidden || store.folderRoot != "" || !strings.Contains(folder.Body.String(), "DATA_SPACE_READ_ONLY") {
		t.Fatalf("roleless principal must not create personal folders: status=%d root=%q body=%s", folder.Code, store.folderRoot, folder.Body.String())
	}
	upload := postDataSpaceOperation(router, "/api/v1/data-spaces/my-files/uploads", `{"path":"not-allowed.csv","contentType":"text/csv","sizeBytes":12}`)
	if upload.Code != http.StatusForbidden || store.presignRoot != "" || !strings.Contains(upload.Body.String(), "DATA_SPACE_READ_ONLY") {
		t.Fatalf("roleless principal must not upload personal files: status=%d root=%q body=%s", upload.Code, store.presignRoot, upload.Body.String())
	}
}

func TestDataSpaceUploadUsesLowerCamelCaseBrowserContract(t *testing.T) {
	store := &fakeDataSpaceObjectStore{}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: store})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	request := httptest.NewRequest(http.MethodPost, "/api/v1/data-spaces/workspace/uploads", strings.NewReader(`{"path":"project/train.py","contentType":"text/x-python","sizeBytes":42}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	body := response.Body.String()
	for _, expected := range []string{
		`"url":"/api/v1/data-spaces/workspace/content?path=project%2Ftrain.py"`,
		`"contentType":"text/x-python"`,
		`"sizeBytes":42`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("upload response must use the browser contract %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `"URL"`) || strings.Contains(body, `"SizeBytes"`) {
		t.Fatalf("upload response must not expose Go field names: %s", body)
	}
}

func TestDataSpaceUploadAllowsZeroByteSourceFiles(t *testing.T) {
	store := &fakeDataSpaceObjectStore{}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: store})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := postDataSpaceOperation(router, "/api/v1/data-spaces/workspace/uploads", `{"path":"project/mmdet3d/__init__.py","contentType":"text/x-python","sizeBytes":0}`)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"sizeBytes":0`) {
		t.Fatalf("zero-byte source file must be preserved in workspace snapshots: status=%d store=%#v body=%s", response.Code, store, response.Body.String())
	}

	negative := postDataSpaceOperation(router, "/api/v1/data-spaces/workspace/uploads", `{"path":"project/bad.py","contentType":"text/x-python","sizeBytes":-1}`)
	if negative.Code != http.StatusBadRequest {
		t.Fatalf("negative upload size must be rejected: status=%d body=%s", negative.Code, negative.Body.String())
	}

	missing := postDataSpaceOperation(router, "/api/v1/data-spaces/workspace/uploads", `{"path":"project/missing-size.py","contentType":"text/x-python"}`)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing upload size must be rejected rather than interpreted as zero: status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestDataSpaceWritesAllowOnlyResponsibleAdministratorsToMaintainSharedRoots(t *testing.T) {
	store := &fakeDataSpaceObjectStore{}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: store})
	tenantAdmin := dataSpaceRouter(handler, auth.Principal{Subject: "admin-a", TenantID: "tenant-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeLocal})

	teamFolder := postDataSpaceOperation(tenantAdmin, "/api/v1/data-spaces/team-shared/folders", `{"path":"datasets/version-1"}`)
	if teamFolder.Code != http.StatusCreated || store.folderRoot != "ray-train/tenants/tenant-a/shared/" {
		t.Fatalf("tenant admin team folder: status=%d root=%q body=%s", teamFolder.Code, store.folderRoot, teamFolder.Body.String())
	}
	teamUpload := postDataSpaceOperation(tenantAdmin, "/api/v1/data-spaces/team-shared/uploads", `{"path":"datasets/version-1/train.csv","contentType":"text/csv","sizeBytes":1024}`)
	if teamUpload.Code != http.StatusCreated || !strings.Contains(teamUpload.Body.String(), "/api/v1/data-spaces/team-shared/content") {
		t.Fatalf("tenant admin team upload: status=%d body=%s", teamUpload.Code, teamUpload.Body.String())
	}

	engineer := dataSpaceRouter(handler, auth.Principal{Subject: "engineer-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
	for _, operation := range []struct {
		path string
		body string
	}{
		{path: "/api/v1/data-spaces/team-shared/folders", body: `{"path":"not-allowed"}`},
		{path: "/api/v1/data-spaces/team-shared/uploads", body: `{"path":"not-allowed.csv","contentType":"text/csv","sizeBytes":12}`},
	} {
		denied := postDataSpaceOperation(engineer, operation.path, operation.body)
		if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "DATA_SPACE_READ_ONLY") {
			t.Fatalf("engineer must not publish team data: status=%d body=%s", denied.Code, denied.Body.String())
		}
	}

	for _, operation := range []struct {
		path string
		body string
	}{
		{path: "/api/v1/data-spaces/public/folders", body: `{"path":"release-v1"}`},
		{path: "/api/v1/data-spaces/public/uploads", body: `{"path":"release-v1/data.csv","contentType":"text/csv","sizeBytes":12}`},
	} {
		denied := postDataSpaceOperation(tenantAdmin, operation.path, operation.body)
		if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "DATA_SPACE_READ_ONLY") {
			t.Fatalf("tenant admin must not publish public data: status=%d body=%s", denied.Code, denied.Body.String())
		}
	}

	superAdmin := dataSpaceRouter(handler, auth.Principal{Subject: "super-a", TenantID: "tenant-a", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal})
	publicFolder := postDataSpaceOperation(superAdmin, "/api/v1/data-spaces/public/folders", `{"path":"release-v1"}`)
	if publicFolder.Code != http.StatusCreated || store.folderRoot != domain.DefaultPublicDataRoot {
		t.Fatalf("super admin public folder: status=%d root=%q body=%s", publicFolder.Code, store.folderRoot, publicFolder.Body.String())
	}
	publicUpload := postDataSpaceOperation(superAdmin, "/api/v1/data-spaces/public/uploads", `{"path":"release-v1/data.csv","contentType":"text/csv","sizeBytes":12}`)
	if publicUpload.Code != http.StatusCreated || !strings.Contains(publicUpload.Body.String(), "/api/v1/data-spaces/public/content") {
		t.Fatalf("super admin public upload: status=%d body=%s", publicUpload.Code, publicUpload.Body.String())
	}

	store.folderRoot = ""
	invalidFolder := postDataSpaceOperation(superAdmin, "/api/v1/data-spaces/public/folders", `{"path":"../escape"}`)
	if invalidFolder.Code != http.StatusBadRequest || store.folderRoot != "" {
		t.Fatalf("admin folder path validation was bypassed: status=%d root=%q body=%s", invalidFolder.Code, store.folderRoot, invalidFolder.Body.String())
	}
	store.presignRoot = ""
	invalidUpload := postDataSpaceOperation(superAdmin, "/api/v1/data-spaces/public/uploads", `{"path":"release-v1/data.csv","contentType":"text/csv\r\nX-Evil: true","sizeBytes":12}`)
	if invalidUpload.Code != http.StatusBadRequest || store.presignRoot != "" {
		t.Fatalf("admin upload content validation was bypassed: status=%d root=%q body=%s", invalidUpload.Code, store.presignRoot, invalidUpload.Body.String())
	}
}

func postDataSpaceOperation(router http.Handler, requestPath, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, requestPath, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

// The store is only touched when the bytes arrive, so an outage now surfaces on
// the upload itself rather than when the ticket is issued.
func TestDataSpaceUploadReportsUnavailableWhenObjectStorageIsUnavailable(t *testing.T) {
	store := &fakeDataSpaceObjectStore{putErr: objectstore.ErrUnavailable}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: store})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
	request := httptest.NewRequest(http.MethodPut, "/api/v1/data-spaces/my-files/content?path=data/train.csv", strings.NewReader("payload"))
	request.Header.Set("Content-Type", "text/csv")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "DATA_SPACE_STORE_UNAVAILABLE") {
		t.Fatalf("storage outage must be surfaced as retryable: status=%d body=%s", response.Code, response.Body.String())
	}
}

// The relay is the whole point of the change: the bytes must land in the
// caller's own space, and a read-only root must still refuse them.
func TestDataSpaceContentRelayStoresBytesInTheCallersOwnSpace(t *testing.T) {
	store := &fakeDataSpaceObjectStore{}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: store})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	request := httptest.NewRequest(http.MethodPut, "/api/v1/data-spaces/my-files/content?path=data/train.csv", strings.NewReader("payload"))
	request.Header.Set("Content-Type", "text/csv")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("relayed upload must succeed: status=%d body=%s", response.Code, response.Body.String())
	}
	if store.putRoot != "ray-train/tenants/tenant-a/users/user-a/files/" || store.putPath != "data/train.csv" {
		t.Fatalf("relayed upload escaped the caller's space: root=%q path=%q", store.putRoot, store.putPath)
	}
	if store.putContent != "payload" || store.putType != "text/csv" {
		t.Fatalf("relayed upload altered the body: content=%q type=%q", store.putContent, store.putType)
	}
}

func TestDataSpaceContentRelayRejectsReadOnlyRootsAndEscapingPaths(t *testing.T) {
	for _, target := range []string{
		"/api/v1/data-spaces/team-shared/content?path=data.csv",
		"/api/v1/data-spaces/public/content?path=data.csv",
		"/api/v1/data-spaces/my-files/content?path=../escape",
		"/api/v1/data-spaces/my-files/content",
	} {
		store := &fakeDataSpaceObjectStore{}
		handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: store})
		router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, target, strings.NewReader("payload")))

		if response.Code < 400 || store.putRoot != "" {
			t.Fatalf("%s must be refused before touching storage: status=%d root=%q", target, response.Code, store.putRoot)
		}
	}
}

func downloadDataSpaceFixture(t *testing.T, roles ...string) (*fakeDataSpaceObjectStore, *gin.Engine) {
	t.Helper()
	if len(roles) == 0 {
		roles = []string{domain.RoleEngineer}
	}
	store := &fakeDataSpaceObjectStore{readContent: "weights", readInfo: objectstore.ArtifactRead{SizeBytes: 7, ContentType: "application/octet-stream"}}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: store})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: roles, AuthType: auth.AuthTypeLocal})
	return store, router
}

func getDataSpaceDownload(router *gin.Engine, target string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	return response
}

// Training runs pick their own output directory, so a checkpoint is frequently
// reachable only by browsing personal storage. Owners may take those weights.
func TestDataSpaceDownloadStreamsCheckpointFromPersonalSpace(t *testing.T) {
	store, router := downloadDataSpaceFixture(t)

	response := getDataSpaceDownload(router, "/api/v1/data-spaces/my-runs/download?path=job-a/epoch_1.pth")

	if response.Code != http.StatusOK || response.Body.String() != "weights" {
		t.Fatalf("owner must receive the checkpoint: status=%d body=%q", response.Code, response.Body.String())
	}
	if store.downloadPath != "job-a/epoch_1.pth" {
		t.Fatalf("download must read the requested path: %q", store.downloadPath)
	}
	if disposition := response.Header().Get("Content-Disposition"); disposition != `attachment; filename="epoch_1.pth"` {
		t.Fatalf("checkpoint must be saved rather than rendered: %q", disposition)
	}
}

// Opening personal storage must not turn into a general file export: the space
// still holds ordinary training data that has no business leaving the platform.
func TestDataSpaceDownloadRejectsNonCheckpointFile(t *testing.T) {
	store, router := downloadDataSpaceFixture(t)

	response := getDataSpaceDownload(router, "/api/v1/data-spaces/my-runs/download?path=job-a/final.bin")

	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("only checkpoints may be downloaded: status=%d body=%q", response.Code, response.Body.String())
	}
	if store.downloadRoot != "" || store.downloadPath != "" {
		t.Fatalf("a rejected download must not read object storage: root=%q path=%q", store.downloadRoot, store.downloadPath)
	}
}

// Governed data stays closed even for the administrators who may publish into
// it, and even when the requested file happens to carry a checkpoint suffix.
func TestDataSpaceDownloadRejectsGovernedSpaces(t *testing.T) {
	for _, space := range []string{"team-shared", "public"} {
		store, router := downloadDataSpaceFixture(t, domain.RoleEngineer, domain.RoleTenantAdmin, domain.RoleSuperAdmin)

		response := getDataSpaceDownload(router, "/api/v1/data-spaces/"+space+"/download?path=weights/epoch_1.pth")

		if response.Code != http.StatusForbidden {
			t.Fatalf("%s must not be downloadable: status=%d body=%q", space, response.Code, response.Body.String())
		}
		if store.downloadRoot != "" || store.downloadPath != "" {
			t.Fatalf("%s must not be read from object storage: root=%q path=%q", space, store.downloadRoot, store.downloadPath)
		}
	}
}
