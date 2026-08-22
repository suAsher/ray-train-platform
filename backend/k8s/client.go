package k8s

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/domain"
)

var (
	rayJobGVR     = schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayjobs"}
	rayClusterGVR = schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayclusters"}
)

type Client struct {
	dynamic    dynamic.Interface
	kubernetes kubernetes.Interface
	discovery  discovery.DiscoveryInterface
	restConfig *rest.Config
	kueueGVR   schema.GroupVersionResource
}

func NewClient(cfg config.Config) (*Client, error) {
	restConfig, err := kubeRESTConfig(cfg)
	if err != nil {
		return nil, err
	}
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes dynamic client: %w", err)
	}
	kubernetesClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes discovery client: %w", err)
	}
	return &Client{dynamic: dynamicClient, kubernetes: kubernetesClient, discovery: discoveryClient, restConfig: restConfig, kueueGVR: localQueueGVR}, nil
}

func kubeRESTConfig(cfg config.Config) (*rest.Config, error) {
	if strings.TrimSpace(cfg.KubeConfig) != "" {
		loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: cfg.KubeConfig}
		overrides := &clientcmd.ConfigOverrides{CurrentContext: cfg.KubeContext}
		loaded, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig: %w", err)
		}
		return loaded, nil
	}
	loaded, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("load in-cluster Kubernetes configuration: %w", err)
	}
	return loaded, nil
}

func NewClientFromInterfaces(dynamicClient dynamic.Interface, kubernetesClient kubernetes.Interface) *Client {
	return &Client{dynamic: dynamicClient, kubernetes: kubernetesClient, kueueGVR: localQueueGVR}
}

func (c *Client) EnsureRayJob(ctx context.Context, resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if resource == nil {
		return nil, fmt.Errorf("RayJob manifest must be an unstructured object")
	}
	if c == nil || c.dynamic == nil {
		return nil, fmt.Errorf("Kubernetes dynamic client is not initialized")
	}
	namespace := resource.GetNamespace()
	if namespace == "" {
		return nil, fmt.Errorf("RayJob namespace is required")
	}
	if resource.GetName() == "" {
		return nil, fmt.Errorf("RayJob name is required")
	}
	jobs := c.dynamic.Resource(rayJobGVR).Namespace(namespace)
	existing, err := jobs.Get(ctx, resource.GetName(), metav1.GetOptions{})
	if err == nil {
		if err := verifyOwnership(existing, resource); err != nil {
			return nil, err
		}
		return existing, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get RayJob %s/%s: %w", namespace, resource.GetName(), err)
	}
	// KubeRay turns the submitter template into a batch Job. A missing
	// restartPolicy is rejected by the Job API and leaves the RayJob stuck in
	// Initializing, so fail the platform submission before it reaches VKE.
	if err := validateRayJobSubmitterRestartPolicy(resource); err != nil {
		return nil, err
	}
	if _, err := jobs.Create(ctx, resource, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("validate RayJob %s/%s against Kubernetes schema: %w", namespace, resource.GetName(), err)
	}
	created, err := jobs.Create(ctx, resource, metav1.CreateOptions{})
	if err == nil {
		return created, nil
	}
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := jobs.Get(ctx, resource.GetName(), metav1.GetOptions{})
		if getErr != nil {
			return nil, fmt.Errorf("get concurrently created RayJob: %w", getErr)
		}
		if ownershipErr := verifyOwnership(existing, resource); ownershipErr != nil {
			return nil, ownershipErr
		}
		return existing, nil
	}
	return nil, fmt.Errorf("create RayJob %s/%s: %w", namespace, resource.GetName(), err)
}

// UpdateRayJobCleanupTTL changes only the terminal cleanup window on a
// platform-owned RayJob. KubeRay exposes one TTL for every terminal state, so
// jobs start with the longer failure window and are shortened only after the
// controller has observed an actual success.
func (c *Client) UpdateRayJobCleanupTTL(ctx context.Context, resource *unstructured.Unstructured, jobID string, ttlSeconds int64) (*unstructured.Unstructured, error) {
	if resource == nil {
		return nil, fmt.Errorf("RayJob resource is required")
	}
	if c == nil || c.dynamic == nil {
		return nil, fmt.Errorf("Kubernetes dynamic client is not initialized")
	}
	if ttlSeconds <= 0 {
		return nil, fmt.Errorf("RayJob cleanup TTL must be positive")
	}
	jobs := c.dynamic.Resource(rayJobGVR).Namespace(resource.GetNamespace())
	result := resource
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentResource, err := jobs.Get(ctx, resource.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			// KubeRay already removed the terminal resource, which is the desired
			// cleanup outcome; there is nothing left to tune.
			return nil
		}
		if err != nil {
			return err
		}
		if currentResource.GetLabels()["ray.io/job-id"] != jobID {
			return fmt.Errorf("resource %s/%s is owned by another job", currentResource.GetNamespace(), currentResource.GetName())
		}
		currentTTL, found, err := unstructured.NestedInt64(currentResource.Object, "spec", "ttlSecondsAfterFinished")
		if err != nil {
			return fmt.Errorf("read RayJob cleanup TTL: %w", err)
		}
		if found && currentTTL == ttlSeconds {
			result = currentResource
			return nil
		}
		updated := currentResource.DeepCopy()
		if err := unstructured.SetNestedField(updated.Object, ttlSeconds, "spec", "ttlSecondsAfterFinished"); err != nil {
			return fmt.Errorf("set RayJob cleanup TTL: %w", err)
		}
		result, err = jobs.Update(ctx, updated, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("update RayJob %s/%s cleanup TTL: %w", resource.GetNamespace(), resource.GetName(), err)
	}
	return result, nil
}

