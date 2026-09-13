package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"ray-train-platform-backend/domain"
)

func servingServiceFixture() (*Client, *domain.TrainingJob, *unstructured.Unstructured) {
	job := &domain.TrainingJob{ID: "job-0123456789abcdef01234567", KubernetesNS: "tenant-a", RayJobName: "rt-serving-1", RayJobUID: "job-uid", RayClusterName: "rt-serving-1-cluster"}
	rayJob := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "ray.io/v1", "kind": "RayJob", "metadata": map[string]any{"name": job.RayJobName, "namespace": job.KubernetesNS, "uid": job.RayJobUID, "labels": map[string]any{"ray.io/job-id": job.ID}}, "status": map[string]any{"rayClusterName": job.RayClusterName}}}
	cluster := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "ray.io/v1", "kind": "RayCluster", "metadata": map[string]any{"name": job.RayClusterName, "namespace": job.KubernetesNS}, "spec": map[string]any{"workerGroupSpecs": []any{map[string]any{"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"platform_job_id": job.ID}}}}}}}}
	cluster.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "ray.io/v1", Kind: "RayJob", Name: job.RayJobName, UID: types.UID(job.RayJobUID), Controller: ptrTrue()}})
	return NewClientFromInterfaces(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), rayJob, cluster), k8sfake.NewSimpleClientset()), job, cluster
}
func ptrTrue() *bool { value := true; return &value }

func TestServingServiceSelectsOnlyOwnedCurrentWorker(t *testing.T) {
	client, job, _ := servingServiceFixture()
	address, err := client.EnsureModelServingService(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if address != "http://serve-"+job.ID+".tenant-a.svc:8000" {
		t.Fatalf("unexpected address %s", address)
	}
	service, err := client.kubernetes.CoreV1().Services(job.KubernetesNS).Get(context.Background(), "serve-"+job.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(service.Spec.Selector) != 3 || service.Spec.Selector["platform_job_id"] != job.ID || service.Spec.Selector["ray.io/cluster"] != job.RayClusterName || service.Spec.Selector["ray.io/node-type"] != "worker" || service.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Fatalf("unsafe selector: %+v", service.Spec)
	}
	if len(service.OwnerReferences) != 1 || string(service.OwnerReferences[0].UID) != job.RayJobUID {
		t.Fatal("service missing attempt ownership")
	}
	if _, err := client.EnsureModelServingService(context.Background(), job); err != nil {
		t.Fatal(err)
	}
}
func TestServingServiceRejectsForeignAndStaleResources(t *testing.T) {
	for _, scenario := range []string{"job-uid", "cluster-name", "cluster-owner", "worker-label", "service-owner", "service-selector"} {
		t.Run(scenario, func(t *testing.T) {
			client, job, cluster := servingServiceFixture()
			switch scenario {
			case "job-uid":
				job.RayJobUID = "stale-uid"
			case "cluster-name":
				job.RayClusterName = "stale-cluster"
			case "cluster-owner":
				cluster.SetOwnerReferences([]metav1.OwnerReference{{Kind: "RayJob", UID: "foreign"}})
				_, _ = client.dynamic.Resource(rayClusterGVR).Namespace(job.KubernetesNS).Update(context.Background(), cluster, metav1.UpdateOptions{})
			case "worker-label":
				_ = unstructured.SetNestedSlice(cluster.Object, []any{map[string]any{"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"platform_job_id": "foreign"}}}}}, "spec", "workerGroupSpecs")
				_, _ = client.dynamic.Resource(rayClusterGVR).Namespace(job.KubernetesNS).Update(context.Background(), cluster, metav1.UpdateOptions{})
			case "service-owner", "service-selector":
				if _, err := client.EnsureModelServingService(context.Background(), job); err != nil {
					t.Fatal(err)
				}
				service, _ := client.kubernetes.CoreV1().Services(job.KubernetesNS).Get(context.Background(), "serve-"+job.ID, metav1.GetOptions{})
				if scenario == "service-owner" {
					service.OwnerReferences[0].UID = "foreign"
				} else {
					service.Spec.Selector["ray.io/cluster"] = "foreign"
				}
				_, _ = client.kubernetes.CoreV1().Services(job.KubernetesNS).Update(context.Background(), service, metav1.UpdateOptions{})
			}
			if _, err := client.EnsureModelServingService(context.Background(), job); err == nil {
				t.Fatal("foreign/stale routing accepted")
			}
		})
	}
}
func TestServingServiceCleanupChecksOwnerAndDeletionUID(t *testing.T) {
	client, job, _ := servingServiceFixture()
	ctx := context.Background()
	if _, err := client.EnsureModelServingService(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteModelServingService(ctx, job.KubernetesNS, job.ID, "foreign"); err == nil {
		t.Fatal("foreign deletion accepted")
	}
	service, _ := client.kubernetes.CoreV1().Services(job.KubernetesNS).Get(ctx, "serve-"+job.ID, metav1.GetOptions{})
	service.UID = "service-uid"
	_, _ = client.kubernetes.CoreV1().Services(job.KubernetesNS).Update(ctx, service, metav1.UpdateOptions{})
	if err := client.DeleteModelServingService(ctx, job.KubernetesNS, job.ID, job.RayJobUID); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteModelServingService(ctx, job.KubernetesNS, job.ID, job.RayJobUID); err != nil {
		t.Fatal(err)
	}
}
