package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// EvaluationRuntime is populated only from the persisted evaluation after a job
// is loaded. JobSpec excludes it from JSON so public submissions cannot supply it.
// It contains no credentials or caller-selected download URL.
type EvaluationRuntime struct {
	EvaluationID          string
	ModelSHA256           string
	DatasetManifestSHA256 string
	DatasetSplit          string
	DatasetSampleCount    int64
	ConfigJSON            string
	ConfigSHA256          string
	EvaluatorID           string
	Protocol              string
	CodeID                string
	CodeSHA256            string
	CodeSizeBytes         int64
	CodeFormat            string
}

func (runtime EvaluationRuntime) Validate() error {
	hashPattern := regexp.MustCompile(`^[0-9a-f]{64}$`)
	idPattern := regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	if !idPattern.MatchString(runtime.EvaluationID) || !idPattern.MatchString(runtime.EvaluatorID) || !hashPattern.MatchString(runtime.ModelSHA256) || !hashPattern.MatchString(runtime.DatasetManifestSHA256) || !hashPattern.MatchString(runtime.ConfigSHA256) {
		return fmt.Errorf("evaluation runtime identifiers are invalid")
	}
	if runtime.Protocol != "model-evaluation-report/v1" || runtime.DatasetSampleCount <= 0 || (runtime.DatasetSplit != "val" && runtime.DatasetSplit != "test") {
		return fmt.Errorf("evaluation runtime protocol or dataset is invalid")
	}
	if len(runtime.ConfigJSON) > 16*1024 || !json.Valid([]byte(runtime.ConfigJSON)) {
		return fmt.Errorf("evaluation runtime config is invalid")
	}
	if !strings.HasPrefix(strings.TrimSpace(runtime.ConfigJSON), "{") {
		return fmt.Errorf("evaluation runtime config must be an object")
	}
	digest := sha256.Sum256([]byte(runtime.ConfigJSON))
	if hex.EncodeToString(digest[:]) != runtime.ConfigSHA256 {
		return fmt.Errorf("evaluation runtime config digest does not match")
	}
	if runtime.CodeID != "" || runtime.CodeSHA256 != "" || runtime.CodeSizeBytes != 0 || runtime.CodeFormat != "" {
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(runtime.CodeID) || !hashPattern.MatchString(runtime.CodeSHA256) || runtime.CodeSizeBytes < 1 || runtime.CodeSizeBytes > 64*1024*1024 || runtime.CodeFormat != "zip" {
			return fmt.Errorf("evaluation code snapshot is invalid")
		}
	}
	return nil
}

// ValidateCodeSource binds the renderer's trusted code snapshot to the stored
// job selector. Neither public JSON nor the job may choose an object-store key.
func (runtime EvaluationRuntime) ValidateCodeSource(source CodeSource) error {
	if runtime.CodeID == "" {
		if source.Type == "evaluation-archive" { return fmt.Errorf("evaluation archive has no trusted code snapshot") }
		return nil
	}
	if err := runtime.Validate(); err != nil { return err }
	if source != (CodeSource{Type: "evaluation-archive", ArtifactID: runtime.CodeID, ArtifactSHA256: runtime.CodeSHA256}) {
		return fmt.Errorf("evaluation source does not match the frozen code snapshot")
	}
	return nil
}
