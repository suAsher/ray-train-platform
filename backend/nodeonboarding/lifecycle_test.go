package nodeonboarding

import (
	"context"
	"encoding/json"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
	"time"
)

func completePod(t *testing.T, k *fake.Clientset, namespace, name string) {
	t.Helper()
	ctx := context.Background()
	pod, err := k.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.UID = types.UID(name + "-uid")
	pod.Spec.NodeName = "gpu-1"
	pod.Status.Phase = corev1.PodSucceeded
	if _, err := k.CoreV1().Pods(namespace).Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}
func step(t *testing.T, c *Controller) {
	t.Helper()
	if err := c.Reconcile(context.Background(), "gpu-1"); err != nil {
		t.Fatal(err)
	}
}
func bindClaims(t *testing.T, c *Controller, k *fake.Clientset) {
	t.Helper()
	ctx := context.Background()
	node := eligibleNode()
	for i := 1; i <= 2; i++ {
		name := resourceName(node, fmt.Sprintf("cache%d", i))
		claim, err := k.CoreV1().PersistentVolumeClaims(c.config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if claim.UID == "" {
			claim.UID = types.UID(name + "-uid")
		}
		claim.Spec.VolumeName = fmt.Sprintf("pv-%d", i)
		claim.Status.Phase = corev1.ClaimBound
		if _, err := k.CoreV1().PersistentVolumeClaims(c.config.Namespace).Update(ctx, claim, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: claim.Spec.VolumeName, UID: types.UID(claim.Spec.VolumeName + "-uid")}, Spec: corev1.PersistentVolumeSpec{StorageClassName: *claim.Spec.StorageClassName, PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete, ClaimRef: &corev1.ObjectReference{Name: claim.Name, Namespace: claim.Namespace, UID: claim.UID}, PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: fmt.Sprintf("/data%d/ray-cache/probe-volume", i)}}, NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "kubernetes.io/hostname", Operator: corev1.NodeSelectorOpIn, Values: []string{node.Name}}}}}}}}}
		if _, err := k.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
}
func TestLifecycleWaitsForCleanupAndRevalidates(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	node := eligibleNode()
	step(t, c)
	prep, _ := k.CoreV1().Pods(c.config.Namespace).Get(ctx, resourceName(node, "prepare"), metav1.GetOptions{})
	if len(prep.Spec.Containers) != 2 || len(prep.Spec.Containers[1].VolumeMounts) != 0 {
		t.Fatal("helper not prewarmed safely")
	}
	completePod(t, k, c.config.Namespace, prep.Name)
	step(t, c)
	step(t, c)
	bindClaims(t, c, k)
	completePod(t, k, c.config.Namespace, resourceName(node, "probe"))
	step(t, c)
	// Restart reads durable UID-bound state and still must await deleted PVs.
	restarted, err := NewController(k, c.config)
	if err != nil {
		t.Fatal(err)
	}
	c = restarted
	step(t, c)
	step(t, c)
	step(t, c)
	n, _ := k.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
	if n.Labels[CacheReadyLabel] != "" {
		t.Fatal("ready before PV deletion")
	}
	for i := 1; i <= 2; i++ {
		if err := k.CoreV1().PersistentVolumes().Delete(ctx, fmt.Sprintf("pv-%d", i), metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	step(t, c)
	n, _ = k.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
	if n.Labels[CacheReadyLabel] != "true" {
		t.Fatal("not ready after successful probes/reclamation")
	}
	step(t, c)
	pods, _ := k.CoreV1().Pods(c.config.Namespace).List(ctx, metav1.ListOptions{})
	if len(pods.Items) != 0 {
		t.Fatal("reprobed before interval")
	}
	c.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	step(t, c)
	pods, _ = k.CoreV1().Pods(c.config.Namespace).List(ctx, metav1.ListOptions{})
	if len(pods.Items) != 1 {
		t.Fatal("did not periodically revalidate")
	}
	n, _ = k.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
	if n.Labels[CacheReadyLabel] != "true" {
		t.Fatal("healthy periodic refresh revoked readiness")
	}
}
func TestNodeUIDReplacementDoesNotReuseSuccess(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	step(t, c)
	n, _ := k.CoreV1().Nodes().Get(ctx, "gpu-1", metav1.GetOptions{})
	n.UID = "replacement"
	n.Labels[CacheReadyLabel] = "true"
	state, _ := json.Marshal(reconcileState{UID: "node-uid", Stage: "ready", At: time.Now()})
	n.Annotations[StateAnnotation] = string(state)
	k.CoreV1().Nodes().Update(ctx, n, metav1.UpdateOptions{})
	step(t, c)
	n, _ = k.CoreV1().Nodes().Get(ctx, "gpu-1", metav1.GetOptions{})
	if n.Labels[CacheReadyLabel] != "" {
		t.Fatal("reused old node readiness")
	}
	if err := c.save(ctx, eligibleNode(), reconcileState{}, true, "test"); err == nil {
		t.Fatal("patched replaced node")
	}
}
func TestRejectsUnownedResourcesAndUnexpectedPV(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	node := eligibleNode()
	pod := c.preparePod(node)
	pod.Labels = nil
	k.CoreV1().Pods(c.config.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err := c.Reconcile(ctx, node.Name); err == nil {
		t.Fatal("adopted unowned pod")
	}
	if _, err := c.cleanup(ctx, node, nil, nil); err == nil {
		t.Fatal("deleted unowned pod")
	}
	k.CoreV1().Pods(c.config.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
	if err := c.ensureClaims(ctx, node); err != nil {
		t.Fatal(err)
	}
	bindClaims(t, c, k)
	pv, _ := k.CoreV1().PersistentVolumes().Get(ctx, "pv-2", metav1.GetOptions{})
	pv.Spec.HostPath.Path = "/data1/ray-cache/wrong"
	k.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{})
	expected, err := c.claimUIDs(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.verifyClaims(ctx, node, expected); err == nil {
		t.Fatal("accepted wrong disk")
	}
}
func TestNFSConfigAndProbeSecurity(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	for _, raw := range []string{`[]`, `null`, `[{"server":"bad/name","path":"/export"}]`, `[{"server":"server","path":"relative"}]`, `[{"server":"server","path":"/export","command":"bad"}]`, `[{"server":"server","path":"/export"}] {}`} {
		cm, _ := k.CoreV1().ConfigMaps(c.config.ConfigNamespace).Get(ctx, c.config.NFSConfigMap, metav1.GetOptions{})
		cm.Data["shares.json"] = raw
		k.CoreV1().ConfigMaps(c.config.ConfigNamespace).Update(ctx, cm, metav1.UpdateOptions{})
		if _, err := c.nfsShares(ctx); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	pod := c.probePod(eligibleNode(), []nfsShare{{Server: "10.0.0.1", Path: "/a"}, {Server: "10.0.0.2", Path: "/b"}})
	if *pod.Spec.AutomountServiceAccountToken || *pod.Spec.SecurityContext.RunAsUser != 1000 || len(pod.Spec.Volumes) != 4 || pod.Spec.NodeName != "" {
		t.Fatal("unsafe probe")
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.NFS != nil && !volume.NFS.ReadOnly {
			t.Fatal("writable NFS")
		}
	}
}
func TestFailedProbeCooldown(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	step(t, c)
	pod, _ := k.CoreV1().Pods(c.config.Namespace).Get(ctx, resourceName(eligibleNode(), "prepare"), metav1.GetOptions{})
	pod.UID = "failed-uid"
	pod.Status.Phase = corev1.PodFailed
	k.CoreV1().Pods(c.config.Namespace).Update(ctx, pod, metav1.UpdateOptions{})
	if err := c.Reconcile(ctx, "gpu-1"); err == nil {
		t.Fatal("missing failure")
	}
	c.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	step(t, c)
	pods, _ := k.CoreV1().Pods(c.config.Namespace).List(ctx, metav1.ListOptions{})
	if len(pods.Items) != 0 {
		t.Fatal("failed owned pod not cleaned")
	}
	step(t, c)
}
