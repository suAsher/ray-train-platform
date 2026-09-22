package assistantidle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	dfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

type cacheFixture struct {
	cache      *ObservationCache
	dynamic    *dfake.FakeDynamicClient
	kubernetes *kfake.Clientset
	mu         sync.Mutex
	watchers   map[string][]*watch.RaceFreeFakeWatcher
	lists      atomic.Int32
	failLists  atomic.Bool
}

func newCacheFixture() *cacheFixture {
	d := dfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{rayJobGVR: "RayJobList", rayClusterGVR: "RayClusterList", workloadGVR: "WorkloadList"})
	k := kfake.NewSimpleClientset()
	f := &cacheFixture{dynamic: d, kubernetes: k, watchers: map[string][]*watch.RaceFreeFakeWatcher{}}
	list := func(a ktesting.Action) (bool, runtime.Object, error) {
		f.lists.Add(1)
		if f.failLists.Load() {
			return true, nil, errors.New("list unavailable")
		}
		switch a.GetResource().Resource {
		case "pods":
			return true, &corev1.PodList{ListMeta: metav1.ListMeta{ResourceVersion: "10"}}, nil
		case "nodes":
			return true, &corev1.NodeList{ListMeta: metav1.ListMeta{ResourceVersion: "10"}}, nil
		default:
			return true, &unstructured.UnstructuredList{Object: map[string]any{"apiVersion": "v1", "kind": "List", "metadata": map[string]any{"resourceVersion": "10"}}}, nil
		}
	}
	watcher := func(a ktesting.Action) (bool, watch.Interface, error) {
		w := watch.NewRaceFreeFake()
		f.mu.Lock()
		f.watchers[a.GetResource().Resource] = append(f.watchers[a.GetResource().Resource], w)
		f.mu.Unlock()
		return true, w, nil
	}
	d.PrependReactor("list", "*", list)
	k.PrependReactor("list", "*", list)
	d.PrependWatchReactor("*", watcher)
	k.PrependWatchReactor("*", watcher)
	f.cache = NewObservationCache(d, k)
	return f
}

func (f *cacheFixture) latest(resource string) *watch.RaceFreeFakeWatcher {
	f.mu.Lock()
	defer f.mu.Unlock()
	ws := f.watchers[resource]
	if len(ws) == 0 {
		return nil
	}
	return ws[len(ws)-1]
}

func cacheEventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("cache condition was not reached")
}

func startCache(t *testing.T, f *cacheFixture) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go f.cache.Run(ctx)
	syncCtx, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	if err := f.cache.WaitForSync(syncCtx); err != nil {
		t.Fatal(err)
	}
	return cancel
}

