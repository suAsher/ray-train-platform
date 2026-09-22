package assistantidle

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestObserveFailsClosedWhenRayServiceRayClusterConfigSuspendIsUnsupported(t *testing.T) {
	adapter := testAdapter(crdWithoutSuspend())

	snapshot, err := adapter.Observe(context.Background())
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got err=%v, want unsupported", err)
	}
	if snapshot.Observation.Fresh || snapshot.Observation.EligibleIdleGPU != 0 || snapshot.Observation.ServiceExists {
		t.Fatalf("unsupported observation must fail closed: %+v", snapshot)
	}
}

func TestObserveReportsTrainingDemandFromPendingAndUnreadyAdmittedWorkloads(t *testing.T) {
	adapter := testAdapter(crdWithSuspend(), workload("tenant-job", "uid-job", "", false, false))

	snapshot, err := adapter.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Observation.Fresh || !snapshot.Observation.TrainingDemand {
		t.Fatalf("pending workload was not demand: %+v", snapshot)
	}

	adapter = testAdapter(crdWithSuspend(), workload("tenant-job", "uid-job", "admission-a", false, false))
	snapshot, err = adapter.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Observation.TrainingDemand {
		t.Fatalf("admitted workload without PodsReady was not demand: %+v", snapshot)
	}
}

func TestObserveExcludesOwnedRayServiceWorkloadFromTrainingDemand(t *testing.T) {
	owned := workload("assistant", "uid-workload", "admission-a", false, true)
	markCondition(owned, "Admitted", "True")
	adapter := testAdapter(crdWithSuspend(), ownedRayService("assistant-uid", true, false), owned)

	snapshot, err := adapter.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Observation.TrainingDemand || !snapshot.Observation.ServiceExists || !snapshot.Observation.Admitted {
		t.Fatalf("owned RayService workload polluted demand/admission: %+v", snapshot)
	}
}

func TestObserveCountsEligibleIdleGPUAfterLabelsTaintsAndPodRequests(t *testing.T) {
	adapter := testAdapter(crdWithSuspend(),
		readyNode("node-a", map[string]string{"accelerator": "nvidia-rtx-4090"}, 2),
		readyNode("node-b", map[string]string{"accelerator": "nvidia-rtx-4090"}, 1),
		readyNode("node-c", map[string]string{"accelerator": "other"}, 8),
		nonReadyNode("node-d", map[string]string{"accelerator": "nvidia-rtx-4090"}, 8),
		gpuPod("tenant-a", "training", "node-a", 1, false),
		gpuPod("assistant-system", "assistant-worker", "node-a", 1, true),
	)

	snapshot, err := adapter.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Observation.EligibleIdleGPU != 1 {
		t.Fatalf("eligible idle GPU slot=%d, want 1", snapshot.Observation.EligibleIdleGPU)
	}
}

func TestObserveClampsLargeIdleGPUFleetToSingleSlot(t *testing.T) {
	adapter := testAdapter(crdWithSuspend(), readyNode("node-a", map[string]string{"accelerator": "nvidia-rtx-4090"}, 24))

	snapshot, err := adapter.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Observation.EligibleIdleGPU != 1 {
		t.Fatalf("eligible idle GPU slot=%d, want 1", snapshot.Observation.EligibleIdleGPU)
	}
}

func TestObserveTreatsUnboundGPUPodAsTrainingDemand(t *testing.T) {
	adapter := testAdapter(crdWithSuspend(), readyNode("node-a", map[string]string{"accelerator": "nvidia-rtx-4090"}, 1), gpuPod("tenant-a", "pending-gpu", "", 1, false))

	snapshot, err := adapter.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Observation.TrainingDemand {
		t.Fatalf("unbound GPU pod was not demand: %+v", snapshot)
	}
}

func TestObserveDoesNotTreatRunningRayJobAsDemand(t *testing.T) {
	adapter := testAdapter(crdWithSuspend(), rayJob("tenant-a", "train", "uid-train", "RUNNING", "Running", false))

	snapshot, err := adapter.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Observation.TrainingDemand {
		t.Fatalf("running RayJob blocked idle assistant: %+v", snapshot)
	}

	adapter = testAdapter(crdWithSuspend(), rayJob("tenant-a", "train", "uid-train", "RUNNING", "Starting", false))
	snapshot, err = adapter.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Observation.TrainingDemand {
		t.Fatalf("transitioning RayJob was not demand: %+v", snapshot)
	}
}

