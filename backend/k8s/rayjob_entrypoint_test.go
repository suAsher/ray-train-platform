package k8s

import (
	"reflect"
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

func TestRenderManagedRayJobEntrypointQuotesArgumentsSafelyWithoutMutatingSpec(t *testing.T) {
	job := managedRenderJob(domain.RayVersionProduction)
	job.Spec.Entrypoint = domain.Entrypoint{
		Command: []string{"python", "-c"},
		Args:    []string{"import os; os.system('touch /tmp/pwned')", "$(id)"},
	}
	wantCommand := append([]string(nil), job.Spec.Entrypoint.Command...)
	wantArgs := append([]string(nil), job.Spec.Entrypoint.Args...)
	manifest := managedManifest(t, job)
	entrypoint, _, _ := unstructured.NestedString(manifest.Object, "spec", "entrypoint")
	want := "raytrain-managed --nodes 2 --gpus-per-node 8 --cpus-per-node 32 --max-failures 3 --checkpoint-every-epochs 0 --checkpoint-keep-latest 0 --checkpoint-keep-best 0 -- python -c 'import os; os.system('\"'\"'touch /tmp/pwned'\"'\"')' '$(id)'"
	if entrypoint != want {
		t.Fatalf("managed user command was not shell-quoted safely:\n got: %q\nwant: %q", entrypoint, want)
	}
	if !reflect.DeepEqual(job.Spec.Entrypoint.Command, wantCommand) || !reflect.DeepEqual(job.Spec.Entrypoint.Args, wantArgs) {
		t.Fatalf("renderer mutated caller-owned command: command=%#v args=%#v", job.Spec.Entrypoint.Command, job.Spec.Entrypoint.Args)
	}
}
