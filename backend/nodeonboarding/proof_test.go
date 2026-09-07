package nodeonboarding

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestForgedAnnotationsCannotGrantReadiness(t *testing.T) {
	for _, stage := range []string{"ready", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			c, k := testController(t)
			ctx := context.Background()
			node, _ := k.CoreV1().Nodes().Get(ctx, "gpu-1", metav1.GetOptions{})
			state, _ := json.Marshal(reconcileState{UID: node.UID, Stage: stage, At: time.Now()})
			node.Annotations = map[string]string{StateAnnotation: string(state)}
			node.Labels[CacheReadyLabel] = "true"
			k.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
			if err := c.register(ctx, node.Name); err != nil {
				t.Fatal(err)
			}
			step(t, c)
			node, _ = k.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
			if node.Labels[CacheReadyLabel] != "" {
				t.Fatal("forged node metadata granted readiness")
			}
			pods, _ := k.CoreV1().Pods(c.config.Namespace).List(ctx, metav1.ListOptions{})
			if len(pods.Items) != 1 {
				t.Fatal("did not start real verification")
			}
		})
	}
}
func TestSpoofedSucceededPodRejected(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	node := eligibleNode()
	pod := c.preparePod(node)
	pod.Spec.Containers[0].Command = []string{"/bin/true"}
	pod.Status.Phase = corev1.PodSucceeded
	pod.UID = "spoof"
	if _, err := k.CoreV1().Pods(c.config.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.Reconcile(ctx, node.Name); err == nil {
		t.Fatal("accepted spoofed immutable pod spec")
	}
}

func TestReadyFastPathRejectsDriftButIgnoresOtherNodes(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	node := eligibleNode()
	if err := c.register(ctx, node.Name); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := c.configurationFingerprint(ctx, node.Name)
	if err != nil {
		t.Fatal(err)
	}
	state := reconcileState{UID: node.UID, Stage: "ready", At: c.now(), Receipt: &verificationReceipt{NodeUID: node.UID, PreparePodUID: "prep", ProbePodUID: "probe", ClaimUIDs: []types.UID{"claim1", "claim2"}, Volumes: []string{"pv1", "pv2"}, Fingerprint: fingerprint, At: c.now()}}
	if err := c.save(ctx, node, state, true, "verified fixture"); err != nil {
		t.Fatal(err)
	}
	if err := c.register(ctx, "another-node"); err != nil {
		t.Fatal(err)
	}
	step(t, c)
	cm, _ := k.CoreV1().ConfigMaps(c.config.ConfigNamespace).Get(ctx, c.config.Data1ConfigMap, metav1.GetOptions{})
	cm.Data["setup"] = "changed"
	k.CoreV1().ConfigMaps(c.config.ConfigNamespace).Update(ctx, cm, metav1.UpdateOptions{})
	if err := c.Reconcile(ctx, node.Name); err == nil {
		t.Fatal("ignored configuration fingerprint drift")
	}
	node, _ = k.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
	if node.Labels[CacheReadyLabel] != "" {
		t.Fatal("drift retained readiness")
	}
}

func TestCleanupNeedsCompleteProtectedProof(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	node := eligibleNode()
	if err := c.storeState(ctx, reconcileState{UID: node.UID, Stage: "cleanup", At: c.now()}); err != nil {
		t.Fatal(err)
	}
	if err := c.Reconcile(ctx, node.Name); err == nil {
		t.Fatal("cleanup accepted missing probe proof")
	}
	node, _ = k.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
	if node.Labels[CacheReadyLabel] != "" {
		t.Fatal("incomplete proof marked ready")
	}
}

func TestPodDefaultsAndPrivileges(t *testing.T) {
	c, _ := testController(t)
	node := eligibleNode()
	desired := c.probePod(node, []nfsShare{{Server: "server", Path: "/exports"}})
	actual := desired.DeepCopy()
	actual.Spec.NodeName = node.Name
	actual.Spec.DNSPolicy = corev1.DNSClusterFirst
	actual.Spec.SchedulerName = corev1.DefaultSchedulerName
	actual.Spec.EnableServiceLinks = pointer(true)
	actual.Spec.Tolerations = []corev1.Toleration{{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: pointer(int64(300))}}
	actual.Spec.Containers[0].TerminationMessagePath = corev1.TerminationMessagePathDefault
	if err := matchPodSpec(actual, desired, node); err != nil {
		t.Fatal(err)
	}
	actual.Spec.Containers[0].SecurityContext.Privileged = pointer(true)
	if err := matchPodSpec(actual, desired, node); err == nil {
		t.Fatal("accepted extra privilege")
	}
}

func TestLostEligibilityRequiresFreshProbeCycle(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	step(t, c)
	completePod(t, k, c.config.Namespace, resourceName(eligibleNode(), "prepare"))
	node, _ := k.CoreV1().Nodes().Get(ctx, "gpu-1", metav1.GetOptions{})
	node.Status.Conditions[0].Status = corev1.ConditionFalse
	k.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	step(t, c)
	node.Status.Conditions[0].Status = corev1.ConditionTrue
	k.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	step(t, c)
	step(t, c)
	step(t, c)
	pod, err := k.CoreV1().Pods(c.config.Namespace).Get(ctx, resourceName(node, "prepare"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pod.Status.Phase == corev1.PodSucceeded {
		t.Fatal("reused pre-reboot prepare success")
	}
}

func TestNFSRejectsRootExport(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	cm, err := k.CoreV1().ConfigMaps(c.config.ConfigNamespace).Get(ctx, c.config.NFSConfigMap, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cm.Data["shares.json"] = `[{"server":"10.0.0.1","path":"/"}]`
	if _, err := k.CoreV1().ConfigMaps(c.config.ConfigNamespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.nfsShares(ctx); err == nil {
		t.Fatal("accepted NFS root export contrary to chart contract")
	}
}