func TestOwnershipRequiresAssistantNamespaceAndDoesNotTrustForeignLabels(t *testing.T) {
	adapter := testAdapter(crdWithSuspend(), ownedRayService("assistant-uid", true, false), gpuPod("tenant-a", "foreign-labelled", "", 1, true))

	snapshot, err := adapter.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Observation.TrainingDemand {
		t.Fatalf("cross-namespace labelled GPU pod was incorrectly treated as own: %+v", snapshot)
	}
}

func TestPodGPUAccountingUsesLimitFallbackAndRestartableInitAsApp(t *testing.T) {
	pod := gpuPod("tenant-a", "gpu-limit-only", "node-a", 0, false)
	pod.Spec.Containers[0].Resources.Requests = nil
	pod.Spec.Containers[0].Resources.Limits = corev1.ResourceList{corev1.ResourceName("nvidia.com/gpu"): *resource.NewQuantity(1, resource.DecimalSI)}
	restart := corev1.ContainerRestartPolicyAlways
	pod.Spec.InitContainers = []corev1.Container{
		{Name: "regular-init", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceName("nvidia.com/gpu"): *resource.NewQuantity(2, resource.DecimalSI)}}},
		{Name: "sidecar-init", RestartPolicy: &restart, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceName("nvidia.com/gpu"): *resource.NewQuantity(1, resource.DecimalSI)}}},
	}
	adapter := testAdapter(crdWithSuspend(), readyNode("node-a", map[string]string{"accelerator": "nvidia-rtx-4090"}, 4), pod)

	snapshot, err := adapter.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Observation.EligibleIdleGPU != 1 {
		t.Fatalf("idle slot=%d, want 1 after accounting limit fallback and restartable init", snapshot.Observation.EligibleIdleGPU)
	}
}

func TestWorkloadAdmissionRequiresAdmittedConditionAndRayJobReadinessCanStandInForMissingPodsReady(t *testing.T) {
	pendingReserved := workload("reserved", "uid-reserved", "cluster-gpu", false, false)
	adapter := testAdapter(crdWithSuspend(), pendingReserved)
	snapshot, err := adapter.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Observation.TrainingDemand {
		t.Fatalf("reserved workload without Admitted condition was not demand: %+v", snapshot)
	}

	readyJob := rayJob("tenant-a", "train", "uid-train", "RUNNING", "Running", false)
	admitted := workload("admitted", "uid-admitted", "cluster-gpu", false, false)
	admitted.SetNamespace("tenant-a")
	controller := true
	admitted.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "ray.io/v1", Kind: "RayJob", Name: "train", UID: "uid-train", Controller: &controller}})
	markCondition(admitted, "Admitted", "True")
	adapter = testAdapter(crdWithSuspend(), readyJob, admitted)
	snapshot, err = adapter.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Observation.TrainingDemand {
		t.Fatalf("ready RayJob with missing PodsReady condition blocked idle assistant: %+v", snapshot)
	}
}

func TestResidualOwnedRayClusterBlocksRecreateAfterRayServiceDisappears(t *testing.T) {
	adapter := testAdapter(crdWithSuspend(), ownedRayCluster("cluster-uid", "assistant-uid"))

	snapshot, err := adapter.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Observation.OwnedChildrenRemaining {
		t.Fatalf("orphan RayCluster did not block recreate: %+v", snapshot)
	}
}

