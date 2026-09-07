package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"
)

func renderOnboardingChart(t *testing.T, overrides map[string]any, namespace string) (string, error) {
	t.Helper()
	dir := "../../helm/ray-node-onboarding"
	raw, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if err != nil {
		return "", err
	}
	var values map[string]any
	if err := yaml.Unmarshal(raw, &values); err != nil {
		return "", err
	}
	for k, v := range overrides {
		values[k] = v
	}
	var tmpl *template.Template
	funcs := template.FuncMap{
		"list":   func(values ...any) []any { return values },
		"add":    func(a, b int) int { return a + b },
		"quote":  func(v any) string { return fmt.Sprintf("%q", v) },
		"toJson": func(v any) string { b, _ := json.Marshal(v); return string(b) },
		"toYaml": func(v any) string { b, _ := yaml.Marshal(v); return strings.TrimSuffix(string(b), "\n") },
		"nindent": func(n int, v string) string {
			return "\n" + strings.Repeat(" ", n) + strings.ReplaceAll(v, "\n", "\n"+strings.Repeat(" ", n))
		},
		"kindIs":     func(kind string, v any) bool { return v != nil && reflect.TypeOf(v).Kind().String() == kind },
		"regexMatch": func(pattern, v string) bool { return regexp.MustCompile(pattern).MatchString(v) },
		"fail":       func(message string) (string, error) { return "", fmt.Errorf("%s", message) },
		"include": func(name string, data any) (string, error) {
			var b bytes.Buffer
			err := tmpl.ExecuteTemplate(&b, name, data)
			return b.String(), err
		},
	}
	tmpl = template.New("chart").Funcs(funcs)
	paths, err := filepath.Glob(filepath.Join(dir, "templates", "*"))
	if err != nil {
		return "", err
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if _, err := tmpl.New(filepath.Base(path)).Parse(string(raw)); err != nil {
			return "", err
		}
	}
	var result bytes.Buffer
	for _, path := range paths {
		if strings.HasSuffix(path, ".yaml") {
			var b bytes.Buffer
			err := tmpl.ExecuteTemplate(&b, filepath.Base(path), map[string]any{"Values": values, "Release": map[string]any{"Namespace": namespace}})
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(b.String()) != "" {
				result.WriteString("---\n")
				result.Write(b.Bytes())
				result.WriteByte('\n')
			}
		}
	}
	return result.String(), nil
}

func onboardingEnabledValues() map[string]any {
	return map[string]any{"enabled": true, "activateController": true, "image": "registry.example/onboarding@sha256:" + strings.Repeat("a", 64), "nfs": map[string]any{"shares": []any{map[string]any{"server": "nfs.example", "path": "/datasets"}}}}
}

func TestNodeOnboardingChartDisabledByDefault(t *testing.T) {
	got, err := renderOnboardingChart(t, nil, "ray-cache-local")
	if err != nil || strings.TrimSpace(got) != "" {
		t.Fatalf("disabled render = %q, %v", got, err)
	}
}

func TestNodeOnboardingChartStagesPoliciesBeforeController(t *testing.T) {
	values := onboardingEnabledValues()
	values["activateController"] = false
	got, err := renderOnboardingChart(t, values, "ray-cache-local")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "replicas: 0") || !strings.Contains(got, "kind: ValidatingAdmissionPolicyBinding") {
		t.Fatal("staging must install policies with no running controller")
	}
}

