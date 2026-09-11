package spkrayjob

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// The CLI is the only surface some users ever see: they never open the Portal,
// so anything the Portal explains has to be reachable from --help too. Every
// item below was learned from a failed run rather than copied from a manual.
func TestHelpExplainsThePlatformContract(t *testing.T) {
	for _, expected := range []string{
		// Training code has no other stable handle on its data or results.
		"PLATFORM_DATASET_PATH",
		"PLATFORM_OUTPUT_PATH",
		"PLATFORM_CHECKPOINT_PATH",
		"PLATFORM_CACHE_PATH",
		// Reading the variables in Python is what avoids the empty-expansion
		// failure, so the rule and its symptom must both be stated.
		"os.environ",
		"PermissionError",
		// The most expensive mistakes: a wrong path or a launcher of one's own.
		"输入目录先确认真实存在",
		"torchrun",
		"python3",
		// A new user should be able to authenticate, configure, submit, observe,
		// and diagnose a job without opening a second manual.
		"账户与安全",
		"--token-stdin",
		".spk-rayjob.yaml",
		"Head 不占 GPU",
		"团队策略自动选择",
		"至少需要 2 个 Worker",
		"2 Worker × 8 GPU",
		"日志完整导出",
		"INVALID_AUTHENTICATION",
		"GPU_QUOTA_EXCEEDED",
		"spk-rayjob submit --help",
	} {
		if !strings.Contains(helpText, expected) {
			t.Fatalf("--help must explain %q:\n%s", expected, helpText)
		}
	}
}

func TestCommandHelpIsUsefulAndDoesNotRequireAuthentication(t *testing.T) {
	tests := []struct {
		arguments []string
		expected  []string
	}{
		{[]string{"submit", "--help"}, []string{"用法：", "--workers", "--gpus-per-worker", "--data-mode", "--resume-from-job", "--watch", "多机强制验收"}},
		{[]string{"help", "submit"}, []string{"用法：", "--workers", "--gpus-per-worker", "--data-mode", "--resume-from-job", "--watch", "多机强制验收"}},
		{[]string{"login", "--help"}, []string{"--token-stdin", "--password-stdin", "PAT"}},
		{[]string{"logs", "--help"}, []string{"-f", "--limit", "完整导出"}},
		{[]string{"connect", "--help"}, []string{"第 1 个 Worker", "运行中", "不需要 --ssh"}},
	}
	for _, test := range tests {
		var stdout bytes.Buffer
		if err := Run(context.Background(), test.arguments, &stdout, &bytes.Buffer{}, func(string) string { return "" }); err != nil {
			t.Fatalf("%v help failed without authentication: %v", test.arguments, err)
		}
		for _, expected := range test.expected {
			if !strings.Contains(stdout.String(), expected) {
				t.Fatalf("%v help must contain %q:\n%s", test.arguments, expected, stdout.String())
			}
		}
	}
}

// A misaligned column is a small thing that makes the whole listing look
// unmaintained, and it is invisible in source review because tabs and spaces
// occupy the same width in most editors.
func TestHelpUsesSpacesSoColumnsAlign(t *testing.T) {
	for index, line := range strings.Split(helpText, "\n") {
		if strings.Contains(line, "\t") {
			t.Fatalf("--help line %d uses a tab and will not align in a terminal: %q", index+1, line)
		}
	}
}
