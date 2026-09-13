package k8s

import (
	"context"
	"fmt"
	"reflect"
	"regexp"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"ray-train-platform-backend/domain"
)

// EnsureModelServingService derives its address entirely from persisted job
// identity and live KubeRay ownership. Caller-supplied hosts are never accepted.
func (c *Client) EnsureModelServingService(ctx context.Context, job *domain.TrainingJob) (string, error) {
	if c == nil || c.dynamic == nil || c.kubernetes == nil || job == nil {
		return "", fmt.Errorf("serving Kubernetes context is unavailable")
	}
	if err := validateServingIdentity(job.KubernetesNS, job.ID, job.RayJobUID); err != nil {
		return "", err
	}
	if job.RayJobName == "" || job.RayClusterName == "" {
		return "", fmt.Errorf("serving worker is not allocated yet")
	}
	resource, err := c.dynamic.Resource(rayJobGVR).Namespace(job.KubernetesNS).Get(ctx, job.RayJobName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get serving RayJob: %w", err)
	}
	clusterName, _, _ := unstructured.NestedString(resource.Object, "status", "rayClusterName")
	if string(resource.GetUID()) != job.RayJobUID || resource.GetLabels()["ray.io/job-id"] != job.ID || clusterName != job.RayClusterName || resource.GetDeletionTimestamp() != nil {
		return "", fmt.Errorf("serving RayJob ownership or current cluster changed")
	}
	cluster, err := c.dynamic.Resource(rayClusterGVR).Namespace(job.KubernetesNS).Get(ctx, clusterName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get serving RayCluster: %w", err)
	}
	if cluster.GetDeletionTimestamp() != nil || !servingRayJobOwner(cluster.GetOwnerReferences(), job.RayJobName, job.RayJobUID) {
		return "", fmt.Errorf("serving RayCluster is not owned by the current job")
	}
	workers, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "workerGroupSpecs")
	if len(workers) != 1 {
		return "", fmt.Errorf("serving requires exactly one worker group")
	}
	worker, ok := workers[0].(map[string]any)
	if !ok {
		return "", fmt.Errorf("serving worker template is invalid")
	}
	workerJob, _, _ := unstructured.NestedString(worker, "template", "metadata", "labels", "platform_job_id")
	if workerJob != job.ID {
		return "", fmt.Errorf("serving worker belongs to another job")
	}
	selector := map[string]string{"ray.io/cluster": clusterName, "ray.io/node-type": "worker", "platform_job_id": job.ID}
	controller := true
	desired := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "serve-" + job.ID, Namespace: job.KubernetesNS, Labels: map[string]string{"app.kubernetes.io/managed-by": "ray-train-platform", "platform_job_id": job.ID}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "ray.io/v1", Kind: "RayJob", Name: job.RayJobName, UID: types.UID(job.RayJobUID), Controller: &controller}}}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: selector, Ports: []corev1.ServicePort{{Name: "http", Protocol: corev1.ProtocolTCP, Port: 8000, TargetPort: intstr.FromInt32(8000)}}}}
	services := c.kubernetes.CoreV1().Services(job.KubernetesNS)
	existing, err := services.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		existing, err = services.Create(ctx, desired, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			existing, err = services.Get(ctx, desired.Name, metav1.GetOptions{})
		}
	}
	if err != nil {
		return "", fmt.Errorf("ensure serving Service: %w", err)
	}
	if existing.Labels["platform_job_id"] != job.ID || !servingRayJobOwner(existing.OwnerReferences, job.RayJobName, job.RayJobUID) || !reflect.DeepEqual(existing.Spec.Selector, selector) || existing.Spec.Type != corev1.ServiceTypeClusterIP || existing.Spec.ExternalName != "" || len(existing.Spec.ExternalIPs) != 0 || len(existing.Spec.Ports) != 1 || existing.Spec.Ports[0].Port != 8000 || existing.Spec.Ports[0].TargetPort != intstr.FromInt32(8000) || existing.Spec.Ports[0].Protocol != corev1.ProtocolTCP {
		return "", fmt.Errorf("serving Service ownership or routing differs from the current job")
	}
	return "http://" + desired.Name + "." + job.KubernetesNS + ".svc:8000", nil
}

func validateServingIdentity(namespace, jobID, uid string) error {
	if len(validation.IsDNS1123Label(namespace)) != 0 || !regexp.MustCompile(`^job-[0-9a-f]{24}$`).MatchString(jobID) || uid == "" {
		return fmt.Errorf("serving job identity is invalid")
	}
	return nil
}

func servingRayJobOwner(owners []metav1.OwnerReference, name, uid string) bool {
	for _, owner := range owners {
		if owner.APIVersion == "ray.io/v1" && owner.Kind == "RayJob" && owner.UID == types.UID(uid) && (name == "" || owner.Name == name) && owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}

// Deletion needs the saved attempt UID even after its RayJob has terminated.
// Kubernetes UID/resourceVersion preconditions prevent replacing a raced object.
func (c *Client) DeleteModelServingService(ctx context.Context, namespace, jobID, rayJobUID string) error {
	if c == nil || c.kubernetes == nil {
		return fmt.Errorf("serving Kubernetes client is unavailable")
	}
	if err := validateServingIdentity(namespace, jobID, rayJobUID); err != nil {
		return err
	}
	services := c.kubernetes.CoreV1().Services(namespace)
	existing, err := services.Get(ctx, "serve-"+jobID, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get serving Service: %w", err)
	}
	if existing.Labels["platform_job_id"] != jobID || !servingRayJobOwner(existing.OwnerReferences, "", rayJobUID) {
		return fmt.Errorf("serving Service belongs to another job attempt")
	}
	uid, version := existing.UID, existing.ResourceVersion
	err = services.Delete(ctx, existing.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete serving Service: %w", err)
	}
	return nil
}
