package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
)

var (
	ErrMLflowRunNotFound   = errors.New("MLflow run was not found")
	ErrMLflowRunNotRunning = errors.New("MLflow run is not running")
	ErrMLflowBatchInvalid  = errors.New("MLflow batch is invalid")
	ErrMLflowBatchConflict = errors.New("MLflow rejected the batch")
)

type MLflowKeyValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (pair *MLflowKeyValue) UnmarshalJSON(data []byte) error {
	var raw struct {
		Key   *string `json:"key"`
		Value *string `json:"value"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if raw.Key == nil || raw.Value == nil {
		return ErrMLflowBatchInvalid
	}
	*pair = MLflowKeyValue{Key: *raw.Key, Value: *raw.Value}
	return nil
}

// Pointer fields distinguish explicit zeroes from missing or null values.
type MLflowLogMetric struct {
	Key       string   `json:"key"`
	Value     *float64 `json:"value"`
	Timestamp *int64   `json:"timestamp"`
	Step      *int64   `json:"step"`
}

type MLflowLogBatch struct {
	Metrics []MLflowLogMetric `json:"metrics"`
	Params  []MLflowKeyValue  `json:"params"`
	Tags    []MLflowKeyValue  `json:"tags"`
}

func (batch MLflowLogBatch) Validate() error {
	if len(batch.Metrics)+len(batch.Params)+len(batch.Tags) == 0 || len(batch.Metrics) > 100 || len(batch.Params) > 100 || len(batch.Tags) > 100 {
		return ErrMLflowBatchInvalid
	}
	for _, metric := range batch.Metrics {
		if !validMLflowIntegrationKey(metric.Key) || metric.Value == nil || metric.Timestamp == nil || metric.Step == nil {
			return ErrMLflowBatchInvalid
		}
		if math.IsNaN(*metric.Value) || math.IsInf(*metric.Value, 0) || *metric.Step < 0 || *metric.Timestamp < 0 || *metric.Timestamp > 253402300799999 {
			return ErrMLflowBatchInvalid
		}
	}
	if !validMLflowIntegrationPairs(batch.Params, 1024) || !validMLflowIntegrationPairs(batch.Tags, 5000) {
		return ErrMLflowBatchInvalid
	}
	return nil
}

func validMLflowIntegrationPairs(pairs []MLflowKeyValue, maxValue int) bool {
	seen := make(map[string]struct{}, len(pairs))
	for _, pair := range pairs {
		if !validMLflowIntegrationKey(pair.Key) || len(pair.Value) > maxValue {
			return false
		}
		if _, exists := seen[pair.Key]; exists {
			return false
		}
		seen[pair.Key] = struct{}{}
	}
	return true
}

func validMLflowIntegrationKey(key string) bool {
	if !safeMetricKey(key) {
		return false
	}
	lower := strings.ToLower(key)
	if strings.HasPrefix(lower, "platform.") || strings.HasPrefix(lower, "mlflow.") {
		return false
	}
	canonical := strings.NewReplacer("_", "", "-", "").Replace(lower)
	switch canonical {
	case "runid", "runname", "experimentid", "experimentname", "jobid", "tenantid", "submitteruserid", "userid", "ownerid", "artifacturi", "provenance", "status", "starttime", "endtime":
		return false
	}
	return true
}
