package k8s

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func gitInitContainerScript(t *testing.T) string {
	t.Helper()
	job := validRenderJob()
	job.Spec.Source = domain.CodeSource{
		Type: "git", URL: "https://github.com/example/train", Commit: "0123456789abcdef",
	}
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	spec, _, err := nestedMap(manifest.Object, "spec", "rayClusterSpec", "headGroupSpec", "template", "spec")
	if err != nil {
		t.Fatalf("read pod spec: %v", err)
	}
	initContainers, _ := spec["initContainers"].([]any)
	if len(initContainers) == 0 {
		t.Fatalf("expected a source materializer init container")
	}
	container, _ := initContainers[0].(map[string]any)
	args, _ := container["args"].([]any)
	if len(args) == 0 {
		t.Fatalf("expected materializer args")
	}
	script, _ := args[0].(string)
	return script
}

// Git refuses to touch a repository whose directory belongs to another user
// ("detected dubious ownership"). The workspace is an emptyDir owned by root
// while the container runs as the image user, so without an explicit
// safe.directory the init container crash-loops and no job can ever start.
func TestGitSourceMaterializerMarksWorkspaceSafe(t *testing.T) {
	script := gitInitContainerScript(t)
	for _, line := range strings.Split(script, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "git ") {
			continue
		}
		if !strings.Contains(line, "safe.directory=/workspace") {
			t.Fatalf("every git command must set safe.directory, missing on %q:\n%s", line, script)
		}
	}
	// --global needs a writable HOME, which the materializer image does not
	// have; writing it fails with "could not lock config file".
	if strings.Contains(script, "--global") {
		t.Fatalf("git config must not be written globally:\n%s", script)
	}
}

func TestGitSourceMaterializerStillCheckoutsRequestedCommit(t *testing.T) {
	script := gitInitContainerScript(t)
	for _, expected := range []string{"init /workspace", "remote add origin", "fetch --depth 1", "checkout --detach FETCH_HEAD"} {
		if !strings.Contains(script, expected) {
			t.Fatalf("expected %q in materializer script:\n%s", expected, script)
		}
	}
}
