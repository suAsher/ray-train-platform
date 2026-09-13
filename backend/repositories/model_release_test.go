package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	me "ray-train-platform-backend/modelevaluation"
	ml "ray-train-platform-backend/modellifecycle"
	mr "ray-train-platform-backend/modelrelease"
)

func releaseFixture(t *testing.T, s *ModelEvaluationRepository) (*ModelReleaseRepository, me.Evaluation) {
	t.Helper()
	if s.db.Dialector.Name() != "postgres" {
		if err := s.db.AutoMigrate(&mr.Release{}, &mr.Publication{}, &mr.History{}, &modelReleaseAudit{}); err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{"CREATE UNIQUE INDEX release_request ON model_releases(applicant_id,tenant_id,idempotency_key)", "CREATE UNIQUE INDEX publication_request ON model_publication_history(model_id,actor_id,idempotency_key)"} {
			if err := s.db.Exec(q).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	e := seedEvaluationSources(t, s)
	e = reserveEvaluationFixture(t, s, e, "release-eval")
	raw := evaluationReportBytes(t, e, 90)
	digest := releaseDigest(raw)
	now := time.Now().UTC()
	if err := s.db.Model(&modelEvaluationRecord{}).Where("id = ?", e.ID).Updates(map[string]any{"state": me.Succeeded, "report_state": me.ReportValid, "report_json": string(raw), "report_sha256": digest, "finished_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	e.State, e.ReportState, e.ReportSHA256 = me.Succeeded, me.ReportValid, digest
	return NewModelReleaseRepository(s.db), e
}
func TestModelReleaseEvidenceApprovalAndRollback(t *testing.T) {
	runModelReleaseTests(t, evaluationTestStore(t))
}
func runModelReleaseTests(t *testing.T, es *ModelEvaluationRepository) {
	s, e := releaseFixture(t, es)
	ctx := context.Background()
	owner := mr.Actor{ID: e.OwnerID, TenantID: e.TenantID, Name: "owner"}
	reviewer := mr.Actor{ID: "reviewer", TenantID: e.TenantID, TenantAdmin: true, Name: "reviewer"}
	request := mr.Request{ModelID: e.ModelID, VersionID: e.VersionID, EvaluationID: e.ID, Reason: "verified metrics", IdempotencyKey: "first"}
	if _, err := s.CreateRequest(ctx, request, reviewer); !errors.Is(err, mr.ErrUnauthorized) {
		t.Fatalf("non-owner request %v", err)
	}
	r, err := s.CreateRequest(ctx, request, owner)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateRequest(ctx, request, owner)
	if err != nil || replay.ID != r.ID {
		t.Fatalf("replay %+v %v", replay, err)
	}
	changed := request
	changed.Reason = "different"
	if _, err := s.CreateRequest(ctx, changed, owner); !errors.Is(err, mr.ErrConflict) {
		t.Fatalf("changed retry %v", err)
	}
	outsider := mr.Actor{ID: "outside", TenantID: "other"}
	if _, err := s.GetRelease(ctx, r.ID, outsider); !errors.Is(err, mr.ErrNotFound) {
		t.Fatalf("TEAM evidence exposed %v", err)
	}
	page, err := s.ListReleases(ctx, mr.Filter{}, outsider)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("TEAM list leaked %+v %v", page, err)
	}
	self := owner
	self.SuperAdmin = true
	if _, err := s.Decide(ctx, r.ID, mr.Decision{Revision: 1, Approve: true, Reason: "self"}, self); !errors.Is(err, mr.ErrUnauthorized) {
		t.Fatalf("self approval %v", err)
	}
	if _, err := s.Publish(ctx, e.ModelID, mr.PublishRequest{ReleaseID: r.ID, Reason: "publish", IdempotencyKey: "pub"}, owner); !errors.Is(err, mr.ErrNotReady) {
		t.Fatalf("pending publish %v", err)
	}
	r, err = s.Decide(ctx, r.ID, mr.Decision{Revision: 1, Approve: false, Reason: "need another review"}, reviewer)
	if err != nil || r.State != mr.Rejected {
		t.Fatalf("reject %+v %v", r, err)
	}
	if _, err := s.Decide(ctx, r.ID, mr.Decision{Revision: 2, Approve: true, Reason: "change decision"}, reviewer); !errors.Is(err, mr.ErrConflict) {
		t.Fatalf("terminal review %v", err)
	}
	request.IdempotencyKey = "second"
	r, err = s.CreateRequest(ctx, request, owner)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.Decide(ctx, r.ID, mr.Decision{Revision: 1, Approve: true, Reason: "independently verified"}, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	pReq := mr.PublishRequest{ReleaseID: r.ID, Reason: "publish approved", IdempotencyKey: "publish"}
	p, err := s.Publish(ctx, e.ModelID, pReq, owner)
	if err != nil || p.Revision != 1 {
		t.Fatalf("publish %+v %v", p, err)
	}
	repeat, err := s.Publish(ctx, e.ModelID, pReq, owner)
	if err != nil || repeat.Revision != 1 {
		t.Fatalf("publish replay %+v %v", repeat, err)
	}
	sharedPublication, err := s.GetPublication(ctx, e.ModelID, outsider)
	if err != nil || sharedPublication.ReleaseID != r.ID {
		t.Fatalf("publication should be shared without exposing evidence %+v %v", sharedPublication, err)
	}
	history, err := s.ListHistory(ctx, e.ModelID, "", 100, outsider)
	if err != nil || len(history.Items) != 1 || history.Items[0].ReleaseID != r.ID {
		t.Fatalf("history should be shared without exposing evidence %+v %v", history, err)
	}
	pReq.IdempotencyKey = "stale"
	if _, err := s.Publish(ctx, e.ModelID, pReq, owner); !errors.Is(err, mr.ErrConflict) {
		t.Fatalf("stale publish %v", err)
	}
	// Publish a second reviewed version, then restore the first by its immutable approval.
	secondEvaluation := secondReleaseVersion(t, s, e)
	secondRequest := request
	secondRequest.VersionID = secondEvaluation.VersionID
	secondRequest.EvaluationID = secondEvaluation.ID
	secondRequest.IdempotencyKey = "new-version"
	secondRelease, err := s.CreateRequest(ctx, secondRequest, owner)
	if err != nil {
		t.Fatal(err)
	}
	secondRelease, err = s.Decide(ctx, secondRelease.ID, mr.Decision{Revision: 1, Approve: true, Reason: "second model reviewed"}, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.Publish(ctx, e.ModelID, mr.PublishRequest{ReleaseID: secondRelease.ID, Revision: 1, IdempotencyKey: "second-publication", Reason: "roll forward"}, owner)
	if err != nil || p.VersionID != secondEvaluation.VersionID || p.Revision != 2 {
		t.Fatalf("second publication %+v %v", p, err)
	}
	pReq.Revision = 2
	pReq.IdempotencyKey = "rollback"
	pReq.Reason = "restore approved version"
	p, err = s.Publish(ctx, e.ModelID, pReq, owner)
	if err != nil || p.Revision != 3 || p.VersionID != e.VersionID {
		t.Fatalf("rollback %+v %v", p, err)
	}
	history, err = s.ListHistory(ctx, e.ModelID, "", 100, owner)
	if err != nil || len(history.Items) != 3 {
		t.Fatalf("history %+v %v", history, err)
	}
	if err := s.db.Model(&ml.Model{}).Where("id = ?", e.ModelID).Update("archived", true).Error; err != nil {
		t.Fatal(err)
	}
	request.IdempotencyKey = "archived"
	if _, err := s.CreateRequest(ctx, request, owner); !errors.Is(err, mr.ErrNotReady) {
		t.Fatalf("archived request %v", err)
	}
	pReq.Revision = 3
	pReq.IdempotencyKey = "archived"
	if _, err := s.Publish(ctx, e.ModelID, pReq, owner); !errors.Is(err, mr.ErrNotReady) {
		t.Fatalf("archived publish %v", err)
	}
	var count int64
	if err := s.db.Model(&modelReleaseAudit{}).Count(&count).Error; err != nil || count != 6 {
		t.Fatalf("audit count %d %v", count, err)
	}
}
func secondReleaseVersion(t *testing.T, s *ModelReleaseRepository, first me.Evaluation) me.Evaluation {
	t.Helper()
	var v ml.Version
	if err := s.db.Where("id = ?", first.VersionID).First(&v).Error; err != nil {
		t.Fatal(err)
	}
	v.ID = uuid.NewString()
	v.Number = 2
	v.IdempotencyKey = "second-snapshot"
	v.SHA256 = strings.Repeat("c", 64)
	v.Parts = []ml.Part{{Index: 0, SizeBytes: 1, SHA256: v.SHA256}}
	if err := s.db.Create(&v).Error; err != nil {
		t.Fatal(err)
	}
	var record modelEvaluationRecord
	if err := s.db.Where("id = ?", first.ID).First(&record).Error; err != nil {
		t.Fatal(err)
	}
	e, err := record.evaluation()
	if err != nil {
		t.Fatal(err)
	}
	e.ID = uuid.NewString()
	e.JobID = "job-" + uuid.NewString()
	e.VersionID = v.ID
	e.ModelSHA256 = v.SHA256
	e.IdempotencyKey = "second-evaluation"
	e.Report = nil
	raw := evaluationReportBytes(t, e, 92)
	snapshot, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	record.ID = e.ID
	record.JobID = e.JobID
	record.VersionID = v.ID
	record.SnapshotJSON = string(snapshot)
	record.IdempotencyKey = e.IdempotencyKey
	record.ReportJSON = string(raw)
	record.ReportSHA256 = releaseDigest(raw)
	if err := s.db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	return e
}
func TestModelReleaseRejectsMissingOrChangedEvidence(t *testing.T) {
	for _, change := range []string{"failed", "wrong-model", "wrong-digest"} {
		t.Run(change, func(t *testing.T) {
			es := evaluationTestStore(t)
			s, e := releaseFixture(t, es)
			switch change {
			case "failed":
				es.db.Model(&modelEvaluationRecord{}).Where("id = ?", e.ID).Update("state", me.Failed)
			case "wrong-model":
				es.db.Model(&modelEvaluationRecord{}).Where("id = ?", e.ID).Update("model_id", "different")
			case "wrong-digest":
				es.db.Model(&ml.Version{}).Where("id = ?", e.VersionID).Update("sha256", strings.Repeat("b", 64))
			}
			_, err := s.CreateRequest(context.Background(), mr.Request{ModelID: e.ModelID, VersionID: e.VersionID, EvaluationID: e.ID, Reason: "verified", IdempotencyKey: "request"}, mr.Actor{ID: e.OwnerID, TenantID: e.TenantID})
			if !errors.Is(err, mr.ErrNotReady) {
				t.Fatalf("%s evidence accepted: %v", change, err)
			}
		})
	}
}