func TestObservationCacheRequiresEveryInitialListAndWatch(t *testing.T) {
	f := newCacheFixture()
	if _, err := f.cache.Kubernetes().CoreV1().Nodes().List(context.Background(), metav1.ListOptions{}); err == nil {
		t.Fatal("unsynced cache allowed observation")
	}
	block := make(chan struct{})
	f.dynamic.PrependWatchReactor("workloads", func(a ktesting.Action) (bool, watch.Interface, error) { <-block; return false, nil, nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.cache.Run(ctx)
	cacheEventually(t, func() bool { return f.latest("pods") != nil })
	if _, err := f.cache.Kubernetes().CoreV1().Pods("").List(ctx, metav1.ListOptions{}); err == nil {
		t.Fatal("partial cache allowed observation")
	}
	close(block)
	syncCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if err := f.cache.WaitForSync(syncCtx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := f.cache.Dynamic().Resource(rayJobGVR).Namespace("").List(ctx, metav1.ListOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.lists.Load(); got != 5 {
		t.Fatalf("cached reads repeated upstream lists: %d", got)
	}
}

func TestObservationCacheAppliesEventsFiltersAndReturnsCopies(t *testing.T) {
	f := newCacheFixture()
	startCache(t, f)
	w := f.latest("pods")
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "training", Namespace: "team-a", Labels: map[string]string{"app": "training"}, ResourceVersion: "11"}, Spec: corev1.PodSpec{NodeName: "gpu-a"}}
	w.Add(p)
	list := func() (*corev1.PodList, error) {
		return f.cache.Kubernetes().CoreV1().Pods("team-a").List(context.Background(), metav1.ListOptions{LabelSelector: "app=training"})
	}
	cacheEventually(t, func() bool { result, err := list(); return err == nil && len(result.Items) == 1 })
	result, _ := list()
	result.Items[0].Spec.NodeName = "tampered"
	result, _ = list()
	if result.Items[0].Spec.NodeName != "gpu-a" {
		t.Fatal("caller mutated cache")
	}
	other, err := f.cache.Kubernetes().CoreV1().Pods("team-b").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(other.Items) != 0 {
		t.Fatal("namespace filter failed", err)
	}
	p = p.DeepCopy()
	p.ResourceVersion = "12"
	p.Spec.NodeName = "gpu-b"
	w.Modify(p)
	cacheEventually(t, func() bool {
		result, err := list()
		return err == nil && len(result.Items) == 1 && result.Items[0].Spec.NodeName == "gpu-b"
	})
	w.Delete(p)
	cacheEventually(t, func() bool { result, err := list(); return err == nil && len(result.Items) == 0 })
}

func TestObservationCacheWatchFailureAndCancellationFailClosed(t *testing.T) {
	for _, fault := range []string{"error", "disconnect", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			f := newCacheFixture()
			cancel := startCache(t, f)
			f.failLists.Store(true)
			switch fault {
			case "error":
				f.latest("rayjobs").Error(&metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonExpired})
			case "disconnect":
				f.latest("rayjobs").Stop()
			case "cancel":
				cancel()
			}
			cacheEventually(t, func() bool {
				_, err := f.cache.Kubernetes().CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
				return err != nil
			})
		})
	}
}

func TestObservationCacheFreshnessExpiresDespiteOpenWatch(t *testing.T) {
	f := newCacheFixture()
	var elapsed atomic.Int64
	base := time.Now()
	f.cache.now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
	startCache(t, f)
	elapsed.Store(int64(36 * time.Second))
	if _, err := f.cache.Kubernetes().CoreV1().Nodes().List(context.Background(), metav1.ListOptions{}); err == nil {
		t.Fatal("expired open watch remained fresh")
	}
}

func TestObservationCacheNormalRefreshKeepsReadyAndRetainsNewDemand(t *testing.T) {
	f := newCacheFixture()
	f.cache.refreshEvery = 100 * time.Millisecond
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	var listCalls, watchCalls atomic.Int32
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "new-demand", Namespace: "team-a", ResourceVersion: "11"}}
	// New watch replays changes since the new List resourceVersion, including
	// changes that arrived while its HTTP request was being established.
	f.kubernetes.PrependWatchReactor("pods", func(a ktesting.Action) (bool, watch.Interface, error) {
		if watchCalls.Add(1) == 1 {
			return false, nil, nil
		}
		w := watch.NewRaceFreeFake()
		w.Add(p)
		f.mu.Lock()
		f.watchers["pods"] = append(f.watchers["pods"], w)
		f.mu.Unlock()
		return true, w, nil
	})
	f.kubernetes.PrependReactor("list", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		if listCalls.Add(1) == 1 {
			return false, nil, nil
		}
		select {
		case started <- struct{}{}:
		default:
		}
		<-block
		return false, nil, nil
	})
	startCache(t, f)
	old := f.latest("pods")
	select {
	case <-started:
	case <-time.After(time.Second):
		close(block)
		t.Fatal("refresh did not begin")
	}
	old.Add(p)
	cacheEventually(t, func() bool {
		result, err := f.cache.Kubernetes().CoreV1().Pods("").List(context.Background(), metav1.ListOptions{})
		return err == nil && len(result.Items) == 1
	})
	close(block)
	cacheEventually(t, func() bool { return f.latest("pods") != old })
	for i := 0; i < 20; i++ {
		if _, err := f.cache.Kubernetes().CoreV1().Nodes().List(context.Background(), metav1.ListOptions{}); err != nil {
			t.Fatal("normal refresh closed cache", err)
		}
	}
	cacheEventually(t, func() bool {
		result, err := f.cache.Kubernetes().CoreV1().Pods("").List(context.Background(), metav1.ListOptions{})
		return err == nil && len(result.Items) == 1
	})
}

