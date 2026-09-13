package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
	ml "ray-train-platform-backend/modellifecycle"
	mr "ray-train-platform-backend/modelrelease"
	ms "ray-train-platform-backend/modelserving"
	"strings"
	"testing"
	"time"
)

func validServingJobID() string { return "job-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24] }

func servingFixture(t *testing.T, es *ModelEvaluationRepository) (*ModelServingRepository, ms.Deployment) {
	t.Helper()
	releases, e := releaseFixture(t, es)
	ctx := context.Background()
	if es.db.Dialector.Name() != "postgres" {
		if err := es.db.AutoMigrate(&modelServingContractRecord{}, &modelServingDeploymentRecord{}, &LocalUserRecord{}); err != nil {
			t.Fatal(err)
		}
		for _, query := range []string{"CREATE UNIQUE INDEX serving_request ON model_serving_deployments(owner_id,tenant_id,idempotency_key)", "CREATE UNIQUE INDEX serving_job ON model_serving_deployments(job_id)", "CREATE UNIQUE INDEX serving_active ON model_serving_deployments(model_id,owner_id) WHERE state IN ('CREATING','SUBMITTED','READY','STOPPING')", "CREATE TABLE model_serving_audits(id TEXT PRIMARY KEY,deployment_id TEXT,actor_id TEXT,action TEXT,created_at DATETIME)"} {
			if err := es.db.Exec(query).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	release, err := releases.CreateRequest(ctx, mr.Request{ModelID: e.ModelID, VersionID: e.VersionID, EvaluationID: e.ID, Reason: "protocol verified", IdempotencyKey: "serving-release"}, mr.Actor{ID: e.OwnerID, TenantID: e.TenantID})
	if err != nil {
		t.Fatal(err)
	}
	release, err = releases.Decide(ctx, release.ID, mr.Decision{Revision: 1, Approve: true, Reason: "independently verified"}, mr.Actor{ID: "reviewer", TenantID: e.TenantID, TenantAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	s := NewModelServingRepository(es.db)
	c, err := s.CreateContract(ctx, ms.Contract{Name: "inference", OwnerID: e.OwnerID, TenantID: e.TenantID, ImageReference: "registry/image:v1", ImageDigest: "sha256:" + strings.Repeat("a", 64), Code: &me.CodeSnapshot{ID: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64), SizeBytes: 156, Format: "zip"}, EntryPoint: []string{"python", "serve.py"}, InputExample: json.RawMessage(`{"z": 1,"a": 2}`), OutputDescription: "result"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	d := ms.Deployment{ID: uuid.NewString(), Name: "test-serving", ReleaseID: release.ID, ModelID: e.ModelID, VersionID: e.VersionID, ModelSHA256: e.ModelSHA256, ModelSizeBytes: 1, FileName: e.FileName, OwnerID: e.OwnerID, TenantID: e.TenantID, Contract: c, Resources: e.Resources, JobID: validServingJobID(), CreatedAt: now, ExpiresAt: now.Add(2 * time.Hour), IdempotencyKey: "serving-first", RequestSHA256: strings.Repeat("c", 64)}
	d.JobSpec = domain.JobSpec{Name: "serving-" + d.ID, Image: c.ImageReference, Source: domain.CodeSource{Type: "serving-archive", ArtifactID: c.Code.ID, ArtifactSHA256: c.Code.SHA256}, Entrypoint: domain.Entrypoint{Command: c.EntryPoint}, Resources: d.Resources, TrainingEngine: domain.TrainingEngineRayTrain, TimeoutSeconds: 7200}
	return s, d
}
func reserveServing(t *testing.T, s *ModelServingRepository, d ms.Deployment) ms.Deployment {
	t.Helper()
	out, created, err := s.ReserveDeployment(context.Background(), d)
	if err != nil || !created {
		t.Fatalf("reserve created=%v error=%v", created, err)
	}
	return out
}
func createServingJob(t *testing.T, s *ModelServingRepository, d ms.Deployment) []byte {
	t.Helper()
	raw, err := json.Marshal(d.JobSpec)
	if err != nil {
		t.Fatal(err)
	}
	j := JobRecord{ID: d.JobID, TenantID: d.TenantID, UserID: d.OwnerID, Name: d.JobSpec.Name, SpecJSON: string(raw), DesiredState: string(domain.DesiredActive), ObservedState: string(domain.StateRunning), KubernetesNS: "eval-test", TrainingEngine: string(domain.TrainingEngineRayTrain), RayVersion: domain.RayVersionCanary, ClusterAttempt: 1, CleanupJSON: "{}", SubmissionOrigin: string(domain.SubmissionOriginServing), ExternalSubmissionID: d.ID}
	if err := s.db.Create(&j).Error; err != nil {
		t.Fatal(err)
	}
	token := []byte(strings.ReplaceAll(d.ID, "-", "")[:32])
	now := time.Now().UTC()
	if err := s.db.Create(&TrainingJobEventTokenRecord{JobID: j.ID, TokenSHA256: trainingEventTokenDigest(token), ExpiresAt: now.Add(time.Hour), RateWindowStartedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDeploymentSubmitted(context.Background(), d.ID, d.JobID); err != nil {
		t.Fatal(err)
	}
	return token
}
func TestModelServingReservationAndSources(t *testing.T) {
	runServingReservationTests(t, evaluationTestStore(t))
}
func runServingReservationTests(t *testing.T, es *ModelEvaluationRepository) {
	s, base := servingFixture(t, es)
	ctx := context.Background()
	d := reserveServing(t, s, base)
	replay, created, err := s.ReserveDeployment(ctx, base)
	if err != nil || created || replay.ID != d.ID {
		t.Fatalf("idempotent retry %+v %v %v", replay, created, err)
	}
	wrong := base
	wrong.RequestSHA256 = strings.Repeat("d", 64)
	if _, _, err := s.ReserveDeployment(ctx, wrong); !errors.Is(err, ms.ErrConflict) {
		t.Fatalf("changed idempotency accepted: %v", err)
	}
	next := base
	next.ID = uuid.NewString()
	next.JobID = validServingJobID()
	next.IdempotencyKey = "second"
	if _, _, err := s.ReserveDeployment(ctx, next); !errors.Is(err, ms.ErrQuota) {
		t.Fatalf("parallel active reservation accepted: %v", err)
	}
	if _, err := s.RequestStop(ctx, d.ID, d.OwnerID, 99); !errors.Is(err, ms.ErrConflict) {
		t.Fatalf("stale stop accepted: %v", err)
	}
	stopped, err := s.RequestStop(ctx, d.ID, d.OwnerID, d.Revision)
	if err != nil || stopped.State != ms.Stopped {
		t.Fatalf("jobless stop %+v %v", stopped, err)
	}
	late := &domain.TrainingJob{ID: d.JobID, UserID: d.OwnerID, TenantID: d.TenantID, ExternalSubmissionID: d.ID, SubmissionOrigin: domain.SubmissionOriginServing, Spec: d.JobSpec}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockServingSubmission(tx, d.ID); err != nil {
			return err
		}
		return validateServingSubmissionReservation(tx, late)
	})
	if !errors.Is(err, ms.ErrConflict) {
		t.Fatalf("late job after stop accepted: %v", err)
	}
	c, err := s.SetContractActive(ctx, base.Contract.ID, false, 1)
	if err != nil || c.Active {
		t.Fatalf("deactivate %+v %v", c, err)
	}
	if _, _, err := s.ReserveDeployment(ctx, next); !errors.Is(err, ms.ErrNotReady) {
		t.Fatalf("inactive contract accepted: %v", err)
	}
	c, err = s.SetContractActive(ctx, c.ID, true, c.Revision)
	if err != nil {
		t.Fatal(err)
	}
	next.Contract = c
	forged := next
	forged.Contract.EntryPoint = []string{"python", "other.py"}
	if _, _, err := s.ReserveDeployment(ctx, forged); !errors.Is(err, ms.ErrConflict) {
		t.Fatalf("forged executable accepted: %v", err)
	}
	next = reserveServing(t, s, next)
	page, err := s.ListDeployments(ctx, ms.Filter{ModelID: base.ModelID, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("page %+v %v", page, err)
	}
	page2, err := s.ListDeployments(ctx, ms.Filter{ModelID: base.ModelID, Cursor: page.NextCursor})
	if err != nil || len(page2.Items) != 1 {
		t.Fatalf("page two %+v %v", page2, err)
	}
	// Shared metadata deliberately contains no evaluation report or JobSpec.
	raw, err := json.Marshal(page2)
	if err != nil || strings.Contains(string(raw), "jobSpec") || strings.Contains(string(raw), "reportSha256") {
		t.Fatalf("private details in metadata: %s %v", raw, err)
	}
	found, err := s.FindDeploymentRequest(ctx, next.TenantID, next.OwnerID, next.IdempotencyKey)
	if err != nil || found.ID != next.ID {
		t.Fatalf("lookup %+v %v", found, err)
	}
	if err := s.FailDeploymentSubmission(ctx, next.ID); err != nil {
		t.Fatal(err)
	}
	third := base
	third.ID = uuid.NewString()
	third.JobID = validServingJobID()
	third.IdempotencyKey = "third"
	third.Contract = c
	if err := s.db.Model(&ml.Model{}).Where("id = ?", base.ModelID).Update("archived", true).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReserveDeployment(ctx, third); !errors.Is(err, ms.ErrNotFound) {
		t.Fatalf("archived model accepted: %v", err)
	}
}
func TestModelServingHealthStopAndTaskToken(t *testing.T) {
	runServingStateTests(t, evaluationTestStore(t))
}
func runServingStateTests(t *testing.T, es *ModelEvaluationRepository) {
	s, base := servingFixture(t, es)
	ctx := context.Background()
	d := reserveServing(t, s, base)
	token := createServingJob(t, s, d)
	d, err := s.GetDeployment(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthorizeServingJobToken(ctx, d.JobID, token, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		job   string
		token []byte
		now   time.Time
	}{{d.JobID, []byte(strings.Repeat("z", 32)), time.Now()}, {d.JobID, token, time.Now().Add(3 * time.Hour)}, {"unrelated", token, time.Now()}} {
		if _, err := s.AuthorizeServingJobToken(ctx, check.job, check.token, check.now); !errors.Is(err, ms.ErrUnauthorized) {
			t.Fatalf("invalid token accepted: %v", err)
		}
	}
	ready, err := s.UpdateObserved(ctx, d.ID, ms.Ready, d.Revision)
	if err != nil || ready.State != ms.Ready {
		t.Fatalf("ready %+v %v", ready, err)
	}
	if _, err := s.UpdateObserved(ctx, d.ID, ms.Failed, ready.Revision); !errors.Is(err, ms.ErrConflict) {
		t.Fatalf("live job freed reservation: %v", err)
	}
	if err := s.FailDeploymentSubmission(ctx, d.ID); !errors.Is(err, ms.ErrConflict) {
		t.Fatalf("existing job submission failed: %v", err)
	}
	stopping, err := s.RequestStop(ctx, d.ID, d.OwnerID, ready.Revision)
	if err != nil || stopping.State != ms.Stopping {
		t.Fatalf("stop %+v %v", stopping, err)
	}
	if _, err := s.AuthorizeServingJobToken(ctx, d.JobID, token, time.Now()); !errors.Is(err, ms.ErrUnauthorized) {
		t.Fatalf("stopping token accepted: %v", err)
	}
	if _, err := s.UpdateObserved(ctx, d.ID, ms.Ready, stopping.Revision); !errors.Is(err, ms.ErrConflict) {
		t.Fatalf("stop regressed: %v", err)
	}
	next := base
	next.ID = uuid.NewString()
	next.JobID = validServingJobID()
	next.IdempotencyKey = "next"
	if _, _, err := s.ReserveDeployment(ctx, next); !errors.Is(err, ms.ErrQuota) {
		t.Fatalf("stop request prematurely freed reservation: %v", err)
	}
	if err := s.db.Model(&JobRecord{}).Where("id = ?", d.JobID).Updates(map[string]any{"observed_state": string(domain.StateCanceled), "desired_state": string(domain.DesiredCanceled)}).Error; err != nil {
		t.Fatal(err)
	}
	stopped, err := s.UpdateObserved(ctx, d.ID, ms.Stopped, stopping.Revision)
	if err != nil || stopped.State != ms.Stopped {
		t.Fatalf("terminal stop %+v %v", stopped, err)
	}
	reserveServing(t, s, next)
	pending, err := s.GetPendingDeployments(ctx, 100)
	if err != nil || len(pending) != 1 || pending[0].ID != next.ID {
		t.Fatalf("pending %+v %v", pending, err)
	}
}
func TestModelServingRejectsSnapshotAndOwnerSubstitution(t *testing.T) {
	for _, change := range []string{"owner", "sha", "size", "version", "unapproved"} {
		t.Run(change, func(t *testing.T) {
			s, d := servingFixture(t, evaluationTestStore(t))
			switch change {
			case "owner":
				d.OwnerID = "outsider"
			case "sha":
				d.ModelSHA256 = strings.Repeat("f", 64)
			case "size":
				d.ModelSizeBytes = 2
			case "version":
				d.VersionID = "other"
			case "unapproved":
				if err := s.db.Model(&mr.Release{}).Where("id = ?", d.ReleaseID).Update("state", mr.Pending).Error; err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := s.ReserveDeployment(context.Background(), d); err == nil {
				t.Fatal("invalid serving source accepted")
			}
		})
	}
}
func TestModelServingSubmissionFrozenFields(t *testing.T) {
	s, d := servingFixture(t, evaluationTestStore(t))
	d = reserveServing(t, s, d)
	j := domain.TrainingJob{ID: d.JobID, UserID: d.OwnerID, TenantID: d.TenantID, SubmissionOrigin: domain.SubmissionOriginServing, ExternalSubmissionID: d.ID, Spec: d.JobSpec}
	validate := func(job domain.TrainingJob) error {
		return s.db.Transaction(func(tx *gorm.DB) error {
			if err := lockServingSubmission(tx, d.ID); err != nil {
				return err
			}
			return validateServingSubmissionReservation(tx, &job)
		})
	}
	j.Spec.Queue = "trusted-queue"
	j.Spec.Priority = "normal"
	if err := validate(j); err != nil {
		t.Fatalf("trusted normalized defaults rejected: %v", err)
	}
	j.Spec.Entrypoint = domain.Entrypoint{Command: []string{"python", "attack.py"}}
	if !errors.Is(validate(j), ms.ErrConflict) {
		t.Fatal("changed entry accepted")
	}
}

func TestModelServingExpiryAndReadinessRequireAuthoritativeJob(t *testing.T) {
	s, d := servingFixture(t, evaluationTestStore(t))
	ctx := context.Background()
	d = reserveServing(t, s, d)
	if _, err := s.UpdateObserved(ctx, d.ID, ms.Ready, d.Revision); !errors.Is(err, ms.ErrNotReady) {
		t.Fatalf("jobless readiness accepted: %v", err)
	}
	if _, err := s.UpdateObserved(ctx, d.ID, ms.Expired, d.Revision); !errors.Is(err, ms.ErrConflict) {
		t.Fatalf("premature expiration accepted: %v", err)
	}
	token := createServingJob(t, s, d)
	d, err := s.GetDeployment(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&JobRecord{}).Where("id = ?", d.JobID).Update("observed_state", "QUEUED").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateObserved(ctx, d.ID, ms.Ready, d.Revision); !errors.Is(err, ms.ErrNotReady) {
		t.Fatalf("queued readiness accepted: %v", err)
	}
	if err := s.db.Model(&JobRecord{}).Where("id = ?", d.JobID).Update("external_submission_id", "different").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthorizeServingJobToken(ctx, d.JobID, token, time.Now()); !errors.Is(err, ms.ErrUnauthorized) {
		t.Fatalf("unrelated job token accepted: %v", err)
	}
	if err := s.db.Model(&JobRecord{}).Where("id = ?", d.JobID).Updates(map[string]any{"external_submission_id": d.ID, "desired_state": string(domain.DesiredCanceled)}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthorizeServingJobToken(ctx, d.JobID, token, time.Now()); !errors.Is(err, ms.ErrUnauthorized) {
		t.Fatalf("cancelled job token accepted: %v", err)
	}
}
