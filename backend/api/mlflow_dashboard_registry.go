package api

import (
	"context"
	"fmt"
	"regexp"

	"ray-train-platform-backend/auth"
	mr "ray-train-platform-backend/modelregistry"
)

// The lookup is optional so older Store implementations retain the existing
// training Run access path. Only durable READY links may extend the shared view.
type mlflowDashboardRegistryLookup interface {
	GetReadyByRunID(context.Context, string) (mr.Record, error)
}

var mlflowDashboardRegistrySourcePattern = regexp.MustCompile(`^mlflow-artifacts:/([0-9]{1,20})/([0-9a-f]{32})/artifacts/checkpoint/[0-9a-f]{64}\.(safetensors|pt|pth|ckpt|bin|onnx)$`)

func (h *Handler) mlflowDashboardRegistryRedirect(ctx context.Context, p auth.Principal, runID string) (string, bool, error) {
	lookup, ok := h.modelRegistryLinks.(mlflowDashboardRegistryLookup)
	if !ok || p.Subject == "" || p.TenantID == "" || p.IntegrationID != "" {
		return "", false, nil
	}
	if _, interactive := mlflowDashboardAuthMarker(p.AuthType); !interactive {
		return "", false, nil
	}
	record, err := lookup.GetReadyByRunID(ctx, runID)
	if err != nil {
		return "", false, fmt.Errorf("MLflow Registry run lookup unavailable")
	}
	if record.State == "NOT_LINKED" {
		return "", false, nil
	}
	if record.State != "READY" || record.RunID != runID {
		return "", false, fmt.Errorf("MLflow Registry run association is invalid")
	}
	source := mlflowDashboardRegistrySourcePattern.FindStringSubmatch(record.SourceURI)
	if len(source) != 4 || source[2] != runID {
		return "", false, fmt.Errorf("MLflow Registry run source is invalid")
	}
	// Build a local MLflow fragment exclusively from validated identifiers. Never
	// return the stored artifact URL, nor trust a URL supplied by the browser.
	return "#/experiments/" + source[1] + "/runs/" + runID, true, nil
}
