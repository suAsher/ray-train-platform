package k8s

import (
	"context"
	"fmt"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Conservatively require publisher workers for this exact dataset version to be
// terminal. Do not rely on a FAILED database row alone; a terminating Pod may
// still write TOS. Training resources are neither selected nor modified.
func (c *Client) CheckDatasetCleanupQuiescent(ctx context.Context, datasetID, versionID string) error {
	if c == nil || c.kubernetes == nil || datasetID == "" || versionID == "" {
		return fmt.Errorf("publisher state unavailable")
	}
	selector := labels.Set{
		publicationNameLabel:          publicationNameValue,
		publicationDatasetHashLabel:   boundedPublicationIdentityHash(datasetID),
		publicationVersionIDHashLabel: boundedPublicationIdentityHash(versionID),
	}.String()
	jobs, err := c.kubernetes.BatchV1().Jobs("").List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("publisher state unavailable")
	}
	for _, job := range jobs.Items {
		terminal := false
		for _, condition := range job.Status.Conditions {
			if (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) && condition.Status == corev1.ConditionTrue {
				terminal = true
			}
		}
		if !terminal || job.Status.Active > 0 {
			return fmt.Errorf("publication work is still active")
		}
	}
	pods, err := c.kubernetes.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("publisher state unavailable")
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
			return fmt.Errorf("publication worker is still active")
		}
	}
	return nil
}