// EnsureDataMountResources creates only platform-labelled, static FSX PV/PVC
// pairs. It never adopts an existing unlabelled resource and reports false
// until Kubernetes has actually bound the claim.
func (c *Client) EnsureDataMountResources(ctx context.Context, binding domain.DataMountBinding, namespace, capacity string) (bool, error) {
	if c == nil || c.kubernetes == nil {
		return false, fmt.Errorf("Kubernetes client is not initialized")
	}
	pv, pvc, err := BuildDataMountResources(binding, namespace, capacity)
	if err != nil {
		return false, err
	}
	if err := ensureOwnedPersistentVolume(ctx, c.kubernetes.CoreV1().PersistentVolumes(), pv, binding.ID); err != nil {
		return false, err
	}
	existingPVC, err := ensureOwnedPersistentVolumeClaim(ctx, c.kubernetes.CoreV1().PersistentVolumeClaims(namespace), pvc, binding.ID)
	if err != nil {
		return false, err
	}
	return existingPVC.Status.Phase == corev1.ClaimBound, nil
}

// EnsureIDCDataMountResources creates a platform-owned static NFS PV/PVC
// pair. As with TOS adapters, it only accepts matching resources carrying the
// binding label and never modifies or deletes a pre-existing object.
func (c *Client) EnsureIDCDataMountResources(ctx context.Context, binding domain.DataMountBinding, namespace, capacity string, source IDCDataMountSource) (bool, error) {
	if c == nil || c.kubernetes == nil {
		return false, fmt.Errorf("Kubernetes client is not initialized")
	}
	pv, pvc, err := BuildIDCDataMountResources(binding, namespace, capacity, source)
	if err != nil {
		return false, err
	}
	if err := ensureOwnedIDCDataMountPV(ctx, c.kubernetes.CoreV1().PersistentVolumes(), pv, binding.ID); err != nil {
		return false, err
	}
	existingPVC, err := ensureOwnedPersistentVolumeClaim(ctx, c.kubernetes.CoreV1().PersistentVolumeClaims(namespace), pvc, binding.ID)
	if err != nil {
		return false, err
	}
	return existingPVC.Status.Phase == corev1.ClaimBound, nil
}

func ensureOwnedPersistentVolume(ctx context.Context, client corev1client.PersistentVolumeInterface, desired *corev1.PersistentVolume, bindingID string) error {
	existing, err := client.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := client.Create(ctx, desired, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("validate data mount PV %s: %w", desired.Name, err)
		}
		if _, err := client.Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create data mount PV %s: %w", desired.Name, err)
		}
		existing, err = client.Get(ctx, desired.Name, metav1.GetOptions{})
	}
	if err != nil {
		return fmt.Errorf("get data mount PV %s: %w", desired.Name, err)
	}
	if err := verifyDataMountOwnership(existing.Labels, bindingID); err != nil {
		return fmt.Errorf("refusing to use data mount PV %s: %w", desired.Name, err)
	}
	if existing.Spec.CSI == nil || desired.Spec.CSI == nil || existing.Spec.CSI.Driver != desired.Spec.CSI.Driver || !reflect.DeepEqual(existing.Spec.CSI.VolumeAttributes, desired.Spec.CSI.VolumeAttributes) {
		return fmt.Errorf("refusing to use data mount PV %s with a different FSX contract", desired.Name)
	}
	if !reflect.DeepEqual(existing.Spec.MountOptions, desired.Spec.MountOptions) {
		return fmt.Errorf("refusing to use data mount PV %s with a different governed FSX mount permission contract; recreate the owned PV/PVC after workloads stop", desired.Name)
	}
	return nil
}

