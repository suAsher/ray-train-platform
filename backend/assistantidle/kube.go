package assistantidle

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

var (
	ErrUnsupported = errors.New("assistant idle RayService rayClusterConfig suspend is unsupported")
	ErrForeign     = errors.New("assistant idle resource is owned by another instance")
)

var (
	crdGVR        = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	rayServiceGVR = schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayservices"}
	rayJobGVR     = schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayjobs"}
	rayClusterGVR = schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayclusters"}
	workloadGVR   = schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta1", Resource: "workloads"}
)

const (
	instanceLabel  = "app.kubernetes.io/instance"
	componentLabel = "app.kubernetes.io/component"
	componentValue = "assistant-idle"
)

type KubeAdapterConfig struct {
	Dynamic           dynamic.Interface
	Kubernetes        kubernetes.Interface
	Namespace         string
	Name              string
	InstanceID        string
	Render            RenderConfig
	NodeAllowlist     []string
	RequiredLabels    map[string]string
	ToleratedTaintKey []string
}

type KubeBackend struct {
	dynamic           dynamic.Interface
	kubernetes        kubernetes.Interface
	namespace         string
	name              string
	instanceID        string
	render            RenderConfig
	nodeAllowlist     map[string]bool
	requiredLabels    map[string]string
	toleratedTaintKey map[string]bool
}

type KubeAdapter = KubeBackend

