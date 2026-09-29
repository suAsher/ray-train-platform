package k8s

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"ray-train-platform-backend/environmentbuild"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEnvironmentJobsSelectDestinationWithoutChangingSources(t *testing.T) {
	for _, host := range []string{"", "harbor.wellspiking.ai", "harbor.qomolo.com"} {
		t.Run(host, func(t *testing.T) {
			r := environmentTestRunner()
			r.config.ImagePullSecrets = []string{"pull-robot", "qomolo-pull-robot"}
			b := environmentTestBuild(r)
			b.RegistryHost = host
			wantHost := host
			if wantHost == "" {
				wantHost = "harbor.wellspiking.ai"
			}
			for _, phase := range []string{environmentbuild.Building, environmentbuild.Validating, environmentbuild.Pushing, environmentbuild.VerifyingPull} {
				b.Status = phase
				job, err := r.renderJob(b)
				if err != nil {
					t.Fatalf("%s: %v", phase, err)
				}
				pod := job.Spec.Template.Spec
				container := pod.Containers[0]
				if !reflect.DeepEqual(pod.ImagePullSecrets, []corev1.LocalObjectReference{{Name: "pull-robot"}, {Name: "qomolo-pull-robot"}}) {
					t.Fatal("platform-managed pull credentials were changed")
				}
				if phase == environmentbuild.VerifyingPull {
					if container.Image != wantHost+"/public/environment@"+b.ImageDigest || len(pod.Volumes) != 0 || len(container.VolumeMounts) != 0 {
						t.Fatal("destination verification must pin the selected host without credential mounts")
					}
				} else if !strings.HasPrefix(container.Image, "harbor.wellspiking.ai/") {
					t.Fatal("destination selection changed a trusted execution image")
				}
				if phase == environmentbuild.Validating {
					if !strings.Contains(strings.Join(container.Args, " "), "--base "+r.config.BaseImage) {
						t.Fatal("assembler base changed to the destination")
					}
					for _, volume := range pod.Volumes {
						if volume.Secret != nil && volume.Secret.SecretName != "pull-robot" {
							t.Fatal("assembler received credentials other than the source pull robot")
						}
					}
				}
				if phase == environmentbuild.Pushing && !strings.Contains(strings.Join(container.Args, " "), "--registry-host "+wantHost+" ") {
					t.Fatal("publisher did not receive the frozen destination host")
				}
			}
		})
	}
}

func TestEnvironmentRunnerRejectsInvalidHostBeforeAnyClusterAction(t *testing.T) {
	for _, host := range []string{"evil.example", "https://harbor.qomolo.com", "harbor.qomolo.com:443", "harbor.qomolo.com.evil.example", "harbor.qomolo.com/", "harbor.qomolo.com\n"} {
		for _, phase := range []string{environmentbuild.Capturing, environmentbuild.Building, environmentbuild.Validating, environmentbuild.Pushing, environmentbuild.VerifyingPull} {
			t.Run(host+"/"+phase, func(t *testing.T) {
				r := environmentTestRunner()
				client := fake.NewSimpleClientset()
				r.client = NewClientFromInterfaces(nil, client)
				b := environmentTestBuild(r)
				b.Status, b.RegistryHost = phase, host
				if _, err := r.Step(context.Background(), b, &environmentbuild.Credentials{Username: "alice", Secret: "test-push-only"}); err == nil {
					t.Fatal("invalid registry host accepted")
				}
				if len(client.Actions()) != 0 {
					t.Fatal("invalid host reached Kubernetes before validation")
				}
				if phase != environmentbuild.Capturing {
					if _, err := r.renderJob(b); err == nil {
						t.Fatal("invalid host was rendered")
					}
				}
			})
		}
	}
}

func TestEnvironmentRunnerCannotReplayDestinationAcrossHosts(t *testing.T) {
	for _, phase := range []string{environmentbuild.Building, environmentbuild.Pushing, environmentbuild.VerifyingPull} {
		t.Run(phase, func(t *testing.T) {
			r := environmentTestRunner()
			b := environmentTestBuild(r)
			b.Status, b.RegistryHost = phase, "harbor.wellspiking.ai"
			credentials := &environmentbuild.Credentials{Username: "alice", Secret: "test-push-only"}
			if _, err := r.Step(context.Background(), b, credentials); err != nil {
				t.Fatal(err)
			}
			b.RegistryHost = "harbor.qomolo.com"
			if _, err := r.Step(context.Background(), b, credentials); err == nil {
				t.Fatal("existing runtime resources were reused for another registry")
			}
		})
	}
}

func TestEnvironmentLegacyRuntimeResourcesRemainWellspikingOnly(t *testing.T) {
	r := environmentTestRunner()
	b := environmentTestBuild(r)
	b.Status = environmentbuild.VerifyingPull
	job, err := r.renderJob(b)
	if err != nil {
		t.Fatal(err)
	}
	delete(job.Annotations, "platform.wellspiking.ai/environment-registry-host")
	r.client = NewClientFromInterfaces(nil, fake.NewSimpleClientset(job))
	if _, err := r.Step(context.Background(), b, nil); err != nil {
		t.Fatalf("legacy Wellspiking job was not accepted: %v", err)
	}
	b.RegistryHost = "harbor.qomolo.com"
	if _, err := r.Step(context.Background(), b, nil); err == nil {
		t.Fatal("legacy resource was reinterpreted as a Qomolo job")
	}
}

func TestEnvironmentRunnerQomoloCredentialIsEphemeralAndNotPullAuth(t *testing.T) {
	r := environmentTestRunner()
	r.config.ImagePullSecrets = []string{"pull-robot", "qomolo-pull-robot"}
	b := environmentTestBuild(r)
	b.Status, b.RegistryHost = environmentbuild.Pushing, "harbor.qomolo.com"
	ctx := context.Background()
	if _, err := r.Step(ctx, b, &environmentbuild.Credentials{Username: "alice", Secret: "test-push-only"}); err != nil {
		t.Fatal(err)
	}
	secrets, err := r.client.kubernetes.CoreV1().Secrets(r.config.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(secrets.Items) != 1 {
		t.Fatalf("ephemeral secrets: %v %v", secrets, err)
	}
	secret := secrets.Items[0]
	if secret.Type != corev1.SecretTypeOpaque || secret.Immutable == nil || !*secret.Immutable || string(secret.Data["secret"]) != "test-push-only" {
		t.Fatal("push credential was persisted as reusable pull authorization")
	}
	b.Status = environmentbuild.VerifyingPull
	job, err := r.renderJob(b)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(job)
	if err != nil || strings.Contains(string(encoded), secret.Name) || strings.Contains(string(encoded), "test-push-only") {
		t.Fatal("pull verification received the user's publishing credential")
	}
	if err := r.Cleanup(ctx, b, false); err != nil {
		t.Fatal(err)
	}
	secrets, err = r.client.kubernetes.CoreV1().Secrets(r.config.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(secrets.Items) != 0 {
		t.Fatal("ephemeral Qomolo publishing credential was not cleaned")
	}
}

func TestEnvironmentSourceImagePolicyRemainsWellspikingOnly(t *testing.T) {
	for _, image := range []string{"harbor.qomolo.com/platform/image@sha256:" + strings.Repeat("a", 64), "harbor.wellspiking.ai/platform/image:latest"} {
		if environmentPinnedImage(image) {
			t.Fatal("source image policy accepted an unsupported source")
		}
	}
}
