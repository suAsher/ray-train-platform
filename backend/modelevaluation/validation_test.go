package modelevaluation

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func validEvaluator() Evaluator {
	return Evaluator{ID: "evaluator-1", Name: "Detection", OwnerID: "owner", TenantID: "team", Revision: 1, Active: true, ImageReference: "harbor.example.test/team/evaluator:v1", ImageDigest: "sha256:" + strings.Repeat("a", 64), GitURL: "https://git.example.test/team/evaluation.git", GitCommit: strings.Repeat("b", 40), EntryPoint: []string{"python", "evaluate.py"}, SchemaVersion: "bev-v1", Protocol: Protocol}
}
func validEvaluation() Evaluation {
	config, hash, _ := CanonicalConfig(json.RawMessage(`{"threshold":0.5}`))
	return Evaluation{ID: "evaluation-1", ModelID: "model-1", VersionID: "version-1", ModelSHA256: strings.Repeat("c", 64), FileName: "weights.pt", Dataset: DatasetSnapshot{ID: "dataset-1", VersionID: "version-1", ManifestSHA256: strings.Repeat("d", 64), SchemaVersion: "bev-v1", Split: "val", Sites: []string{}, SampleCount: 10, Visibility: Public}, Evaluator: validEvaluator(), Config: config, ConfigSHA256: hash, Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 4, MemoryPerWorker: "16Gi"}, OwnerID: "owner", TenantID: "team", State: Running, ReportState: ReportPending, Revision: 1}
}
func TestCanonicalConfigStableAndExact(t *testing.T) {
	a, hashA, err := CanonicalConfig(json.RawMessage(`{"z":1.0,"a":{"b":1e1,"x":-0}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, hashB, err := CanonicalConfig(json.RawMessage(` { "a":{"x":0,"b":10}, "z":1 } `))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) || hashA != hashB || len(hashA) != 64 {
		t.Fatalf("not canonical: %s %s", a, b)
	}
	_, largeA, err := CanonicalConfig(json.RawMessage(`{"n":9007199254740992}`))
	if err != nil {
		t.Fatal(err)
	}
	_, largeB, err := CanonicalConfig(json.RawMessage(`{"n":9007199254740993}`))
	if err != nil || largeA == largeB {
		t.Fatal("large integers rounded into same config")
	}
}
func TestCanonicalConfigRejectsAmbiguousOrUnboundedInput(t *testing.T) {
	for _, raw := range []string{`[]`, `null`, `{"x":1,"x":2}`, `{"x":{"a":1,"a":2}}`, `{} {}`, `{"x":NaN}`, `{"x":1e999}`, `{"x":"` + strings.Repeat("a", MaxConfigBytes) + `"}`, strings.Repeat(`{"a":`, 40) + `0` + strings.Repeat(`}`, 40)} {
		if _, _, err := CanonicalConfig(json.RawMessage(raw)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted bad config %.80s: %v", raw, err)
		}
	}
}
func TestFrozenEvaluationAndVisibility(t *testing.T) {
	e := validEvaluation()
	if err := ValidateEvaluation(e); err != nil {
		t.Fatal(err)
	}
	if !CanRead(e, "another-team", false) {
		t.Fatal("public result hidden")
	}
	e.Dataset.Visibility = Team
	e.Dataset.TenantID = "data-team"
	if CanRead(e, "team", false) || !CanRead(e, "data-team", false) || !CanRead(e, "other", true) {
		t.Fatal("result widened dataset access")
	}
	e.Dataset.Visibility = "UNKNOWN"
	if CanRead(e, "", true) {
		t.Fatal("invalid visibility must fail closed")
	}
}
func TestEvaluatorAndRequestValidation(t *testing.T) {
	if err := ValidateEvaluator(validEvaluator()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Evaluator){func(e *Evaluator) { e.ImageDigest = "latest" }, func(e *Evaluator) { e.GitCommit = "main" }, func(e *Evaluator) { e.GitURL = "https://user:secret@example.test/x" }, func(e *Evaluator) { e.EntryPoint = nil }, func(e *Evaluator) { e.Protocol = "other" }} {
		e := validEvaluator()
		change(&e)
		if !errors.Is(ValidateEvaluator(e), ErrInvalid) {
			t.Fatalf("bad evaluator accepted %+v", e)
		}
	}
	r := Request{ModelID: "model-1", VersionID: "version-1", DatasetID: "dataset-1", DatasetVersionID: "version-2", EvaluatorID: "evaluator-1", Split: "test", Config: json.RawMessage(`{}`), IdempotencyKey: "request-1", Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 4, MemoryPerWorker: "16Gi"}}
	if err := ValidateRequest(r); err != nil {
		t.Fatal(err)
	}
	r.Split = "train"
	if !errors.Is(ValidateRequest(r), ErrInvalid) {
		t.Fatal("training split accepted")
	}
}

func TestSitesResourcesAndCanonicalInputAreImmutable(t *testing.T) {
	sites := []string{"site-b", "site-a"}
	canonical, err := CanonicalSites(sites)
	if err != nil || canonical[0] != "site-a" || sites[0] != "site-b" {
		t.Fatalf("site normalization mutated input: %v %v", sites, err)
	}
	if _, err := CanonicalSites([]string{"site-a", "site-a"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("duplicate sites accepted")
	}
	if _, err := CanonicalSites([]string{"../outside"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unsafe site accepted")
	}
	for _, r := range []domain.Resources{
		{WorkerReplicas: 2, GPUsPerWorker: 1, CPUPerWorker: 4, MemoryPerWorker: "16Gi"},
		{WorkerReplicas: 1, GPUsPerWorker: 9, CPUPerWorker: 4, MemoryPerWorker: "16Gi"},
		{WorkerReplicas: 1, GPUsPerWorker: 8, CPUPerWorker: 1, MemoryPerWorker: "16Gi"},
		{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 33, MemoryPerWorker: "16Gi"},
		{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 4, MemoryPerWorker: "129Gi"},
		{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 4, MemoryPerWorker: "0"},
	} {
		if !errors.Is(ValidateResources(r), ErrInvalid) {
			t.Fatalf("unsafe resource accepted: %+v", r)
		}
	}
}

func TestEvaluationResourcesProvideOneCPUPerGPUWorker(t *testing.T) {
	r := domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 8, CPUPerWorker: 8, MemoryPerWorker: "16Gi"}
	if err := ValidateResources(r); err != nil {
		t.Fatalf("equal CPU/GPU allocation rejected: %v", err)
	}
	r.CPUPerWorker = 7
	if err := ValidateResources(r); !errors.Is(err, ErrInvalid) {
		t.Fatalf("insufficient CPU placement accepted: %v", err)
	}
}
