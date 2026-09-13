package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
	ms "ray-train-platform-backend/modelserving"
	"testing"
	"time"
)

func TestModelServingPostgresReservationAndImmutability(t *testing.T) {
	es, _ := evaluationPostgresStores(t, false)
	runServingReservationTests(t, es)
	var r modelServingDeploymentRecord
	if err := es.db.Where("state = ?", ms.Stopped).First(&r).Error; err != nil {
		t.Fatal(err)
	}
	for _, update := range []map[string]any{{"state": ms.Creating}, {"snapshot_json": "{}"}, {"job_id": "replacement"}, {"expires_at": time.Now().Add(24 * time.Hour)}} {
		if err := es.db.Model(&modelServingDeploymentRecord{}).Where("id = ?", r.ID).Updates(update).Error; err == nil {
			t.Fatalf("immutable serving mutation accepted: %v", update)
		}
	}
	var c modelServingContractRecord
	if err := es.db.First(&c).Error; err != nil {
		t.Fatal(err)
	}
	if err := es.db.Model(&c).Update("snapshot_json", "{}").Error; err == nil {
		t.Fatal("mutable serving executable")
	}
	if err := es.db.Exec("DELETE FROM model_serving_audits WHERE deployment_id = ?", r.ID).Error; err == nil {
		t.Fatal("serving audit history deleted")
	}
}
func TestModelServingPostgresStateAndTokens(t *testing.T) {
	es, _ := evaluationPostgresStores(t, false)
	runServingStateTests(t, es)
}
func TestModelServingPostgresConcurrentReservations(t *testing.T) {
	es, other := evaluationPostgresStores(t, false)
	a, base := servingFixture(t, es)
	b := NewModelServingRepository(other.db)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	type result struct {
		d       ms.Deployment
		created bool
		err     error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, s := range []*ModelServingRepository{a, b} {
		go func(s *ModelServingRepository) {
			<-start
			d, created, err := s.ReserveDeployment(ctx, base)
			results <- result{d, created, err}
		}(s)
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.d.ID != second.d.ID || first.created == second.created {
		t.Fatalf("concurrent retry: %+v %+v", first, second)
	}
	stopped, err := a.RequestStop(ctx, first.d.ID, base.OwnerID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != ms.Stopped {
		t.Fatal(stopped.State)
	}
	for _, s := range []*ModelServingRepository{a, b} {
		go func(s *ModelServingRepository) {
			d := base
			d.ID = uuid.NewString()
			d.JobID = validServingJobID()
			d.IdempotencyKey = d.ID
			out, created, err := s.ReserveDeployment(ctx, d)
			results <- result{out, created, err}
		}(s)
	}
	first, second = <-results, <-results
	if (first.err == nil) == (second.err == nil) || (first.err != nil && !errors.Is(first.err, ms.ErrQuota)) || (second.err != nil && !errors.Is(second.err, ms.ErrQuota)) {
		t.Fatalf("concurrent distinct: %+v %+v", first, second)
	}
}
func TestModelServingPostgresStopVersusSubmission(t *testing.T) {
	es, other := evaluationPostgresStores(t, false)
	a, base := servingFixture(t, es)
	b := NewModelServingRepository(other.db)
	d := reserveServing(t, a, base)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; _, err := a.RequestStop(ctx, d.ID, d.OwnerID, d.Revision); results <- err }()
	go func() {
		<-start
		results <- b.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := lockServingSubmission(tx, d.ID); err != nil {
				return err
			}
			input := domain.TrainingJob{ID: d.JobID, UserID: d.OwnerID, TenantID: d.TenantID, SubmissionOrigin: domain.SubmissionOriginServing, ExternalSubmissionID: d.ID, Spec: d.JobSpec}
			if err := validateServingSubmissionReservation(tx, &input); err != nil {
				return err
			}
			raw, err := json.Marshal(d.JobSpec)
			if err != nil {
				return err
			}
			return tx.Create(&JobRecord{ID: d.JobID, TenantID: d.TenantID, UserID: d.OwnerID, Name: d.JobSpec.Name, SpecJSON: string(raw), DesiredState: string(domain.DesiredActive), ObservedState: string(domain.StateRunning), KubernetesNS: "eval-test", TrainingEngine: string(domain.TrainingEngineRayTrain), RayVersion: domain.RayVersionCanary, ClusterAttempt: 1, CleanupJSON: "{}", SubmissionOrigin: string(domain.SubmissionOriginServing), ExternalSubmissionID: d.ID}).Error
		})
	}()
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil && !errors.Is(err, ms.ErrConflict) {
			t.Fatal(err)
		}
	}
	current, err := a.GetDeployment(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := a.db.Model(&JobRecord{}).Where("id = ?", d.JobID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if (count == 0 && current.State != ms.Stopped) || (count == 1 && current.State != ms.Stopping) {
		t.Fatalf("late submission escaped cancellation: jobs=%d state=%s", count, current.State)
	}
}
