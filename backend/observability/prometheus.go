package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type MetricPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

type MetricSeries struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Points []MetricPoint     `json:"points"`
}

type JobMetrics struct {
	Loss         *float64       `json:"loss"`
	Throughput   *float64       `json:"throughput"`
	LearningRate *float64       `json:"learningRate"`
	Epoch        *float64       `json:"epoch"`
	Series       []MetricSeries `json:"series"`
}

type PrometheusClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

var jobMetricQueries = map[string]string{
	"loss":         `platform_training_loss{platform_job_id="%s"}`,
	"throughput":   `platform_training_throughput{platform_job_id="%s"}`,
	"learningRate": `platform_learning_rate{platform_job_id="%s"}`,
	"epoch":        `platform_training_epoch{platform_job_id="%s"}`,
}

func (c *PrometheusClient) QueryJobMetrics(ctx context.Context, jobID string, window time.Duration) (JobMetrics, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return JobMetrics{}, fmt.Errorf("Prometheus URL is not configured")
	}
	if !safeLabelValue(jobID) {
		return JobMetrics{}, fmt.Errorf("job id is invalid")
	}
	if window <= 0 || window > 7*24*time.Hour {
		window = 24 * time.Hour
	}
	end := time.Now().UTC()
	start := end.Add(-window)
	metrics := JobMetrics{Series: make([]MetricSeries, 0, len(jobMetricQueries))}
	for name, expression := range jobMetricQueries {
		series, err := c.queryRange(ctx, fmt.Sprintf(expression, jobID), start, end)
		if err != nil {
			return JobMetrics{}, err
		}
		for index := range series {
			series[index].Name = name
			metrics.Series = append(metrics.Series, series[index])
		}
		latest, ok := latestMetricValue(series)
		if !ok {
			continue
		}
		switch name {
		case "loss":
			metrics.Loss = &latest
		case "throughput":
			metrics.Throughput = &latest
		case "learningRate":
			metrics.LearningRate = &latest
		case "epoch":
			metrics.Epoch = &latest
		}
	}
	return metrics, nil
}

func (c *PrometheusClient) queryRange(ctx context.Context, expression string, start, end time.Time) ([]MetricSeries, error) {
	return c.queryRangeWithStep(ctx, expression, start, end, 15*time.Second)
}

func (c *PrometheusClient) queryRangeWithStep(ctx context.Context, expression string, start, end time.Time, step time.Duration) ([]MetricSeries, error) {
	endpoint, err := url.Parse(strings.TrimRight(c.BaseURL, "/") + "/api/v1/query_range")
	if err != nil {
		return nil, fmt.Errorf("parse Prometheus URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("query", expression)
	query.Set("start", strconv.FormatInt(start.Unix(), 10))
	query.Set("end", strconv.FormatInt(end.Unix(), 10))
	if step < time.Second {
		step = 15 * time.Second
	}
	query.Set("step", strconv.FormatInt(int64(step/time.Second), 10))
	endpoint.RawQuery = query.Encode()
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create Prometheus request: %w", err)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("query Prometheus: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return nil, fmt.Errorf("Prometheus returned HTTP %d", response.StatusCode)
	}
	var payload prometheusResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxPrometheusResponseBytes)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Prometheus response: %w", err)
	}
	if payload.Status != "success" {
		return nil, fmt.Errorf("Prometheus query was not successful")
	}
	series := make([]MetricSeries, 0, len(payload.Data.Result))
	for _, result := range payload.Data.Result {
		points := make([]MetricPoint, 0, len(result.Values))
		for _, pair := range result.Values {
			if len(pair) != 2 {
				continue
			}
			timestamp, err := parsePrometheusTimestamp(pair[0])
			if err != nil {
				continue
			}
			var rawValue string
			if err := json.Unmarshal(pair[1], &rawValue); err != nil {
				continue
			}
			value, err := strconv.ParseFloat(rawValue, 64)
			if err != nil {
				continue
			}
			points = append(points, MetricPoint{Timestamp: time.Unix(int64(timestamp), int64((timestamp-float64(int64(timestamp)))*float64(time.Second))).UTC(), Value: value})
		}
		if len(points) > 0 {
			labels := make(map[string]string, len(result.Metric))
			for key, value := range result.Metric {
				labels[key] = value
			}
			series = append(series, MetricSeries{Labels: labels, Points: points})
		}
	}
	return series, nil
}

func parsePrometheusTimestamp(raw json.RawMessage) (float64, error) {
	var timestamp float64
	if err := json.Unmarshal(raw, &timestamp); err == nil {
		return timestamp, nil
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(encoded, 64)
}

func latestMetricValue(series []MetricSeries) (float64, bool) {
	var latest MetricPoint
	found := false
	for _, item := range series {
		for _, point := range item.Points {
			if !found || point.Timestamp.After(latest.Timestamp) {
				latest = point
				found = true
			}
		}
	}
	return latest.Value, found
}

type prometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Metric map[string]string   `json:"metric"`
			Values [][]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
}
