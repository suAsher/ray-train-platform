package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"unicode"
)

type TrainingEngine string

const (
	TrainingEngineRayDDP   TrainingEngine = "ray-ddp"
	TrainingEngineRayTrain TrainingEngine = "ray-train"

	RayVersionLegacy     = "2.35.0"
	RayVersionProduction = "2.56.1"
	RayVersionCanary     = "2.58.0"

	ManagedMaxFailuresLimit           = 10
	ManagedCheckpointEveryEpochsLimit = 100000
	ManagedCheckpointRetentionLimit   = 1000
)

func (engine TrainingEngine) Resolved() TrainingEngine {
	if strings.TrimSpace(string(engine)) == "" {
		return TrainingEngineRayDDP
	}
	return engine
}

type DataMode string

const (
	DataModeMount   DataMode = "mount"
	DataModeCache   DataMode = "cache"
	DataModeRayData DataMode = "ray-data"
)

type RayDataFormat string

const (
	RayDataFormatParquet RayDataFormat = "parquet"
	RayDataFormatImages  RayDataFormat = "images"
)

// RayDataDatasetConfig is an immutable, validated reference to a registered
// dataset below the stable governed input mount. Its fields stay private so
// callers cannot bypass NewRayDataDatasetConfig; JSON decoding revalidates the
// same contract at API and persistence boundaries.
type RayDataDatasetConfig struct {
	format RayDataFormat
	uri    string
}

func NewRayDataDatasetConfig(format RayDataFormat, relativePath string) (RayDataDatasetConfig, error) {
	format = RayDataFormat(strings.TrimSpace(string(format)))
	switch format {
	case RayDataFormatParquet, RayDataFormatImages:
	default:
		return RayDataDatasetConfig{}, fmt.Errorf("unsupported Ray Data format %q", format)
	}

	relativePath = strings.TrimSpace(relativePath)
	if relativePath == "" {
		return RayDataDatasetConfig{}, fmt.Errorf("Ray Data dataset path is required")
	}
	if strings.IndexFunc(relativePath, unicode.IsControl) >= 0 {
		return RayDataDatasetConfig{}, fmt.Errorf("Ray Data dataset path contains control characters")
	}
	if strings.Contains(relativePath, `\`) {
		return RayDataDatasetConfig{}, fmt.Errorf("Ray Data dataset path must use POSIX separators")
	}
	parsed, err := url.Parse(relativePath)
	if err != nil {
		return RayDataDatasetConfig{}, fmt.Errorf("invalid Ray Data dataset path: %w", err)
	}
	if parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil {
		return RayDataDatasetConfig{}, fmt.Errorf("Ray Data dataset path must not contain a URI scheme or credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return RayDataDatasetConfig{}, fmt.Errorf("Ray Data dataset path must not contain a query or fragment")
	}
	if path.IsAbs(relativePath) {
		return RayDataDatasetConfig{}, fmt.Errorf("Ray Data dataset path must be relative to %s", DataMountInputPath)
	}
	for _, segment := range strings.Split(relativePath, "/") {
		if segment == "." || segment == ".." {
			return RayDataDatasetConfig{}, fmt.Errorf("Ray Data dataset path must not contain traversal segments")
		}
	}
	cleaned := path.Clean(relativePath)
	if cleaned == "." || strings.HasPrefix(cleaned, "../") || cleaned != relativePath {
		return RayDataDatasetConfig{}, fmt.Errorf("Ray Data dataset path must be a clean relative path")
	}
	return RayDataDatasetConfig{format: format, uri: path.Join(DataMountInputPath, cleaned)}, nil
}

func (config RayDataDatasetConfig) Format() RayDataFormat { return config.format }

func (config RayDataDatasetConfig) URI() string { return config.uri }

func (config RayDataDatasetConfig) IsZero() bool {
	return config.format == "" && config.uri == ""
}

func (config RayDataDatasetConfig) Validate() error {
	if config.IsZero() {
		return fmt.Errorf("Ray Data dataset config is required")
	}
	prefix := DataMountInputPath + "/"
	if !strings.HasPrefix(config.uri, prefix) {
		return fmt.Errorf("Ray Data dataset URI must stay below %s", DataMountInputPath)
	}
	validated, err := NewRayDataDatasetConfig(config.format, strings.TrimPrefix(config.uri, prefix))
	if err != nil {
		return err
	}
	if validated != config {
		return fmt.Errorf("Ray Data dataset config is not canonical")
	}
	return nil
}

func (config RayDataDatasetConfig) MarshalJSON() ([]byte, error) {
	if config.IsZero() {
		return []byte("null"), nil
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Format RayDataFormat `json:"format"`
		URI    string        `json:"uri"`
	}{Format: config.format, URI: config.uri})
}

func (config *RayDataDatasetConfig) UnmarshalJSON(payload []byte) error {
	if config == nil {
		return fmt.Errorf("Ray Data dataset config target is nil")
	}
	if strings.TrimSpace(string(payload)) == "null" {
		*config = RayDataDatasetConfig{}
		return nil
	}
	var encoded struct {
		Format RayDataFormat `json:"format"`
		URI    string        `json:"uri"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&encoded); err != nil {
		return fmt.Errorf("decode Ray Data dataset config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode Ray Data dataset config: trailing JSON content")
	}
	prefix := DataMountInputPath + "/"
	if !strings.HasPrefix(encoded.URI, prefix) {
		return fmt.Errorf("Ray Data dataset URI must stay below %s", DataMountInputPath)
	}
	validated, err := NewRayDataDatasetConfig(encoded.Format, strings.TrimPrefix(encoded.URI, prefix))
	if err != nil {
		return err
	}
	if validated.uri != encoded.URI {
		return fmt.Errorf("Ray Data dataset URI is not canonical")
	}
	*config = validated
	return nil
}

