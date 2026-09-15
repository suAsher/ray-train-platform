package warehousesync

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	fw "ray-train-platform-backend/functionwarehouse"
)

type memoryStore struct {
	mu       sync.Mutex
	ops      map[string]Operation
	renewErr error
	saveErr  error
}

func (m *memoryStore) Create(_ context.Context, op Operation) (Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, old := range m.ops {
		if old.OwnerID == op.OwnerID && old.TenantID == op.TenantID && old.IdempotencyKey == op.IdempotencyKey {
			if old.RequestSHA256 != op.RequestSHA256 {
				return Operation{}, ErrConflict
			}
			return old, nil
		}
	}
	m.ops[op.ID] = op
	return op, nil
}
func (m *memoryStore) Get(_ context.Context, id string) (Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.ops[id]
	if !ok {
		return Operation{}, ErrNotFound
	}
	return v, nil
}
func (m *memoryStore) ListJob(_ context.Context, owner, tenant, job string) ([]Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []Operation
	for _, v := range m.ops {
		if v.OwnerID == owner && v.TenantID == tenant && v.JobID == job {
			result = append(result, v)
		}
	}
	return result, nil
}
func (m *memoryStore) Claim(_ context.Context, lease string, now, until time.Time) (Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, v := range m.ops {
		if (v.State == Queued || v.State == WaitingSource) && v.LeaseID == "" && !v.NextAttemptAt.After(now) {
			v.LeaseID = lease
			v.LeaseExpiresAt = &until
			m.ops[id] = v
			return v, nil
		}
	}
	return Operation{}, ErrNotFound
}
func (m *memoryStore) Save(_ context.Context, op Operation, lease string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	old := m.ops[op.ID]
	if old.LeaseID != lease || old.State == Canceled {
		return ErrConflict
	}
	m.ops[op.ID] = op
	return nil
}
func (m *memoryStore) Renew(_ context.Context, id, lease string, until time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.renewErr != nil {
		return m.renewErr
	}
	v := m.ops[id]
	if v.LeaseID != lease {
		return ErrConflict
	}
	v.LeaseExpiresAt = &until
	m.ops[id] = v
	return nil
}
func (m *memoryStore) Resume(_ context.Context, id string, a Actor, cipher []byte) (Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.ops[id]
	if !ok {
		return Operation{}, ErrNotFound
	}
	if v.OwnerID != a.ID || v.TenantID != a.TenantID {
		return Operation{}, ErrForbidden
	}
	if v.State != Failed && v.State != WaitingReauth {
		return Operation{}, ErrConflict
	}
	v.State = Queued
	v.Credential = cipher
	m.ops[id] = v
	return v, nil
}
func (m *memoryStore) Cancel(_ context.Context, id string, a Actor) (Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.ops[id]
	if !ok {
		return Operation{}, ErrNotFound
	}
	if v.OwnerID != a.ID || v.TenantID != a.TenantID {
		return Operation{}, ErrForbidden
	}
	v.State = Canceled
	v.Credential = nil
	v.LeaseID = ""
	m.ops[id] = v
	return v, nil
}

type sourceStub struct {
	err      error
	opened   int
	prepared int
	files    []File
}

func (s *sourceStub) Prepare(_ context.Context, op Operation) ([]File, SourceIdentity, error) {
	s.prepared++
	if s.err != nil {
		return nil, SourceIdentity{}, s.err
	}
	if s.files != nil {
		return s.files, SourceIdentity{JobID: op.JobID, RunID: "run", ExperimentID: "42"}, nil
	}
	return []File{{ModelID: "model", VersionID: "snapshot", Name: "best.pth", Size: 3, SHA256: strings.Repeat("a", 64)}}, SourceIdentity{JobID: op.JobID, RunID: "run", ExperimentID: "42"}, nil
}
func (s *sourceStub) Open(context.Context, File) (io.ReadCloser, error) {
	s.opened++
	return io.NopCloser(strings.NewReader("abc")), nil
}

type upstreamStub struct {
	denied                     bool
	unauthorized               bool
	uploads, creates, verifies int
	createErr                  error
	verifyErr                  error
	beforeCreate               func()
	expectedNames              map[string]bool
	uploadedNames              []string
	createdRequest             fw.CreateVersionRequest
}

