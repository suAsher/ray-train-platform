package assistantidle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	typedcore "k8s.io/client-go/kubernetes/typed/core/v1"
	clientcache "k8s.io/client-go/tools/cache"
)

var ErrObservationCacheUnavailable = errors.New("assistant observation cache is not synchronized or fresh")

// ObservationCache serves bounded, read-only observations while leaving writes
// and individual object lookups on the original Kubernetes clients. Each stream
// uses List+Watch from the list RV, with overlapping generations so normal
// refresh does not close the gate. A live socket alone never extends freshness.
type ObservationCache struct {
	dynamic      dynamic.Interface
	kubernetes   kubernetes.Interface
	now          func() time.Time
	refreshEvery time.Duration
	mu           sync.RWMutex
	runOnce      sync.Once
	ctx          context.Context
	streams      map[string]*observationStream
}

type observationStream struct {
	list       func(context.Context) (runtime.Object, error)
	watch      func(context.Context, metav1.ListOptions) (watch.Interface, error)
	store      clientcache.Store
	validUntil time.Time
	healthy    bool
}

type observationRefresh struct {
	store      clientcache.Store
	watch      watch.Interface
	cancel     context.CancelFunc
	validUntil time.Time
	err        error
}

func NewObservationCache(d dynamic.Interface, k kubernetes.Interface) *ObservationCache {
	return NewObservationCacheWithWatchClients(d, k, d, k)
}

// NewObservationCacheWithWatchClients keeps all uncached methods on the exact
// original action clients, including their transport-level timeouts.
func NewObservationCacheWithWatchClients(d dynamic.Interface, k kubernetes.Interface, watchDynamic dynamic.Interface, watchKubernetes kubernetes.Interface) *ObservationCache {
	c := &ObservationCache{dynamic: d, kubernetes: k, now: time.Now, refreshEvery: 20 * time.Second, streams: map[string]*observationStream{}}
	for _, gvr := range []schema.GroupVersionResource{rayJobGVR, rayClusterGVR, workloadGVR} {
		client := watchDynamic.Resource(gvr).Namespace(metav1.NamespaceAll)
		c.streams[gvr.Resource] = &observationStream{
			list:  func(ctx context.Context) (runtime.Object, error) { return client.List(ctx, metav1.ListOptions{}) },
			watch: client.Watch,
		}
	}
	pods, nodes := watchKubernetes.CoreV1().Pods(metav1.NamespaceAll), watchKubernetes.CoreV1().Nodes()
	c.streams["pods"] = &observationStream{list: func(ctx context.Context) (runtime.Object, error) { return pods.List(ctx, metav1.ListOptions{}) }, watch: pods.Watch}
	c.streams["nodes"] = &observationStream{list: func(ctx context.Context) (runtime.Object, error) { return nodes.List(ctx, metav1.ListOptions{}) }, watch: nodes.Watch}
	return c
}

func (c *ObservationCache) Dynamic() dynamic.Interface {
	return observationDynamic{Interface: c.dynamic, cache: c}
}
func (c *ObservationCache) Kubernetes() kubernetes.Interface {
	return observationKubernetes{Interface: c.kubernetes, cache: c}
}

func (c *ObservationCache) Run(ctx context.Context) {
	c.runOnce.Do(func() {
		c.mu.Lock()
		c.ctx = ctx
		c.mu.Unlock()
		var workers sync.WaitGroup
		for _, stream := range c.streams {
			workers.Add(1)
			go func(s *observationStream) { defer workers.Done(); c.runStream(ctx, s) }(stream)
		}
		workers.Wait()
	})
}

func (c *ObservationCache) WaitForSync(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		c.mu.RLock()
		ready := c.readyLocked()
		c.mu.RUnlock()
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *ObservationCache) readyLocked() bool {
	if c.ctx == nil || c.ctx.Err() != nil {
		return false
	}
	now := c.now()
	for _, s := range c.streams {
		if !s.healthy || s.store == nil || !now.Before(s.validUntil) {
			return false
		}
	}
	return true
}

