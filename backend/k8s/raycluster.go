package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"ray-train-platform-backend/domain"
)

type WorkspaceRenderOptions struct {
	NodeSelector     map[string]string
	Image            string
	RayVersion       string
	ServiceAccount   string
	ImagePullSecrets []string
	IDCExistingClaim string
	IDCMountPath     string
	JupyterBasePath  string
}

func RenderDevRayCluster(workspace domain.DevWorkspace, options WorkspaceRenderOptions) (*unstructured.Unstructured, error) {
	if workspace.Name == "" || !isDNSLabel(workspace.Name) {
		return nil, fmt.Errorf("workspace name must be a lowercase DNS label")
	}
	if workspace.Namespace == "" || !isDNSLabel(workspace.Namespace) {
		return nil, fmt.Errorf("workspace namespace must be a lowercase DNS label")
	}
	if workspace.GPUCount < 1 || workspace.GPUCount > 1 {
		return nil, fmt.Errorf("dev workspace GPU count must be 1 in V1")
	}
	if err := domain.ValidatePinnedImage(options.Image); err != nil {
		return nil, fmt.Errorf("workspace image: %w", err)
	}
	rayVersion := options.RayVersion
	if rayVersion == "" {
		rayVersion = "2.35.0"
	}
	mountPath := options.IDCMountPath
	if mountPath == "" {
		mountPath = "/mnt/idc"
	}
	volumes := []any{
		map[string]any{"name": "dshm", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "16Gi"}},
	}
	if options.IDCExistingClaim != "" {
		volumes = append(volumes, map[string]any{"name": "idc-storage", "persistentVolumeClaim": map[string]any{"claimName": options.IDCExistingClaim}})
	}
	headMounts := []any{map[string]any{"name": "dshm", "mountPath": "/dev/shm"}}
	workerMounts := []any{map[string]any{"name": "dshm", "mountPath": "/dev/shm"}}
	if options.IDCExistingClaim != "" {
		headMounts = append(headMounts, map[string]any{"name": "idc-storage", "mountPath": mountPath})
		workerMounts = append(workerMounts, map[string]any{"name": "idc-storage", "mountPath": mountPath})
	}
	securityContext := map[string]any{"seccompProfile": map[string]any{"type": "RuntimeDefault"}}
	containerSecurity := map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}}
	head := map[string]any{
		"rayStartParams": map[string]any{"dashboard-host": "0.0.0.0", "num-gpus": "0"},
		"template": map[string]any{"spec": map[string]any{
			"serviceAccountName":           options.ServiceAccount,
			"automountServiceAccountToken": options.ServiceAccount != "",
			"securityContext":              securityContext,
			"containers": []any{map[string]any{
				"name": "ray-head", "image": options.Image, "imagePullPolicy": "IfNotPresent",
				"command":      []any{"/bin/sh", "-c"},
				"args":         []any{"ray start --head --dashboard-host=0.0.0.0 --num-gpus=0 & exec jupyter lab --ip=0.0.0.0 --port=8888 --no-browser --ServerApp.token='' --ServerApp.base_url='" + strings.TrimRight(options.JupyterBasePath, "/") + "/'"},
				"ports":        []any{map[string]any{"name": "jupyter", "containerPort": int64(8888)}, map[string]any{"name": "dashboard", "containerPort": int64(8265)}},
				"resources":    map[string]any{"requests": map[string]any{"cpu": "4", "memory": "16Gi"}, "limits": map[string]any{"cpu": "8", "memory": "32Gi"}},
				"volumeMounts": headMounts, "securityContext": containerSecurity,
			}},
			"volumes": volumes,
		}},
	}
	if pullSecrets := renderImagePullSecrets(options.ImagePullSecrets); len(pullSecrets) > 0 {
		head["template"].(map[string]any)["spec"].(map[string]any)["imagePullSecrets"] = pullSecrets
	}
	head["template"].(map[string]any)["spec"].(map[string]any)["nodeSelector"] = trainingNodeSelector(RenderOptions{NodeSelector: options.NodeSelector})
	if options.ServiceAccount == "" {
		delete(head["template"].(map[string]any)["spec"].(map[string]any), "serviceAccountName")
	}
	worker := map[string]any{
		"groupName": "dev-workers", "replicas": int64(1), "minReplicas": int64(1), "maxReplicas": int64(1),
		"rayStartParams": map[string]any{"num-gpus": strconv.Itoa(workspace.GPUCount)},
		"template": map[string]any{"spec": map[string]any{
			"serviceAccountName":           options.ServiceAccount,
			"automountServiceAccountToken": options.ServiceAccount != "",
			"securityContext":              securityContext,
			"containers": []any{map[string]any{
				"name": "ray-worker", "image": options.Image, "imagePullPolicy": "IfNotPresent",
				"resources":    map[string]any{"requests": map[string]any{"cpu": "4", "memory": "16Gi", "nvidia.com/gpu": strconv.Itoa(workspace.GPUCount)}, "limits": map[string]any{"cpu": "8", "memory": "32Gi", "nvidia.com/gpu": strconv.Itoa(workspace.GPUCount)}},
				"env":          []any{map[string]any{"name": "NCCL_P2P_DISABLE", "value": "1"}, map[string]any{"name": "NCCL_IB_DISABLE", "value": "1"}},
				"volumeMounts": workerMounts, "securityContext": containerSecurity,
			}},
			"volumes": volumes,
		}},
	}
	if pullSecrets := renderImagePullSecrets(options.ImagePullSecrets); len(pullSecrets) > 0 {
		worker["template"].(map[string]any)["spec"].(map[string]any)["imagePullSecrets"] = pullSecrets
	}
	worker["template"].(map[string]any)["spec"].(map[string]any)["nodeSelector"] = trainingNodeSelector(RenderOptions{NodeSelector: options.NodeSelector})
	if options.ServiceAccount == "" {
		delete(worker["template"].(map[string]any)["spec"].(map[string]any), "serviceAccountName")
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": RayAPIVersion, "kind": "RayCluster",
		"metadata": map[string]any{"name": workspace.Name, "namespace": workspace.Namespace, "labels": map[string]any{"app.kubernetes.io/managed-by": "ray-train-platform", "ray.io/dev-workspace": "true", "ray.io/workspace-id": workspace.ID, "ray.io/tenant-id": workspace.TenantID}},
		"spec":     map[string]any{"rayVersion": rayVersion, "headGroupSpec": head, "workerGroupSpecs": []any{worker}},
	}}, nil
}

