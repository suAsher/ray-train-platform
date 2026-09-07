package nodeonboarding

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{Namespace: "onboarding", StateConfigMap: "proof", Data1ConfigMap: "data1", Data2ConfigMap: "data2", Data1StorageClass: "ray-cache-local-data1", Data2StorageClass: "ray-cache-local-data2", Image: "registry/controller@sha256:" + string(makeHex()), HelperImage: "busybox@sha256:" + string(makeHex()), NFSConfigMap: "nfs", RevalidateAfter: time.Hour, RetryAfter: time.Minute}
}
func makeHex() []byte {
	b := make([]byte, 64)
	for i := range b {
		b[i] = 'a'
	}
	return b
}
func eligibleNode() *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu-1", UID: types.UID("node-uid"), Labels: map[string]string{"accelerator": "nvidia-rtx-4090", "platform.wellspiking.ai/gpu-pool": "production", "kubernetes.io/hostname": "gpu-1"}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}, Allocatable: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("8")}}}
}
func testController(t *testing.T) (*Controller, *fake.Clientset) {
	t.Helper()
	c := testConfig()
	binding := storagev1.VolumeBindingWaitForFirstConsumer
	client := fake.NewSimpleClientset(eligibleNode(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: c.Data1ConfigMap, Namespace: c.Namespace}, Data: map[string]string{"config.json": emptyConfig, "setup": "keep"}}, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: c.Data2ConfigMap, Namespace: c.Namespace}, Data: map[string]string{"config.json": emptyConfig}}, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: c.NFSConfigMap, Namespace: c.Namespace}, Data: map[string]string{"shares.json": `[{"server":"10.0.0.1","path":"/exports"}]`}}, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: c.Data1StorageClass}, VolumeBindingMode: &binding}, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: c.Data2StorageClass}, VolumeBindingMode: &binding})
	if _, err := client.CoreV1().ConfigMaps(c.Namespace).Create(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: c.StateConfigMap, Namespace: c.Namespace}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	client.PrependReactor("create", "persistentvolumeclaims", func(action ktesting.Action) (bool, runtime.Object, error) {
		claim := action.(ktesting.CreateAction).GetObject().(*corev1.PersistentVolumeClaim)
		if claim.UID == "" {
			claim.UID = types.UID(claim.Name + "-uid")
		}
		return false, nil, nil
	})
	controller, err := NewController(client, c)
	if err != nil {
		t.Fatal(err)
	}
	return controller, client
}
func TestControllerRejectsLabelOnlyReadiness(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	n, _ := k.CoreV1().Nodes().Get(ctx, "gpu-1", metav1.GetOptions{})
	n.Labels[CacheReadyLabel] = "true"
	k.CoreV1().Nodes().Update(ctx, n, metav1.UpdateOptions{})
	if err := c.Reconcile(ctx, "gpu-1"); err != nil {
		t.Fatal(err)
	}
	n, _ = k.CoreV1().Nodes().Get(ctx, "gpu-1", metav1.GetOptions{})
	if n.Labels[CacheReadyLabel] != "" {
		t.Fatal("trusted unverified ready label")
	}
	pods, _ := k.CoreV1().Pods(c.config.Namespace).List(ctx, metav1.ListOptions{})
	if len(pods.Items) != 1 {
		t.Fatalf("prep pods %d", len(pods.Items))
	}
}
func TestControllerRejectsIneligibleNode(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	n, _ := k.CoreV1().Nodes().Get(ctx, "gpu-1", metav1.GetOptions{})
	delete(n.Labels, "accelerator")
	n.Labels[CacheReadyLabel] = "true"
	k.CoreV1().Nodes().Update(ctx, n, metav1.UpdateOptions{})
	if err := c.Reconcile(ctx, n.Name); err != nil {
		t.Fatal(err)
	}
	pods, _ := k.CoreV1().Pods(c.config.Namespace).List(ctx, metav1.ListOptions{})
	if len(pods.Items) != 0 {
		t.Fatal("created pod for ineligible node")
	}
}
func TestControllerPartialRegistrationDoesNotReady(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	if err := c.Reconcile(ctx, "gpu-1"); err != nil {
		t.Fatal(err)
	}
	pods, _ := k.CoreV1().Pods(c.config.Namespace).List(ctx, metav1.ListOptions{})
	p := pods.Items[0]
	p.UID = "prep-uid"
	p.Status.Phase = corev1.PodSucceeded
	k.CoreV1().Pods(c.config.Namespace).Update(ctx, &p, metav1.UpdateOptions{})
	cm, _ := k.CoreV1().ConfigMaps(c.config.Namespace).Get(ctx, c.config.Data2ConfigMap, metav1.GetOptions{})
	cm.Data["config.json"] = `{}`
	k.CoreV1().ConfigMaps(c.config.Namespace).Update(ctx, cm, metav1.UpdateOptions{})
	if err := c.Reconcile(ctx, "gpu-1"); err == nil {
		t.Fatal("accepted invalid second map")
	}
	n, _ := k.CoreV1().Nodes().Get(ctx, "gpu-1", metav1.GetOptions{})
	if n.Labels[CacheReadyLabel] != "" {
		t.Fatal("ready on partial registration")
	}
	cm, _ = k.CoreV1().ConfigMaps(c.config.Namespace).Get(ctx, c.config.Data1ConfigMap, metav1.GetOptions{})
	if cm.Data["setup"] != "keep" {
		t.Fatal("lost unrelated config")
	}
}
func TestConfigRequiresImmutableImagesAndNFS(t *testing.T) {
	for _, change := range []func(*Config){func(c *Config) { c.Image = "latest" }, func(c *Config) { c.HelperImage = "busybox:latest" }, func(c *Config) { c.Namespace = "" }, func(c *Config) { c.NFSConfigMap = "" }, func(c *Config) { c.Data2ConfigMap = c.Data1ConfigMap }} {
		config := testConfig()
		change(&config)
		if _, err := NewController(fake.NewSimpleClientset(), config); err == nil {
			t.Fatal("accepted unsafe configuration")
		}
	}
}
