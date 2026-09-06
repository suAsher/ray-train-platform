package domain

import (
	"strings"
	"testing"
)

func TestImageEnvironmentValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     ImageEnvironment
		invalid bool
	}{
		{"empty legacy", ImageEnvironment{}, false},
		{"declared", ImageEnvironment{Python: "3.11", CUDA: "12.4", PyTorch: "2.5.1", MLflow: "2.17.2", Dependencies: "numpy==1.26.4\npyarrow==17.0.0", UseCases: "分类训练", ValidationNotes: "CPU 自检通过；GPU 未验证"}, false},
		{"version limit", ImageEnvironment{Python: strings.Repeat("a", 129)}, true},
		{"dependencies limit", ImageEnvironment{Dependencies: strings.Repeat("a", 12001)}, true},
		{"use cases limit", ImageEnvironment{UseCases: strings.Repeat("a", 2001)}, true},
		{"notes limit", ImageEnvironment{ValidationNotes: strings.Repeat("a", 4001)}, true},
		{"version newline", ImageEnvironment{CUDA: "12\n4"}, true},
		{"nul", ImageEnvironment{Dependencies: "numpy\x00"}, true},
		{"terminal escape", ImageEnvironment{ValidationNotes: "\x1b[31m"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.env.Validate(); (err != nil) != tc.invalid {
				t.Fatalf("Validate()=%v invalid=%v", err, tc.invalid)
			}
		})
	}
}
