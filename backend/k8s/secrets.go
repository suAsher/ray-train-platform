package k8s

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const imagePullSecretSourceLabel = "ray-train-platform/source-secret"

const idcSFTPPrivateKeySecretLabel = "platform.wellspiking.ai/idc-sftp-key"

const (
	TrainingEventTokenBytes = 32
	TrainingEventTokenKey   = "token"
	trainingEventJobLabel   = "platform.wellspiking.ai/training-event-job"
)

func TrainingEventSecretName(jobID string) string {
	return "raytrain-event-" + strings.TrimSpace(jobID)
}

// EnsureTrainingEventTokenSecret returns the raw 256-bit credential while
// persisting only its URL-safe representation in one immutable, job-labelled
// Secret. Retries reuse the same credential; they never rotate a running job.
func (c *Client) EnsureTrainingEventTokenSecret(ctx context.Context, namespace, jobID string) ([]byte, error) {
	if c == nil || c.kubernetes == nil {
		return nil, fmt.Errorf("Kubernetes client is not initialized")
	}
	namespace = strings.TrimSpace(namespace)
	jobID = strings.TrimSpace(jobID)
	name := TrainingEventSecretName(jobID)
	if namespace == "" || !isDNSLabel(namespace) || jobID == "" || !isDNSLabel(jobID) || !isDNSSubdomain(name) || len(name) > 63 {
		return nil, fmt.Errorf("namespace and job ID must form valid DNS labels")
	}
	secrets := c.kubernetes.CoreV1().Secrets(namespace)
	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return trainingEventTokenFromSecret(existing, jobID)
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get training event Secret %s/%s: %w", namespace, name, err)
	}
	raw := make([]byte, TrainingEventTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate training event token: %w", err)
	}
	immutable := true
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/part-of": "ray-train-platform", "app.kubernetes.io/managed-by": "ray-train-platform",
				trainingEventJobLabel: jobID,
			},
		},
		Type: corev1.SecretTypeOpaque, Immutable: &immutable,
		Data: map[string][]byte{TrainingEventTokenKey: []byte(base64.RawURLEncoding.EncodeToString(raw))},
	}
	created, err := secrets.Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		created, err = secrets.Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("create training event Secret %s/%s: %w", namespace, name, err)
	}
	return trainingEventTokenFromSecret(created, jobID)
}

func trainingEventTokenFromSecret(secret *corev1.Secret, jobID string) ([]byte, error) {
	if secret == nil || secret.Type != corev1.SecretTypeOpaque || secret.Immutable == nil || !*secret.Immutable ||
		secret.Labels["app.kubernetes.io/managed-by"] != "ray-train-platform" || secret.Labels[trainingEventJobLabel] != jobID {
		return nil, fmt.Errorf("refusing to use unmanaged training event Secret")
	}
	raw, err := base64.RawURLEncoding.DecodeString(string(secret.Data[TrainingEventTokenKey]))
	if err != nil || len(raw) != TrainingEventTokenBytes {
		return nil, fmt.Errorf("training event Secret has an invalid token")
	}
	return append([]byte(nil), raw...), nil
}

// EnsureImagePullSecret copies one explicitly configured registry credential
// from the platform namespace into a tenant namespace. Secret references never
// cross namespace boundaries, so this is required before a Ray Pod on a new
// node can pull a private image. It refuses to adopt or replace a tenant-owned
// Secret with the same name.
func (c *Client) EnsureImagePullSecret(ctx context.Context, sourceNamespace, targetNamespace, name string) error {
	if c == nil || c.kubernetes == nil {
		return fmt.Errorf("Kubernetes client is not initialized")
	}
	sourceNamespace = strings.TrimSpace(sourceNamespace)
	targetNamespace = strings.TrimSpace(targetNamespace)
	name = strings.TrimSpace(name)
	if sourceNamespace == "" || targetNamespace == "" || name == "" {
		return fmt.Errorf("source namespace, target namespace, and Secret name are required")
	}
	if sourceNamespace == targetNamespace {
		return nil
	}
	source, err := c.kubernetes.CoreV1().Secrets(sourceNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get platform image pull Secret %s/%s: %w", sourceNamespace, name, err)
	}
	if source.Type != corev1.SecretTypeDockerConfigJson || len(source.Data[corev1.DockerConfigJsonKey]) == 0 {
		return fmt.Errorf("platform Secret %s/%s is not a non-empty docker config JSON Secret", sourceNamespace, name)
	}
	targets := c.kubernetes.CoreV1().Secrets(targetNamespace)
	existing, err := targets.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if existing.Labels["app.kubernetes.io/managed-by"] != "ray-train-platform" || existing.Labels[imagePullSecretSourceLabel] != name {
			return fmt.Errorf("refusing to replace unmanaged image pull Secret %s/%s", targetNamespace, name)
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get tenant image pull Secret %s/%s: %w", targetNamespace, name, err)
	}
	copy := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: targetNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/part-of":    "ray-train-platform",
				"app.kubernetes.io/managed-by": "ray-train-platform",
				imagePullSecretSourceLabel:     name,
			},
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: append([]byte(nil), source.Data[corev1.DockerConfigJsonKey]...)},
	}
	if _, err := targets.Create(ctx, copy, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create tenant image pull Secret %s/%s: %w", targetNamespace, name, err)
	}
	return nil
}

