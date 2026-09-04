package spkrayjob

import (
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
	} {
		if !strings.Contains(helpText, expected) {
			t.Fatalf("--help must explain %q:\n%s", expected, helpText)
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
