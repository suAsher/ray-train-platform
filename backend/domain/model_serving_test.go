package domain

import (
	"strings"
	"testing"
)

func validServingRuntime() ServingRuntime {
	return ServingRuntime{DeploymentID: "01234567-89ab-cdef-0123-456789abcdef", ModelSHA256: strings.Repeat("a", 64), ModelSizeBytes: 156, CodeID: strings.Repeat("b", 32), CodeSHA256: strings.Repeat("c", 64), CodeSizeBytes: 1024, CodeFormat: "zip", Protocol: "model-serving-http/v1"}
}

func TestServingRuntimeFrozenCodeAndModel(t *testing.T) {
	runtime := validServingRuntime()
	if err := runtime.Validate(); err != nil {
		t.Fatal(err)
	}
	source := CodeSource{Type: "serving-archive", ArtifactID: runtime.CodeID, ArtifactSHA256: runtime.CodeSHA256}
	if err := runtime.ValidateCodeSource(source); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ServingRuntime){
		func(r *ServingRuntime) { r.DeploymentID = "../../another" },
		func(r *ServingRuntime) { r.CodeID = "../other" },
		func(r *ServingRuntime) { r.ModelSHA256 = "invalid" },
		func(r *ServingRuntime) { r.ModelSizeBytes = 0 },
		func(r *ServingRuntime) { r.CodeSizeBytes = 64*1024*1024 + 1 },
		func(r *ServingRuntime) { r.CodeFormat = "tar" },
		func(r *ServingRuntime) { r.Protocol = "unknown" },
	} {
		copy := runtime
		mutate(&copy)
		if copy.Validate() == nil {
			t.Fatal("untrusted serving runtime accepted")
		}
	}
	for _, mutate := range []func(*CodeSource){
		func(s *CodeSource) { s.ArtifactSHA256 = strings.Repeat("d", 64) },
		func(s *CodeSource) { s.ArtifactObjectKey = "other/code.zip" },
		func(s *CodeSource) { s.URL = "https://outside.example/code.zip" },
		func(s *CodeSource) { s.Type = "evaluation-archive" },
	} {
		copy := source
		mutate(&copy)
		if runtime.ValidateCodeSource(copy) == nil {
			t.Fatal("forged source accepted")
		}
	}
}
