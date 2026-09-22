package k8s

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"
)

func assistantStatusFixture() (*Client, *kfake.Clientset) {
	ns := "raytrain-assistant-test"
	count := int32(1)
	deployment := &apps.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "assistant-idle-controller", Namespace: ns}, Spec: apps.DeploymentSpec{Replicas: &count, Template: core.PodTemplateSpec{Spec: core.PodSpec{Volumes: []core.Volume{{Name: "config", VolumeSource: core.VolumeSource{ConfigMap: &core.ConfigMapVolumeSource{LocalObjectReference: core.LocalObjectReference{Name: "assistant-idle-config-current"}}}}}}}}, Status: apps.DeploymentStatus{ReadyReplicas: 1, AvailableReplicas: 1}}
	config := &core.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "assistant-idle-config-current", Namespace: ns}, Data: map[string]string{"config.json": `{"enabled":true,"render":{"Name":"assistant-canary","Namespace":"raytrain-assistant-test","ServeImage":"do-not-return","GateURL":"http://private.invalid"}}`}}
	old := config.DeepCopy()
	old.Name = "assistant-idle-config-old"
	old.Data = map[string]string{"config.json": "not-current"}
	pod := &core.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker-a", Namespace: ns, Labels: map[string]string{"app.kubernetes.io/instance": "assistant-canary", "app.kubernetes.io/component": "assistant-idle", "raytrain.wellspiking.ai/assistant-role": "worker"}}, Spec: core.PodSpec{NodeName: "node-a", Containers: []core.Container{{Name: "serve", Env: []core.EnvVar{{Name: "SECRET", Value: "never-return"}}, Resources: core.ResourceRequirements{Requests: core.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}}}}}, Status: core.PodStatus{Phase: core.PodRunning, Conditions: []core.PodCondition{{Type: core.PodReady, Status: core.ConditionTrue}}, ContainerStatuses: []core.ContainerStatus{{RestartCount: 2}}}}
	k := kfake.NewSimpleClientset(deployment, config, old, pod)
	service := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "ray.io/v1", "kind": "RayService", "metadata": map[string]any{"name": "assistant-canary", "namespace": ns}, "spec": map[string]any{"serveConfigV2": "private-env", "rayClusterConfig": map[string]any{"suspend": true}}, "status": map[string]any{"serviceStatus": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}}
	d := dfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{assistantRayServiceGVR: "RayServiceList"}, service)
	return NewClientFromInterfaces(d, k), k
}
func TestAssistantStatusUsesCurrentDeploymentConfigAndSafeProjection(t *testing.T) {
	c, k := assistantStatusFixture()
	result, err := c.ObserveAssistantStatus(context.Background(), "raytrain-assistant-test")
	if err != nil {
		t.Fatal(err)
	}
	if result.RayService.Name != "assistant-canary" || !result.RayService.Present || len(result.Pods) != 1 || result.Pods[0].GPURequested != 1 || result.Pods[0].Restarts != 2 {
		t.Fatalf("bad projection=%+v", result)
	}
	raw, _ := json.Marshal(result)
	for _, secret := range []string{"never-return", "do-not-return", "private-env", "private.invalid", "config.json"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("configuration leaked")
		}
	}
	for _, action := range k.Actions() {
		if action.GetNamespace() != "raytrain-assistant-test" || (action.GetVerb() != "get" && action.GetVerb() != "list") {
			t.Fatalf("unsafe action=%+v", action)
		}
	}
}
func TestAssistantStatusRejectsNamespaceBeforeKubernetesAccess(t *testing.T) {
	c, k := assistantStatusFixture()
	for _, ns := range []string{"default", "tenant-a", "raytrain-assistant-../../x"} {
		if _, err := c.ObserveAssistantStatus(context.Background(), ns); err == nil {
			t.Fatal("unsafe namespace accepted")
		}
	}
	if len(k.Actions()) != 0 {
		t.Fatal("unsafe namespace reached client")
	}
}
func TestAssistantStatusNeverFallsBackToHistoricalConfig(t *testing.T) {
	c, k := assistantStatusFixture()
	ctx := context.Background()
	ns := "raytrain-assistant-test"
	if err := k.CoreV1().ConfigMaps(ns).Delete(ctx, "assistant-idle-config-current", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ObserveAssistantStatus(ctx, ns); err == nil {
		t.Fatal("missing current config accepted")
	}
	for _, a := range k.Actions() {
		if a.GetResource().Resource == "configmaps" && a.GetVerb() == "list" {
			t.Fatal("historical configs enumerated")
		}
	}
}
func TestAssistantStatusUsesInstanceAndSuspension(t *testing.T) {
	c, k := assistantStatusFixture()
	ctx := context.Background()
	ns := "raytrain-assistant-test"
	cm, err := k.CoreV1().ConfigMaps(ns).Get(ctx, "assistant-idle-config-current", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cm.Data["config.json"] = `{"enabled":true,"render":{"Name":"assistant-canary","Namespace":"raytrain-assistant-test","InstanceID":"other-instance"}}`
	if _, err = k.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := c.ObserveAssistantStatus(ctx, ns)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pods) != 0 || got.RayService.Ready || !got.RayService.Suspended {
		t.Fatalf("wrong scope/readiness: %+v", got)
	}
}
