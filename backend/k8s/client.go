package k8s

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"ray-train-platform-backend/config"
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
