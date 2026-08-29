package k8s

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"ray-train-platform-backend/domain"
)

func TestEnsureTrainingEventTokenSecretUsesCryptoRandomImmutableJobScope(t *testing.T) {
	client := NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset())
	raw, err := client.EnsureTrainingEventTokenSecret(context.Background(), "tenant-a", "job-0123456789abcdef01234567")
	if err != nil {
		t.Fatalf("ensure training event token: %v", err)
	}
	if len(raw) != TrainingEventTokenBytes {
		t.Fatalf("raw token bytes=%d, want %d", len(raw), TrainingEventTokenBytes)
	}
	secret, err := client.kubernetes.CoreV1().Secrets("tenant-a").Get(context.Background(), TrainingEventSecretName("job-0123456789abcdef01234567"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if secret.Immutable == nil || !*secret.Immutable || secret.Labels[trainingEventJobLabel] != "job-0123456789abcdef01234567" {
		t.Fatalf("event token Secret is not immutable and job scoped: %#v", secret)
	}
	encoded := string(secret.Data[TrainingEventTokenKey])
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || string(decoded) != string(raw) {
		t.Fatalf("Secret does not contain the encoded random token: %v", err)
	}
	again, err := client.EnsureTrainingEventTokenSecret(context.Background(), "tenant-a", "job-0123456789abcdef01234567")
	if err != nil || string(again) != string(raw) {
		t.Fatalf("retry rotated the job token: same=%t err=%v", string(again) == string(raw), err)
	}
}

func TestEnsureTrainingEventTokenSecretRefusesUnmanagedCollision(t *testing.T) {
	name := TrainingEventSecretName("job-0123456789abcdef01234567")
	core := k8sfake.NewSimpleClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tenant-a"}, Data: map[string][]byte{TrainingEventTokenKey: []byte("attacker")}})
	client := NewClientFromInterfaces(nil, core)
	if _, err := client.EnsureTrainingEventTokenSecret(context.Background(), "tenant-a", "job-0123456789abcdef01234567"); err == nil {
		t.Fatal("unmanaged Secret collision was accepted")
	}
}

func TestRenderManagedRayJobMountsEventTokenOnlyIntoHeadAndWorkers(t *testing.T) {
	job := validRenderJob()
	job.ID = "job-0123456789abcdef01234567"
	job.Spec.TrainingEngine = domain.TrainingEngineRayTrain
	job.Spec.RayVersion = domain.RayVersionProduction
	job.Spec.Managed = domain.ManagedTrainingPolicy{MaxFailures: 2, Checkpoint: domain.CheckpointPolicy{EveryEpochs: 1, KeepLatest: 3, KeepBest: 1}}
	options := testRenderOptions()
	options.TrainingEventBaseURL = "http://ray-train-backend.ray-train-platform.svc:8080/api/v1/internal"
	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatal(err)
	}
	spec, _, _ := nestedMap(manifest.Object, "spec")
	submitter, _, _ := nestedMap(spec, "submitterPodTemplate", "spec")
	cluster, _, _ := nestedMap(spec, "rayClusterSpec")
	head, _, _ := nestedMap(cluster, "headGroupSpec", "template", "spec")
	workers, _, _ := nestedSlice(cluster, "workerGroupSpecs")
	worker, _, _ := nestedMap(workers[0].(map[string]any), "template", "spec")

	assertNoTrainingEventSecret(t, submitter, job.ID)
	assertTrainingEventSecret(t, head, job.ID)
	assertTrainingEventSecret(t, worker, job.ID)

	legacy := job
	legacy.Spec.TrainingEngine = domain.TrainingEngineRayDDP
	legacy.Spec.RayVersion = domain.RayVersionLegacy
	legacy.Spec.Managed = domain.ManagedTrainingPolicy{}
	legacyManifest, err := RenderRayJob(legacy, options)
	if err != nil {
		t.Fatal(err)
	}
	if encoded := fmt.Sprintf("%#v", legacyManifest.Object); strings.Contains(encoded, TrainingEventTokenKey) || strings.Contains(encoded, TrainingEventSecretName(job.ID)) {
		t.Fatalf("legacy Ray DDP workload received a managed event credential: %s", encoded)
	}
}

func TestRenderManagedRayJobRejectsUnsafeTrainingEventBaseURL(t *testing.T) {
	job := validRenderJob()
	job.ID = "job-0123456789abcdef01234567"
	job.Spec.TrainingEngine = domain.TrainingEngineRayTrain
	job.Spec.RayVersion = domain.RayVersionProduction
	job.Spec.Managed = domain.ManagedTrainingPolicy{MaxFailures: 2, Checkpoint: domain.CheckpointPolicy{EveryEpochs: 1, KeepLatest: 3, KeepBest: 1}}
	for _, candidate := range []string{
		"http://user:password@backend/api/v1/internal",
		"http://backend/api/v1/../admin",
		"http://backend/api/v1/internal\nINJECTED=value",
	} {
		options := testRenderOptions()
		options.TrainingEventBaseURL = candidate
		if _, err := RenderRayJob(job, options); err == nil {
			t.Fatalf("unsafe training event base URL accepted: %q", candidate)
		}
	}
}

