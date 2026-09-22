package assistantidle

import (
	"context"
	"encoding/json"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"strings"
	"testing"
	"time"
)

func podRenderConfig() RenderConfig {
	cfg := validRenderConfig()
	cfg.RuntimeType = "pod"
	cfg.GateURL = "https://assistant-idle-controller.raytrain-assistant-system.svc.cluster.local:8443/gate"
	cfg.TLSSecretName = "assistant-inference-tls"
	cfg.AuthSecretName = "assistant-inference-auth"
	cfg.GateCASecretName = "assistant-gate-ca"
	return cfg
}

func TestRenderPodUsesKueueGateTLSAndOneGPUWithoutRay(t *testing.T) {
	obj, err := RenderPod(podRenderConfig())
	if err != nil {
		t.Fatal(err)
	}
	if obj.GetKind() != "Pod" || obj.GetAPIVersion() != "v1" {
		t.Fatal(obj)
	}
	raw, _ := json.Marshal(obj.Object)
	body := string(raw)
	for _, want := range []string{"kueue.x-k8s.io/admission", "assistant_serve.standalone", "\"nvidia.com/gpu\":\"1\"", "\"automountServiceAccountToken\":false", "/run/assistant/tls", "/run/assistant/auth", "/run/assistant/gate-ca", "\"scheme\":\"HTTPS\"", "\"activeDeadlineSeconds\":3600"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s", want)
		}
	}
	for _, bad := range []string{"RayService", "rayStartParams", "6379", "8265", "hostPath", "\"privileged\":true"} {
		if strings.Contains(body, bad) {
			t.Fatalf("unexpected %s", bad)
		}
	}
	cfg := podRenderConfig()
	cfg.GateURL = "http://assistant-gate.svc.cluster.local:8443/gate"
	if _, err := RenderPod(cfg); err == nil {
		t.Fatal("accepted HTTP gate")
	}
	cfg = podRenderConfig()
	cfg.TLSSecretName = ""
	if _, err := RenderPod(cfg); err == nil {
		t.Fatal("accepted missing TLS")
	}
}

func TestPodAdmissionRequiresMatchingWorkloadUIDAndRemovedGate(t *testing.T) {
	cfg := podRenderConfig()
	cfg.Name, cfg.Namespace, cfg.InstanceID = "raytrain-assistant", "assistant-system", "assistant-instance"
	for _, tc := range []struct {
		name     string
		ownerUID string
		gated    bool
		want     bool
	}{
		{"admitted", "pod-uid", false, true}, {"stale workload", "other", false, false}, {"still gated", "pod-uid", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod, err := RenderPod(cfg)
			if err != nil {
				t.Fatal(err)
			}
			pod.SetUID("pod-uid")
			pod.SetCreationTimestamp(metav1.NewTime(time.Now()))
			pod.Object["status"] = map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
			if !tc.gated {
				unstructured.RemoveNestedField(pod.Object, "spec", "schedulingGates")
			}
			w := workload("assistant", "workload-uid", "admission", true, true)
			controller := true
			w.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: cfg.Name, UID: types.UID(tc.ownerUID), Controller: &controller}})
			markCondition(w, "Admitted", "True")
			adapter := testAdapter(pod, w)
			adapter.render = cfg
			adapter.demand = staticDemand(false)
			snapshot, err := adapter.Observe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got := snapshot.Observation.Admitted && snapshot.Observation.ServiceReady; got != tc.want {
				t.Fatalf("ready=%t snapshot=%+v", got, snapshot)
			}
			if err := adapter.Delete(context.Background(), "wrong-uid"); err == nil {
				t.Fatal("deleted stale UID")
			}
			if err := adapter.Delete(context.Background(), "pod-uid"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type staticDemand bool

func (d staticDemand) Pending(context.Context) (bool, error) { return bool(d), nil }

func TestPodMissingAuthoritativeDemandFailsClosed(t *testing.T) {
	adapter := testAdapter()
	adapter.render = podRenderConfig()
	if _, err := adapter.Observe(context.Background()); err == nil {
		t.Fatal("missing DB observer was treated as idle")
	}
	adapter.demand = staticDemand(true)
	snapshot, err := adapter.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Observation.TrainingDemand {
		t.Fatal("DB pending training was ignored")
	}
}
