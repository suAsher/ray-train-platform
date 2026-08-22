package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type LogLine struct {
	Timestamp time.Time         `json:"timestamp"`
	Line      string            `json:"line"`
	Stream    map[string]string `json:"stream,omitempty"`
}

type LokiClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

func (c *LokiClient) QueryJobLogs(ctx context.Context, jobID string, limit int) ([]LogLine, error) {
	now := time.Now().UTC()
	return c.QueryJobLogsInRange(ctx, jobID, limit, now.Add(-30*24*time.Hour), now)
}

// QueryJobLogsInRange returns job logs from an explicit lifecycle window. Callers
// should use the job's creation and completion times so completed jobs remain
// available without making Loki scan the entire retention period on every request.
func (c *LokiClient) QueryJobLogsInRange(ctx context.Context, jobID string, limit int, start, end time.Time) ([]LogLine, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("Loki URL is not configured")
	}
	if !safeLabelValue(jobID) {
		return nil, fmt.Errorf("job id is invalid")
	}
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return nil, fmt.Errorf("log query range is invalid")
	}
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	endpoint, err := url.Parse(strings.TrimRight(c.BaseURL, "/") + "/loki/api/v1/query_range")
	if err != nil {
		return nil, fmt.Errorf("parse Loki URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("query", `{platform_job_id="`+jobID+`"}`)
	query.Set("limit", strconv.Itoa(limit))
	query.Set("direction", "forward")
	query.Set("start", start.UTC().Format(time.RFC3339Nano))
	query.Set("end", end.UTC().Format(time.RFC3339Nano))
	endpoint.RawQuery = query.Encode()
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create Loki request: %w", err)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("query Loki: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return nil, fmt.Errorf("Loki returned HTTP %d", response.StatusCode)
	}
	var payload lokiResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Loki response: %w", err)
	}
	lines := make([]LogLine, 0)
	for _, stream := range payload.Data.Result {
		for _, pair := range stream.Values {
			if len(pair) != 2 {
				continue
			}
			nanoseconds, err := strconv.ParseInt(pair[0], 10, 64)
			if err != nil {
				continue
			}
			lines = append(lines, LogLine{Timestamp: time.Unix(0, nanoseconds).UTC(), Line: pair[1], Stream: stream.Stream})
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Timestamp.Before(lines[j].Timestamp) })
	return lines, nil
}

type lokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func safeLabelValue(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("-_", char) {
			continue
		}
		return false
	}
	return true
}
