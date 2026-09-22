package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coordv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	kfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"ray-train-platform-backend/assistantidle"
)

func TestObservationClientsAllowInitialListBeyondControllerStepBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/configmaps/action-probe") {
			<-r.Context().Done()
			return
		}
		if r.URL.Query().Get("watch") == "true" {
			if r.URL.Query().Get("resourceVersion") != "10" {
				t.Error("watch did not resume list resourceVersion")
			}
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		version, kind := "v1", "NodeList"
		switch {
		case strings.HasSuffix(r.URL.Path, "/pods"):
			time.Sleep(2200 * time.Millisecond)
			kind = "PodList"
		case strings.HasSuffix(r.URL.Path, "/rayjobs"):
			version, kind = "ray.io/v1", "RayJobList"
		case strings.HasSuffix(r.URL.Path, "/rayclusters"):
			version, kind = "ray.io/v1", "RayClusterList"
		case strings.HasSuffix(r.URL.Path, "/workloads"):
			version, kind = "kueue.x-k8s.io/v1beta1", "WorkloadList"
		}
		fmt.Fprintf(w, `{"apiVersion":%q,"kind":%q,"metadata":{"resourceVersion":"10"},"items":[]}`, version, kind)
	}))
	defer server.Close()
	rc := &rest.Config{Host: server.URL, Timeout: 2 * time.Second}
	actionDynamic, err := dynamic.NewForConfig(rc)
	if err != nil {
		t.Fatal(err)
	}
	actionKubernetes, err := kubernetes.NewForConfig(rc)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := newObservationCache(rc, actionDynamic, actionKubernetes)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Timeout != 2*time.Second {
		t.Fatal("watch setup changed original action timeout")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cache.Run(ctx)
	wait, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	if err := cache.WaitForSync(wait); err != nil {
		t.Fatal(err)
	}
	attempt, stopAttempt := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stopAttempt()
	if _, err := cache.Kubernetes().CoreV1().Pods("").List(attempt, metav1.ListOptions{}); err != nil {
		t.Fatal("cached observe retained slow list", err)
	}
	actionCtx, stopAction := context.WithTimeout(ctx, 5*time.Second)
	defer stopAction()
	started := time.Now()
	if _, err := cache.Kubernetes().CoreV1().ConfigMaps("assistant").Get(actionCtx, "action-probe", metav1.GetOptions{}); err == nil {
		t.Fatal("unresponsive uncached action succeeded")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("uncached action lost original 2s HTTP timeout")
	}
}

func TestReaperKeepsFreshLeaseAndReclaimsAfterExpiry(t *testing.T) {
	now := time.Now().UTC()
	ns := "raytrain-assistant-test"
	name := "assistant-idle"
	service := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "ray.io/v1", "kind": "RayService", "metadata": map[string]any{"name": name, "namespace": ns, "uid": "owned", "creationTimestamp": now.Add(-time.Minute).Format(time.RFC3339), "labels": map[string]any{"app.kubernetes.io/instance": name, "app.kubernetes.io/component": "assistant-idle"}}}}
	dyn := dfake.NewSimpleDynamicClient(runtime.NewScheme(), service)
	holder := "controller"
	duration := int32(30)
	renewed := metav1.NewMicroTime(now)
	lease := &coordv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: coordv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &duration, RenewTime: &renewed}}
	typed := kfake.NewSimpleClientset(lease)
	backend := assistantidle.NewKubeBackend(assistantidle.KubeAdapterConfig{Dynamic: dyn, Kubernetes: typed, Name: name, Namespace: ns, InstanceID: name})
	cfg := assistantidle.RuntimeConfig{LeaseName: name, Render: assistantidle.RenderConfig{Name: name, Namespace: ns}}
	if err := reap(context.Background(), typed, backend, cfg, now); err != nil {
		t.Fatal(err)
	}
	gvr := schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayservices"}
	if _, err := dyn.Resource(gvr).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{}); err != nil {
		t.Fatal("deleted fresh service", err)
	}
	if err := reap(context.Background(), typed, backend, cfg, now.Add(31*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := dyn.Resource(gvr).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{}); err == nil {
		t.Fatal("failed to delete expired service")
	}
}
func TestReaperBoundsLifetimeAndNeverDeletesForeignService(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	ns := "raytrain-assistant-test"
	name := "assistant-idle"
	for _, tc := range []struct {
		name                                         string
		age                                          time.Duration
		missingLease, foreign, wantDelete, wantError bool
	}{
		{"maximum lifetime", time.Hour + 16*time.Second, false, false, true, false},
		{"missing lease", time.Minute, true, false, true, false},
		{"foreign service", time.Hour + 16*time.Second, true, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instance := name
			if tc.foreign {
				instance = "someone-else"
			}
			service := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "ray.io/v1", "kind": "RayService", "metadata": map[string]any{"name": name, "namespace": ns, "uid": "owned", "creationTimestamp": now.Add(-tc.age).Format(time.RFC3339), "labels": map[string]any{"app.kubernetes.io/instance": instance, "app.kubernetes.io/component": "assistant-idle"}}}}
			dyn := dfake.NewSimpleDynamicClient(runtime.NewScheme(), service)
			typed := kfake.NewSimpleClientset()
			if !tc.missingLease {
				holder := "controller"
				duration := int32(30)
				renewed := metav1.NewMicroTime(now)
				_, err := typed.CoordinationV1().Leases(ns).Create(context.Background(), &coordv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: coordv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &duration, RenewTime: &renewed}}, metav1.CreateOptions{})
				if err != nil {
					t.Fatal(err)
				}
			}
			backend := assistantidle.NewKubeBackend(assistantidle.KubeAdapterConfig{Dynamic: dyn, Kubernetes: typed, Name: name, Namespace: ns, InstanceID: name})
			cfg := assistantidle.RuntimeConfig{LeaseName: name, Render: assistantidle.RenderConfig{Name: name, Namespace: ns}}
			err := reap(context.Background(), typed, backend, cfg, now)
			if (err != nil) != tc.wantError {
				t.Fatal(err)
			}
			_, err = dyn.Resource(schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayservices"}).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
			if (err != nil) != tc.wantDelete {
				t.Fatalf("deleted=%t want=%t", err != nil, tc.wantDelete)
			}
		})
	}
}