type CheckpointPolicy struct {
	EveryEpochs int `json:"everyEpochs,omitempty"`
	KeepLatest  int `json:"keepLatest,omitempty"`
	KeepBest    int `json:"keepBest,omitempty"`
}

type ManagedTrainingPolicy struct {
	MaxFailures int                  `json:"maxFailures,omitempty"`
	Checkpoint  CheckpointPolicy     `json:"checkpoint,omitempty"`
	RayData     RayDataDatasetConfig `json:"rayData,omitzero"`
}

func (policy ManagedTrainingPolicy) Validate() error {
	if policy.MaxFailures < 0 || policy.MaxFailures > ManagedMaxFailuresLimit {
		return fmt.Errorf("maxFailures must be between 0 and %d", ManagedMaxFailuresLimit)
	}
	if policy.Checkpoint.EveryEpochs < 0 || policy.Checkpoint.EveryEpochs > ManagedCheckpointEveryEpochsLimit {
		return fmt.Errorf("checkpoint.everyEpochs must be between 0 and %d", ManagedCheckpointEveryEpochsLimit)
	}
	if policy.Checkpoint.KeepLatest < 0 || policy.Checkpoint.KeepLatest > ManagedCheckpointRetentionLimit {
		return fmt.Errorf("checkpoint.keepLatest must be between 0 and %d", ManagedCheckpointRetentionLimit)
	}
	if policy.Checkpoint.KeepBest < 0 || policy.Checkpoint.KeepBest > ManagedCheckpointRetentionLimit {
		return fmt.Errorf("checkpoint.keepBest must be between 0 and %d", ManagedCheckpointRetentionLimit)
	}
	if !policy.RayData.IsZero() {
		if err := policy.RayData.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (policy ManagedTrainingPolicy) ValidateDataMode(mode DataMode) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if mode == DataModeRayData {
		return policy.RayData.Validate()
	}
	if !policy.RayData.IsZero() {
		return fmt.Errorf("Ray Data dataset config requires ray-data mode")
	}
	return nil
}

// ValidateResolvedDataMode enforces the server-resolved storage contract that
// cannot be checked while validating an untrusted submission payload. Ray
// Data reads only from the governed input PVC mounted at its stable path.
func (policy ManagedTrainingPolicy) ValidateResolvedDataMode(mode DataMode, mounts ResolvedDataSpaceMounts) error {
	if mode != DataModeRayData {
		return nil
	}
	if mounts.Input == nil {
		return fmt.Errorf("ray-data requires a resolved governed input mount")
	}
	if mounts.Input.MountPath != DataMountInputPath || !mounts.Input.ReadOnly {
		return fmt.Errorf("ray-data input must be mounted read-only at %s", DataMountInputPath)
	}
	return nil
}
