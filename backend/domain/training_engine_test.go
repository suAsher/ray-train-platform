package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTrainingEngineDefaultsOldJobsToRayDDP(t *testing.T) {
	if got := (TrainingEngine("")).Resolved(); got != TrainingEngineRayDDP {
		t.Fatalf("old job resolved to %q", got)
	}
}

func TestTrainingEngineDefaultsWhitespaceToRayDDP(t *testing.T) {
	if got := (TrainingEngine(" \t\n")).Resolved(); got != TrainingEngineRayDDP {
		t.Fatalf("whitespace engine resolved to %q", got)
	}
}

func TestManagedTrainingPolicyIsBounded(t *testing.T) {
	policy := ManagedTrainingPolicy{
		MaxFailures: 2,
		Checkpoint:  CheckpointPolicy{EveryEpochs: 1, KeepLatest: 3, KeepBest: 1},
	}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedTrainingPolicyAcceptsEveryUpperBoundary(t *testing.T) {
	policy := ManagedTrainingPolicy{
		MaxFailures: 10,
		Checkpoint:  CheckpointPolicy{EveryEpochs: 100000, KeepLatest: 1000, KeepBest: 1000},
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("upper boundaries must remain valid: %v", err)
	}
}

func TestManagedTrainingPolicyRejectsValuesOutsideBounds(t *testing.T) {
	tests := []struct {
		name    string
		policy  ManagedTrainingPolicy
		wantErr string
	}{
		{name: "negative max failures", policy: ManagedTrainingPolicy{MaxFailures: -1}, wantErr: "maxFailures"},
		{name: "too many failures", policy: ManagedTrainingPolicy{MaxFailures: 11}, wantErr: "maxFailures"},
		{name: "negative checkpoint frequency", policy: ManagedTrainingPolicy{Checkpoint: CheckpointPolicy{EveryEpochs: -1}}, wantErr: "everyEpochs"},
		{name: "too many checkpoint epochs", policy: ManagedTrainingPolicy{Checkpoint: CheckpointPolicy{EveryEpochs: 100001}}, wantErr: "everyEpochs"},
		{name: "negative latest retention", policy: ManagedTrainingPolicy{Checkpoint: CheckpointPolicy{KeepLatest: -1}}, wantErr: "keepLatest"},
		{name: "too many latest checkpoints", policy: ManagedTrainingPolicy{Checkpoint: CheckpointPolicy{KeepLatest: 1001}}, wantErr: "keepLatest"},
		{name: "negative best retention", policy: ManagedTrainingPolicy{Checkpoint: CheckpointPolicy{KeepBest: -1}}, wantErr: "keepBest"},
		{name: "too many best checkpoints", policy: ManagedTrainingPolicy{Checkpoint: CheckpointPolicy{KeepBest: 1001}}, wantErr: "keepBest"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.policy.Validate()
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("expected error containing %q, got %v", test.wantErr, err)
			}
		})
	}
}

func TestRayDataDatasetConfigValidatesStableMountedPaths(t *testing.T) {
	config, err := NewRayDataDatasetConfig(RayDataFormatImages, "shards/train")
	if err != nil {
		t.Fatalf("new Ray Data config: %v", err)
	}
	if config.Format() != RayDataFormatImages || config.URI() != "/mnt/data/input/shards/train" {
		t.Fatalf("unexpected immutable config: format=%q uri=%q", config.Format(), config.URI())
	}

	want := config
	if _, err := NewRayDataDatasetConfig(RayDataFormatParquet, "tables/train.parquet"); err != nil {
		t.Fatalf("registered Parquet dataset was rejected: %v", err)
	}
	if !reflect.DeepEqual(config, want) {
		t.Fatal("constructing another config mutated the first config")
	}
}

func TestRayDataFileStagingAcceptsTheSelectedInputRoot(t *testing.T) {
	config, err := NewRayDataDatasetConfig(RayDataFormatFiles, ".")
	if err != nil {
		t.Fatalf("create file staging dataset: %v", err)
	}
	if config.Format() != RayDataFormatFiles || config.URI() != DataMountInputPath {
		t.Fatalf("unexpected file staging dataset: format=%q uri=%q", config.Format(), config.URI())
	}
}

func TestRayDataDatasetConfigRejectsUnsupportedOrUnsafeInputs(t *testing.T) {
	tests := []struct {
		name   string
		format RayDataFormat
		path   string
	}{
		{name: "unsupported format", format: "pkl", path: "bevfusion/train.pkl"},
		{name: "absolute path", format: RayDataFormatParquet, path: "/mnt/storage/public/train.parquet"},
		{name: "traversal", format: RayDataFormatImages, path: "images/../private"},
		{name: "storage URI", format: RayDataFormatParquet, path: "tos://bucket/train.parquet"},
		{name: "raw credentials", format: RayDataFormatImages, path: "user:secret@host/images"},
		{name: "control character", format: RayDataFormatImages, path: "images\nsecret"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewRayDataDatasetConfig(test.format, test.path); err == nil {
				t.Fatalf("unsafe Ray Data config was accepted: format=%q path=%q", test.format, test.path)
			}
		})
	}
}

