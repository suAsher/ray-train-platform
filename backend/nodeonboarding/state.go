package nodeonboarding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type verificationReceipt struct {
	NodeUID       types.UID   `json:"nodeUID"`
	PreparePodUID types.UID   `json:"preparePodUID"`
	ProbePodUID   types.UID   `json:"probePodUID"`
	ClaimUIDs     []types.UID `json:"claimUIDs"`
	Volumes       []string    `json:"volumes"`
	Fingerprint   string      `json:"fingerprint"`
	At            time.Time   `json:"at"`
}

func (c *Controller) revalidateInterval(uid types.UID) time.Duration {
	digest := sha256.Sum256([]byte(uid))
	window := c.config.RevalidateAfter / 10
	if window > 30*time.Minute {
		window = 30 * time.Minute
	}
	fraction := uint16(digest[0])<<8 | uint16(digest[1])
	return c.config.RevalidateAfter + time.Duration(int64(window)*int64(fraction)/65535)
}

func validReceipt(receipt *verificationReceipt, uid types.UID) bool {
	return receipt != nil && receipt.NodeUID == uid && receipt.PreparePodUID != "" && receipt.ProbePodUID != "" && receipt.PreparePodUID != receipt.ProbePodUID && len(receipt.ClaimUIDs) == 2 && receipt.ClaimUIDs[0] != "" && receipt.ClaimUIDs[1] != "" && receipt.ClaimUIDs[0] != receipt.ClaimUIDs[1] && len(receipt.Volumes) == 2 && receipt.Volumes[0] != "" && receipt.Volumes[1] != "" && receipt.Volumes[0] != receipt.Volumes[1] && len(receipt.Fingerprint) == 64 && !receipt.At.IsZero()
}

func (c *Controller) loadState(ctx context.Context, node *corev1.Node) (reconcileState, error) {
	_, states, err := c.stateStore(ctx)
	if err != nil {
		return reconcileState{}, err
	}
	state, ok := states[string(node.UID)]
	if !ok {
		return reconcileState{UID: node.UID, Stage: "prepare", At: c.now()}, nil
	}
	if state.UID != node.UID {
		return reconcileState{}, fmt.Errorf("proof store node UID mismatch")
	}
	return state, nil
}
func (c *Controller) stateStore(ctx context.Context) (*corev1.ConfigMap, map[string]reconcileState, error) {
	cm, err := c.client.CoreV1().ConfigMaps(c.config.Namespace).Get(ctx, c.config.StateConfigMap, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("protected proof store: %w", err)
	}
	states := map[string]reconcileState{}
	if raw := cm.Data["state.json"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &states); err != nil || states == nil {
			return nil, nil, fmt.Errorf("invalid protected proof store")
		}
	}
	return cm, states, nil
}
func (c *Controller) storeState(ctx context.Context, state reconcileState) error {
	cm, states, err := c.stateStore(ctx)
	if err != nil {
		return err
	}
	if len(states) >= 256 {
		if _, exists := states[string(state.UID)]; !exists {
			return fmt.Errorf("proof store capacity reached (256 nodes)")
		}
	}
	states[string(state.UID)] = state
	encoded, err := json.Marshal(states)
	if err != nil {
		return err
	}
	next := cm.DeepCopy()
	if next.Data == nil {
		next.Data = map[string]string{}
	}
	next.Data["state.json"] = string(encoded)
	if cm.Data["state.json"] == string(encoded) {
		return nil
	}
	_, err = c.client.CoreV1().ConfigMaps(c.config.Namespace).Update(ctx, next, metav1.UpdateOptions{})
	return err
}

// The full maps are validated on every call, while the fingerprint deliberately
// excludes other nodes' entries so enrolling a new node doesn't revoke healthy peers.
func (c *Controller) configurationFingerprint(ctx context.Context, node string) (string, error) {
	if err := c.checkRegistered(ctx, node); err != nil {
		return "", err
	}
	shares, err := c.nfsShares(ctx)
	if err != nil {
		return "", err
	}
	configs, err := c.runtimeFingerprintInputs(ctx, node)
	if err != nil {
		return "", err
	}
	policy := struct {
		Node, Data1Class, Data2Class, Image, HelperImage string
		Shares                                           []nfsShare
		Configs                                          []map[string]any
	}{node, c.config.Data1StorageClass, c.config.Data2StorageClass, c.config.Image, c.config.HelperImage, shares, configs}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (c *Controller) runtimeFingerprintInputs(ctx context.Context, node string) ([]map[string]any, error) {
	configs := make([]map[string]any, 0, 2)
	for i, name := range []string{c.config.Data1ConfigMap, c.config.Data2ConfigMap} {
		cm, err := c.client.CoreV1().ConfigMaps(c.config.ConfigNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		updated, err := MergeNodePathMap([]byte(cm.Data["config.json"]), node, fmt.Sprintf("/data%d/ray-cache", i+1))
		if err != nil || string(updated) != cm.Data["config.json"] {
			return nil, fmt.Errorf("registration changed while fingerprinting")
		}
		config, err := strictObject(updated)
		if err != nil {
			return nil, err
		}
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(config["nodePathMap"], &entries); err != nil {
			return nil, err
		}
		selected := map[string]map[string]json.RawMessage{}
		for _, entry := range entries {
			var name string
			if err := json.Unmarshal(entry["node"], &name); err != nil {
				return nil, err
			}
			if name == node || name == defaultNode {
				selected[name] = entry
			}
		}
		mapping, err := json.Marshal(selected)
		if err != nil {
			return nil, err
		}
		config["nodePathMap"] = mapping
		other := map[string]string{}
		for key, value := range cm.Data {
			if key != "config.json" {
				other[key] = value
			}
		}
		className := []string{c.config.Data1StorageClass, c.config.Data2StorageClass}[i]
		class, err := c.client.StorageV1().StorageClasses().Get(ctx, className, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		classPolicy := map[string]any{"provisioner": class.Provisioner, "parameters": class.Parameters, "reclaimPolicy": class.ReclaimPolicy, "bindingMode": class.VolumeBindingMode, "mountOptions": class.MountOptions, "topologies": class.AllowedTopologies}
		configs = append(configs, map[string]any{"uid": cm.UID, "config": config, "other": other, "class": classPolicy})
	}
	return configs, nil
}