func assertNoTrainingEventSecret(t *testing.T, pod map[string]any, jobID string) {
	t.Helper()
	if strings.Contains(fmt.Sprintf("%#v", pod), TrainingEventSecretName(jobID)) {
		t.Fatal("submitter received the managed training event Secret")
	}
}

func assertTrainingEventSecret(t *testing.T, pod map[string]any, jobID string) {
	t.Helper()
	encoded := fmt.Sprintf("%#v", pod)
	for _, fragment := range []string{TrainingEventSecretName(jobID), TrainingEventTokenKey, "RAYTRAIN_EVENT_TOKEN_FILE", "RAYTRAIN_EVENT_ENDPOINT"} {
		if !strings.Contains(encoded, fragment) {
			t.Fatalf("managed pod is missing %q: %s", fragment, encoded)
		}
	}
}

func TestEnsureGitCredentialSecretRotatesExistingSecretWithResourceVersion(t *testing.T) {
	const namespace = "tenant-a"
	const name = "git-cred-gitexamplecom"
	kube := k8sfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace, ResourceVersion: "17",
			Labels: map[string]string{"app.kubernetes.io/managed-by": "ray-train-platform"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"GIT_TOKEN": []byte("old-token")},
	})

	var updated *corev1.Secret
	kube.PrependReactor("update", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		candidate := action.(k8stesting.UpdateAction).GetObject().(*corev1.Secret)
		updated = candidate.DeepCopy()
		if candidate.ResourceVersion != "17" {
			return true, nil, fmt.Errorf("update missing existing resource version")
		}
		return false, nil, nil
	})

	client := &Client{kubernetes: kube}
	if err := client.EnsureGitCredentialSecret(context.Background(), namespace, name, "token-user", "new-token"); err != nil {
		t.Fatalf("rotate git credential secret: %v", err)
	}
	if updated == nil {
		t.Fatal("expected an update for the existing secret")
	}
	if got := updated.StringData["GIT_USERNAME"]; got != "token-user" {
		t.Fatalf("unexpected rotated username %q", got)
	}
	if got := updated.StringData["GIT_TOKEN"]; got != "new-token" {
		t.Fatalf("unexpected rotated token %q", got)
	}
}

func TestEnsureImagePullSecretCopiesOnlyTheNamedPlatformSecret(t *testing.T) {
	const sourceNamespace = "ray-train-platform"
	const targetNamespace = "tenant-local"
	const name = "registry-pull"
	source := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sourceNamespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{"registry.example":{"auth":"token"}}}`)},
	}
	client := &Client{kubernetes: k8sfake.NewSimpleClientset(source)}

	if err := client.EnsureImagePullSecret(context.Background(), sourceNamespace, targetNamespace, name); err != nil {
		t.Fatalf("copy image pull secret: %v", err)
	}
	copied, err := client.kubernetes.CoreV1().Secrets(targetNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get copied image pull secret: %v", err)
	}
	if copied.Type != corev1.SecretTypeDockerConfigJson || string(copied.Data[corev1.DockerConfigJsonKey]) != string(source.Data[corev1.DockerConfigJsonKey]) {
		t.Fatalf("image pull secret was not copied faithfully: %#v", copied)
	}
	if copied.Labels["app.kubernetes.io/managed-by"] != "ray-train-platform" || copied.Labels["ray-train-platform/source-secret"] != name {
		t.Fatalf("copied image pull secret lacks ownership labels: %#v", copied.Labels)
	}
}

func TestEnsureIDCPrivateKeySecretCreatesOwnerScopedNonRotatingKey(t *testing.T) {
	const namespace = "tenant-team-a"
	const name = "idc-sftp-user-a"
	client := &Client{kubernetes: k8sfake.NewSimpleClientset()}
	if err := client.EnsureIDCPrivateKeySecret(context.Background(), namespace, name, []byte("private-key-material")); err != nil {
		t.Fatalf("create IDC private-key Secret: %v", err)
	}
	secret, err := client.kubernetes.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get IDC private-key Secret: %v", err)
	}
	if secret.Type != corev1.SecretTypeOpaque || string(secret.Data["id_ed25519"]) != "private-key-material" {
		t.Fatalf("unexpected IDC private-key Secret: %#v", secret)
	}
	if secret.Labels["app.kubernetes.io/managed-by"] != "ray-train-platform" || secret.Labels["platform.wellspiking.ai/idc-sftp-key"] != "true" {
		t.Fatalf("IDC private-key Secret lacks ownership labels: %#v", secret.Labels)
	}
	if err := client.EnsureIDCPrivateKeySecret(context.Background(), namespace, name, []byte("replacement-key")); err != nil {
		t.Fatalf("repeat IDC private-key Secret creation: %v", err)
	}
	secret, _ = client.kubernetes.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if string(secret.Data["id_ed25519"]) != "private-key-material" {
		t.Fatalf("existing IDC private key was rotated without an explicit rotation request: %#v", secret.Data)
	}
}
