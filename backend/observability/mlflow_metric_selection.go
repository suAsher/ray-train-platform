package observability

import "sort"

type mlflowMetric struct {
	Key   string            `json:"key"`
	Value mlflowMetricValue `json:"value"`
}

// Select a bounded, deterministic summary. MLflow's response order is not a
// relevance ranking; class-specific metrics must not displace training loss.
func selectMLflowMetrics(metrics []mlflowMetric) (map[string]float64, []string) {
	values := make(map[string]float64, len(metrics))
	for _, metric := range metrics {
		if safeMetricKey(metric.Key) && metric.Value.Valid {
			values[metric.Key] = metric.Value.Value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := mlflowMetricPriority(keys[i]), mlflowMetricPriority(keys[j])
		if left != right {
			return left < right
		}
		return keys[i] < keys[j]
	})
	if len(keys) > maxMLflowMetricKeys {
		keys = keys[:maxMLflowMetricKeys]
	}
	latest := make(map[string]float64, len(keys))
	for _, key := range keys {
		latest[key] = values[key]
	}
	// Preserve the existing alphabetical order of metric-history series.
	sort.Strings(keys)
	return latest, keys
}

func mlflowMetricPriority(key string) int {
	switch key {
	case "train/loss", "train_loss", "training_loss", "loss":
		return 0
	case "train/learning_rate", "train/lr", "learning_rate", "learning-rate", "lr":
		return 1
	case "train/epoch", "epoch", "current_epoch":
		return 2
	case "mAP", "map", "mean_average_precision", "val/object/map", "val/mAP":
		return 3
	case "NDS", "nds", "val/object/nds", "val/NDS":
		return 4
	case "val/loss", "validation/loss", "val_loss":
		return 5
	default:
		return 6
	}
}
