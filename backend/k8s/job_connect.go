package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

var (
	ErrJobWorkerNotFound   = errors.New("job worker not found")
	ErrJobWorkerNotRunning = errors.New("job worker is not running")
)

// JobWorkerTarget is resolved from the authenticated platform job. Clients
// never supply namespaces, Pod names, container names, or commands.
type JobWorkerTarget struct {
	Namespace     string
	PodName       string
	ContainerName string
}

// ResolveJobWorker returns a stable zero-based worker ordinal. Head and
// submitter Pods are deliberately excluded from this user-facing operation.
func (c *Client) ResolveJobWorker(ctx context.Context, namespace, jobID string, worker int) (JobWorkerTarget, error) {
	if c == nil || c.kubernetes == nil {
		return JobWorkerTarget{}, fmt.Errorf("Kubernetes client is not initialized")
	}
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(jobID) == "" || worker < 0 {
		return JobWorkerTarget{}, ErrJobWorkerNotFound
	}
	pods, err := c.kubernetes.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: platformJobIDLabel + "=" + jobID,
	})
	if err != nil {
		return JobWorkerTarget{}, fmt.Errorf("list job workers: %w", err)
	}
	workers := make([]corev1.Pod, 0, len(pods.Items))
	for _, pod := range pods.Items {
		if podRuntimeRole(pod) == "worker" {
			workers = append(workers, pod)
		}
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].Name < workers[j].Name })
	if worker >= len(workers) {
		return JobWorkerTarget{}, ErrJobWorkerNotFound
	}
	pod := workers[worker]
	if pod.Status.Phase != corev1.PodRunning {
		return JobWorkerTarget{}, ErrJobWorkerNotRunning
	}
	container := ""
	for _, candidate := range pod.Spec.Containers {
		if candidate.Name == "ray-worker" {
			container = candidate.Name
			break
		}
		if container == "" {
			container = candidate.Name
		}
	}
	if container == "" {
		return JobWorkerTarget{}, ErrJobWorkerNotFound
	}
	return JobWorkerTarget{Namespace: namespace, PodName: pod.Name, ContainerName: container}, nil
}

// ConnectJobWorker opens a fixed interactive terminal in the resolved worker.
// The command is platform-owned; this API intentionally cannot execute an
// arbitrary caller-provided command.
func (c *Client) ConnectJobWorker(ctx context.Context, target JobWorkerTarget, stdin io.Reader, stdout io.Writer) error {
	if c == nil || c.kubernetes == nil || c.restConfig == nil {
		return fmt.Errorf("Kubernetes worker connection is not configured")
	}
	request := c.kubernetes.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(target.Namespace).
		Name(target.PodName).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: target.ContainerName,
			Command:   []string{"/bin/sh", "-lc", "if command -v bash >/dev/null 2>&1; then exec bash -l; else exec sh -l; fi"},
			Stdin:     true, Stdout: true, TTY: true,
		}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(c.restConfig, "POST", request.URL())
	if err != nil {
		return fmt.Errorf("create worker connection: %w", err)
	}
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Tty: true}); err != nil {
		return fmt.Errorf("stream worker connection: %w", err)
	}
	return nil
}
