package k8s

import (
	"context"
	"fmt"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Conservatively require publisher workers for this exact dataset version to be
// terminal. Do not rely on a FAILED database row alone; a terminating Pod may
// still write TOS. Training resources are neither selected nor modified.
func (c *Client) CheckDatasetCleanupQuiescent(ctx context.Context, datasetID, versionID string) error {
	if c == nil || c.kubernetes == nil || datasetID == "" || versionID == "" {
		return fmt.Errorf("publisher state unavailable")
	}
	selector := publicationNameLabel + "=" + publicationNameValue
	jobs, err := c.kubernetes.BatchV1().Jobs("").List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("publisher state unavailable")
	}
	for _, job := range jobs.Items {
		if publicationKnownOtherVersion(job.Labels, datasetID, versionID) {
			continue
		}
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
		if publicationKnownOtherVersion(pod.Labels, datasetID, versionID) {
			continue
		}
		if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
			return fmt.Errorf("publication worker is still active")
		}
	}
	return nil
}

// Missing identity labels are unknown, not evidence that work is unrelated.
func publicationKnownOtherVersion(labels map[string]string, datasetID, versionID string) bool {
	dataset, version := labels[publicationDatasetHashLabel], labels[publicationVersionIDHashLabel]
	return dataset != "" && version != "" && (dataset != boundedPublicationIdentityHash(datasetID) || version != boundedPublicationIdentityHash(versionID))
}