func (c *ObservationCache) runStream(ctx context.Context, s *observationStream) {
	var active watch.Interface
	var cancelWatch context.CancelFunc
	var events <-chan watch.Event
	results := make(chan observationRefresh)
	timer := time.NewTimer(0)
	defer timer.Stop()
	defer func() {
		if active != nil {
			active.Stop()
		}
		if cancelWatch != nil {
			cancelWatch()
		}
		c.mu.Lock()
		s.healthy = false
		c.mu.Unlock()
	}()
	pending := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if !pending {
				pending = true
				go c.refresh(ctx, s, results)
			}
		case result := <-results:
			pending = false
			if result.err != nil {
				// A failed background refresh does not interrupt a still-valid
				// watch; the old snapshot's original deadline remains binding.
				timer.Reset(time.Second)
				continue
			}
			old, stopOld := active, cancelWatch
			active, cancelWatch, events = result.watch, result.cancel, result.watch.ResultChan()
			c.mu.Lock()
			s.store, s.validUntil, s.healthy = result.store, result.validUntil, true
			c.mu.Unlock()
			if old != nil {
				old.Stop()
			}
			if stopOld != nil {
				stopOld()
			}
			timer.Reset(c.refreshEvery)
		case event, ok := <-events:
			if !ok || event.Type == watch.Error || !c.applyEvent(s, event) {
				c.mu.Lock()
				s.healthy = false
				c.mu.Unlock()
				if active != nil {
					active.Stop()
				}
				if cancelWatch != nil {
					cancelWatch()
				}
				active, cancelWatch, events = nil, nil, nil
				if !pending {
					timer.Reset(time.Millisecond)
				}
			}
		}
	}
}

func (c *ObservationCache) refresh(ctx context.Context, s *observationStream, results chan<- observationRefresh) {
	result := c.prepareRefresh(ctx, s)
	select {
	case results <- result:
	case <-ctx.Done():
		if result.watch != nil {
			result.watch.Stop()
		}
		if result.cancel != nil {
			result.cancel()
		}
	}
}

func (c *ObservationCache) prepareRefresh(ctx context.Context, s *observationStream) observationRefresh {
	validUntil := c.now().Add(35 * time.Second)
	attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
	object, err := s.list(attempt)
	cancel()
	if err != nil {
		return observationRefresh{err: err}
	}
	metadata, err := meta.ListAccessor(object)
	if err != nil || metadata.GetResourceVersion() == "" {
		return observationRefresh{err: ErrObservationCacheUnavailable}
	}
	objects, err := meta.ExtractList(object)
	if err != nil {
		return observationRefresh{err: err}
	}
	store := clientcache.NewStore(clientcache.MetaNamespaceKeyFunc)
	for _, obj := range objects {
		if err := store.Add(compactObservation(obj)); err != nil {
			return observationRefresh{err: err}
		}
	}
	// A separate watch context outlives List, and is canceled as soon as its
	// replacement becomes active. It also bounds a hung watch establishment.
	watchCtx, stopWatch := context.WithTimeout(ctx, 60*time.Second)
	seconds := int64(60)
	w, err := s.watch(watchCtx, metav1.ListOptions{ResourceVersion: metadata.GetResourceVersion(), AllowWatchBookmarks: true, TimeoutSeconds: &seconds})
	if err != nil {
		stopWatch()
		return observationRefresh{err: err}
	}
	return observationRefresh{store: store, watch: w, cancel: stopWatch, validUntil: validUntil}
}

func (c *ObservationCache) applyEvent(s *observationStream, event watch.Event) bool {
	if event.Type == watch.Bookmark {
		return true
	}
	if event.Object == nil {
		return false
	}
	obj := compactObservation(event.Object)
	c.mu.Lock()
	defer c.mu.Unlock()
	if s.store == nil {
		return false
	}
	switch event.Type {
	case watch.Added, watch.Modified:
		return s.store.Update(obj) == nil
	case watch.Deleted:
		return s.store.Delete(obj) == nil
	default:
		return false
	}
}

