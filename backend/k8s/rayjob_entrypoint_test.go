package k8s

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"ray-train-platform-backend/domain"
)

func renderedEntrypoint(t *testing.T, command, args []string) (string, string) {
	t.Helper()
	job := validRenderJob()
	job.Spec.Entrypoint = domain.Entrypoint{Command: command, Args: args}
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	entrypoint, _, err := unstructured.NestedString(manifest.Object, "spec", "entrypoint")
	if err != nil {
		t.Fatalf("read entrypoint: %v", err)
	}
	runtimeEnv, _, _ := unstructured.NestedString(manifest.Object, "spec", "runtimeEnvYAML")
	return entrypoint, runtimeEnv
}

// KubeRay passes spec.entrypoint to `ray job submit -- <entrypoint>`. A shell
// operator such as && terminates the submitted command, so Ray runs only the
// first half on the cluster, reports SUCCEEDED, and the real training command
// executes in the submitter pod instead — a silent false success.
func TestRenderRayJobEntrypointHasNoShellOperators(t *testing.T) {
	entrypoint, _ := renderedEntrypoint(t, []string{"python", "train.py"}, []string{"--epochs", "3"})
	for _, operator := range []string{"&&", "||", ";", "|"} {
		if strings.Contains(entrypoint, operator) {
			t.Fatalf("entrypoint must not contain the shell operator %q: %q", operator, entrypoint)
		}
	}
	if !strings.HasPrefix(entrypoint, "python train.py") {
		t.Fatalf("entrypoint should invoke the user command directly, got %q", entrypoint)
	}
}

// The working directory has to come from the Ray runtime env, which is what
// actually ships the materialized source to the driver and the workers.
func TestRenderRayJobSetsWorkingDirectoryThroughRuntimeEnv(t *testing.T) {
	_, runtimeEnv := renderedEntrypoint(t, []string{"python", "train.py"}, nil)
	if !strings.Contains(runtimeEnv, "working_dir: /workspace") {
		t.Fatalf("runtime env must set working_dir, got %q", runtimeEnv)
	}
}

func TestRenderRayJobEntrypointQuotesArgumentsSafely(t *testing.T) {
	entrypoint, _ := renderedEntrypoint(t, []string{"python", "-c"}, []string{"print('hello world')"})
	if !strings.Contains(entrypoint, "python -c ") {
		t.Fatalf("unexpected entrypoint: %q", entrypoint)
	}
	// The script is one argument, so it must stay quoted as a single token.
	if !strings.Contains(entrypoint, "'print('\"'\"'hello world'\"'\"')'") {
		t.Fatalf("script argument was not safely quoted: %q", entrypoint)
	}
}
