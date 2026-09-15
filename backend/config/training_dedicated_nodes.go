package config

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

// parseTrainingDedicatedNodes reads deployment-owned, exclusive tenant/node
// assignments. Missing configuration preserves shared placement. Invalid policy
// fails closed at startup without echoing deployment contents in the error.
func parseTrainingDedicatedNodes(raw string) (map[string][]string, error) {
	assignments := make(map[string][]string)
	if strings.TrimSpace(raw) == "" {
		return assignments, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, fmt.Errorf("TRAINING_DEDICATED_NODES must be a JSON object")
	}
	assignedNodes := make(map[string]bool)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("TRAINING_DEDICATED_NODES contains an invalid tenant entry")
		}
		tenantID, ok := key.(string)
		if !ok || len(k8svalidation.IsDNS1123Label(tenantID)) != 0 {
			return nil, fmt.Errorf("TRAINING_DEDICATED_NODES tenant IDs must be valid DNS labels")
		}
		if _, exists := assignments[tenantID]; exists {
			return nil, fmt.Errorf("TRAINING_DEDICATED_NODES contains a duplicate tenant entry")
		}
		var nodes []string
		if err := decoder.Decode(&nodes); err != nil || len(nodes) == 0 {
			return nil, fmt.Errorf("TRAINING_DEDICATED_NODES entries must contain nonempty node arrays")
		}
		for _, node := range nodes {
			if len(k8svalidation.IsDNS1123Subdomain(node)) != 0 {
				return nil, fmt.Errorf("TRAINING_DEDICATED_NODES node names must be valid DNS subdomains")
			}
			if assignedNodes[node] {
				return nil, fmt.Errorf("TRAINING_DEDICATED_NODES cannot assign a node more than once")
			}
			assignedNodes[node] = true
		}
		assignments[tenantID] = nodes
	}
	if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
		return nil, fmt.Errorf("TRAINING_DEDICATED_NODES must be a complete JSON object")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("TRAINING_DEDICATED_NODES must contain exactly one JSON object")
	}
	return assignments, nil
}