func TestNodeOnboardingChartEnabledBoundaries(t *testing.T) {
	got, err := renderOnboardingChart(t, onboardingEnabledValues(), "ray-cache-local")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"kind: Deployment", "replicas: 1", "type: Recreate", "serviceAccountName: node-onboarding", "runAsNonRoot: true", "readOnlyRootFilesystem: true", "allowPrivilegeEscalation: false", "drop: [\"ALL\"]",
		"--data1-config-map=ray-cache-local-data1-runtime", "--data2-config-map=ray-cache-local-data2-runtime", "--probe-service-account=node-onboarding-probe", "--leader-election-lease=node-onboarding",
		"kind: ValidatingAdmissionPolicy", "kind: ValidatingAdmissionPolicyBinding", "failurePolicy: Fail", "validationActions: [Deny]", "request.userInfo.username == 'system:serviceaccount:ray-cache-local:node-onboarding'",
		"object.spec == oldObject.spec", "object.metadata.finalizers", "object.metadata.ownerReferences", "platform.wellspiking.ai/cache-ready", "platform.wellspiking.ai/onboarding-node-uid", "config.json",
		"/proc/1/mountinfo", "Directory", "readOnly", "/app/node-onboarding", "prepare", "shares.json", "object.spec.automountServiceAccountToken == false",
	} {
		if !strings.Contains(got, required) {
			t.Errorf("missing boundary %q", required)
		}
	}
	for _, forbidden := range []string{"kind: Service\n", "kind: Namespace", "kind: DaemonSet", "privileged: true", "hostPID: true", "hostNetwork: true", "resources: [\"secrets\"]", "resources: [\"jobs\"]"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("unexpected %q", forbidden)
		}
	}
	decoder := yaml.NewDecoder(strings.NewReader(got))
	for {
		var object map[string]any
		if err := decoder.Decode(&object); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatal(err)
		}
		kind, _ := object["kind"].(string)
		if kind == "ClusterRole" || kind == "Role" {
			for _, rule := range object["rules"].([]any) {
				r := rule.(map[string]any)
				resources, verbs := fmt.Sprint(r["resources"]), fmt.Sprint(r["verbs"])
				if strings.Contains(resources, "persistentvolumes") && verbs != "[get]" {
					t.Errorf("PV writes/list not authorized: %v", r)
				}
				if strings.Contains(resources, "configmaps") && !strings.Contains(verbs, "get") && !strings.Contains(fmt.Sprint(r["resourceNames"]), "runtime") && fmt.Sprint(r["resourceNames"]) != "[node-onboarding-state]" {
					t.Errorf("ConfigMap write must name external maps: %v", r)
				}
				if strings.Contains(verbs, "*") || strings.Contains(resources, "*") {
					t.Errorf("wildcard RBAC: %v", r)
				}
			}
		}
	}
}

func TestNodeOnboardingChartRejectsUnsafeConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name      string
		overrides map[string]any
		namespace string
	}{
		{"namespace", nil, "default"},
		{"mutable image", map[string]any{"image": "registry.example/controller:latest"}, "ray-cache-local"},
		{"no NFS", map[string]any{"nfs": map[string]any{"shares": []any{}}}, "ray-cache-local"},
		{"NFS relative", map[string]any{"nfs": map[string]any{"shares": []any{map[string]any{"server": "nfs.example", "path": "relative"}}}}, "ray-cache-local"},
		{"NFS traversal", map[string]any{"nfs": map[string]any{"shares": []any{map[string]any{"server": "nfs.example", "path": "/data/../root"}}}}, "ray-cache-local"},
		{"same maps", map[string]any{"data1ConfigMap": "same", "data2ConfigMap": "same"}, "ray-cache-local"},
		{"legacy map", map[string]any{"data1ConfigMap": "ray-cache-local-data1-config"}, "ray-cache-local"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			values := onboardingEnabledValues()
			for k, v := range tt.overrides {
				values[k] = v
			}
			if _, err := renderOnboardingChart(t, values, tt.namespace); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

func TestNodeOnboardingProofAndProbeContract(t *testing.T) {
	got, err := renderOnboardingChart(t, onboardingEnabledValues(), "ray-cache-local")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"--state-config-map=node-onboarding-state", "name: node-onboarding-state", "helm.sh/resource-policy: keep",
		"state.json", "object.spec.preemptionPolicy == 'Never'", "object.spec.schedulerName == 'default-scheduler'",
		"object.spec.dnsPolicy == 'ClusterFirst'", "!has(object.spec.dnsConfig)", "object.spec.hostAliases",
		"/sys/dev/block", "/sys/devices", "/host-sys/dev/block", "/host-sys/devices",
		"size(object.spec.volumes) == 5", "test -r /nfs-0/.",
	} {
		if !strings.Contains(got, required) {
			t.Errorf("missing new fixed boundary %q", required)
		}
	}
	if strings.Contains(got, "ls /nfs-") {
		t.Error("NFS readiness must use the bounded read test")
	}
	decoder := yaml.NewDecoder(strings.NewReader(got))
	for {
		var obj map[string]any
		if err := decoder.Decode(&obj); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatal(err)
		}
		meta, _ := obj["metadata"].(map[string]any)
		if obj["kind"] == "ConfigMap" && meta["name"] == "node-onboarding-state" {
			if _, ok := obj["data"]; ok {
				t.Error("Helm must not own proof-store data")
			}
		}
		if obj["kind"] == "Role" || obj["kind"] == "ClusterRole" {
			for _, item := range obj["rules"].([]any) {
				r := item.(map[string]any)
				resources := fmt.Sprint(r["resources"])
				verbs := fmt.Sprint(r["verbs"])
				if strings.Contains(verbs, "watch") {
					t.Errorf("controller polls; unexpected watch permission %v", r)
				}
				if strings.Contains(verbs, "list") && resources != "[nodes]" {
					t.Errorf("only node listing is needed: %v", r)
				}
				if resources == "[events]" && verbs != "[create]" {
					t.Errorf("events are create only: %v", r)
				}
			}
		}
	}
}

