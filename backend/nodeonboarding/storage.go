package nodeonboarding

import (
	"context"
	"fmt"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func (c *Controller) register(ctx context.Context, node string) error {
	for i, name := range []string{c.config.Data1ConfigMap, c.config.Data2ConfigMap} {
		cm, err := c.client.CoreV1().ConfigMaps(c.config.ConfigNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		updated, err := MergeNodePathMap([]byte(cm.Data["config.json"]), node, fmt.Sprintf("/data%d/ray-cache", i+1))
		if err != nil {
			return fmt.Errorf("config %s: %w", name, err)
		}
		if string(updated) == cm.Data["config.json"] {
			continue
		}
		next := cm.DeepCopy()
		next.Data["config.json"] = string(updated)
		if _, err = c.client.CoreV1().ConfigMaps(c.config.ConfigNamespace).Update(ctx, next, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}
func (c *Controller) checkRegistered(ctx context.Context, node string) error {
	for i, name := range []string{c.config.Data1ConfigMap, c.config.Data2ConfigMap} {
		cm, err := c.client.CoreV1().ConfigMaps(c.config.ConfigNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		updated, err := MergeNodePathMap([]byte(cm.Data["config.json"]), node, fmt.Sprintf("/data%d/ray-cache", i+1))
		if err != nil {
			return err
		}
		if string(updated) != cm.Data["config.json"] {
			return fmt.Errorf("node registration no longer present")
		}
	}
	return nil
}
func (c *Controller) ensureClaims(ctx context.Context, node *corev1.Node) error {
	for i, class := range []string{c.config.Data1StorageClass, c.config.Data2StorageClass} {
		sc, err := c.client.StorageV1().StorageClasses().Get(ctx, class, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if sc.VolumeBindingMode == nil || *sc.VolumeBindingMode != storagev1.VolumeBindingWaitForFirstConsumer {
			return fmt.Errorf("class %s must use WaitForFirstConsumer", class)
		}
		name := resourceName(node, fmt.Sprintf("cache%d", i+1))
		claim, err := c.client.CoreV1().PersistentVolumeClaims(c.config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = c.client.CoreV1().PersistentVolumeClaims(c.config.Namespace).Create(ctx, &corev1.PersistentVolumeClaim{ObjectMeta: objectMeta(node, c.config.Namespace, fmt.Sprintf("cache%d", i+1)), Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &class, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}, metav1.CreateOptions{})
			if err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !owned(claim, node) || claim.Spec.StorageClassName == nil || *claim.Spec.StorageClassName != class {
			return fmt.Errorf("unexpected probe PVC %s", name)
		}
	}
	return nil
}
func (c *Controller) verifyClaims(ctx context.Context, node *corev1.Node, expected []types.UID) ([]string, error) {
	if !sameClaimUIDs(expected, expected) {
		return nil, fmt.Errorf("two recorded PVC UIDs required")
	}
	var volumes []string
	for i, class := range []string{c.config.Data1StorageClass, c.config.Data2StorageClass} {
		claim, err := c.client.CoreV1().PersistentVolumeClaims(c.config.Namespace).Get(ctx, resourceName(node, fmt.Sprintf("cache%d", i+1)), metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if !owned(claim, node) || claim.UID != expected[i] || claim.Status.Phase != corev1.ClaimBound || claim.Spec.VolumeName == "" {
			return nil, fmt.Errorf("probe PVC not safely bound")
		}
		pv, err := c.client.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.UID != claim.UID || pv.Spec.ClaimRef.Namespace != c.config.Namespace || pv.Spec.ClaimRef.Name != claim.Name || pv.Spec.StorageClassName != class || pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete {
			return nil, fmt.Errorf("unexpected PV binding/reclaim policy")
		}
		root := fmt.Sprintf("/data%d/ray-cache/", i+1)
		actual := ""
		if pv.Spec.HostPath != nil {
			actual = pv.Spec.HostPath.Path
		} else if pv.Spec.Local != nil {
			actual = pv.Spec.Local.Path
		}
		if !strings.HasPrefix(actual, root) || path.Clean(actual) != actual || actual == strings.TrimSuffix(root, "/") {
			return nil, fmt.Errorf("unexpected PV cache path")
		}
		if !pvOnNode(pv, node) {
			return nil, fmt.Errorf("unexpected PV node affinity")
		}
		volumes = append(volumes, pv.Name)
	}
	return volumes, nil
}
func pvOnNode(pv *corev1.PersistentVolume, node *corev1.Node) bool {
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil || len(pv.Spec.NodeAffinity.Required.NodeSelectorTerms) == 0 {
		return false
	}
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		bound := false
		for _, expr := range term.MatchExpressions {
			if expr.Key == "kubernetes.io/hostname" && expr.Operator == corev1.NodeSelectorOpIn && len(expr.Values) == 1 && expr.Values[0] == node.Labels["kubernetes.io/hostname"] {
				bound = true
			}
		}
		for _, expr := range term.MatchFields {
			if expr.Key == "metadata.name" && expr.Operator == corev1.NodeSelectorOpIn && len(expr.Values) == 1 && expr.Values[0] == node.Name {
				bound = true
			}
		}
		if !bound {
			return false
		}
	}
	return true
}
func (c *Controller) claimVolumes(ctx context.Context, node *corev1.Node) ([]string, error) {
	var names []string
	for _, suffix := range []string{"cache1", "cache2"} {
		claim, err := c.client.CoreV1().PersistentVolumeClaims(c.config.Namespace).Get(ctx, resourceName(node, suffix), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !owned(claim, node) {
			return nil, fmt.Errorf("refusing unowned probe PVC")
		}
		if claim.Spec.VolumeName != "" {
			names = append(names, claim.Spec.VolumeName)
		}
	}
	return names, nil
}

func (c *Controller) cleanup(ctx context.Context, node *corev1.Node, volumes []string, expected map[string]types.UID) (bool, error) {
	if err := c.checkCleanupClaimUIDs(ctx, node, expected); err != nil {
		return false, err
	}
	pending := false
	for _, suffix := range []string{"prepare", "probe"} {
		pod, err := c.client.CoreV1().Pods(c.config.Namespace).Get(ctx, resourceName(node, suffix), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if !owned(pod, node) || pod.UID == "" {
			return false, fmt.Errorf("refusing cleanup of unowned pod")
		}
		if expected[suffix] != pod.UID {
			return false, fmt.Errorf("probe Pod UID changed before cleanup")
		}
		uid := pod.UID
		if err := c.client.CoreV1().Pods(c.config.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		pending = true
	}
	if pending {
		return false, nil
	}
	for _, suffix := range []string{"cache1", "cache2"} {
		claim, err := c.client.CoreV1().PersistentVolumeClaims(c.config.Namespace).Get(ctx, resourceName(node, suffix), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if !owned(claim, node) || claim.UID == "" {
			return false, fmt.Errorf("refusing cleanup of unowned PVC")
		}
		if expected[suffix] != claim.UID {
			return false, fmt.Errorf("PVC UID changed during cleanup")
		}
		uid := claim.UID
		if err := c.client.CoreV1().PersistentVolumeClaims(c.config.Namespace).Delete(ctx, claim.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		pending = true
	}
	for _, name := range volumes {
		_, err := c.client.CoreV1().PersistentVolumes().Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			pending = true
		} else if !apierrors.IsNotFound(err) {
			return false, err
		}
	}
	return !pending, nil
}