func (u *upstreamStub) GetWarehouse(context.Context, string, string) (fw.Warehouse, error) {
	if u.unauthorized {
		return fw.Warehouse{}, fw.ErrUnauthorized
	}
	p := []string{"edit"}
	if u.denied {
		p = []string{"view"}
	}
	return fw.Warehouse{ID: "warehouse", GroupID: fw.Environments()[1].GroupID, PermissionCodes: p}, nil
}
func (u *upstreamStub) ListModelTypes(context.Context, string, string) ([]fw.ModelType, error) {
	return []fw.ModelType{{ID: "type", FunctionWarehouseID: "warehouse", GroupID: fw.Environments()[1].GroupID}}, nil
}
func (u *upstreamStub) Upload(ctx context.Context, token, name, prefix string, size int64, sha string, open func(context.Context) (io.ReadCloser, error)) (fw.UploadedFile, error) {
	u.uploads++
	u.uploadedNames = append(u.uploadedNames, name)
	allowed := name == "best.pth"
	if u.expectedNames != nil {
		allowed = u.expectedNames[name]
	}
	if !allowed || !strings.HasPrefix(prefix, "raytrain/") || (u.expectedNames == nil && !strings.HasSuffix(prefix, "/snapshot")) {
		return fw.UploadedFile{}, fw.ErrInvalid
	}
	r, err := open(ctx)
	if err != nil {
		return fw.UploadedFile{}, err
	}
	defer r.Close()
	if _, err = io.Copy(io.Discard, r); err != nil {
		return fw.UploadedFile{}, err
	}
	return fw.UploadedFile{Filename: name, FileSize: size, FileSHA256: sha, URL: "https://example.invalid/test"}, nil
}
func (u *upstreamStub) CreateVersion(_ context.Context, _ string, request fw.CreateVersionRequest) (fw.Version, error) {
	u.creates++
	u.createdRequest = request
	if u.beforeCreate != nil {
		u.beforeCreate()
	}
	return fw.Version{ID: "external-version"}, u.createErr
}
func (u *upstreamStub) VerifyVersion(context.Context, string, string, string, fw.CreateVersionRequest) (fw.Version, error) {
	u.verifies++
	return fw.Version{ID: "external-version"}, u.verifyErr
}
func fixture(t *testing.T) (*Service, *memoryStore, *sourceStub, *upstreamStub, Actor, Request) {
	t.Helper()
	m := &memoryStore{ops: map[string]Operation{}}
	src := &sourceStub{}
	u := &upstreamStub{}
	s, err := NewService(m, src, map[fw.Environment]Upstream{fw.Development: u}, bytes.Repeat([]byte("p"), 32))
	if err != nil {
		t.Fatal(err)
	}
	return s, m, src, u, Actor{ID: "owner", Name: "User", TenantID: "tenant"}, Request{JobID: "job-one", Environment: fw.Development, WarehouseID: "warehouse", ModelTypeID: "type", Version: "v1", Paths: []string{"weights/best.pth"}, IdempotencyKey: "operation-one"}
}
func runOne(t *testing.T, s *Service, m *memoryStore) Operation {
	t.Helper()
	op, err := m.Claim(context.Background(), "lease", time.Now(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	s.process(context.Background(), op)
	got, err := m.Get(context.Background(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestIntentionalDuplicateCreatesNewOperationButSubmissionRetryDoesNot(t *testing.T) {
	s, _, _, _, a, r := fixture(t)
	one, err := s.Create(context.Background(), a, r, "secret")
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.Create(context.Background(), a, r, "secret")
	if err != nil || same.ID != one.ID {
		t.Fatal("same operation not reused")
	}
	r.IdempotencyKey = "operation-two"
	two, err := s.Create(context.Background(), a, r, "secret")
	if err != nil || two.ID == one.ID {
		t.Fatal("intentional duplicate rejected")
	}
	r.Version = "v2"
	if _, err = s.Create(context.Background(), a, r, "secret"); !errors.Is(err, ErrConflict) {
		t.Fatal("key reuse with changed request accepted")
	}
}
func TestPendingSourceDoesNotUpload(t *testing.T) {
	s, m, src, u, a, r := fixture(t)
	src.err = ErrPending
	if _, err := s.Create(context.Background(), a, r, "secret"); err != nil {
		t.Fatal(err)
	}
	op := runOne(t, s, m)
	if op.State != WaitingSource || u.uploads != 0 || u.creates != 0 || len(op.Credential) == 0 || op.LeaseID != "" {
		t.Fatalf("pending source side effect: %+v", op)
	}
	if op.NextAttemptAt.Before(time.Now().Add(25 * time.Second)) {
		t.Fatal("source polled too frequently")
	}
}
func TestPermissionRevocationBlocksUpload(t *testing.T) {
	s, m, _, u, a, r := fixture(t)
	if _, err := s.Create(context.Background(), a, r, "secret"); err != nil {
		t.Fatal(err)
	}
	u.denied = true
	op := runOne(t, s, m)
	if op.State != Failed || u.uploads != 0 || len(op.Credential) != 0 {
		t.Fatal("revoked permission accepted")
	}
}
func TestUnknownOutcomeIsNotRetried(t *testing.T) {
	s, m, _, u, a, r := fixture(t)
	u.createErr = fw.ErrUnknownOutcome
	if _, err := s.Create(context.Background(), a, r, "secret"); err != nil {
		t.Fatal(err)
	}
	op := runOne(t, s, m)
	if op.State != Unknown || u.creates != 1 || len(op.Credential) != 0 {
		t.Fatal("uncertain outcome was not fenced")
	}
	if _, err := s.Retry(context.Background(), op.ID, a, "new-secret"); !errors.Is(err, ErrConflict) {
		t.Fatal("uncertain create retried")
	}
}
func TestRenewFailurePreventsPublication(t *testing.T) {
	s, m, _, u, a, r := fixture(t)
	if _, err := s.Create(context.Background(), a, r, "secret"); err != nil {
		t.Fatal(err)
	}
	m.renewErr = ErrConflict
	_ = runOne(t, s, m)
	if u.creates != 0 {
		t.Fatal("published after lease loss")
	}
}
func TestSuccessfulSyncPersistsSourcesAndErasesCredential(t *testing.T) {
	s, m, _, u, a, r := fixture(t)
	if _, err := s.Create(context.Background(), a, r, "secret"); err != nil {
		t.Fatal(err)
	}
	op := runOne(t, s, m)
	if op.State != Succeeded || op.RunID != "run" || op.ExperimentID != "42" || op.TargetVersionID != "external-version" || u.uploads != 1 || u.verifies != 1 || len(op.Credential) != 0 {
		t.Fatalf("bad successful state: %+v", op)
	}
}
func TestReauthenticationAndOwnership(t *testing.T) {
	s, m, _, u, a, r := fixture(t)
	created, err := s.Create(context.Background(), a, r, "secret")
	if err != nil {
		t.Fatal(err)
	}
	u.unauthorized = true
	op := runOne(t, s, m)
	if op.State != WaitingReauth || len(op.Credential) != 0 {
		t.Fatal("expired token not cleared")
	}
	u.unauthorized = false
	foreign := a
	foreign.TenantID = "other"
	if _, err := s.Retry(context.Background(), created.ID, foreign, "new"); !errors.Is(err, ErrForbidden) {
		t.Fatal("cross-tenant retry accepted")
	}
	if _, err := s.Retry(context.Background(), created.ID, a, "new"); err != nil {
		t.Fatal(err)
	}
}
func TestInvalidPathsAndMissingEditPermissionRejectedBeforeCreate(t *testing.T) {
	s, m, _, u, a, r := fixture(t)
	for _, paths := range [][]string{{"../best.pth"}, {"/best.pth"}, {"a/best.pth", "b/best.pth"}, {"a//best.pth"}, {"a\\best.pth"}, {"."}, {"configs/"}, {"*.yaml"}, {"config?.yml"}, {"[ab].json"}, {"{a,b}.yaml"}, {"file\tname"}, {"file\x7f"}, {"C:config"}, {""}, {}, {"a", "b", "c", "d", "e", "f", "g", "h", "i"}} {
		r.Paths = paths
		if _, err := s.Create(context.Background(), a, r, "secret"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid paths: %v", paths)
		}
	}
	r.Paths = []string{"best.pth"}
	u.denied = true
	if _, err := s.Create(context.Background(), a, r, "secret"); !errors.Is(err, fw.ErrForbidden) {
		t.Fatal("read only target accepted")
	}
	if len(m.ops) != 0 {
		t.Fatal("invalid request persisted")
	}
}
func TestFailedVerificationKeepsTargetButNeverReportsSuccess(t *testing.T) {
	s, m, _, u, a, r := fixture(t)
	u.verifyErr = fw.ErrUnknownOutcome
	if _, err := s.Create(context.Background(), a, r, "secret"); err != nil {
		t.Fatal(err)
	}
	op := runOne(t, s, m)
	if op.State != Unknown || op.TargetVersionID != "external-version" {
		t.Fatal("verification mismatch lost external identity")
	}
}

func TestSourceFailureDoesNotExposeRawErrorOrUpload(t *testing.T) {
	s, m, src, u, a, r := fixture(t)
	src.err = errors.New("storage credential=do-not-return")
	if _, err := s.Create(context.Background(), a, r, "secret"); err != nil {
		t.Fatal(err)
	}
	op := runOne(t, s, m)
	if op.State != Failed || u.uploads != 0 || strings.Contains(op.Message, "credential") || len(op.Credential) != 0 {
		t.Fatal("source failure leaked or uploaded")
	}
}
func TestSaveFailurePreventsSideEffects(t *testing.T) {
	s, m, _, u, a, r := fixture(t)
	if _, err := s.Create(context.Background(), a, r, "secret"); err != nil {
		t.Fatal(err)
	}
	m.saveErr = ErrConflict
	_ = runOne(t, s, m)
	if u.uploads != 0 || u.creates != 0 {
		t.Fatal("side effect without persisted lease stage")
	}
}
func TestRegisteringRecoveryNeverRepeatsCreate(t *testing.T) {
	s, m, _, u, a, r := fixture(t)
	created, err := s.Create(context.Background(), a, r, "secret")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := m.Claim(context.Background(), "lease", time.Now(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	claimed.State = Registering
	m.ops[created.ID] = claimed
	s.process(context.Background(), claimed)
	op, _ := m.Get(context.Background(), created.ID)
	if op.State != Unknown || u.creates != 0 || u.uploads != 0 || len(op.Credential) != 0 {
		t.Fatal("registration recovery duplicated side effects")
	}
}
func TestCancelAndListRespectOwnership(t *testing.T) {
	s, m, _, u, a, r := fixture(t)
	op, err := s.Create(context.Background(), a, r, "secret")
	if err != nil {
		t.Fatal(err)
	}
	foreign := a
	foreign.ID = "foreign"
	if _, err := s.Cancel(context.Background(), op.ID, foreign); !errors.Is(err, ErrForbidden) {
		t.Fatal("foreign cancel accepted")
	}
	items, err := s.ListJob(context.Background(), foreign, r.JobID)
	if err != nil || len(items) != 0 {
		t.Fatal("foreign list returned operation")
	}
	items, err = s.ListJob(context.Background(), a, r.JobID)
	if err != nil || len(items) != 1 {
		t.Fatal("owner list omitted operation")
	}
	got, err := s.Cancel(context.Background(), op.ID, a)
	if err != nil || got.State != Canceled || len(got.Credential) != 0 {
		t.Fatal("cancel did not revoke credential")
	}
	if _, err := m.Claim(context.Background(), "lease", time.Now(), time.Now().Add(time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatal("canceled operation claimed")
	}
	if u.creates != 0 {
		t.Fatal("canceled task created version")
	}
	if _, err := s.Cancel(context.Background(), op.ID, a); !errors.Is(err, ErrConflict) {
		t.Fatal("canceled task canceled again")
	}
}
func TestInvalidServiceAndRequestConfiguration(t *testing.T) {
	s, m, src, u, a, r := fixture(t)
	if _, err := NewService(nil, src, map[fw.Environment]Upstream{fw.Development: u}, make([]byte, 32)); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil store")
	}
	if _, err := NewService(m, src, map[fw.Environment]Upstream{"attacker": u}, make([]byte, 32)); !errors.Is(err, ErrInvalid) {
		t.Fatal("arbitrary target")
	}
	r.Environment = "attacker"
	if _, err := s.Create(context.Background(), a, r, "secret"); !errors.Is(err, ErrInvalid) {
		t.Fatal("arbitrary environment")
	}
	r.Environment = fw.Development
	r.IdempotencyKey = "bad/key"
	if _, err := s.Create(context.Background(), a, r, "secret"); !errors.Is(err, ErrInvalid) {
		t.Fatal("arbitrary key")
	}
	r.IdempotencyKey = "valid"
	if _, err := s.Create(context.Background(), a, r, ""); !errors.Is(err, fw.ErrUnauthorized) {
		t.Fatal("empty credential")
	}
}
func TestSourceIdentityValidationRejectsIncompleteAndDuplicateFiles(t *testing.T) {
	_, _, src, _, _, r := fixture(t)
	op := Operation{JobID: r.JobID, Paths: r.Paths}
	files, identity, _ := src.Prepare(context.Background(), op)
	if !validSource(op, files, identity) {
		t.Fatal("valid source refused")
	}
	other := identity
	other.RunID = ""
	if validSource(op, files, other) {
		t.Fatal("missing run accepted")
	}
	other = identity
	other.JobID = "other"
	if validSource(op, files, other) {
		t.Fatal("foreign job accepted")
	}
	bad := append([]File(nil), files...)
	bad[0].SHA256 = "bad"
	if validSource(op, bad, identity) {
		t.Fatal("invalid digest accepted")
	}
	bad = append(files, files[0])
	op.Paths = []string{"best.pth", "second.pth"}
	if validSource(op, bad, identity) {
		t.Fatal("duplicate snapshot filename accepted")
	}
}
func TestSafeErrorsAndStoppedWorker(t *testing.T) {
	secret := errors.New("private-token-internal")
	if strings.Contains(safeError(secret).Error(), "private-token") {
		t.Fatal("raw internal error leaked")
	}
	if safeError(nil) != nil {
		t.Fatal("nil converted to error")
	}
	s, _, _, _, _, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Run(ctx)
}

func TestManualAndAutomaticSyncAcceptMixedArtifactTypes(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(strconv.FormatBool(automatic), func(t *testing.T) {
			s, m, src, u, actor, request := fixture(t)
			request.Automatic = automatic
			request.Paths = []string{"weights/model.pth", "configs/train.yaml", "configs/inference.yml", "labels.json", "pipeline.config", "configuration.py", "说明.txt", "README"}
			u.expectedNames = make(map[string]bool)
			names := make([]string, 0, len(request.Paths))
			for i, relative := range request.Paths {
				name := path.Base(relative)
				names = append(names, name)
				u.expectedNames[name] = true
				src.files = append(src.files, File{ModelID: "model", VersionID: "snapshot-" + strconv.Itoa(i), Name: name, Size: 3, SHA256: strings.Repeat("a", 64)})
			}
			if _, err := s.Create(context.Background(), actor, request, "secret"); err != nil {
				t.Fatal(err)
			}
			op := runOne(t, s, m)
			if op.State != Succeeded || !reflect.DeepEqual(names, u.uploadedNames) || len(op.Files) != len(names) || u.creates != 1 || u.verifies != 1 {
				t.Fatalf("mixed files not synchronized: state=%s names=%v", op.State, u.uploadedNames)
			}
			if u.createdRequest.JobID != request.JobID || u.createdRequest.RunID != "run" || u.createdRequest.ExperimentID != "42" || len(u.createdRequest.Paths) != len(names) {
				t.Fatal("mixed files lost shared training identity")
			}
			if len(op.Credential) != 0 {
				t.Fatal("completed sync retained credential")
			}
		})
	}
}
