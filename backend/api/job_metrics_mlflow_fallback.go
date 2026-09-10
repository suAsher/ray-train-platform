package api

import (
	"math"
	"strings"
	"time"

	"ray-train-platform-backend/observability"
)

// mergeMLflowMetricFallback adds only canonical curves which Prometheus did
// not return. Managed workers have always written rank-zero scalars to MLflow;
// this preserves the Prometheus path for live infrastructure telemetry while
// making older approved runtime images observable without rebuilding them.
func mergeMLflowMetricFallback(metrics observability.JobMetrics, experiment observability.JobExperiment) observability.JobMetrics {
	for _, source := range experiment.Series {
		name, ok := canonicalMLflowCurveName(source.Key)
		if !ok || jobMetricsHasCurve(metrics, name) {
			continue
		}
		points := mlflowMetricPoints(source.Points)
		if len(points) == 0 {
			continue
		}
		metrics.Series = append(metrics.Series, observability.MetricSeries{Name: name, Points: points})
		latest := points[len(points)-1].Value
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
	return metrics
}

func canonicalMLflowCurveName(key string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "loss", "train/loss":
		return "loss", true
	case "throughput", "samples_per_second", "train/throughput", "train/samples_per_second":
		return "throughput", true
	case "learning_rate", "lr", "train/learning_rate", "train/lr":
		return "learningRate", true
	case "epoch", "train/epoch":
		return "epoch", true
	default:
		return "", false
	}
}

func jobMetricsHasCurve(metrics observability.JobMetrics, name string) bool {
	switch name {
	case "loss":
		return metrics.Loss != nil
	case "throughput":
		return metrics.Throughput != nil
	case "learningRate":
		return metrics.LearningRate != nil
	case "epoch":
		return metrics.Epoch != nil
	default:
		return true
	}
}

func jobMetricsNeedMLflowFallback(metrics observability.JobMetrics) bool {
	return metrics.Loss == nil || metrics.Throughput == nil || metrics.LearningRate == nil || metrics.Epoch == nil
}

func mlflowMetricPoints(source []observability.MLflowMetricPoint) []observability.MetricPoint {
	points := make([]observability.MetricPoint, 0, len(source))
	for _, point := range source {
		if point.TimestampMS <= 0 || math.IsNaN(point.Value) || math.IsInf(point.Value, 0) {
			continue
		}
		points = append(points, observability.MetricPoint{
			Timestamp: time.UnixMilli(point.TimestampMS).UTC(),
			Value:     point.Value,
		})
	}
	return points
}
