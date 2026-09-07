package k8s

import (
	"context"
	"errors"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"testing"
)

func TestDatasetCleanupRequiresQuiescentPublishers(t *testing.T) {
	for _, active := range []bool{false, true} {
		phase := corev1.PodFailed
		if active {
			phase = corev1.PodRunning
		}
		client := &Client{kubernetes: fake.NewSimpleClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "publisher", Namespace: "platform", Labels: cleanupPublicationLabels("data", "version-one")}, Status: corev1.PodStatus{Phase: phase}})}
		err := client.CheckDatasetCleanupQuiescent(context.Background(), "data", "version-one")
		if (err != nil) != active {
			t.Fatalf("active=%v err=%v", active, err)
		}
	}
	client := &Client{kubernetes: fake.NewSimpleClientset(&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "platform", Labels: cleanupPublicationLabels("data", "version-one")}})}
	if client.CheckDatasetCleanupQuiescent(context.Background(), "data", "version-one") == nil {
		t.Fatal("pending Job accepted")
	}
}

func TestDatasetCleanupIgnoresOtherPublicationWork(t *testing.T) {
	client := &Client{kubernetes: fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "other-publisher", Namespace: "platform", Labels: cleanupPublicationLabels("other-data", "other-version")},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "other-pending", Namespace: "platform", Labels: cleanupPublicationLabels("other-data", "other-version")},
		},
	)}
	if err := client.CheckDatasetCleanupQuiescent(context.Background(), "data", "version-one"); err != nil {
		t.Fatalf("other publication blocked cleanup: %v", err)
	}
}

func TestDatasetCleanupBlocksPublisherWithUnknownIdentity(t *testing.T) {
	client := &Client{kubernetes: fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy-publisher", Namespace: "platform", Labels: map[string]string{publicationNameLabel: publicationNameValue}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	})}
	if client.CheckDatasetCleanupQuiescent(context.Background(), "data", "version-one") == nil {
		t.Fatal("publisher with missing identity labels was silently ignored")
	}
}

func TestDatasetCleanupFailsClosedOnUnknownClusterState(t *testing.T) {
	if (&Client{}).CheckDatasetCleanupQuiescent(context.Background(), "data", "version") == nil {
		t.Fatal("missing client accepted")
	}
	for _, resource := range []string{"pods", "jobs"} {
		f := fake.NewSimpleClientset()
		f.PrependReactor("list", resource, func(ktesting.Action) (bool, kruntime.Object, error) {
			return true, nil, errors.New("private cluster error")
		})
		if (&Client{kubernetes: f}).CheckDatasetCleanupQuiescent(context.Background(), "data", "version") == nil {
			t.Fatal("list error accepted")
		}
	}
}

func cleanupPublicationLabels(datasetID, versionID string) map[string]string {
	return map[string]string{
		publicationNameLabel:          publicationNameValue,
		publicationDatasetHashLabel:   boundedPublicationIdentityHash(datasetID),
		publicationVersionIDHashLabel: boundedPublicationIdentityHash(versionID),
	}
}