func TestObservationCacheRecoversOnlyWithNewCompleteListAndWatch(t *testing.T) {
	f := newCacheFixture()
	startCache(t, f)
	old := f.latest("workloads")
	f.failLists.Store(true)
	old.Stop()
	cacheEventually(t, func() bool {
		_, err := f.cache.Dynamic().Resource(workloadGVR).List(context.Background(), metav1.ListOptions{})
		return err != nil
	})
	f.failLists.Store(false)
	cacheEventually(t, func() bool {
		_, err := f.cache.Dynamic().Resource(workloadGVR).List(context.Background(), metav1.ListOptions{})
		return err == nil && f.latest("workloads") != old
	})
}

func TestObservationCacheProjectionPreservesSchedulingAndOwnership(t *testing.T) {
	f := newCacheFixture()
	startCache(t, f)
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kueue.x-k8s.io/v1beta1", "kind": "Workload",
		"metadata": map[string]any{"name": "pending", "namespace": "tenant", "uid": "uid", "resourceVersion": "11", "labels": map[string]any{"env": "production"}, "annotations": map[string]any{"large": "removed"}, "ownerReferences": []any{map[string]any{"apiVersion": "ray.io/v1", "kind": "RayJob", "name": "job", "uid": "job-uid"}}},
		"spec":     map[string]any{"podSets": []any{map[string]any{"name": "worker", "count": int64(1), "template": map[string]any{"spec": map[string]any{"nodeSelector": map[string]any{"env": "production"}, "containers": []any{map[string]any{"name": "main", "env": []any{map[string]any{"name": "USER_DATA", "value": "removed"}}, "resources": map[string]any{"limits": map[string]any{"nvidia.com/gpu": "1"}}}}}}}}},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "Admitted", "status": "False"}}},
	}}
	f.latest("workloads").Add(obj)
	var projected *unstructured.Unstructured
	cacheEventually(t, func() bool {
		list, err := f.cache.Dynamic().Resource(workloadGVR).Namespace("tenant").List(context.Background(), metav1.ListOptions{})
		if err != nil || len(list.Items) != 1 {
			return false
		}
		projected = &list.Items[0]
		return true
	})
	if len(projected.GetAnnotations()) != 0 || projected.GetLabels()["env"] != "production" || len(projected.GetOwnerReferences()) != 1 {
		t.Fatal("metadata projection lost scheduling identity")
	}
	podSets, _, _ := unstructured.NestedSlice(projected.Object, "spec", "podSets")
	template := podSets[0].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	container := template["containers"].([]any)[0].(map[string]any)
	limits := container["resources"].(map[string]any)["limits"].(map[string]any)
	if template["nodeSelector"].(map[string]any)["env"] != "production" || limits["nvidia.com/gpu"] != "1" {
		t.Fatal("projection removed scheduling constraints")
	}
	if _, exists := container["env"]; exists {
		t.Fatal("projection retained large user environment")
	}
	if obj.GetAnnotations()["large"] != "removed" {
		t.Fatal("projection mutated API object")
	}
}

func TestObservationCacheForwardsNonListOperations(t *testing.T) {
	f := newCacheFixture()
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "ray.io/v1", "kind": "RayService", "metadata": map[string]any{"name": "owned", "namespace": "assistant"}}}
	client := f.cache.Dynamic().Resource(rayServiceGVR).Namespace("assistant")
	if _, err := client.Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(context.Background(), "owned", metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := client.Delete(context.Background(), "owned", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cache.Kubernetes().CoreV1().ConfigMaps("assistant").Create(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "probe"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}
