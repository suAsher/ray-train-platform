package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	validation "k8s.io/apimachinery/pkg/util/validation"
	"ray-train-platform-backend/assistantidle"
	"ray-train-platform-backend/config"
)

var assistantRayServiceGVR = schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayservices"}
var errAssistantStatus = errors.New("assistant status observation unavailable")

type AssistantDeploymentStatus struct {
	Name      string `json:"name"`
	Desired   int32  `json:"desired"`
	Ready     int32  `json:"ready"`
	Available int32  `json:"available"`
}
type AssistantPodStatus struct {
	Name         string `json:"name"`
	Node         string `json:"node"`
	Role         string `json:"role"`
	Phase        string `json:"phase"`
	Ready        bool   `json:"ready"`
	GPURequested int64  `json:"gpuRequested"`
	Restarts     int32  `json:"restarts"`
}
type AssistantRayServiceStatus struct {
	Name      string `json:"name"`
	Present   bool   `json:"present"`
	Ready     bool   `json:"ready"`
	Suspended bool   `json:"suspended"`
	Status    string `json:"status"`
}
type AssistantIdleStatus struct {
	RuntimeType          string                         `json:"runtimeType"`
	InferencePod         AssistantRayServiceStatus      `json:"inferencePod"`
	Configured           bool                           `json:"configured"`
	Namespace            string                         `json:"namespace,omitempty"`
	Enabled              *bool                          `json:"enabled"`
	ObservationAvailable bool                           `json:"observationAvailable"`
	InferenceReady       bool                           `json:"inferenceReady"`
	Reason               string                         `json:"reason"`
	Deployments          []AssistantDeploymentStatus    `json:"deployments"`
	Pods                 []AssistantPodStatus           `json:"pods"`
	RayService           AssistantRayServiceStatus      `json:"rayService"`
	Controller           assistantidle.ControllerStatus `json:"controller"`
	Policy               assistantidle.StatusPolicy     `json:"policy"`
}

func EmptyAssistantStatus(namespace string) AssistantIdleStatus {
	reason := "not_configured"
	if namespace != "" {
		reason = "observation_unavailable"
	}
	return AssistantIdleStatus{Configured: namespace != "", Namespace: namespace, Reason: reason, Deployments: []AssistantDeploymentStatus{}, Pods: []AssistantPodStatus{}, RayService: AssistantRayServiceStatus{Status: "Unknown"}, Controller: assistantidle.ControllerStatus{State: assistantidle.StateUnknown, Reason: "awaiting_observation"}, Policy: assistantidle.DefaultStatusPolicy()}
}