func (c *Client) EnsureRayCluster(ctx context.Context, resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if c == nil || c.dynamic == nil {
		return nil, fmt.Errorf("Kubernetes dynamic client is not initialized")
	}
	if resource == nil || resource.GetNamespace() == "" || resource.GetName() == "" {
		return nil, fmt.Errorf("RayCluster namespace and name are required")
	}
	clusters := c.dynamic.Resource(rayClusterGVR).Namespace(resource.GetNamespace())
	existing, err := clusters.Get(ctx, resource.GetName(), metav1.GetOptions{})
	if err == nil {
		if err := verifyOwnershipLabel(existing, resource, "ray.io/workspace-id"); err != nil {
			return nil, err
		}
		return existing, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get RayCluster: %w", err)
	}
	if _, err := clusters.Create(ctx, resource, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("validate RayCluster: %w", err)
	}
	created, err := clusters.Create(ctx, resource, metav1.CreateOptions{})
	if err == nil {
		return created, nil
	}
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := clusters.Get(ctx, resource.GetName(), metav1.GetOptions{})
		if getErr != nil {
			return nil, getErr
		}
		if ownershipErr := verifyOwnershipLabel(existing, resource, "ray.io/workspace-id"); ownershipErr != nil {
			return nil, ownershipErr
		}
		return existing, nil
	}
	return nil, fmt.Errorf("create RayCluster: %w", err)
}

func (c *Client) GetRayCluster(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	if c == nil || c.dynamic == nil {
		return nil, fmt.Errorf("Kubernetes dynamic client is not initialized")
	}
	return c.dynamic.Resource(rayClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

func MapRayClusterState(resource *unstructured.Unstructured) string {
	if resource == nil {
		return "FAILED"
	}
	status, found, err := nestedMap(resource.Object, "status")
	if err != nil || !found {
		return "SUBMITTED"
	}
	for _, field := range []string{"state", "phase", "headPodStatus"} {
		value := strings.ToUpper(stringValue(status, field))
		switch value {
		case "READY", "RUNNING", "HEALTHY", "READY_TO_RUN":
			return "RUNNING"
		case "FAILED", "ERROR":
			return "FAILED"
		}
	}
	return "SUBMITTED"
}

func (c *Client) DeleteRayCluster(ctx context.Context, namespace, name, workspaceID string) error {
	if c == nil || c.dynamic == nil {
		return fmt.Errorf("Kubernetes dynamic client is not initialized")
	}
	clusters := c.dynamic.Resource(rayClusterGVR).Namespace(namespace)
	existing, err := clusters.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get RayCluster before delete: %w", err)
	}
	if workspaceID != "" && existing.GetLabels()["ray.io/workspace-id"] != workspaceID {
		return fmt.Errorf("refusing to delete RayCluster owned by another workspace")
	}
	propagation := metav1.DeletePropagationBackground
	if err := clusters.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete RayCluster: %w", err)
	}
	return nil
}

func (c *Client) EnsureWorkspaceService(ctx context.Context, namespace, clusterName, workspaceID string) error {
	if c == nil || c.kubernetes == nil {
		return fmt.Errorf("Kubernetes client is not initialized")
	}
	services := c.kubernetes.CoreV1().Services(namespace)
	serviceName := clusterName + "-head-svc"
	existing, err := services.Get(ctx, serviceName, metav1.GetOptions{})
	if err == nil {
		if existing.Labels["ray.io/workspace-id"] != workspaceID {
			return fmt.Errorf("workspace service is owned by another workspace")
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get workspace service: %w", err)
	}
	_, err = services.Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: serviceName, Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "ray-train-platform", "ray.io/workspace-id": workspaceID}}, Spec: corev1.ServiceSpec{Selector: map[string]string{"ray.io/cluster": clusterName, "ray.io/node-type": "head"}, Ports: []corev1.ServicePort{{Name: "jupyter", Port: 8888}, {Name: "dashboard", Port: 8265}}}}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create workspace service: %w", err)
	}
	return nil
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
