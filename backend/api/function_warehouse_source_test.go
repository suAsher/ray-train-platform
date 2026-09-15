package api

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
	ml "ray-train-platform-backend/modellifecycle"
	"ray-train-platform-backend/observability"
	ws "ray-train-platform-backend/warehousesync"
)

type warehouseSourceAccounts struct {
	*fakeJobRepository
	account  domain.LocalUser
	found    bool
	lookedUp string
}

func (r *warehouseSourceAccounts) ResolveOAuth2ProxyAccount(_ context.Context, name string) (domain.LocalUser, bool, error) {
	r.lookedUp = name
	return r.account, r.found, nil
}

type warehouseSourceModels struct {
	*modelStoreFake
	created []ml.Model
}

func (s *warehouseSourceModels) CreateModel(_ context.Context, model ml.Model) (ml.Model, error) {
	s.created = append(s.created, model)
	model.ID = "model-1"
	return model, nil
}

type warehouseSourceExperiments struct {
	fakeExperimentProvider
	source observability.JobSource
	err    error
	calls  int
}

func (e *warehouseSourceExperiments) ResolveJobSource(_ context.Context, tenant, job string) (observability.JobSource, error) {
	e.calls++
	e.tenant, e.jobID = tenant, job
	return e.source, e.err
}

type warehouseSourceSnapshots struct {
	requests  []ml.VersionRequest
	state     string
	downloads int
}