func TestManagedPolicyRequiresRayDataConfigOnlyInRayDataMode(t *testing.T) {
	config, err := NewRayDataDatasetConfig(RayDataFormatImages, "images/train")
	if err != nil {
		t.Fatal(err)
	}
	policy := ManagedTrainingPolicy{RayData: config}
	if err := policy.ValidateDataMode(DataModeRayData); err != nil {
		t.Fatalf("valid Ray Data policy was rejected: %v", err)
	}
	if err := (ManagedTrainingPolicy{}).ValidateDataMode(DataModeRayData); err == nil {
		t.Fatal("ray-data mode accepted a missing dataset config")
	}
	if err := policy.ValidateDataMode(DataModeMount); err == nil {
		t.Fatal("mount mode accepted a Ray Data config")
	}
}

func TestManagedPolicyDistinguishesStreamingFromNodeLocalStaging(t *testing.T) {
	stream, err := NewRayDataDatasetConfig(RayDataFormatImages, "images/train")
	if err != nil {
		t.Fatal(err)
	}
	files, err := NewRayDataDatasetConfig(RayDataFormatFiles, ".")
	if err != nil {
		t.Fatal(err)
	}
	if err := (ManagedTrainingPolicy{RayData: stream}).ValidateDataMode(DataModeRayDataStage); err == nil || !strings.Contains(err.Error(), "files") {
		t.Fatalf("staging accepted a streaming dataset: %v", err)
	}
	if err := (ManagedTrainingPolicy{RayData: files}).ValidateDataMode(DataModeRayData); err == nil || !strings.Contains(err.Error(), "parquet or images") {
		t.Fatalf("streaming accepted a file staging dataset: %v", err)
	}
	if err := (ManagedTrainingPolicy{RayData: files}).ValidateDataMode(DataModeRayDataStage); err != nil {
		t.Fatalf("file staging policy rejected: %v", err)
	}
}

func TestManagedPolicyRequiresResolvedGovernedInputForRayData(t *testing.T) {
	config, err := NewRayDataDatasetConfig(RayDataFormatImages, "images/train")
	if err != nil {
		t.Fatal(err)
	}
	policy := ManagedTrainingPolicy{RayData: config}
	validInput := &ResolvedDataMount{
		Space: DataSpaceTeamShared, BindingSpace: DataSpaceTeamShared,
		ClaimName: "data-team-a", SubPath: "datasets/train", MountPath: DataMountInputPath, ReadOnly: true,
	}

	for _, test := range []struct {
		name    string
		mode    DataMode
		mounts  ResolvedDataSpaceMounts
		wantErr string
	}{
		{name: "mount mode has no ray data requirement", mode: DataModeMount},
		{name: "ray data missing input", mode: DataModeRayData, wantErr: "resolved governed input mount"},
		{name: "ray data writable input", mode: DataModeRayData, mounts: ResolvedDataSpaceMounts{Input: &ResolvedDataMount{
			Space: DataSpaceTeamShared, BindingSpace: DataSpaceTeamShared,
			ClaimName: "data-team-a", SubPath: "datasets/train", MountPath: DataMountInputPath,
		}}, wantErr: "read-only at /mnt/data/input"},
		{name: "ray data wrong input path", mode: DataModeRayData, mounts: ResolvedDataSpaceMounts{Input: &ResolvedDataMount{
			Space: DataSpaceTeamShared, BindingSpace: DataSpaceTeamShared,
			ClaimName: "data-team-a", SubPath: "datasets/train", MountPath: "/mnt/data/other", ReadOnly: true,
		}}, wantErr: "read-only at /mnt/data/input"},
		{name: "ray data governed input", mode: DataModeRayData, mounts: ResolvedDataSpaceMounts{Input: validInput}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := policy.ValidateResolvedDataMode(test.mode, test.mounts)
			if test.wantErr == "" && err != nil {
				t.Fatalf("validate resolved data mode: %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", test.wantErr, err)
			}
		})
	}
}

func TestRayDataDatasetConfigJSONRoundTripRevalidatesTheStableURI(t *testing.T) {
	config, err := NewRayDataDatasetConfig(RayDataFormatParquet, "tables/train.parquet")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(ManagedTrainingPolicy{RayData: config})
	if err != nil {
		t.Fatalf("marshal Ray Data policy: %v", err)
	}
	if !strings.Contains(string(payload), `"rayData":{"format":"parquet","uri":"/mnt/data/input/tables/train.parquet"}`) {
		t.Fatalf("unexpected Ray Data policy JSON: %s", payload)
	}

	var decoded ManagedTrainingPolicy
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal Ray Data policy: %v", err)
	}
	if decoded.RayData != config {
		t.Fatalf("Ray Data policy changed across JSON: got %#v want %#v", decoded.RayData, config)
	}

	unsafe := [][]byte{
		[]byte(`{"rayData":{"format":"parquet","uri":"tos://access:secret@bucket/train.parquet"}}`),
		[]byte(`{"rayData":{"format":"parquet","uri":"/mnt/data/input/train.parquet","accessKey":"raw-credential"}}`),
	}
	for _, payload := range unsafe {
		if err := json.Unmarshal(payload, &decoded); err == nil {
			t.Fatalf("JSON decoding accepted raw object-store credentials: %s", payload)
		}
	}
}
