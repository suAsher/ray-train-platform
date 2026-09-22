package assistantidle

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRuntimeConfigRejectsUnsafeOrUnknownConfiguration(t *testing.T) {
	cfg := RuntimeConfig{RuntimeType:"rayservice", InstanceID: "assistant-idle", LeaseName: "assistant-idle", Render: RenderConfig{Name: "assistant-idle", Namespace: "raytrain-assistant-idle", QueueName: "assistant-idle", ServeImage: "harbor.wellspiking.ai/public/assistant@sha256:" + strings.Repeat("a", 64), ModelPVC: "assistant-model", ImagePullSecrets: []string{"harbor-registry"}, GateURL: "http://assistant-gate.raytrain-assistant-idle.svc.cluster.local:8080/gate", AllowedWorkerNodes: []string{"shared-gpu"}, RequiredNodeLabels: map[string]string{"accelerator": "nvidia-rtx-4090"}}}
	data, _ := json.Marshal(cfg)
	if _, err := ParseRuntimeConfig(strings.NewReader(string(data))); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"{}", " null"} {
		if _, err := ParseRuntimeConfig(strings.NewReader(string(data) + suffix)); err == nil {
			t.Fatal("accepted trailing JSON")
		}
	}
	if _, err := ParseRuntimeConfig(strings.NewReader(`{"unknown":true}`)); err == nil {
		t.Fatal("accepted unknown field")
	}
	missingRuntime := cfg
	missingRuntime.RuntimeType=""
	missingData,_:=json.Marshal(missingRuntime)
	if _,err:=ParseRuntimeConfig(strings.NewReader(string(missingData)));err==nil {t.Fatal("missing runtimeType silently enabled legacy Ray")}
	cfg.Render.Namespace = "ray-train-platform"
	data, _ = json.Marshal(cfg)
	if _, err := ParseRuntimeConfig(strings.NewReader(string(data))); err == nil {
		t.Fatal("accepted shared namespace")
	}
}
