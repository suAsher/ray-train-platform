package domain

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTask16PortalPayloadMatchesTrainingEngineDomainContract(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	script := filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", "scripts", "e2e-training.sh"))

	for _, test := range []struct {
		engine      TrainingEngine
		wantManaged bool
	}{
		{engine: TrainingEngineRayDDP},
		{engine: TrainingEngineRayTrain, wantManaged: true},
	} {
		t.Run(string(test.engine), func(t *testing.T) {
			command := exec.Command("bash", "-c", `source "$1"; TRAINING_IMAGE='registry.example/train@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'; PORTAL_SOURCE_SNAPSHOT_ID='snapshot-contract'; build_portal_payload 'acc-contract-job' "$2" 1 1 single_gpu ''`, "fixture", script, string(test.engine))
			payload, err := command.Output()
			if err != nil {
				t.Fatalf("build Portal payload: %v", err)
			}
			var request struct {
				Spec JobSpec `json:"spec"`
			}
			if err := json.Unmarshal(payload, &request); err != nil {
				t.Fatalf("decode Portal payload: %v", err)
			}
			request.Spec.RayVersion = RayVersionProduction
			if err := request.Spec.validateTrainingRuntime(); err != nil {
				t.Fatalf("payload violates engine domain contract: %v", err)
			}
			managed := request.Spec.Managed != (ManagedTrainingPolicy{})
			if managed != test.wantManaged {
				t.Fatalf("managed policy present=%v, want %v", managed, test.wantManaged)
			}
		})
	}
}
