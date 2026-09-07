package nodeonboarding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const EligibilitySelector = "accelerator=nvidia-rtx-4090,platform.wellspiking.ai/gpu-pool=production"
const StateAnnotation = "platform.wellspiking.ai/onboarding-state"
const ReasonAnnotation = "platform.wellspiking.ai/onboarding-reason"
const ownerLabel = "platform.wellspiking.ai/onboarding-node-uid"

type Config struct {
	Namespace, ConfigNamespace, ProbeServiceAccount string
	Data1ConfigMap, Data2ConfigMap                  string
	Data1StorageClass, Data2StorageClass            string
	Image, HelperImage, NFSConfigMap                string
	StateConfigMap                                  string
	RevalidateAfter, RetryAfter                     time.Duration
}

type Controller struct {
	client kubernetes.Interface
	config Config
	now    func() time.Time
}
type reconcileState struct {
	UID           types.UID            `json:"uid"`
	Stage         string               `json:"stage"`
	At            time.Time            `json:"at"`
	Volumes       []string             `json:"volumes,omitempty"`
	PreparePodUID types.UID            `json:"preparePodUID,omitempty"`
	ProbePodUID   types.UID            `json:"probePodUID,omitempty"`
	ClaimUIDs     []types.UID          `json:"claimUIDs,omitempty"`
	Fingerprint   string               `json:"fingerprint,omitempty"`
	Receipt       *verificationReceipt `json:"receipt,omitempty"`
}

func NewController(client kubernetes.Interface, config Config) (*Controller, error) {
	if client == nil {
		return nil, fmt.Errorf("Kubernetes client required")
	}
	for _, name := range []string{config.Namespace, config.Data1ConfigMap, config.Data2ConfigMap, config.Data1StorageClass, config.Data2StorageClass, config.NFSConfigMap, config.StateConfigMap} {
		if !validNode(name) {
			return nil, fmt.Errorf("valid namespace, config and class names required")
		}
	}
	if config.ConfigNamespace == "" {
		config.ConfigNamespace = config.Namespace
	}
	if !validNode(config.ConfigNamespace) {
		return nil, fmt.Errorf("invalid config namespace")
	}
	if config.ProbeServiceAccount == "" {
		config.ProbeServiceAccount = "node-onboarding-probe"
	}
	if !validNode(config.ProbeServiceAccount) {
		return nil, fmt.Errorf("invalid probe service account")
	}
	if config.Data1ConfigMap == config.Data2ConfigMap || config.Data1StorageClass == config.Data2StorageClass {
		return nil, fmt.Errorf("distinct disk configs and classes required")
	}
	if config.Namespace == config.ConfigNamespace && (config.StateConfigMap == config.Data1ConfigMap || config.StateConfigMap == config.Data2ConfigMap || config.StateConfigMap == config.NFSConfigMap) {
		return nil, fmt.Errorf("proof store must be separate from runtime configuration")
	}
	for _, image := range []string{config.Image, config.HelperImage} {
		parts := strings.Split(image, "@sha256:")
		if len(parts) != 2 || parts[0] == "" || len(parts[1]) != 64 {
			return nil, fmt.Errorf("immutable image digest required")
		}
		if _, err := hex.DecodeString(parts[1]); err != nil {
			return nil, fmt.Errorf("invalid image digest")
		}
	}
	if config.RevalidateAfter <= 0 {
		config.RevalidateAfter = 6 * time.Hour
	}
	if config.RetryAfter <= 0 {
		config.RetryAfter = 5 * time.Minute
	}
	return &Controller{client: client, config: config, now: time.Now}, nil
}

