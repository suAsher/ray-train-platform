package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var mlflowDashboardExperimentIDPattern = regexp.MustCompile(`^[0-9]{1,20}$`)

// Public MLflow visibility is independent of the viewer's platform tenant.
// Resolve the exact Run instead of searching the viewer's latest 100 Runs.
func (h *Handler) sharedMLflowDashboardRunRedirect(ctx context.Context, runID string) (string, error) {
	target, err := parseMLflowNativeTarget(h.mlflowTrackingURL)
	if err != nil {
		return "", err
	}
	target.Path = strings.TrimSuffix(target.Path, "/") + "/api/2.0/mlflow/runs/get"
	target.RawQuery = url.Values{"run_id": []string{runID}}.Encode()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Transport: mlflowDashboardTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("MLflow Run lookup unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("MLflow Run was not found")
	}
	var payload struct {
		Run struct {
			Info struct {
				RunID string `json:"run_id"`
				ExperimentID string `json:"experiment_id"`
			} `json:"info"`
		} `json:"run"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&payload); err != nil || payload.Run.Info.RunID != runID || !mlflowDashboardExperimentIDPattern.MatchString(payload.Run.Info.ExperimentID) {
		return "", fmt.Errorf("MLflow Run response is invalid")
	}
	return "#/experiments/" + payload.Run.Info.ExperimentID + "/runs/" + runID, nil
}
