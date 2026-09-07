package datasetpublisher

import (
	"context"
	"errors"
	"testing"
)

type cleanupFencedPublicationRepository struct {
	memoryPublicationRunRepository
	called bool
}

func (r *cleanupFencedPublicationRepository) WithDatasetPublicationWrite(ctx context.Context, datasetID, versionID string, fn func() error) error {
	r.called = true
	return errors.New("cleanup fence refuses stale publisher")
}
func TestPublicationReconcileChecksCleanupFenceBeforeSideEffects(t *testing.T) {
	r := &cleanupFencedPublicationRepository{}
	jobs := &scriptedPublicationJobClient{}
	c, err := NewController(r, jobs, publicationControllerOptions())
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Reconcile(context.Background(), publicationReconcileRequest("run-one"))
	if !r.called || err == nil {
		t.Fatalf("called=%v err=%v", r.called, err)
	}
}
