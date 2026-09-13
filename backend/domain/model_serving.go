package domain

import (
	"fmt"
	"regexp"
)

// ServingRuntime is loaded from an immutable deployment record, never public
// JobSpec JSON. It contains no credentials, object-store keys, or target URLs.
type ServingRuntime struct {
	DeploymentID   string
	ModelSHA256    string
	ModelSizeBytes int64
	CodeID         string
	CodeSHA256     string
	CodeSizeBytes  int64
	CodeFormat     string
	Protocol       string
}

func (runtime ServingRuntime) Validate() error {
	hash := regexp.MustCompile(`^[0-9a-f]{64}$`)
	identifier := regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	if !identifier.MatchString(runtime.DeploymentID) || !hash.MatchString(runtime.ModelSHA256) || runtime.ModelSizeBytes < 1 || runtime.ModelSizeBytes > 20*1024*1024*1024 {
		return fmt.Errorf("serving deployment or model snapshot is invalid")
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(runtime.CodeID) || !hash.MatchString(runtime.CodeSHA256) || runtime.CodeSizeBytes < 1 || runtime.CodeSizeBytes > 64*1024*1024 || runtime.CodeFormat != "zip" {
		return fmt.Errorf("serving code snapshot is invalid")
	}
	if runtime.Protocol != "model-serving-http/v1" {
		return fmt.Errorf("serving protocol is unsupported")
	}
	return nil
}

func (runtime ServingRuntime) ValidateCodeSource(source CodeSource) error {
	if err := runtime.Validate(); err != nil {
		return err
	}
	if source != (CodeSource{Type: "serving-archive", ArtifactID: runtime.CodeID, ArtifactSHA256: runtime.CodeSHA256}) {
		return fmt.Errorf("serving source does not match the frozen code snapshot")
	}
	return nil
}