// EnsureGitCredentialSecret writes a private-repository token into the tenant
// namespace. Keeping it in Kubernetes rather than the platform database means
// a database dump never contains usable repository credentials, and the
// materializer reads it through the normal Secret mechanism.
func (c *Client) EnsureGitCredentialSecret(ctx context.Context, namespace, name, username, token string) error {
	if c == nil || c.kubernetes == nil {
		return fmt.Errorf("Kubernetes client is not initialized")
	}
	if namespace == "" || name == "" || token == "" {
		return fmt.Errorf("namespace, name and token are required")
	}
	if username == "" {
		// Most providers accept any non-empty username when a token is used as
		// the password.
		username = "git"
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/part-of":    "ray-train-platform",
				"app.kubernetes.io/managed-by": "ray-train-platform",
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"GIT_USERNAME": username,
			"GIT_TOKEN":    token,
		},
	}
	secrets := c.kubernetes.CoreV1().Secrets(namespace)
	_, err := secrets.Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := secrets.Get(ctx, name, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("get existing git credential secret: %w", getErr)
		}
		if existing.Labels["app.kubernetes.io/managed-by"] != "ray-train-platform" {
			return fmt.Errorf("refusing to update unmanaged git credential secret %s/%s", namespace, name)
		}
		updated := existing.DeepCopy()
		updated.Type = corev1.SecretTypeOpaque
		updated.StringData = map[string]string{
			"GIT_USERNAME": username,
			"GIT_TOKEN":    token,
		}
		if _, err = secrets.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update git credential secret: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("create git credential secret: %w", err)
	}
	return nil
}

// ReadGitCredentialSecret is restricted to platform-managed credential
// Secrets. It is used only for an administrator-approved HTTPS repository
// connectivity check; the caller must never serialize the returned values.
func (c *Client) ReadGitCredentialSecret(ctx context.Context, namespace, name string) (string, string, error) {
	if c == nil || c.kubernetes == nil {
		return "", "", fmt.Errorf("Kubernetes client is not initialized")
	}
	secret, err := c.kubernetes.CoreV1().Secrets(strings.TrimSpace(namespace)).Get(ctx, strings.TrimSpace(name), metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("get git credential secret: %w", err)
	}
	if secret.Labels["app.kubernetes.io/managed-by"] != "ray-train-platform" || secret.Type != corev1.SecretTypeOpaque {
		return "", "", fmt.Errorf("refusing to read unmanaged git credential secret")
	}
	username := string(secret.Data["GIT_USERNAME"])
	token := string(secret.Data["GIT_TOKEN"])
	// Kubernetes API servers materialize StringData into Data. The fallback
	// keeps the lightweight fake client usable in isolated tests.
	if username == "" {
		username = secret.StringData["GIT_USERNAME"]
	}
	if token == "" {
		token = secret.StringData["GIT_TOKEN"]
	}
	if username == "" || token == "" {
		return "", "", fmt.Errorf("git credential secret is incomplete")
	}
	return username, token, nil
}

// EnsureIDCPrivateKeySecret stores a platform-generated SFTP private key only
// in the caller's tenant namespace. Existing managed keys are deliberately not
// rotated by retries; rotation must be an explicit audited operation followed
// by remote public-key installation and connection verification.
func (c *Client) EnsureIDCPrivateKeySecret(ctx context.Context, namespace, name string, privateKey []byte) error {
	if c == nil || c.kubernetes == nil {
		return fmt.Errorf("Kubernetes client is not initialized")
	}
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" || name == "" || len(privateKey) == 0 {
		return fmt.Errorf("namespace, Secret name and IDC private key are required")
	}
	secrets := c.kubernetes.CoreV1().Secrets(namespace)
	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if existing.Labels["app.kubernetes.io/managed-by"] != "ray-train-platform" || existing.Labels[idcSFTPPrivateKeySecretLabel] != "true" {
			return fmt.Errorf("refusing to use unmanaged IDC private-key Secret %s/%s", namespace, name)
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get IDC private-key Secret %s/%s: %w", namespace, name, err)
	}
	immutable := true
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/part-of":    "ray-train-platform",
				"app.kubernetes.io/managed-by": "ray-train-platform",
				idcSFTPPrivateKeySecretLabel:   "true",
			},
		},
		Type:      corev1.SecretTypeOpaque,
		Immutable: &immutable,
		Data:      map[string][]byte{"id_ed25519": append([]byte(nil), privateKey...)},
	}
	if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create IDC private-key Secret %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (c *Client) DeleteSecret(ctx context.Context, namespace, name string) error {
	if c == nil || c.kubernetes == nil {
		return fmt.Errorf("Kubernetes client is not initialized")
	}
	err := c.kubernetes.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete secret: %w", err)
	}
	return nil
}
