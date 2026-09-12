package mlflowtracking

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestBatchValidationProtectsOwnershipAndFiniteValues(t *testing.T) {
	value, timestamp, step := 1.0, int64(1000), int64(0)
	for _, batch := range []Batch{
		{}, {Metrics: make([]Metric, 101)}, {Params: make([]Pair, 101)}, {Tags: make([]Pair, 101)},
		{Metrics: []Metric{{Key: "loss", Value: nil, Timestamp: &timestamp, Step: &step}}},
		{Params: []Pair{{Key: "platform.job_id", Value: "forged"}}},
		{Tags: []Pair{{Key: "mlflow.runName", Value: "forged"}}},
		{Tags: []Pair{{Key: "run_id", Value: "forged"}}},
		{Tags: []Pair{{Key: "bad key", Value: "forged"}}},
		{Params: []Pair{{Key: "version", Value: "v1"}, {Key: "version", Value: "v2"}}},
		{Params: []Pair{{Key: "version", Value: strings.Repeat("x", 1025)}}},
		{Tags: []Pair{{Key: "version", Value: strings.Repeat("x", 5001)}}},
	} {
		if err := batch.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid batch=%+v %v", batch, err)
		}
	}
	for _, invalid := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := (Batch{Metrics: []Metric{{Key: "loss", Value: &invalid, Timestamp: &timestamp, Step: &step}}}).Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("nonfinite metric accepted: %v", err)
		}
	}
	valid := Batch{Metrics: []Metric{{Key: "loss", Value: &value, Timestamp: &timestamp, Step: &step}}, Params: []Pair{{Key: "version", Value: ""}}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"key":"x"}`, `{"key":"x","value":null}`, `{"key":"x","value":1}`, `{"key":"x","value":"v","extra":1}`} {
		var pair Pair
		if json.Unmarshal([]byte(raw), &pair) == nil {
			t.Fatalf("invalid pair=%s", raw)
		}
	}
	var pair Pair
	if err := json.Unmarshal([]byte(`{"key":"x","value":""}`), &pair); err != nil {
		t.Fatal(err)
	}
}
