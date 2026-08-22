package k8s

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func gitMaterializer(t *testing.T, options RenderOptions) map[string]any {
	t.Helper()
	job := validRenderJob()
	job.Spec.Source = domain.CodeSource{Type: "git", URL: "https://git.example.com/team/train", Commit: "0123456789abcdef"}
	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	spec, _, err := nestedMap(manifest.Object, "spec", "submitterPodTemplate", "spec")
	if err != nil {
		t.Fatalf("read pod spec: %v", err)
	}
	initContainers, _ := spec["initContainers"].([]any)
	container, _ := initContainers[0].(map[string]any)
	return container
}

func containerScript(container map[string]any) string {
	args, _ := container["args"].([]any)
	if len(args) == 0 {
		return ""
	}
	script, _ := args[0].(string)
	return script
}

// A private repository needs a token, and the token must arrive as an
// environment variable from a Secret rather than being written into the URL,
// which would leak it into the Pod spec and into git's process arguments.
func TestGitMaterializerInjectsCredentialFromSecret(t *testing.T) {
	options := testRenderOptions()
	options.GitCredentialSecret = "git-cred-gitexamplecom"
	container := gitMaterializer(t, options)

	env, _ := container["env"].([]any)
	found := map[string]bool{}
	for _, item := range env {
		entry, _ := item.(map[string]any)
		name, _ := entry["name"].(string)
		if valueFrom, ok := entry["valueFrom"].(map[string]any); ok {
			secretRef, _ := valueFrom["secretKeyRef"].(map[string]any)
			if secretRef["name"] == "git-cred-gitexamplecom" {
				found[name] = true
			}
		}
	}
	if !found["GIT_USERNAME"] || !found["GIT_TOKEN"] {
		t.Fatalf("expected GIT_USERNAME and GIT_TOKEN from the secret, got %v", found)
	}

	script := containerScript(container)
	if !strings.Contains(script, "GIT_ASKPASS") && !strings.Contains(script, "credential") {
		t.Fatalf("the script must feed the credential to git without embedding it in the URL:\n%s", script)
	}
	// The token must never be interpolated into the remote URL.
	if strings.Contains(script, "https://$GIT_USERNAME:$GIT_TOKEN@") {
		t.Fatalf("token must not be written into the remote URL:\n%s", script)
	}
}

func TestGitMaterializerOmitsCredentialForPublicRepositories(t *testing.T) {
	container := gitMaterializer(t, testRenderOptions())
	env, _ := container["env"].([]any)
	for _, item := range env {
		entry, _ := item.(map[string]any)
		if name, _ := entry["name"].(string); name == "GIT_TOKEN" {
			t.Fatalf("a public repository must not receive a credential")
		}
	}
	if strings.Contains(containerScript(container), "GIT_ASKPASS") {
		t.Fatalf("no credential helper should be configured without a secret")
	}
}