func TestDeleteRayServiceRequiresOwnedInstanceAndUsesForegroundUIDPrecondition(t *testing.T) {
	adapter := testAdapter(crdWithSuspend(), ownedRayService("assistant-uid", false, false))

	if err := adapter.Delete(context.Background(), "wrong"); err == nil {
		t.Fatal("delete with stale UID was accepted")
	}

	if err := adapter.Delete(context.Background(), "assistant-uid"); err != nil {
		t.Fatal(err)
	}
	deletes := adapter.dynamic.(*dynamicfake.FakeDynamicClient).Actions()
	var found bool
	for _, action := range deletes {
		deleteAction, ok := action.(k8stesting.DeleteActionImpl)
		if !ok || deleteAction.GetResource().Resource != "rayservices" {
			continue
		}
		found = true
		options := deleteAction.GetDeleteOptions()
		if options.Preconditions == nil || options.Preconditions.UID == nil || string(*options.Preconditions.UID) != "assistant-uid" {
			t.Fatalf("delete missed UID precondition: %+v", options)
		}
		if options.PropagationPolicy == nil || *options.PropagationPolicy != metav1.DeletePropagationForeground {
			t.Fatalf("delete missed foreground propagation: %+v", options)
		}
	}
	if !found {
		t.Fatal("RayService delete action was not recorded")
	}
}

func TestDeleteRayServiceRefusesForeignInstance(t *testing.T) {
	adapter := testAdapter(crdWithSuspend(), foreignRayService("foreign-uid"))

	err := adapter.Delete(context.Background(), "foreign-uid")
	if err == nil {
		t.Fatal("foreign RayService was deleted")
	}
	if !apierrors.IsForbidden(err) {
		t.Fatalf("got %v, want forbidden", err)
	}
}

func TestOwnServiceReadsOnlyFixedOwnedRayServiceWithoutCapabilityGate(t *testing.T) {
	created := metav1.NewTime(time.Now().Add(-time.Minute))
	service := ownedRayService("assistant-uid", false, false)
	service.SetCreationTimestamp(created)
	adapter := testAdapter(crdWithoutSuspend(), service)

	uid, gotCreated, err := adapter.OwnService(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if uid != "assistant-uid" || !gotCreated.Equal(created.Time) {
		t.Fatalf("own service uid=%q created=%s", uid, gotCreated)
	}
}

func testAdapter(objects ...runtime.Object) *KubeAdapter {
	listKinds := map[schema.GroupVersionResource]string{
		rayServiceGVR: "RayServiceList",
		rayJobGVR:     "RayJobList",
		rayClusterGVR: "RayClusterList",
		workloadGVR:   "WorkloadList",
		crdGVR:        "CustomResourceDefinitionList",
	}
	dynamicObjects := make([]runtime.Object, 0, len(objects))
	kubeObjects := make([]runtime.Object, 0, len(objects))
	for _, object := range objects {
		switch object.(type) {
		case *corev1.Node, *corev1.Pod:
			kubeObjects = append(kubeObjects, object)
		default:
			dynamicObjects = append(dynamicObjects, object)
		}
	}
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, dynamicObjects...)
	return NewKubeAdapter(KubeAdapterConfig{
		Dynamic:        dynamic,
		Kubernetes:     k8sfake.NewSimpleClientset(kubeObjects...),
		Namespace:      "assistant-system",
		Name:           "raytrain-assistant",
		InstanceID:     "assistant-instance",
		RequiredLabels: map[string]string{"accelerator": "nvidia-rtx-4090"},
	})
}

func crdWithSuspend() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": "rayservices.ray.io"},
		"spec": map[string]any{
			"group": "ray.io",
			"versions": []any{map[string]any{
				"name":    "v1",
				"served":  true,
				"storage": true,
				"schema": map[string]any{"openAPIV3Schema": map[string]any{
					"properties": map[string]any{"spec": map[string]any{"properties": map[string]any{"rayClusterConfig": map[string]any{"properties": map[string]any{"suspend": map[string]any{"type": "boolean"}}}}}},
				}},
			}},
		},
	}}
}

func crdWithoutSuspend() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": "rayservices.ray.io"},
		"spec": map[string]any{
			"group": "ray.io",
			"versions": []any{map[string]any{
				"name":    "v1",
				"served":  true,
				"storage": true,
				"schema": map[string]any{"openAPIV3Schema": map[string]any{
					"properties": map[string]any{"spec": map[string]any{"properties": map[string]any{}}},
				}},
			}},
		},
	}}
}

