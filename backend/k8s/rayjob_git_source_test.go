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
	spec, _, err := nestedMap(manifest.Object, "spec", "submitterPodTemplate", "spec")
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

// A source fetch happens before KubeRay can start scheduling the Ray cluster.
// It must be non-interactive and bounded, otherwise a half-open Git HTTPS
// connection leaves a GPU job in Initializing indefinitely.
func TestGitSourceMaterializerBoundsStalledFetch(t *testing.T) {
	script := gitInitContainerScript(t)
	for _, expected := range []string{
		"export GIT_TERMINAL_PROMPT=0",
		"timeout 180 git",
		"-c http.lowSpeedLimit=1024",
		"-c http.lowSpeedTime=60",
		"Git fetch failed or exceeded 180 seconds",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("expected %q in bounded Git source materializer:\n%s", expected, script)
		}
	}
}

// The submitter uploads /workspace as Ray's runtime environment. Making the
// head fetch the same repository is redundant and lets one transient Git
// connection hold the entire GPU job in Initializing. The runtime environment
// then distributes the uploaded code to the driver and workers.
func TestGitSourceIsMaterializedOnlyInSubmitterPod(t *testing.T) {
	job := validRenderJob()
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}

	cluster, _, err := nestedMap(manifest.Object, "spec", "rayClusterSpec")
	if err != nil {
		t.Fatalf("read cluster spec: %v", err)
	}
	head, _, err := nestedMap(cluster, "headGroupSpec", "template", "spec")
	if err != nil {
		t.Fatalf("read head spec: %v", err)
	}
	if init, _ := head["initContainers"].([]any); len(init) != 0 {
		t.Fatalf("head must not fetch source independently: %#v", init)
	}

	workers, _, err := nestedSlice(cluster, "workerGroupSpecs")
	if err != nil || len(workers) != 1 {
		t.Fatalf("read worker specs: %v", err)
	}
	worker := workers[0].(map[string]any)
	workerSpec, _, err := nestedMap(worker, "template", "spec")
	if err != nil {
		t.Fatalf("read worker spec: %v", err)
	}
	if init, _ := workerSpec["initContainers"].([]any); len(init) != 0 {
		t.Fatalf("worker must receive code from Ray runtime env, not Git: %#v", init)
	}

	submitter, _, err := nestedMap(manifest.Object, "spec", "submitterPodTemplate", "spec")
	if err != nil {
		t.Fatalf("read submitter spec: %v", err)
	}
	if init, _ := submitter["initContainers"].([]any); len(init) != 1 {
		t.Fatalf("submitter must be the only source materializer: %#v", init)
	}
}