func eligible(node *corev1.Node) bool {
	if node.Labels["accelerator"] != "nvidia-rtx-4090" || node.Labels["platform.wellspiking.ai/gpu-pool"] != "production" || node.Labels["type"] == "virtual-kubelet" || node.Labels["virtual-kubelet.io/provider"] != "" {
		return false
	}
	gpu := node.Status.Allocatable[corev1.ResourceName("nvidia.com/gpu")]
	if gpu.Sign() <= 0 {
		return false
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// Reconcile performs a bounded step and relies on resource versions and Node UID
// checks. It never touches training resources or deletes provisioner mappings.
func (c *Controller) Reconcile(ctx context.Context, name string) (result error) {
	node, err := c.client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	state, err := c.loadState(ctx, node)
	if err != nil {
		_ = c.patchNode(ctx, node, reconcileState{UID: node.UID, Stage: "prepare", At: c.now()}, false, "protected proof store unavailable")
		return err
	}
	defer func() {
		if result != nil {
			state.Receipt = nil
			if state.Stage == "ready" {
				state = reconcileState{UID: node.UID, Stage: "prepare", At: c.now()}
			}
			message := result.Error()
			if len(message) > 400 {
				message = message[:400]
			}
			if err := c.save(ctx, node, state, false, message); err != nil {
				result = fmt.Errorf("%w; status: %v", result, err)
			}
		}
	}()
	if !eligible(node) {
		state.Receipt = nil
		state.Stage = "reset"
		state.At = c.now()
		return c.save(ctx, node, state, false, "waiting for production labels, Ready and allocatable GPU")
	}
	keepReady := false
	if validReceipt(state.Receipt, node.UID) {
		fingerprint, err := c.configurationFingerprint(ctx, node.Name)
		if err != nil {
			return err
		}
		if fingerprint != state.Receipt.Fingerprint {
			return fmt.Errorf("verified storage configuration changed")
		}
		keepReady = true
	}
	if state.Stage == "ready" && !keepReady {
		return fmt.Errorf("ready state has no complete protected receipt")
	}
	if state.Stage == "ready" && c.now().Sub(state.Receipt.At) < c.revalidateInterval(node.UID) {
		if node.Labels[CacheReadyLabel] == "true" {
			return nil
		}
		return c.save(ctx, node, state, true, "cache and NFS verified")
	}
	if state.Stage == "ready" {
		state = reconcileState{UID: node.UID, Stage: "prepare", At: c.now(), Receipt: state.Receipt}
	}
	if err := c.save(ctx, node, state, keepReady, "validating node storage"); err != nil {
		return err
	}
	switch state.Stage {
	case "reset":
		state, err = c.captureResetUIDs(ctx, node, state)
		if err != nil {
			return err
		}
		volumes, err := c.claimVolumes(ctx, node)
		if err != nil {
			return err
		}
		for _, volume := range volumes {
			found := false
			for _, saved := range state.Volumes {
				if saved == volume {
					found = true
				}
			}
			if !found {
				state.Volumes = append(state.Volumes, volume)
			}
		}
		if err := c.save(ctx, node, state, false, "reclaiming previous probes before fresh validation"); err != nil {
			return err
		}
		done, err := c.cleanup(ctx, node, state.Volumes, expectedResourceUIDs(state))
		if err != nil {
			return err
		}
		if !done {
			return nil
		}
		state = reconcileState{UID: node.UID, Stage: "prepare", At: c.now()}
		return c.save(ctx, node, state, false, "starting fresh node validation")
	case "prepare":
		pod, err := c.ensurePod(ctx, node, c.preparePod(node))
		if err != nil {
			return err
		}
		if pod.Status.Phase == corev1.PodFailed || (pod.Status.Phase != corev1.PodSucceeded && c.now().Sub(state.At) > 15*time.Minute) {
			return c.failed(ctx, node, pod, state)
		}
		if pod.Status.Phase != corev1.PodSucceeded {
			return nil
		}
		if pod.UID == "" {
			return fmt.Errorf("successful prepare Pod has no UID")
		}
		if err := c.register(ctx, node.Name); err != nil {
			return err
		}
		state.Stage = "probe"
		state.PreparePodUID = pod.UID
		state.Fingerprint, err = c.configurationFingerprint(ctx, node.Name)
		if err != nil {
			return err
		}
		state.At = c.now()
		return c.save(ctx, node, state, keepReady, "cache directories prepared and maps registered")
	case "probe":
		prepare, err := c.ensurePod(ctx, node, c.preparePod(node))
		if err != nil {
			return err
		}
		if prepare.Status.Phase != corev1.PodSucceeded || prepare.UID == "" || prepare.UID != state.PreparePodUID {
			return fmt.Errorf("prepare proof missing or replaced")
		}
		if err := c.ensureClaims(ctx, node); err != nil {
			return err
		}
		claimUIDs, err := c.claimUIDs(ctx, node)
		if err != nil {
			return err
		}
		if state.ClaimUIDs == nil {
			state.ClaimUIDs = claimUIDs
			if err := c.save(ctx, node, state, keepReady, "PVC identities recorded before probe creation"); err != nil {
				return err
			}
		} else if !sameClaimUIDs(state.ClaimUIDs, claimUIDs) {
			return fmt.Errorf("PVC UID changed after probe identities were recorded")
		}
		shares, err := c.nfsShares(ctx)
		if err != nil {
			return err
		}
		pod, err := c.ensurePod(ctx, node, c.probePod(node, shares))
		if err != nil {
			return err
		}
		if pod.Status.Phase == corev1.PodFailed || (pod.Status.Phase != corev1.PodSucceeded && c.now().Sub(state.At) > 15*time.Minute) {
			return c.failed(ctx, node, pod, state)
		}
		if pod.Status.Phase != corev1.PodSucceeded {
			return nil
		}
		if pod.UID == "" {
			return fmt.Errorf("successful probe Pod has no UID")
		}
		volumes, err := c.verifyClaims(ctx, node, state.ClaimUIDs)
		if err != nil {
			return err
		}
		state.Stage = "cleanup"
		state.ProbePodUID = pod.UID
		state.Volumes = volumes
		state.At = c.now()
		return c.save(ctx, node, state, keepReady, "disk and NFS probes passed; waiting for reclamation")
	case "cleanup":
		proof := &verificationReceipt{NodeUID: node.UID, PreparePodUID: state.PreparePodUID, ProbePodUID: state.ProbePodUID, ClaimUIDs: state.ClaimUIDs, Volumes: state.Volumes, Fingerprint: state.Fingerprint, At: state.At}
		if !validReceipt(proof, node.UID) {
			return fmt.Errorf("cleanup requires complete protected probe proof")
		}
		done, err := c.cleanup(ctx, node, state.Volumes, expectedResourceUIDs(state))
		if err != nil {
			return err
		}
		if !done {
			return nil
		}
		fingerprint, err := c.configurationFingerprint(ctx, node.Name)
		if err != nil {
			return err
		}
		if fingerprint != state.Fingerprint {
			return fmt.Errorf("configuration changed during probes")
		}
		state.Stage = "ready"
		state.At = c.now()
		proof.At = c.now()
		state.Receipt = proof
		state.Volumes = nil
		return c.save(ctx, node, state, true, "cache and NFS verified")
	default:
		return fmt.Errorf("unknown onboarding stage %q", state.Stage)
	}
}

func (c *Controller) save(ctx context.Context, node *corev1.Node, state reconcileState, ready bool, reason string) error {
	if ready && !validReceipt(state.Receipt, node.UID) {
		return fmt.Errorf("readiness requires protected verification receipt")
	}
	if err := c.storeState(ctx, state); err != nil {
		_ = c.patchNode(ctx, node, state, false, "unable to persist protected proof")
		return err
	}
	return c.patchNode(ctx, node, state, ready, reason)
}

func (c *Controller) patchNode(ctx context.Context, node *corev1.Node, state reconcileState, ready bool, reason string) error {
	latest, err := c.client.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if latest.UID != node.UID {
		return fmt.Errorf("node UID changed")
	}
	if ready && !eligible(latest) {
		return fmt.Errorf("node eligibility changed")
	}
	if latest.Annotations == nil {
		latest.Annotations = map[string]string{}
	}
	if latest.Labels == nil {
		latest.Labels = map[string]string{}
	}
	var previous reconcileState
	_ = json.Unmarshal([]byte(latest.Annotations[StateAnnotation]), &previous)
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	latest.Annotations[StateAnnotation] = string(encoded)
	latest.Annotations[ReasonAnnotation] = reason
	if ready {
		latest.Labels[CacheReadyLabel] = "true"
	} else {
		delete(latest.Labels, CacheReadyLabel)
	}
	operations := []map[string]any{{"op": "test", "path": "/metadata/uid", "value": string(node.UID)}}
	if latest.ResourceVersion != "" {
		operations = append(operations, map[string]any{"op": "test", "path": "/metadata/resourceVersion", "value": latest.ResourceVersion})
	}
	operations = append(operations, map[string]any{"op": "add", "path": "/metadata/labels", "value": latest.Labels}, map[string]any{"op": "add", "path": "/metadata/annotations", "value": latest.Annotations})
	patch, err := json.Marshal(operations)
	if err != nil {
		return err
	}
	_, err = c.client.CoreV1().Nodes().Patch(ctx, latest.Name, types.JSONPatchType, patch, metav1.PatchOptions{})
	if err == nil && previous.Stage != state.Stage {
		c.stageEvent(ctx, node, state.Stage, reason)
	}
	return err
}

func resourceName(node *corev1.Node, suffix string) string {
	sum := sha256.Sum256([]byte(node.UID))
	return "onboard-" + hex.EncodeToString(sum[:10]) + "-" + suffix
}
func owned(meta metav1.Object, node *corev1.Node) bool {
	if meta.GetLabels()[ownerLabel] != string(node.UID) {
		return false
	}
	for _, owner := range meta.GetOwnerReferences() {
		if owner.APIVersion == "v1" && owner.Kind == "Node" && owner.Name == node.Name && owner.UID == node.UID {
			return true
		}
	}
	return false
}
func objectMeta(node *corev1.Node, namespace, suffix string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: resourceName(node, suffix), Namespace: namespace, Labels: map[string]string{ownerLabel: string(node.UID)}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: node.Name, UID: node.UID}}}
}
func (c *Controller) ensurePod(ctx context.Context, node *corev1.Node, desired *corev1.Pod) (*corev1.Pod, error) {
	pod, err := c.client.CoreV1().Pods(c.config.Namespace).Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return c.client.CoreV1().Pods(c.config.Namespace).Create(ctx, desired, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, err
	}
	if !owned(pod, node) {
		return nil, fmt.Errorf("refusing unowned pod %s", pod.Name)
	}
	if pod.DeletionTimestamp != nil {
		return nil, fmt.Errorf("waiting for previous probe Pod deletion")
	}
	if err := matchPodSpec(pod, desired, node); err != nil {
		return nil, err
	}
	return pod, nil
}
func (c *Controller) failed(ctx context.Context, node *corev1.Node, pod *corev1.Pod, state reconcileState) error {
	state.Receipt = nil
	if c.now().Sub(state.At) < c.config.RetryAfter {
		return fmt.Errorf("%s failed; retry cooldown (NFS failures may require apt-get install -y nfs-common)", state.Stage)
	}
	if pod.UID == "" {
		return fmt.Errorf("failed pod has no UID")
	}
	uid := pod.UID
	if err := c.client.CoreV1().Pods(c.config.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
		return err
	}
	state.At = c.now()
	return c.save(ctx, node, state, false, "retrying failed probe after cooldown")
}
