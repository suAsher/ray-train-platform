package config

import (
	"os"
	"strings"
	"testing"
)

func TestPrometheusOperatorProfileUsesGenericNamesAndBoundedStorage(t *testing.T) {
	contents, err := os.ReadFile("../../ops/observability/prometheus-operator/20-values-production.yaml")
	if err != nil {
		t.Fatalf("read Prometheus Operator production values: %v", err)
	}
	values := string(contents)
	for _, expected := range []string{
		"fullnameOverride: prometheus",
		"imagePullSecrets:",
		"name: harbor-registry",
		"replicas: 2",
		"retention: 15d",
		"storage: 50Gi",
		"storageClassName: ebs-ssd",
		"registry: harbor.wellspiking.ai",
		"repository: guofeng.su/prometheus-operator",
		"repository: guofeng.su/prometheus-config-reloader",
		"repository: guofeng.su/thanos",
		"repository: guofeng.su/kube-webhook-certgen",
		"repository: hub/grafana/grafana",
		"repository: guofeng.su/kube-state-metrics",
		"tag: v1.12.1",
		"digest: sha256:8c9bac11973b94b59be88d6e11fee4429aa743c8846cdc75d65b18db33f6a106",
		"sha: 74550ba3e8bf93f47bc574231090d340ae9c01d25cd11ff74799e65f9fdb9a48",
		"sha: 6249f7aaadd3695df637fb2eb4cb9a9955611eee691c3970892fe9c0dc3f2db6",
		"sha: sha256:f6e5dc7c1193809dd59aad3f391eb7df72c18723f8dd505928d6f6152a93e4d3",
		"serviceMonitorSelectorNilUsesHelmValues: false",
		"enabled: true",
		"requiredDuringSchedulingIgnoredDuringExecution:",
		"minAvailable: 1",
	} {
		if !strings.Contains(values, expected) {
			t.Fatalf("Prometheus Operator production values must contain %q", expected)
		}
	}
	if strings.Contains(values, "ray-observability") {
		t.Fatal("Prometheus Operator production values must not use the temporary Ray observability name")
	}
	if strings.Contains(values, "prometheusOperator:\n  replicas:") {
		t.Fatal("kube-prometheus-stack intentionally runs a single restart-safe operator; HA belongs to the Prometheus, Alertmanager and Grafana data plane")
	}
}