func ensureOwnedIDCDataMountPV(ctx context.Context, client corev1client.PersistentVolumeInterface, desired *corev1.PersistentVolume, bindingID string) error {
	existing, err := client.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := client.Create(ctx, desired, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("validate IDC data mount PV %s: %w", desired.Name, err)
		}
		if _, err := client.Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create IDC data mount PV %s: %w", desired.Name, err)
		}
		existing, err = client.Get(ctx, desired.Name, metav1.GetOptions{})
	}
	if err != nil {
		return fmt.Errorf("get IDC data mount PV %s: %w", desired.Name, err)
	}
	if err := verifyDataMountOwnership(existing.Labels, bindingID); err != nil {
		return fmt.Errorf("refusing to use IDC data mount PV %s: %w", desired.Name, err)
	}
	if existing.Spec.NFS == nil || desired.Spec.NFS == nil || existing.Spec.NFS.Server != desired.Spec.NFS.Server || existing.Spec.NFS.Path != desired.Spec.NFS.Path || !existing.Spec.NFS.ReadOnly {
		return fmt.Errorf("refusing to use IDC data mount PV %s with a different read-only NFS contract", desired.Name)
	}
	return nil
}

func ensureOwnedPersistentVolumeClaim(ctx context.Context, client corev1client.PersistentVolumeClaimInterface, desired *corev1.PersistentVolumeClaim, bindingID string) (*corev1.PersistentVolumeClaim, error) {
	existing, err := client.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := client.Create(ctx, desired, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("validate data mount PVC %s/%s: %w", desired.Namespace, desired.Name, err)
		}
		if _, err := client.Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("create data mount PVC %s/%s: %w", desired.Namespace, desired.Name, err)
		}
		existing, err = client.Get(ctx, desired.Name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("get data mount PVC %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	if err := verifyDataMountOwnership(existing.Labels, bindingID); err != nil {
		return nil, fmt.Errorf("refusing to use data mount PVC %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	if existing.Spec.VolumeName != desired.Spec.VolumeName || !reflect.DeepEqual(existing.Spec.AccessModes, desired.Spec.AccessModes) || !sameStorageClass(existing.Spec.StorageClassName, desired.Spec.StorageClassName) {
		return nil, fmt.Errorf("refusing to use data mount PVC %s/%s bound to another volume", desired.Namespace, desired.Name)
	}
	return existing, nil
}

func sameStorageClass(actual, desired *string) bool {
	if actual == nil || desired == nil {
		return actual == nil && desired == nil
	}
	return *actual == *desired
}

func verifyDataMountOwnership(labels map[string]string, bindingID string) error {
	if labels["app.kubernetes.io/managed-by"] != "ray-train-platform" || labels[managedDataMountLabel] != bindingID {
		return fmt.Errorf("resource is not owned by this data mount binding")
	}
	return nil
}

func (c *Client) GetRayJob(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	if c == nil || c.dynamic == nil {
		return nil, fmt.Errorf("Kubernetes dynamic client is not initialized")
	}
	return c.dynamic.Resource(rayJobGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

func (c *Client) DeleteRayJob(ctx context.Context, namespace, name, jobID string) error {
	if c == nil || c.dynamic == nil {
		return fmt.Errorf("Kubernetes dynamic client is not initialized")
	}
	jobs := c.dynamic.Resource(rayJobGVR).Namespace(namespace)
	existing, err := jobs.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get RayJob before delete: %w", err)
	}
	if jobID != "" && existing.GetLabels()["ray.io/job-id"] != jobID {
		return fmt.Errorf("refusing to delete RayJob owned by another job")
	}
	propagation := metav1.DeletePropagationBackground
	if err := jobs.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete RayJob %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (c *Client) WaitForRayJobDeletion(ctx context.Context, namespace, name string) error {
	if c == nil || c.dynamic == nil {
		return fmt.Errorf("Kubernetes dynamic client is not initialized")
	}
	return wait.PollUntilContextCancel(ctx, 500_000_000, true, func(ctx context.Context) (bool, error) {
		_, err := c.GetRayJob(ctx, namespace, name)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
}

func verifyOwnership(existing, desired *unstructured.Unstructured) error {
	return verifyOwnershipLabel(existing, desired, "ray.io/job-id")
}

func verifyOwnershipLabel(existing, desired *unstructured.Unstructured, labelKey string) error {
	desiredID := desired.GetLabels()[labelKey]
	if desiredID == "" {
		return fmt.Errorf("resource manifest is missing %s ownership label", labelKey)
	}
	if existing.GetLabels()[labelKey] != desiredID {
		return fmt.Errorf("resource %s/%s is owned by another object", existing.GetNamespace(), existing.GetName())
	}
	return nil
}
