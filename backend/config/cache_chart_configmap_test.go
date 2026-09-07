package config

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"
)

func cacheChartFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile("../../helm/ray-cache-local/" + path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Execute the chart's actual helper with the small subset of Helm functions it
// uses. Full-chart rendering remains covered by the Helm shell contracts.
func renderCacheConfigMapName(t *testing.T, values map[string]any) (string, error) {
	t.Helper()
	var tmpl *template.Template
	funcs := template.FuncMap{
		"default": func(fallback, value any) any {
			if value == nil || reflect.ValueOf(value).IsZero() {
				return fallback
			}
			return value
		},
		"trunc": func(n int, value string) string {
			if len(value) > n {
				return value[:n]
			}
			return value
		},
		"trimSuffix": func(suffix, value string) string { return strings.TrimSuffix(value, suffix) },
		"quote":      func(value any) string { return fmt.Sprintf("%q", value) },
		"kindIs": func(kind string, value any) bool {
			return value != nil && reflect.TypeOf(value).Kind().String() == kind
		},
		"regexMatch": func(pattern, value string) bool { return regexp.MustCompile(pattern).MatchString(value) },
		"fail":       func(message string) (string, error) { return "", fmt.Errorf("%s", message) },
		"include": func(name string, data any) (string, error) {
			var output bytes.Buffer
			err := tmpl.ExecuteTemplate(&output, name, data)
			return output.String(), err
		},
	}
	var err error
	tmpl, err = template.New("helpers").Funcs(funcs).Parse(cacheChartFile(t, "templates/_helpers.tpl"))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = tmpl.ExecuteTemplate(&output, "ray-cache-local.configMapName", map[string]any{
		"Values": values, "Chart": map[string]any{"Name": "ray-cache-local"},
	})
	if err != nil {
		return "", err
	}
	var name any
	if err := yaml.Unmarshal(output.Bytes(), &name); err != nil {
		return "", err
	}
	if value, ok := name.(string); ok {
		return value, nil
	}
	return "", fmt.Errorf("ConfigMap name must render as a YAML string, got %T", name)
}

func TestCacheChartConfigMapName(t *testing.T) {
	for _, tt := range []struct {
		name   string
		values map[string]any
		want   string
	}{
		{"unset", map[string]any{}, "ray-cache-local-config"},
		{"empty", map[string]any{"existingConfigMap": ""}, "ray-cache-local-config"},
		{"legacy override", map[string]any{"fullnameOverride": "ray-cache-local-data1"}, "ray-cache-local-data1-config"},
		{"external", map[string]any{"existingConfigMap": "cache-data1-managed"}, "cache-data1-managed"},
		{"numeric string", map[string]any{"existingConfigMap": "123"}, "123"},
		{"boolean string", map[string]any{"existingConfigMap": "true"}, "true"},
		{"DNS subdomain", map[string]any{"existingConfigMap": "cache.config-1"}, "cache.config-1"},
		{"maximum name", map[string]any{"existingConfigMap": strings.Repeat("a", 253)}, strings.Repeat("a", 253)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := renderCacheConfigMapName(t, tt.values)
			if err != nil || got != tt.want {
				t.Fatalf("name = %q, err = %v; want %q", got, err, tt.want)
			}
		})
	}
	for _, invalid := range []any{"UPPER", "bad_name", "-bad", "bad-", ".bad", "bad.", "bad..name", "a.-b", "a-.b", " bad", "bad\nname", "{{ .Release.Name }}", strings.Repeat("a", 254), 42, false, []string{"config"}} {
		t.Run(fmt.Sprintf("invalid %v", invalid), func(t *testing.T) {
			_, err := renderCacheConfigMapName(t, map[string]any{"existingConfigMap": invalid})
			if err == nil || !strings.Contains(err.Error(), "existingConfigMap") {
				t.Fatalf("invalid name must fail with field-specific error, got %v", err)
			}
		})
	}
}

func TestCacheChartAllConsumersSelectCompleteConfigMap(t *testing.T) {
	var values map[string]any
	if err := yaml.Unmarshal([]byte(cacheChartFile(t, "values.yaml")), &values); err != nil {
		t.Fatal(err)
	}
	if value, ok := values["existingConfigMap"]; !ok || value != "" {
		t.Fatal("existingConfigMap must default to the empty string")
	}
	for file, count := range map[string]int{"templates/deployment.yaml": 2, "templates/monitor-daemonset.yaml": 1} {
		content := cacheChartFile(t, file)
		if got := strings.Count(content, `include "ray-cache-local.configMapName" .`); got != count {
			t.Errorf("%s: got %d config map references, want %d", file, got, count)
		}
		if strings.Contains(content, `include "ray-cache-local.fullname" . }}-config`) {
			t.Errorf("%s still references legacy config directly", file)
		}
	}
	legacy := cacheChartFile(t, "templates/configmap.yaml")
	if strings.Contains(legacy, "existingConfigMap") || strings.Contains(legacy, "ray-cache-local.configMapName") {
		t.Fatal("legacy Helm ConfigMap must remain independent for rollback")
	}
	for _, key := range []string{"config.json:", "setup:", "teardown:", "helperPod.yaml:", "collect-cache-metrics:"} {
		if !strings.Contains(legacy, key) {
			t.Errorf("legacy complete ConfigMap missing %s", key)
		}
	}
}
