// Package nodeonboarding contains fail-closed validation primitives for node onboarding.
// These functions do not inspect hosts, modify Kubernetes resources, or grant readiness.
package nodeonboarding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const CacheReadyLabel = "platform.wellspiking.ai/cache-ready"
const defaultNode = "DEFAULT_PATH_FOR_NON_LISTED_NODES"

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func validNode(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) > 63 || !dnsLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// MergeNodePathMap adds a node to a single-root local-path-provisioner config.
// All existing node mappings must use root and the default mapping must deny access.
// Unknown fields are preserved. Errors return nil and never change configJSON.
func MergeNodePathMap(configJSON []byte, nodeName string, root string) ([]byte, error) {
	if !validNode(nodeName) {
		return nil, fmt.Errorf("invalid node name %q", nodeName)
	}
	if root != "/data1/ray-cache" && root != "/data2/ray-cache" {
		return nil, fmt.Errorf("unsupported cache root %q", root)
	}
	config, err := strictObject(configJSON)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(config["nodePathMap"], &entries); err != nil || entries == nil {
		return nil, fmt.Errorf("nodePathMap must be an array")
	}
	seen := make(map[string]bool)
	for i, entry := range entries {
		name, err := validateMapping(entry, root)
		if err != nil {
			return nil, fmt.Errorf("nodePathMap[%d]: %w", i, err)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate node %q", name)
		}
		seen[name] = true
	}
	if !seen[defaultNode] {
		return nil, fmt.Errorf("empty deny-default mapping required")
	}
	if seen[nodeName] {
		return bytes.Clone(configJSON), nil
	}
	entry, err := json.Marshal(map[string]any{"node": nodeName, "paths": []string{root}})
	if err != nil {
		return nil, err
	}
	updated := append(append([]json.RawMessage(nil), entries...), entry)
	encoded, err := json.Marshal(updated)
	if err != nil {
		return nil, err
	}
	config["nodePathMap"] = encoded
	return json.Marshal(config)
}

func validateMapping(raw json.RawMessage, root string) (string, error) {
	entry, err := strictObject(raw)
	if err != nil {
		return "", err
	}
	var name string
	if err := json.Unmarshal(entry["node"], &name); err != nil || (name != defaultNode && !validNode(name)) {
		return "", fmt.Errorf("invalid node")
	}
	var paths []string
	if err := json.Unmarshal(entry["paths"], &paths); err != nil || paths == nil {
		return "", fmt.Errorf("paths must be an array of strings")
	}
	if name == defaultNode {
		if len(paths) != 0 {
			return "", fmt.Errorf("default mapping must be empty")
		}
	} else if len(paths) != 1 || paths[0] != root {
		return "", fmt.Errorf("node %q must use exactly %q", name, root)
	}
	return name, nil
}

// Decode objects explicitly so duplicate security-sensitive keys cannot be hidden
// by encoding/json's usual last-key-wins behavior.
func strictObject(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("expected JSON object")
	}
	result := make(map[string]json.RawMessage)
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, err
		}
		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("expected object key")
		}
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("duplicate key %q", name)
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		result[name] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing JSON")
	}
	return result, nil
}