func (c *ObservationCache) list(ctx context.Context, resource, namespace string, opts metav1.ListOptions) ([]runtime.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Pagination/RV semantics are not implemented by this private projection.
	// Never silently claim a partial list is a complete cluster observation.
	if opts.Continue != "" || opts.Limit != 0 || opts.ResourceVersion != "" || opts.ResourceVersionMatch != "" {
		return nil, fmt.Errorf("unsupported cached list options")
	}
	selector, err := labels.Parse(opts.LabelSelector)
	if err != nil {
		return nil, err
	}
	fieldSelector, err := fields.ParseSelector(opts.FieldSelector)
	if err != nil {
		return nil, err
	}
	for _, requirement := range fieldSelector.Requirements() {
		if requirement.Field != "metadata.name" && requirement.Field != "metadata.namespace" && !(resource == "pods" && (requirement.Field == "spec.nodeName" || requirement.Field == "status.phase")) {
			return nil, fmt.Errorf("unsupported cached field selector")
		}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.readyLocked() {
		return nil, ErrObservationCacheUnavailable
	}
	s := c.streams[resource]
	if s == nil {
		return nil, ErrObservationCacheUnavailable
	}
	result := make([]runtime.Object, 0)
	for _, value := range s.store.List() {
		obj := value.(runtime.Object)
		metadata, err := meta.Accessor(obj)
		if err != nil {
			return nil, err
		}
		if namespace != "" && metadata.GetNamespace() != namespace {
			continue
		}
		if !selector.Matches(labels.Set(metadata.GetLabels())) {
			continue
		}
		set := fields.Set{"metadata.name": metadata.GetName(), "metadata.namespace": metadata.GetNamespace()}
		if pod, ok := obj.(*corev1.Pod); ok {
			set["spec.nodeName"], set["status.phase"] = pod.Spec.NodeName, string(pod.Status.Phase)
		}
		if fieldSelector.Matches(set) {
			result = append(result, obj.DeepCopyObject())
		}
	}
	return result, nil
}

// Remove user scripts, environment values and API bookkeeping; keep complete
// scheduling/resource/owner/status structures. This is a private observation
// projection and is never used as input to Update or Create.
func compactObservation(obj runtime.Object) runtime.Object {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		root := make(map[string]any, len(u.Object))
		for key, value := range u.Object {
			root[key] = value
		}
		if spec, ok := root["spec"].(map[string]any); ok {
			filtered := make(map[string]any, len(spec))
			for key, value := range spec {
				if key != "entrypoint" && key != "runtimeEnvYAML" {
					filtered[key] = value
				}
			}
			root["spec"] = filtered
		}
		return &unstructured.Unstructured{Object: compactObservationMap(root)}
	}
	copy := obj.DeepCopyObject()
	if metadata, err := meta.Accessor(copy); err == nil {
		metadata.SetManagedFields(nil)
		metadata.SetAnnotations(nil)
	}
	switch v := copy.(type) {
	case *corev1.Pod:
		for i := range v.Spec.Containers {
			compactContainer(&v.Spec.Containers[i])
		}
		for i := range v.Spec.InitContainers {
			compactContainer(&v.Spec.InitContainers[i])
		}
		for i := range v.Spec.EphemeralContainers {
			e := &v.Spec.EphemeralContainers[i]
			e.Env, e.EnvFrom, e.Command, e.Args = nil, nil, nil, nil
		}
	case *corev1.Node:
		v.Status.Images = nil
	}
	return copy
}

func compactContainer(c *corev1.Container) { c.Env, c.EnvFrom, c.Command, c.Args = nil, nil, nil, nil }

func compactObservationMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		if key == "metadata" {
			if metadata, ok := value.(map[string]any); ok {
				filtered := make(map[string]any, len(metadata))
				for field, item := range metadata {
					if field != "managedFields" && field != "annotations" {
						filtered[field] = item
					}
				}
				value = filtered
			}
		}
		if key == "containers" || key == "initContainers" || key == "ephemeralContainers" {
			if containers, ok := value.([]any); ok {
				filtered := make([]any, len(containers))
				for i, container := range containers {
					fields, ok := container.(map[string]any)
					if !ok {
						filtered[i] = container
						continue
					}
					copy := make(map[string]any, len(fields))
					for field, item := range fields {
						if field != "env" && field != "envFrom" && field != "command" && field != "args" {
							copy[field] = item
						}
					}
					filtered[i] = copy
				}
				value = filtered
			}
		}
		result[key] = compactObservationValue(value)
	}
	return result
}

func compactObservationValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return compactObservationMap(v)
	case []any:
		result := make([]any, len(v))
		for i := range v {
			result[i] = compactObservationValue(v[i])
		}
		return result
	default:
		return value
	}
}

type observationDynamic struct {
	dynamic.Interface
	cache *ObservationCache
}

func (d observationDynamic) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	client := d.Interface.Resource(gvr)
	if gvr != rayJobGVR && gvr != rayClusterGVR && gvr != workloadGVR {
		return client
	}
	return observationNamespaceable{NamespaceableResourceInterface: client, cache: d.cache, resource: gvr.Resource}
}

type observationNamespaceable struct {
	dynamic.NamespaceableResourceInterface
	cache    *ObservationCache
	resource string
}

func (d observationNamespaceable) Namespace(ns string) dynamic.ResourceInterface {
	return observationResource{ResourceInterface: d.NamespaceableResourceInterface.Namespace(ns), cache: d.cache, resource: d.resource, namespace: ns}
}
func (d observationNamespaceable) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return d.Namespace("").List(ctx, opts)
}

type observationResource struct {
	dynamic.ResourceInterface
	cache               *ObservationCache
	resource, namespace string
}

func (d observationResource) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	items, err := d.cache.list(ctx, d.resource, d.namespace, opts)
	if err != nil {
		return nil, err
	}
	result := &unstructured.UnstructuredList{}
	for _, item := range items {
		result.Items = append(result.Items, *item.(*unstructured.Unstructured))
	}
	return result, nil
}

type observationKubernetes struct {
	kubernetes.Interface
	cache *ObservationCache
}

func (k observationKubernetes) CoreV1() typedcore.CoreV1Interface {
	return observationCore{CoreV1Interface: k.Interface.CoreV1(), cache: k.cache}
}

type observationCore struct {
	typedcore.CoreV1Interface
	cache *ObservationCache
}

func (k observationCore) Pods(ns string) typedcore.PodInterface {
	return observationPods{PodInterface: k.CoreV1Interface.Pods(ns), cache: k.cache, namespace: ns}
}
func (k observationCore) Nodes() typedcore.NodeInterface {
	return observationNodes{NodeInterface: k.CoreV1Interface.Nodes(), cache: k.cache}
}

type observationPods struct {
	typedcore.PodInterface
	cache     *ObservationCache
	namespace string
}

func (p observationPods) List(ctx context.Context, opts metav1.ListOptions) (*corev1.PodList, error) {
	items, err := p.cache.list(ctx, "pods", p.namespace, opts)
	if err != nil {
		return nil, err
	}
	result := &corev1.PodList{}
	for _, item := range items {
		result.Items = append(result.Items, *item.(*corev1.Pod))
	}
	return result, nil
}

type observationNodes struct {
	typedcore.NodeInterface
	cache *ObservationCache
}

func (n observationNodes) List(ctx context.Context, opts metav1.ListOptions) (*corev1.NodeList, error) {
	items, err := n.cache.list(ctx, "nodes", "", opts)
	if err != nil {
		return nil, err
	}
	result := &corev1.NodeList{}
	for _, item := range items {
		result.Items = append(result.Items, *item.(*corev1.Node))
	}
	return result, nil
}