func NewKubeBackend(config KubeAdapterConfig) *KubeBackend {
	allowlist := make(map[string]bool, len(config.NodeAllowlist))
	for _, name := range config.NodeAllowlist {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			allowlist[trimmed] = true
		}
	}
	taintKeys := config.ToleratedTaintKey
	if len(taintKeys) == 0 {
		taintKeys = config.Render.withDefaults().ToleratedTaintKeys
	}
	tolerated := make(map[string]bool, len(taintKeys))
	for _, key := range taintKeys {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			tolerated[trimmed] = true
		}
	}
	labels := make(map[string]string, len(config.RequiredLabels))
	for key, value := range config.RequiredLabels {
		labels[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return &KubeBackend{
		dynamic:           config.Dynamic,
		kubernetes:        config.Kubernetes,
		namespace:         strings.TrimSpace(config.Namespace),
		name:              strings.TrimSpace(config.Name),
		instanceID:        strings.TrimSpace(config.InstanceID),
		render:            config.Render,
		nodeAllowlist:     allowlist,
		requiredLabels:    labels,
		toleratedTaintKey: tolerated,
	}
}

func NewKubeAdapter(config KubeAdapterConfig) *KubeAdapter {
	return NewKubeBackend(config)
}

func (b *KubeBackend) Observe(ctx context.Context) (Snapshot, error) {
	if err := b.validate(); err != nil {
		return Snapshot{}, err
	}
	if err := b.requireRayServiceSuspend(ctx); err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Observation: Observation{Fresh: true, Enabled: true}}
	service, serviceExists, err := b.ownedRayService(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	var serviceUID string
	if serviceExists {
		serviceUID = string(service.GetUID())
		snapshot.UID = serviceUID
		snapshot.CreatedAt = service.GetCreationTimestamp().Time
		snapshot.Observation.ServiceExists = true
		snapshot.Observation.ServiceDeleting = service.GetDeletionTimestamp() != nil
		snapshot.Observation.ServiceReady = conditionTrue(service.Object, "Ready")
	}

	rayJobDemand, readyRayJobs, err := b.observeRayJobs(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	workloadDemand, ownedWorkloadRemaining, admitted, podsReady, err := b.observeWorkloads(ctx, serviceUID, readyRayJobs)
	if err != nil {
		return Snapshot{}, err
	}
	clusterRemaining, err := b.observeRayClusters(ctx, serviceExists, serviceUID)
	if err != nil {
		return Snapshot{}, err
	}
	idleGPU, podDemand, ownedPodRemaining, err := b.observeNodesAndPods(ctx, serviceExists)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Observation.TrainingDemand = workloadDemand || rayJobDemand || podDemand
	snapshot.Observation.OtherPending = workloadDemand || rayJobDemand || podDemand
	snapshot.Observation.EligibleIdleGPU = idleGPU
	snapshot.Observation.Admitted = serviceExists && admitted
	snapshot.Observation.ServiceReady = snapshot.Observation.ServiceReady && podsReady
	if !serviceExists && (ownedWorkloadRemaining || ownedPodRemaining || clusterRemaining) {
		snapshot.Observation.OwnedChildrenRemaining = true
	}
	return snapshot, nil
}

func (b *KubeBackend) Create(ctx context.Context) error {
	if err := b.validate(); err != nil {
		return err
	}
	if err := b.requireRayServiceSuspend(ctx); err != nil {
		return err
	}
	render := b.render
	render.Namespace = b.namespace
	render.Name = b.name
	render.InstanceID = b.instanceID
	if len(render.AllowedWorkerNodes) == 0 {
		render.AllowedWorkerNodes = sortedKeys(b.nodeAllowlist)
	}
	if len(render.RequiredNodeLabels) == 0 {
		render.RequiredNodeLabels = copyStringMap(b.requiredLabels)
	}
	resource, err := RenderRayService(render)
	if err != nil {
		return err
	}
	if resource.GetNamespace() != b.namespace || resource.GetName() != b.name || !b.ownedLabels(resource.GetLabels()) {
		return ErrForeign
	}
	_, err = b.dynamic.Resource(rayServiceGVR).Namespace(b.namespace).Create(ctx, resource, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, _, getErr := b.ownedRayService(ctx)
		return getErr
	}
	return err
}

func (b *KubeBackend) OwnService(ctx context.Context) (string, time.Time, error) {
	if err := b.validate(); err != nil {
		return "", time.Time{}, err
	}
	resource, exists, err := b.ownedRayService(ctx)
	if err != nil || !exists {
		return "", time.Time{}, err
	}
	return string(resource.GetUID()), resource.GetCreationTimestamp().Time, nil
}

func (b *KubeBackend) Delete(ctx context.Context, uid string) error {
	if err := b.validate(); err != nil {
		return err
	}
	resource, exists, err := b.ownedRayService(ctx)
	if err != nil || !exists {
		return err
	}
	if uid == "" || string(resource.GetUID()) != uid {
		return apierrors.NewConflict(schema.GroupResource{Group: "ray.io", Resource: "rayservices"}, b.name, fmt.Errorf("stale RayService UID"))
	}
	expected := types.UID(uid)
	propagation := metav1.DeletePropagationForeground
	return b.dynamic.Resource(rayServiceGVR).Namespace(b.namespace).Delete(ctx, b.name, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
		Preconditions:     &metav1.Preconditions{UID: &expected},
	})
}

func (b *KubeBackend) validate() error {
	if b == nil || b.dynamic == nil || b.kubernetes == nil {
		return fmt.Errorf("assistant idle Kubernetes clients are required")
	}
	if b.namespace == "" || b.name == "" || b.instanceID == "" {
		return fmt.Errorf("assistant idle namespace, name, and instance ID are required")
	}
	return nil
}

func (b *KubeBackend) requireRayServiceSuspend(ctx context.Context) error {
	crd, err := b.dynamic.Resource(crdGVR).Get(ctx, "rayservices.ray.io", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("%w: inspect RayService CRD: %v", ErrUnsupported, err)
	}
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	for _, item := range versions {
		version, ok := item.(map[string]any)
		if !ok || fmt.Sprint(version["name"]) != "v1" {
			continue
		}
		suspend, found, _ := unstructured.NestedMap(version, "schema", "openAPIV3Schema", "properties", "spec", "properties", "rayClusterConfig", "properties", "suspend")
		if found && fmt.Sprint(suspend["type"]) == "boolean" {
			return nil
		}
	}
	return ErrUnsupported
}

func (b *KubeBackend) ownedRayService(ctx context.Context) (*unstructured.Unstructured, bool, error) {
	resource, err := b.dynamic.Resource(rayServiceGVR).Namespace(b.namespace).Get(ctx, b.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !b.ownedLabels(resource.GetLabels()) {
		return nil, true, apierrors.NewForbidden(schema.GroupResource{Group: "ray.io", Resource: "rayservices"}, b.name, ErrForeign)
	}
	return resource, true, nil
}

func (b *KubeBackend) observeWorkloads(ctx context.Context, serviceUID string, readyRayJobs map[string]bool) (demand, ownedRemaining, admitted, podsReady bool, err error) {
	list, err := b.dynamic.Resource(workloadGVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, false, false, false, err
	}
	podsReady = true
	for i := range list.Items {
		item := &list.Items[i]
		if workloadFinished(item.Object) {
			continue
		}
		owned := b.ownedObject(item, serviceUID)
		if owned {
			ownedRemaining = true
			if serviceUID != "" && b.ownedByRayServiceUID(item, serviceUID) && workloadAdmitted(item.Object) {
				admitted = true
				if status, found := conditionStatus(item.Object, "PodsReady"); found && status != "True" {
					podsReady = false
				}
			}
			continue
		}
		if !workloadAdmitted(item.Object) {
			demand = true
			continue
		}
		if !conditionTrue(item.Object, "PodsReady") && !workloadHasReadyRayJob(item, readyRayJobs) {
			demand = true
		}
	}
	return demand, ownedRemaining, admitted, podsReady, nil
}

func (b *KubeBackend) observeRayJobs(ctx context.Context) (bool, map[string]bool, error) {
	list, err := b.dynamic.Resource(rayJobGVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	ready := map[string]bool{}
	demand := false
	for i := range list.Items {
		item := &list.Items[i]
		if b.ownedObject(item, "") || rayJobTerminal(item.Object) {
			continue
		}
		isReady := rayJobRunningReady(item.Object)
		ready[ownerKey(item.GetNamespace(), item.GetName(), string(item.GetUID()))] = isReady
		if item.GetLabels()["kueue.x-k8s.io/queue-name"] != "" && !isReady {
			demand = true
		}
	}
	return demand, ready, nil
}

func (b *KubeBackend) observeRayClusters(ctx context.Context, serviceExists bool, serviceUID string) (bool, error) {
	if serviceExists {
		return false, nil
	}
	list, err := b.dynamic.Resource(rayClusterGVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for i := range list.Items {
		item := &list.Items[i]
		if b.ownedObject(item, serviceUID) {
			return true, nil
		}
	}
	return false, nil
}

func (b *KubeBackend) observeNodesAndPods(ctx context.Context, serviceExists bool) (int, bool, bool, error) {
	nodes, err := b.kubernetes.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, false, false, err
	}
	pods, err := b.kubernetes.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, false, false, err
	}
	capacity := map[string]int64{}
	for _, node := range nodes.Items {
		if b.nodeEligible(node) {
			capacity[node.Name] = gpuQuantity(node.Status.Allocatable)
		}
	}
	used := map[string]int64{}
	var demand bool
	var ownedRemaining bool
	for _, pod := range pods.Items {
		if terminalPod(pod) {
			continue
		}
		gpus := podGPURequests(pod)
		if gpus == 0 {
			continue
		}
		owned := b.ownedPod(pod)
		if owned {
			ownedRemaining = true
		}
		if !owned && pod.Status.Phase == corev1.PodPending {
			demand = true
		}
		if pod.Spec.NodeName == "" {
			continue
		}
		used[pod.Spec.NodeName] += gpus
	}
	var idle int64
	for node, total := range capacity {
		available := total - used[node]
		if available > 0 {
			idle += available
		}
	}
	if serviceExists {
		ownedRemaining = false
	}
	if idle > 0 {
		return 1, demand, ownedRemaining, nil
	}
	return 0, demand, ownedRemaining, nil
}

func (b *KubeBackend) nodeEligible(node corev1.Node) bool {
	if len(b.nodeAllowlist) > 0 && !b.nodeAllowlist[node.Name] {
		return false
	}
	if node.Spec.Unschedulable || !nodeReady(node) || hasUntoleratedTaint(node, b.toleratedTaintKey) || node.Labels[dedicatedTenantKey] != "" {
		return false
	}
	for key, value := range b.requiredLabels {
		if node.Labels[key] != value {
			return false
		}
	}
	return gpuQuantity(node.Status.Allocatable) > 0
}

func (b *KubeBackend) ownedObject(obj metav1.Object, serviceUID string) bool {
	if obj.GetNamespace() != b.namespace {
		return false
	}
	if b.ownedLabels(obj.GetLabels()) {
		return true
	}
	return b.ownedByRayServiceUID(obj, serviceUID)
}

func (b *KubeBackend) ownedByRayServiceUID(obj metav1.Object, serviceUID string) bool {
	if serviceUID == "" {
		return false
	}
	for _, owner := range obj.GetOwnerReferences() {
		if owner.APIVersion == "ray.io/v1" && owner.Kind == "RayService" && owner.Name == b.name && string(owner.UID) == serviceUID && owner.UID != "" && owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}

func (b *KubeBackend) ownedPod(pod corev1.Pod) bool {
	return pod.Namespace == b.namespace && b.ownedLabels(pod.Labels)
}

func (b *KubeBackend) ownedLabels(labels map[string]string) bool {
	return labels[instanceLabel] == b.instanceID && labels[componentLabel] == componentValue
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func copyStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func conditionTrue(obj map[string]any, conditionType string) bool {
	status, found := conditionStatus(obj, conditionType)
	return found && status == "True"
}

func conditionStatus(obj map[string]any, conditionType string) (string, bool) {
	conditions, _, _ := unstructured.NestedSlice(obj, "status", "conditions")
	for _, item := range conditions {
		condition, ok := item.(map[string]any)
		if ok && fmt.Sprint(condition["type"]) == conditionType {
			return fmt.Sprint(condition["status"]), true
		}
	}
	return "", false
}

func workloadFinished(obj map[string]any) bool {
	return conditionTrue(obj, "Finished")
}

func workloadAdmitted(obj map[string]any) bool {
	admission, found, _ := unstructured.NestedMap(obj, "status", "admission")
	return found && len(admission) > 0 && conditionTrue(obj, "Admitted")
}

func rayJobTerminal(obj map[string]any) bool {
	status := strings.ToUpper(strings.TrimSpace(stringValue(obj, "status", "jobStatus")))
	switch status {
	case "SUCCEEDED", "SUCCESS", "COMPLETED", "FAILED", "ERROR", "STOPPED", "CANCELED", "CANCELLED":
		return true
	default:
		return false
	}
}

func rayJobRunningReady(obj map[string]any) bool {
	jobStatus := strings.ToUpper(strings.TrimSpace(stringValue(obj, "status", "jobStatus")))
	deploymentStatus := strings.ToUpper(strings.TrimSpace(stringValue(obj, "status", "deploymentStatus")))
	readyPods, _, _ := unstructured.NestedInt64(obj, "status", "readyPods")
	return jobStatus == "RUNNING" && (deploymentStatus == "RUNNING" || readyPods > 0)
}

func workloadHasReadyRayJob(workload *unstructured.Unstructured, readyRayJobs map[string]bool) bool {
	for _, owner := range workload.GetOwnerReferences() {
		if owner.APIVersion == "ray.io/v1" && owner.Kind == "RayJob" && owner.UID != "" && owner.Controller != nil && *owner.Controller && readyRayJobs[ownerKey(workload.GetNamespace(), owner.Name, string(owner.UID))] {
			return true
		}
	}
	return false
}

func ownerKey(namespace, name, uid string) string {
	return namespace + "/" + name + "/" + uid
}

func stringValue(obj map[string]any, fields ...string) string {
	value, _, _ := unstructured.NestedString(obj, fields...)
	return value
}

func terminalPod(pod corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

func podGPURequests(pod corev1.Pod) int64 {
	var sidecars int64
	var initPeak int64
	for _, container := range pod.Spec.InitContainers {
		gpus := containerGPU(container)
		if container.RestartPolicy != nil && *container.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			sidecars += gpus
			continue
		}
		if peak := sidecars + gpus; peak > initPeak {
			initPeak = peak
		}
	}
	steady := sidecars
	for _, container := range pod.Spec.Containers {
		steady += containerGPU(container)
	}
	if steady > initPeak {
		return steady
	}
	return initPeak
}

func containerGPU(container corev1.Container) int64 {
	if gpus := gpuQuantity(container.Resources.Requests); gpus > 0 {
		return gpus
	}
	return gpuQuantity(container.Resources.Limits)
}

func gpuQuantity(list corev1.ResourceList) int64 {
	quantity, ok := list[corev1.ResourceName("nvidia.com/gpu")]
	if !ok {
		return 0
	}
	return quantity.Value()
}

func nodeReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func hasUntoleratedTaint(node corev1.Node, tolerated map[string]bool) bool {
	for _, taint := range node.Spec.Taints {
		if taint.Effect == corev1.TaintEffectNoSchedule || taint.Effect == corev1.TaintEffectNoExecute {
			if !tolerated[taint.Key] {
				return true
			}
		}
	}
	return false
}