func TestNodeOnboardingPodResourceCELContract(t *testing.T) {
	got, err := renderOnboardingChart(t, onboardingEnabledValues(), "ray-cache-local")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"dyn(c.resources).requests == {'cpu': '20m', 'memory': '32Mi'}",
		"dyn(c.resources).limits == {'cpu': '200m', 'memory': '64Mi'}",
	} {
		if !strings.Contains(got, required) {
			t.Errorf("resource limits require schema-compatible exact map comparison: %s", required)
		}
	}
}

func TestNodeOnboardingPVCResourceCELContract(t *testing.T) {
	got, err := renderOnboardingChart(t, onboardingEnabledValues(), "ray-cache-local")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"dyn(object.spec.resources).requests == {'storage': '1Gi'}",
		"!has(dyn(object.spec.resources).limits) || size(dyn(object.spec.resources).limits) == 0",
	} {
		if !strings.Contains(got, required) {
			t.Errorf("PVC resources require schema-compatible exact comparison: %s", required)
		}
	}
}

func TestNodeOnboardingProbePriorityClass(t *testing.T) {
	got, err := renderOnboardingChart(t, onboardingEnabledValues(), "ray-cache-local")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"kind: PriorityClass", "value: -1000", "globalDefault: false", "preemptionPolicy: Never", "object.spec.priorityClassName == 'node-onboarding-probe'", "object.spec.priority == -1000"} {
		if !strings.Contains(got, required) {
			t.Errorf("missing fixed non-preempting priority contract %q", required)
		}
	}
}

func TestNodeOnboardingAllowsOnlyBoundedTopologyLabels(t *testing.T) {
	got, err := renderOnboardingChart(t, onboardingEnabledValues(), "ray-cache-local")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"'topology.kubernetes.io/region'", "'topology.kubernetes.io/zone'", "size(object.metadata.labels[k]) <= 63", "object.metadata.labels.all(k,"} {
		if !strings.Contains(got, required) {
			t.Errorf("missing bounded topology label contract %q", required)
		}
	}
	if strings.Contains(got, "size(object.metadata.labels) == 1") {
		t.Error("must allow the two standard admission-injected topology labels")
	}
}

func TestNodeOnboardingAllowsOnlyExactVKEProbeMetadata(t *testing.T) {
	got, err := renderOnboardingChart(t, onboardingEnabledValues(), "ray-cache-local")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"k == 'vke.volcengine.com/cello-pod-evict-policy' && object.metadata.annotations[k] == 'allow'",
		"'cpu': '20m', 'memory': '32Mi', 'vke.volcengine.com/eni-ip': '1'",
		"'cpu': '200m', 'memory': '64Mi', 'vke.volcengine.com/eni-ip': '1'",
		"('vke.volcengine.com/eni-ip' in dyn(c.resources).requests) == ('vke.volcengine.com/eni-ip' in dyn(c.resources).limits)",
	} {
		if !strings.Contains(got, required) {
			t.Errorf("missing exact VKE mutation contract %q", required)
		}
	}
}