// ObserveAssistantStatus only reads the dedicated namespace and the config
// referenced by the fixed controller Deployment, never historical ConfigMaps.
func (c *Client) ObserveAssistantStatus(ctx context.Context, namespace string) (AssistantIdleStatus, error) {
	status := EmptyAssistantStatus(namespace)
	if namespace == "" {
		return status, nil
	}
	if config.ValidateAssistantIdleNamespace(namespace) != nil || c == nil || c.kubernetes == nil || c.dynamic == nil {
		return status, errAssistantStatus
	}
	configName := ""
	for _, name := range []string{"assistant-idle-controller", "assistant-idle-reaper"} {
		deployment, err := c.kubernetes.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		item := AssistantDeploymentStatus{Name: name}
		if err != nil && !apierrors.IsNotFound(err) {
			return status, errAssistantStatus
		}
		if err == nil {
			item.Desired = 1
			if deployment.Spec.Replicas != nil {
				item.Desired = *deployment.Spec.Replicas
			}
			item.Ready = deployment.Status.ReadyReplicas
			item.Available = deployment.Status.AvailableReplicas
			if name == "assistant-idle-controller" {
				for _, v := range deployment.Spec.Template.Spec.Volumes {
					if v.Name == "config" && v.ConfigMap != nil {
						configName = v.ConfigMap.Name
					}
				}
			}
		}
		status.Deployments = append(status.Deployments, item)
	}
	if !strings.HasPrefix(configName, "assistant-idle-config-") || len(validation.IsDNS1123Subdomain(configName)) != 0 {
		return status, errAssistantStatus
	}
	cm, err := c.kubernetes.CoreV1().ConfigMaps(namespace).Get(ctx, configName, metav1.GetOptions{})
	if err != nil {
		return status, errAssistantStatus
	}
	raw := cm.Data["config.json"]
	if len(raw) == 0 || len(raw) > 65536 {
		return status, errAssistantStatus
	}
	var cfg struct {
		RuntimeType string `json:"runtimeType"`
		Enabled     *bool  `json:"enabled"`
		Render      struct {
			Name       string
			Namespace  string
			InstanceID string
		} `json:"render"`
	}
	if json.Unmarshal([]byte(raw), &cfg) != nil || cfg.Enabled == nil || cfg.Render.Namespace != namespace || cfg.Render.Name == "" || len(validation.IsDNS1123Label(cfg.Render.Name)) != 0 {
		return status, errAssistantStatus
	}
	if cfg.Render.InstanceID == "" {
		cfg.Render.InstanceID = cfg.Render.Name
	}
	if len(validation.IsDNS1123Label(cfg.Render.InstanceID)) != 0 {
		return status, errAssistantStatus
	}
	status.Enabled = cfg.Enabled
	if cfg.RuntimeType == "pod" {
		status.RuntimeType = "pod"
		status.InferencePod, err = c.assistantInferencePodStatus(ctx, namespace, cfg.Render.Name, cfg.Render.InstanceID)
		if err != nil {
			return status, errAssistantStatus
		}
	} else if cfg.RuntimeType == "" || cfg.RuntimeType == "rayservice" {
		status.RuntimeType = "rayservice"
		status.RayService.Name = cfg.Render.Name
		service, err := c.dynamic.Resource(assistantRayServiceGVR).Namespace(namespace).Get(ctx, cfg.Render.Name, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return status, errAssistantStatus
		}
		if err == nil {
			status.RayService.Present = true
			status.RayService.Suspended, _, _ = unstructured.NestedBool(service.Object, "spec", "rayClusterConfig", "suspend")
			value, _, _ := unstructured.NestedString(service.Object, "status", "serviceStatus")
			switch value {
			case "Running", "Restarting", "WaitForServeDeploymentReady", "WaitForServeDeploymentHealthy", "Failed", "Suspended", "Pending":
				status.RayService.Status = value
			}
			conditions, _, _ := unstructured.NestedSlice(service.Object, "status", "conditions")
			for _, v := range conditions {
				if condition, ok := v.(map[string]any); ok && condition["type"] == "Ready" && condition["status"] == "True" {
					status.RayService.Ready = true
				}
			}
			if status.RayService.Suspended {
				status.RayService.Ready = false
			}
			if service.GetDeletionTimestamp() != nil {
				status.RayService.Ready = false
				status.RayService.Status = "Deleting"
			}
		}
	} else {
		return status, errAssistantStatus
	}
	pods, err := c.kubernetes.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/instance=" + cfg.Render.InstanceID, Limit: 33})
	if err != nil || pods.Continue != "" || len(pods.Items) > 32 {
		return status, errAssistantStatus
	}
	for _, pod := range pods.Items {
		role := assistantPodRole(pod)
		if role == "" {
			continue
		}
		item := AssistantPodStatus{Name: pod.Name, Node: pod.Spec.NodeName, Role: role, Phase: "Unknown", GPURequested: assistantidle.PodGPURequested(pod)}
		switch pod.Status.Phase {
		case corev1.PodPending, corev1.PodRunning, corev1.PodSucceeded, corev1.PodFailed:
			item.Phase = string(pod.Status.Phase)
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				item.Ready = true
			}
		}
		if pod.DeletionTimestamp != nil {
			item.Ready = false
		}
		for _, container := range pod.Status.ContainerStatuses {
			item.Restarts += container.RestartCount
		}
		for _, container := range pod.Status.InitContainerStatuses {
			item.Restarts += container.RestartCount
		}
		status.Pods = append(status.Pods, item)
	}
	sort.Slice(status.Pods, func(i, j int) bool { return status.Pods[i].Name < status.Pods[j].Name })
	return status, nil
}
func assistantPodRole(pod corev1.Pod) string {
	switch pod.Labels["app.kubernetes.io/name"] {
	case "assistant-idle-controller":
		return "controller"
	case "assistant-idle-reaper":
		return "reaper"
	}
	if pod.Labels["app.kubernetes.io/component"] != "assistant-idle" {
		return ""
	}
	switch pod.Labels["raytrain.wellspiking.ai/assistant-role"] {
	case "head":
		return "head"
	case "worker":
		return "worker"
	case "inference":
		return "inference"
	}
	return ""
}

func (c *Client) assistantInferencePodStatus(ctx context.Context, namespace, name, instance string) (AssistantRayServiceStatus, error) {
	result := AssistantRayServiceStatus{Name: name, Status: "Absent"}
	pod, err := c.kubernetes.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return result, nil
	}
	if err != nil {
		return result, errAssistantStatus
	}
	if pod.Labels["app.kubernetes.io/instance"] != instance || assistantPodRole(*pod) != "inference" {
		return result, errAssistantStatus
	}
	result.Present = true
	result.Suspended = len(pod.Spec.SchedulingGates) != 0
	switch pod.Status.Phase {
	case corev1.PodPending, corev1.PodRunning, corev1.PodSucceeded, corev1.PodFailed:
		result.Status = string(pod.Status.Phase)
	default:
		result.Status = "Unknown"
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue && pod.Status.Phase == corev1.PodRunning && !result.Suspended && assistantidle.PodGPURequested(*pod) == 1 {
			result.Ready = true
		}
	}
	if pod.DeletionTimestamp != nil {
		result.Ready = false
		result.Status = "Deleting"
	}
	return result, nil
}
