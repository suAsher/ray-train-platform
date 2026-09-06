package domain

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

// ImageEnvironment is administrator-declared documentation, not a platform
// attestation. ValidationNotes must never be interpreted as verified status.
type ImageEnvironment struct {
	Python          string `json:"python,omitempty"`
	CUDA            string `json:"cuda,omitempty"`
	PyTorch         string `json:"pytorch,omitempty"`
	MLflow          string `json:"mlflow,omitempty"`
	Dependencies    string `json:"dependencies,omitempty"`
	UseCases        string `json:"useCases,omitempty"`
	ValidationNotes string `json:"validationNotes,omitempty"`
}

func (e ImageEnvironment) Validate() error {
	for _, field := range []struct {
		name, value string
		limit       int
		multiline   bool
	}{
		{"python", e.Python, 128, false}, {"cuda", e.CUDA, 128, false},
		{"pytorch", e.PyTorch, 128, false}, {"mlflow", e.MLflow, 128, false},
		{"dependencies", e.Dependencies, 12000, true}, {"useCases", e.UseCases, 2000, true},
		{"validationNotes", e.ValidationNotes, 4000, true},
	} {
		if !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) > field.limit {
			return fmt.Errorf("image environment %s must be valid text of at most %d characters", field.name, field.limit)
		}
		for _, r := range field.value {
			if unicode.IsControl(r) && !(field.multiline && (r == '\n' || r == '\r' || r == '\t')) {
				return fmt.Errorf("image environment %s contains unsupported control characters", field.name)
			}
		}
	}
	return nil
}
