package nodeonboarding

import (
	"context"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func (c *Controller) claimUIDs(ctx context.Context, node *corev1.Node) ([]types.UID, error) {
	uids := make([]types.UID, 2)
	for i, suffix := range []string{"cache1", "cache2"} {
		claim, err := c.client.CoreV1().PersistentVolumeClaims(c.config.Namespace).Get(ctx, resourceName(node, suffix), metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if !owned(claim, node) || claim.UID == "" || claim.DeletionTimestamp != nil {
			return nil, fmt.Errorf("probe PVC identity unavailable")
		}
		uids[i] = claim.UID
	}
	return uids, nil
}
func sameClaimUIDs(first, second []types.UID) bool {
	return len(first) == 2 && len(second) == 2 && first[0] != "" && first[1] != "" && first[0] == second[0] && first[1] == second[1] && first[0] != first[1]
}
func expectedResourceUIDs(state reconcileState) map[string]types.UID {
	uids := map[string]types.UID{"prepare": state.PreparePodUID, "probe": state.ProbePodUID}
	if len(state.ClaimUIDs) == 2 {
		uids["cache1"] = state.ClaimUIDs[0]
		uids["cache2"] = state.ClaimUIDs[1]
	}
	return uids
}

// Reset may reclaim an unfinished cycle, but records every current UID before
// deletion and never replaces an identity that was already recorded as evidence.
func (c *Controller) captureResetUIDs(ctx context.Context, node *corev1.Node, state reconcileState) (reconcileState, error) {
	expected := expectedResourceUIDs(state)
	for _, suffix := range []string{"prepare", "probe", "cache1", "cache2"} {
		var object metav1.Object
		var err error
		if suffix == "prepare" || suffix == "probe" {
			object, err = c.client.CoreV1().Pods(c.config.Namespace).Get(ctx, resourceName(node, suffix), metav1.GetOptions{})
		} else {
			object, err = c.client.CoreV1().PersistentVolumeClaims(c.config.Namespace).Get(ctx, resourceName(node, suffix), metav1.GetOptions{})
		}
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return state, err
		}
		if !owned(object, node) || object.GetUID() == "" {
			return state, fmt.Errorf("refusing unowned reset resource")
		}
		if expected[suffix] != "" && expected[suffix] != object.GetUID() {
			return state, fmt.Errorf("resource UID changed before reset")
		}
		expected[suffix] = object.GetUID()
	}
	state.PreparePodUID = expected["prepare"]
	state.ProbePodUID = expected["probe"]
	state.ClaimUIDs = []types.UID{expected["cache1"], expected["cache2"]}
	return state, nil
}

func (c *Controller) checkCleanupClaimUIDs(ctx context.Context, node *corev1.Node, expected map[string]types.UID) error {
	for _, suffix := range []string{"cache1", "cache2"} {
		claim, err := c.client.CoreV1().PersistentVolumeClaims(c.config.Namespace).Get(ctx, resourceName(node, suffix), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !owned(claim, node) || claim.UID == "" || expected[suffix] != claim.UID {
			return fmt.Errorf("PVC UID changed before cleanup")
		}
	}
	return nil
}