func ownedRayService(uid string, ready, deleting bool) *unstructured.Unstructured {
	rs := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ray.io/v1",
		"kind":       "RayService",
		"metadata": map[string]any{
			"name":      "raytrain-assistant",
			"namespace": "assistant-system",
			"uid":       uid,
			"labels": map[string]any{
				"app.kubernetes.io/instance":  "assistant-instance",
				"app.kubernetes.io/component": "assistant-idle",
			},
		},
		"spec": map[string]any{"suspend": false},
	}}
	if ready {
		_ = unstructured.SetNestedSlice(rs.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions")
	}
	if deleting {
		rs.SetDeletionTimestamp(&metav1.Time{})
	}
	return rs
}

func foreignRayService(uid string) *unstructured.Unstructured {
	rs := ownedRayService(uid, false, false)
	rs.SetLabels(map[string]string{"app.kubernetes.io/instance": "other"})
	return rs
}

func workload(name, uid, admission string, podsReady, owned bool) *unstructured.Unstructured {
	labels := map[string]any{}
	owners := []any{}
	if owned {
		labels["app.kubernetes.io/instance"] = "assistant-instance"
		labels["app.kubernetes.io/component"] = "assistant-idle"
		owners = append(owners, map[string]any{"apiVersion": "ray.io/v1", "kind": "RayService", "name": "raytrain-assistant", "uid": "assistant-uid", "controller": true})
	}
	w := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kueue.x-k8s.io/v1beta1",
		"kind":       "Workload",
		"metadata": map[string]any{
			"name":            name,
			"namespace":       "assistant-system",
			"uid":             uid,
			"labels":          labels,
			"ownerReferences": owners,
		},
		"status": map[string]any{},
	}}
	if admission != "" {
		_ = unstructured.SetNestedField(w.Object, admission, "status", "admission", "clusterQueue")
	}
	if podsReady {
		markCondition(w, "PodsReady", "True")
	}
	return w
}

func markCondition(obj *unstructured.Unstructured, conditionType, status string) {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	conditions = append(conditions, map[string]any{"type": conditionType, "status": status})
	_ = unstructured.SetNestedSlice(obj.Object, conditions, "status", "conditions")
}

func rayJob(namespace, name, uid, jobStatus, deploymentStatus string, owned bool) *unstructured.Unstructured {
	labels := map[string]any{"kueue.x-k8s.io/queue-name": "team-queue"}
	if owned {
		labels["app.kubernetes.io/instance"] = "assistant-instance"
		labels["app.kubernetes.io/component"] = "assistant-idle"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ray.io/v1",
		"kind":       "RayJob",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"uid":       uid,
			"labels":    labels,
		},
		"status": map[string]any{
			"jobStatus":        jobStatus,
			"deploymentStatus": deploymentStatus,
		},
	}}
}

func ownedRayCluster(uid, ownerUID string) *unstructured.Unstructured {
	controller := true
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ray.io/v1",
		"kind":       "RayCluster",
		"metadata": map[string]any{
			"name":      "raytrain-assistant-cluster",
			"namespace": "assistant-system",
			"uid":       uid,
			"labels": map[string]any{
				"app.kubernetes.io/instance":  "assistant-instance",
				"app.kubernetes.io/component": "assistant-idle",
			},
			"ownerReferences": []any{map[string]any{
				"apiVersion": "ray.io/v1",
				"kind":       "RayService",
				"name":       "raytrain-assistant",
				"uid":        ownerUID,
				"controller": controller,
			}},
		},
	}}
}

func readyNode(name string, labels map[string]string, gpus int64) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{corev1.ResourceName("nvidia.com/gpu"): *resource.NewQuantity(gpus, resource.DecimalSI)},
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func nonReadyNode(name string, labels map[string]string, gpus int64) *corev1.Node {
	node := readyNode(name, labels, gpus)
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}
	return node
}

func gpuPod(namespace, name, node string, gpus int64, owned bool) *corev1.Pod {
	labels := map[string]string{}
	if owned {
		labels["app.kubernetes.io/instance"] = "assistant-instance"
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels, UID: types.UID(name + "-uid")},
		Spec: corev1.PodSpec{
			NodeName: node,
			Containers: []corev1.Container{{
				Name: "worker",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceName("nvidia.com/gpu"): *resource.NewQuantity(gpus, resource.DecimalSI),
				}},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}
