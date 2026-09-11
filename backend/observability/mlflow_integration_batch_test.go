package observability

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestMLflowIntegrationBatchMetricBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name            string
		value           float64
		timestamp, step int64
		valid           bool
	}{
		{"explicit zero", 0, 0, 0, true},
		{"negative metric is legal", -1, 1000, 1, true},
		{"last supported millisecond", 1, 253402300799999, 1, true},
		{"NaN", math.NaN(), 1000, 1, false},
		{"positive infinity", math.Inf(1), 1000, 1, false},
		{"negative infinity", math.Inf(-1), 1000, 1, false},
		{"negative timestamp", 1, -1, 1, false},
		{"out of range timestamp", 1, 253402300800000, 1, false},
		{"negative step", 1, 1000, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			batch := MLflowLogBatch{Metrics: []MLflowLogMetric{{Key: "loss", Value: &tc.value, Timestamp: &tc.timestamp, Step: &tc.step}}}
			err := batch.Validate()
			if (err == nil) != tc.valid || (!tc.valid && !errors.Is(err, ErrMLflowBatchInvalid)) {
				t.Fatalf("validation=%v valid=%v", err, tc.valid)
			}
		})
	}
}

func TestMLflowIntegrationBatchRejectsMissingMetricFields(t *testing.T) {
	value, timestamp, step := 1.0, int64(1000), int64(1)
	for _, metric := range []MLflowLogMetric{
		{Key: "loss", Timestamp: &timestamp, Step: &step},
		{Key: "loss", Value: &value, Step: &step},
		{Key: "loss", Value: &value, Timestamp: &timestamp},
		{Key: "", Value: &value, Timestamp: &timestamp, Step: &step},
	} {
		if err := (MLflowLogBatch{Metrics: []MLflowLogMetric{metric}}).Validate(); !errors.Is(err, ErrMLflowBatchInvalid) {
			t.Fatalf("incomplete metric accepted: %+v", metric)
		}
	}
}

func TestMLflowIntegrationBatchProtectsOwnershipAndBoundsPairs(t *testing.T) {
	for _, key := range []string{"", "bad key", strings.Repeat("k", 129), "platform.job_id", "PLATFORM.provenance", "mlflow.runName", "MLFLOW.parentRunId", "run_id", "experiment-id", "tenant_id", "submitter_user_id", "artifact_uri"} {
		if err := (MLflowLogBatch{Tags: []MLflowKeyValue{{Key: key, Value: "attempt"}}}).Validate(); !errors.Is(err, ErrMLflowBatchInvalid) {
			t.Fatalf("unsafe key accepted: %q", key)
		}
	}
	for _, batch := range []MLflowLogBatch{
		{Params: []MLflowKeyValue{{Key: "x", Value: strings.Repeat("v", 1025)}}},
		{Tags: []MLflowKeyValue{{Key: "x", Value: strings.Repeat("v", 5001)}}},
		{Params: []MLflowKeyValue{{Key: "x", Value: "1"}, {Key: "x", Value: "2"}}},
		{Tags: []MLflowKeyValue{{Key: "x", Value: "1"}, {Key: "x", Value: "2"}}},
		{},
	} {
		if err := batch.Validate(); !errors.Is(err, ErrMLflowBatchInvalid) {
			t.Fatalf("invalid batch accepted: %+v", batch)
		}
	}
	valid := MLflowLogBatch{Params: []MLflowKeyValue{{Key: "config/optional-value", Value: strings.Repeat("v", 1024)}}, Tags: []MLflowKeyValue{{Key: "review.v1", Value: strings.Repeat("v", 5000)}}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("legal bounds rejected: %v", err)
	}
}

func TestMLflowIntegrationBatchCollectionLimits(t *testing.T) {
	for _, count := range []int{100, 101} {
		pairs := make([]MLflowKeyValue, count)
		metrics := make([]MLflowLogMetric, count)
		value, timestamp, step := 1.0, int64(1000), int64(1)
		for i := range pairs {
			pairs[i] = MLflowKeyValue{Key: fmt.Sprintf("key%d", i), Value: "v"}
			metrics[i] = MLflowLogMetric{Key: "loss", Value: &value, Timestamp: &timestamp, Step: &step}
		}
		for _, batch := range []MLflowLogBatch{{Params: pairs}, {Tags: pairs}, {Metrics: metrics}} {
			if err := batch.Validate(); (err == nil) != (count == 100) {
				t.Fatalf("collection count=%d validation=%v", count, err)
			}
		}
	}
}

func TestMLflowIntegrationKeyValueDecodingRequiresExplicitStrings(t *testing.T) {
	for _, input := range []string{`{"key":"x"}`, `{"value":"v"}`, `{"key":"x","value":null}`, `{"key":"x","value":1}`, `{"key":"x","value":"v","unexpected":true}`} {
		var pair MLflowKeyValue
		if err := json.Unmarshal([]byte(input), &pair); err == nil {
			t.Fatalf("invalid pair accepted: %s", input)
		}
	}
	var pair MLflowKeyValue
	if err := json.Unmarshal([]byte(`{"key":"optional","value":""}`), &pair); err != nil || pair.Key != "optional" || pair.Value != "" {
		t.Fatalf("explicit empty value rejected: %+v %v", pair, err)
	}
}