func (s *warehouseSourceSnapshots) RequestVersion(_ context.Context, r ml.VersionRequest) (ml.Version, error) {
	s.requests = append(s.requests, r)
	return ml.Version{ID: "version-1", ModelID: r.ModelID, State: s.state, FileName: r.FileName, SizeBytes: 7, SHA256: strings.Repeat("a", 64)}, nil
}
func (s *warehouseSourceSnapshots) Download(_ context.Context, _ ml.Version, w io.Writer) error {
	s.downloads++
	_, err := io.WriteString(w, "weights")
	return err
}
func warehouseSourceJob() domain.TrainingJob {
	return domain.TrainingJob{ID: "job-a", UserID: "alice", TenantID: "local", ObservedState: domain.StateSucceeded,
		Spec: domain.JobSpec{ResolvedStorage: domain.ResolvedStorageMounts{Output: &domain.ResolvedStorageMount{ClaimName: "tos-personal", MountPath: domain.MyStorageMountPath, RelativePath: "train/run-1"}}}}
}
func warehouseSourceFixture() (warehouseSyncSource, ws.Operation, *warehouseSourceAccounts, *warehouseSourceModels, *warehouseSourceExperiments, *warehouseSourceSnapshots) {
	repo := &warehouseSourceAccounts{fakeJobRepository: &fakeJobRepository{jobs: []domain.TrainingJob{warehouseSourceJob()}}, found: true, account: domain.LocalUser{ID: "alice", Username: "alice", TenantID: "local", Roles: []string{domain.RoleEngineer}}}
	models := &warehouseSourceModels{modelStoreFake: &modelStoreFake{}}
	experiments := &warehouseSourceExperiments{source: observability.JobSource{JobID: "job-a", RunID: "run-a", ExperimentID: "42"}}
	snapshots := &warehouseSourceSnapshots{state: ml.Ready}
	h := NewHandler(repo, Options{Models: models, ModelSnapshots: snapshots, Experiments: experiments})
	op := ws.Operation{ID: "operation-1", OwnerID: "alice", OwnerName: "alice", TenantID: "local", JobID: "job-a", Paths: []string{"checkpoints/best.pth"}, Automatic: true}
	return warehouseSyncSource{h: h}, op, repo, models, experiments, snapshots
}
func TestWarehouseSourceRevalidatesAccountAndJobBeforeReadingWeights(t *testing.T) {
	for _, tc := range []string{"disabled", "missing", "different account", "moved team", "roles revoked", "foreign job", "foreign tenant"} {
		t.Run(tc, func(t *testing.T) {
			source, op, repo, models, exp, snapshots := warehouseSourceFixture()
			switch tc {
			case "disabled":
				repo.account.Disabled = true
			case "missing":
				repo.found = false
			case "different account":
				repo.account.ID = "other"
			case "moved team":
				repo.account.TenantID = "other"
			case "roles revoked":
				repo.account.Roles = nil
			case "foreign job":
				repo.jobs[0].UserID = "other"
			case "foreign tenant":
				repo.jobs[0].TenantID = "other"
			}
			_, _, err := source.Prepare(context.Background(), op)
			if err == nil || repo.lookedUp != "alice" || len(models.created) != 0 || exp.calls != 0 || len(snapshots.requests) != 0 {
				t.Fatal("revoked or foreign source reached snapshot creation")
			}
		})
	}
}
func TestWarehouseSourceAutomaticAndManualTerminalRules(t *testing.T) {
	for _, tc := range []struct {
		state     domain.State
		automatic bool
		want      error
	}{
		{domain.StateRunning, true, ws.ErrPending}, {domain.StateRunning, false, ws.ErrPending},
		{domain.StateFailed, true, ws.ErrSourceFailed}, {domain.StateCanceled, true, ws.ErrSourceFailed}, {domain.StateTimedOut, true, ws.ErrSourceFailed},
		{domain.StateSucceeded, true, nil}, {domain.StateFailed, false, nil}, {domain.StateCanceled, false, nil}, {domain.StateTimedOut, false, nil},
	} {
		t.Run(string(tc.state)+map[bool]string{true: " automatic", false: " manual"}[tc.automatic], func(t *testing.T) {
			source, op, repo, _, _, snapshots := warehouseSourceFixture()
			op.Automatic = tc.automatic
			repo.jobs[0].ObservedState = tc.state
			_, _, err := source.Prepare(context.Background(), op)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if tc.want != nil && len(snapshots.requests) != 0 {
				t.Fatal("unfinished/failed automatic job was copied")
			}
		})
	}
}
func TestWarehouseSourceRequiresUnambiguousMLflowBeforeCopy(t *testing.T) {
	for _, failure := range []error{observability.ErrJobSourceUnavailable, observability.ErrAmbiguousJobSource} {
		source, op, _, models, exp, snapshots := warehouseSourceFixture()
		exp.err = failure
		_, _, err := source.Prepare(context.Background(), op)
		if !errors.Is(err, failure) || len(models.created) != 0 || len(snapshots.requests) != 0 {
			t.Fatal("unverified MLflow source created snapshots")
		}
	}
}
func TestWarehouseSourcePreparesStableCopiesWithoutDownloading(t *testing.T) {
	for _, state := range []string{ml.Pending, ml.Ready} {
		t.Run(state, func(t *testing.T) {
			source, op, _, models, exp, snapshots := warehouseSourceFixture()
			snapshots.state = state
			files, identity, err := source.Prepare(context.Background(), op)
			if state == ml.Pending {
				if !errors.Is(err, ws.ErrPending) || len(files) != 0 {
					t.Fatal("pending copy presented as uploadable")
				}
			} else if err != nil || len(files) != 1 || files[0].ModelID != "model-1" || files[0].SHA256 != strings.Repeat("a", 64) {
				t.Fatalf("invalid ready copy: %v %v", files, err)
			}
			if identity.JobID != "job-a" || identity.RunID != "run-a" || identity.ExperimentID != "42" || exp.tenant != "local" {
				t.Fatal("source identity lost")
			}
			if len(models.created) != 1 || models.created[0].IdempotencyKey != "warehouse-sync:operation-1" || len(snapshots.requests) != 1 || snapshots.downloads != 0 {
				t.Fatal("copy preparation is not stable or started downloading")
			}
			r := snapshots.requests[0]
			if r.SourceRoot != "ray-train/tenants/local/users/alice/train/run-1" || r.RelativePath != "checkpoints/best.pth" || r.FileName != "best.pth" || r.RunID != "run-a" || r.IdempotencyKey != "warehouse-sync:operation-1:0" {
				t.Fatalf("unsafe or unstable source: %+v", r)
			}
		})
	}
}
func TestWarehouseSourceSupportsLogicalRunsButRejectsLegacyRoots(t *testing.T) {
	source, op, repo, _, _, snapshots := warehouseSourceFixture()
	job := repo.jobs[0]
	job.Spec.ResolvedStorage = domain.ResolvedStorageMounts{}
	job.Spec.ResolvedDataMounts = domain.ResolvedDataSpaceMounts{Output: &domain.ResolvedDataMount{Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace, ClaimName: "personal", SubPath: "runs/job-a", MountPath: domain.DataMountOutputPath}}
	repo.jobs = []domain.TrainingJob{job}
	if _, _, err := source.Prepare(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if snapshots.requests[0].SourceRoot != "ray-train/tenants/local/users/alice/runs/job-a" {
		t.Fatal("logical source root not rebuilt")
	}
	repo.jobs = []domain.TrainingJob{artifactJob("job-a", "local")}
	repo.jobs[0].UserID = "alice"
	repo.jobs[0].ObservedState = domain.StateSucceeded
	if _, _, err := source.Prepare(context.Background(), op); !errors.Is(err, ws.ErrForbidden) {
		t.Fatalf("legacy source unexpectedly accepted: %v", err)
	}
}
func TestWarehouseSourceOpenRejectsSnapshotChanges(t *testing.T) {
	for _, change := range []string{"state", "hash", "size", "name"} {
		t.Run(change, func(t *testing.T) {
			source, _, _, models, _, snapshots := warehouseSourceFixture()
			file := ws.File{ModelID: "model-1", VersionID: "version-1", Name: "best.pth", Size: 7, SHA256: strings.Repeat("a", 64)}
			v := ml.Version{ID: file.VersionID, ModelID: file.ModelID, FileName: file.Name, SizeBytes: file.Size, SHA256: file.SHA256, State: ml.Ready}
			switch change {
			case "state":
				v.State = ml.Pending
			case "hash":
				v.SHA256 = strings.Repeat("b", 64)
			case "size":
				v.SizeBytes++
			case "name":
				v.FileName = "other.pth"
			}
			models.version = v
			reader, err := source.Open(context.Background(), file)
			if reader != nil || !errors.Is(err, ws.ErrConflict) || snapshots.downloads != 0 {
				t.Fatal("changed snapshot was opened")
			}
		})
	}
}
func TestWarehouseSourceCloseReleasesBlockedSnapshotWriter(t *testing.T) {
	source, _, _, models, _, _ := warehouseSourceFixture()
	file := ws.File{ModelID: "model-1", VersionID: "version-1", Name: "best.pth", Size: 7, SHA256: strings.Repeat("a", 64)}
	models.version = ml.Version{ID: file.VersionID, ModelID: file.ModelID, FileName: file.Name, SizeBytes: file.Size, SHA256: file.SHA256, State: ml.Ready}
	snapshots := &cancellableSnapshotFake{done: make(chan struct{})}
	source.h.modelSnapshots = snapshots
	reader, err := source.Open(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-snapshots.done:
	case <-time.After(time.Second):
		t.Fatal("snapshot writer leaked after upload reader close")
	}
}
func TestWarehouseSourceOpenReadsVerifiedSnapshot(t *testing.T) {
	source, _, _, models, _, _ := warehouseSourceFixture()
	file := ws.File{ModelID: "model-1", VersionID: "version-1", Name: "best.pth", Size: 7, SHA256: strings.Repeat("a", 64)}
	models.version = ml.Version{ID: file.VersionID, ModelID: file.ModelID, FileName: file.Name, SizeBytes: file.Size, SHA256: file.SHA256, State: ml.Ready}
	reader, err := source.Open(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "weights" {
		t.Fatalf("snapshot read failed: %q %v", data, err)
	}
}

func TestWarehouseSourceSnapshotsMixedFilesUnderSameJobOutput(t *testing.T) {
	source, op, _, _, _, snapshots := warehouseSourceFixture()
	op.Paths = []string{"weights/model.pth", "configs/train.yaml", "configs/infer.yml", "model.config", "config.py", "labels.json", "README", "asset.custom"}
	files, identity, err := source.Prepare(context.Background(), op)
	if err != nil || len(files) != len(op.Paths) || len(snapshots.requests) != len(op.Paths) {
		t.Fatalf("mixed snapshots: %v %v", files, err)
	}
	for i, relative := range op.Paths {
		request := snapshots.requests[i]
		if request.SourceRoot != "ray-train/tenants/local/users/alice/train/run-1" || request.RelativePath != relative || request.FileName != path.Base(relative) || request.JobID != identity.JobID || request.RunID != identity.RunID || files[i].Name != path.Base(relative) {
			t.Fatalf("wrong file source: %+v", request)
		}
	}
}
