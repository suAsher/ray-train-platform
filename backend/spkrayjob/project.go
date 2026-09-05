package spkrayjob

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	yamlv3 "gopkg.in/yaml.v3"
	"ray-train-platform-backend/domain"
	"sigs.k8s.io/yaml"
)

func parseTrainingEngine(value string) (domain.TrainingEngine, error) {
	engine := domain.TrainingEngine(strings.TrimSpace(value)).Resolved()
	if engine != domain.TrainingEngineRayDDP && engine != domain.TrainingEngineRayTrain {
		return "", fmt.Errorf("不支持的训练引擎 %q；可选值为 ray-ddp 或 ray-train", value)
	}
	return engine, nil
}

func parseDataMode(value string, cache projectCache) (domain.DataMode, error) {
	mode := domain.DataMode(strings.TrimSpace(value))
	if mode == "" {
		if strings.TrimSpace(cache.Preload) == string(domain.CachePreloadInput) {
			return domain.DataModeCache, nil
		}
		return domain.DataModeMount, nil
	}
	switch mode {
	case domain.DataModeMount, domain.DataModeCache, domain.DataModeRayData, domain.DataModeRayDataStage, domain.DataModeStreaming:
		return mode, nil
	default:
		return "", fmt.Errorf("不支持的数据模式 %q；可选值为 mount、cache、ray-data-stage、ray-data 或 streaming", value)
	}
}

type datasetFlagOverride struct {
	Reference       domain.DatasetReference
	DatasetProvided bool
	VersionProvided bool
}

// parseDatasetFlag supports both the explicit two-flag form and the concise
// DATASET:VERSION form documented for daily submissions. Dataset references
// remain public catalogue identifiers; object-store paths are rejected later
// by the shared domain validator rather than being translated by the client.
func parseDatasetFlag(datasetValue, versionValue string, datasetProvided, versionProvided bool) (datasetFlagOverride, error) {
	override := datasetFlagOverride{
		Reference: domain.DatasetReference{
			Dataset: strings.TrimSpace(datasetValue),
			Version: strings.TrimSpace(versionValue),
		},
		DatasetProvided: datasetProvided,
		VersionProvided: versionProvided,
	}
	if !datasetProvided {
		return override, nil
	}
	if strings.ContainsAny(override.Reference.Dataset, "/\\") {
		return datasetFlagOverride{}, fmt.Errorf("--dataset 只接受公开数据集 ID/slug，不能传对象存储或 manifest 路径")
	}
	if !strings.Contains(override.Reference.Dataset, ":") {
		return override, nil
	}
	if strings.Count(override.Reference.Dataset, ":") != 1 {
		return datasetFlagOverride{}, fmt.Errorf("--dataset 必须是 DATASET 或 DATASET:VERSION，不能是对象存储路径")
	}
	if versionProvided {
		return datasetFlagOverride{}, fmt.Errorf("--dataset 中已包含版本时不能再传 --dataset-version")
	}
	parts := strings.SplitN(override.Reference.Dataset, ":", 2)
	dataset, version := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if dataset == "" || version == "" {
		return datasetFlagOverride{}, fmt.Errorf("--dataset 简写必须同时包含数据集和版本：DATASET:VERSION")
	}
	override.Reference = domain.DatasetReference{Dataset: dataset, Version: version}
	override.VersionProvided = true
	return override, nil
}

// projectFileName is committed next to the training code. It turns the daily
// loop into "edit, then run spk-rayjob submit" instead of retyping an approved
// image reference, GPU layout and data paths on every iteration.
const projectFileName = ".spk-rayjob.yaml"

type projectLocation struct {
	Space string `json:"space,omitempty"`
	Path  string `json:"path,omitempty"`
}

type projectCache struct {
	Mode    string `json:"mode,omitempty"`
	Size    string `json:"size,omitempty"`
	Preload string `json:"preload,omitempty"`
}

type projectRayData struct {
	Format string `json:"format,omitempty"`
	Path   string `json:"path,omitempty"`
}

type project struct {
	Name            string                    `json:"name,omitempty"`
	Image           string                    `json:"image,omitempty"`
	Entrypoint      string                    `json:"entrypoint,omitempty"`
	Engine          string                    `json:"engine,omitempty"`
	DataMode        string                    `json:"dataMode,omitempty"`
	DatasetRef      domain.DatasetReference   `json:"datasetRef,omitempty"`
	CachePolicy     domain.DatasetCachePolicy `json:"cachePolicy,omitempty"`
	Workers         int                       `json:"workers,omitempty"`
	GPUsPerWorker   int                       `json:"gpusPerWorker,omitempty"`
	CPUPerWorker    int64                     `json:"cpuPerWorker,omitempty"`
	MemoryPerWorker string                    `json:"memoryPerWorker,omitempty"`
	ExecutionMode   string                    `json:"executionMode,omitempty"`
	Cache           projectCache              `json:"cache,omitempty"`
	RayData         projectRayData            `json:"rayData,omitempty"`
	Input           projectLocation           `json:"input,omitempty"`
	Checkpoint      projectLocation           `json:"checkpoint,omitempty"`
	Output          projectLocation           `json:"output,omitempty"`
}

// submitOverrides carries the flags a caller actually typed. The provided*
// fields exist because zero is a meaningful value for a flag that was never
// set, and a project default must survive it.
type submitOverrides struct {
	Name            string
	Image           string
	Entrypoint      string
	Engine          string
	DataMode        string
	DatasetRef      domain.DatasetReference
	CachePolicy     domain.DatasetCachePolicy
	Workers         int
	GPUsPerWorker   int
	CPUPerWorker    int64
	MemoryPerWorker string
	ExecutionMode   string
	Cache           projectCache
	RayData         projectRayData
	Input           projectLocation
	Checkpoint      projectLocation
	Output          projectLocation

	providedName           bool
	providedImage          bool
	providedEntrypoint     bool
	providedEngine         bool
	providedDataMode       bool
	providedDataset        bool
	providedDatasetVersion bool
	providedCachePolicy    bool
	providedDatasetSites   bool
	providedWorkers        bool
	providedGPUs           bool
	providedCPU            bool
	providedMemory         bool
	providedMode           bool
	providedCacheMode      bool
	providedCacheSize      bool
	providedCachePreload   bool
	providedRayData        bool
	providedInput          bool
	providedCheckpoint     bool
	providedOutput         bool
}

func (base project) merge(overrides submitOverrides) project {
	merged := base
	if overrides.providedName {
		merged.Name = overrides.Name
	}
	if overrides.providedImage {
		merged.Image = overrides.Image
	}
	if overrides.providedEntrypoint {
		merged.Entrypoint = overrides.Entrypoint
	}
	if overrides.providedEngine {
		merged.Engine = overrides.Engine
	}
	if overrides.providedDataMode {
		merged.DataMode = overrides.DataMode
	}
	if overrides.providedDataset {
		merged.DatasetRef.Dataset = overrides.DatasetRef.Dataset
		if merged.DatasetRef.Dataset != base.DatasetRef.Dataset {
			merged.DatasetRef.Sites = ""
		}
	}
	if overrides.providedDatasetVersion {
		merged.DatasetRef.Version = overrides.DatasetRef.Version
	}
	if overrides.providedDatasetSites {
		merged.DatasetRef.Sites = overrides.DatasetRef.Sites
	}
	if overrides.providedCachePolicy {
		merged.CachePolicy = overrides.CachePolicy
	}
	if overrides.providedWorkers {
		merged.Workers = overrides.Workers
	}
	if overrides.providedGPUs {
		merged.GPUsPerWorker = overrides.GPUsPerWorker
	}
	if overrides.providedCPU {
		merged.CPUPerWorker = overrides.CPUPerWorker
	}
	if overrides.providedMemory {
		merged.MemoryPerWorker = overrides.MemoryPerWorker
	}
	if overrides.providedMode {
		merged.ExecutionMode = overrides.ExecutionMode
	}
	if overrides.providedCacheMode {
		merged.Cache.Mode = overrides.Cache.Mode
		if strings.TrimSpace(overrides.Cache.Mode) == "off" {
			merged.Cache.Size = ""
			merged.Cache.Preload = ""
		}
	}
	if overrides.providedCacheSize {
		merged.Cache.Size = overrides.Cache.Size
	}
	if overrides.providedCachePreload {
		merged.Cache.Preload = overrides.Cache.Preload
	}
	if overrides.providedRayData {
		merged.RayData = overrides.RayData
	}
	if overrides.providedInput {
		merged.Input = overrides.Input
	}
	if overrides.providedCheckpoint {
		merged.Checkpoint = overrides.Checkpoint
	}
	if overrides.providedOutput {
		merged.Output = overrides.Output
	}
	return merged
}

// loadProject reads the submission defaults committed with the code. A missing
// file is not an error: every value can still be supplied by a flag.
func loadProject(directory string) (project, error) {
	path := filepath.Join(directory, projectFileName)
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return project{}, nil
	}
	if err != nil {
		return project{}, fmt.Errorf("read %s: %w", projectFileName, err)
	}
	var loaded project
	if err := yaml.UnmarshalStrict(contents, &loaded); err != nil {
		return project{}, fmt.Errorf("%s is not a valid project file: %w", projectFileName, err)
	}
	// YAML 1.1 treats an unquoted `off` as boolean false. The JSON-tag-aware
	// decoder converts every false-like spelling to the string "false". Restore
	// only the documented lowercase `off`; rejecting the other spellings avoids
	// silently turning a typo such as `no` into a valid cache mode.
	if loaded.Cache.Mode == "false" {
		if !hasUnquotedOffCacheMode(contents) {
			return project{}, fmt.Errorf("%s is not a valid project file: cache.mode 只能使用 off 或 runtime，不能使用 YAML 布尔值 false/no", projectFileName)
		}
		loaded.Cache.Mode = "off"
	}
	return loaded, nil
}

func hasUnquotedOffCacheMode(contents []byte) bool {
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(contents, &document); err != nil || len(document.Content) == 0 {
		return false
	}
	cache := yamlMappingValue(document.Content[0], "cache")
	mode := yamlMappingValue(cache, "mode")
	return mode != nil && mode.Kind == yamlv3.ScalarNode && mode.Style == 0 && mode.Value == "off"
}

func yamlMappingValue(mapping *yamlv3.Node, key string) *yamlv3.Node {
	if mapping == nil || mapping.Kind != yamlv3.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

// starterProject is the shape written by init. The location fields are
// pointers so an unset one is omitted entirely: a starter file that greets the
// user with `input: {}` reads like a broken template rather than a default.
type starterProject struct {
	Name            string                    `json:"name,omitempty"`
	Image           string                    `json:"image,omitempty"`
	Entrypoint      string                    `json:"entrypoint,omitempty"`
	Engine          string                    `json:"engine,omitempty"`
	DataMode        string                    `json:"dataMode,omitempty"`
	DatasetRef      *domain.DatasetReference  `json:"datasetRef,omitempty"`
	CachePolicy     domain.DatasetCachePolicy `json:"cachePolicy,omitempty"`
	Workers         int                       `json:"workers,omitempty"`
	GPUsPerWorker   int                       `json:"gpusPerWorker,omitempty"`
	CPUPerWorker    int64                     `json:"cpuPerWorker,omitempty"`
	MemoryPerWorker string                    `json:"memoryPerWorker,omitempty"`
	ExecutionMode   string                    `json:"executionMode,omitempty"`
	Cache           *projectCache             `json:"cache,omitempty"`
	RayData         *projectRayData           `json:"rayData,omitempty"`
	Input           *projectLocation          `json:"input,omitempty"`
	Checkpoint      *projectLocation          `json:"checkpoint,omitempty"`
	Output          *projectLocation          `json:"output,omitempty"`
}

func newStarterProject(value project) starterProject {
	optional := func(location projectLocation) *projectLocation {
		if location.Space == "" && location.Path == "" {
			return nil
		}
		return &location
	}
	optionalCache := func(cache projectCache) *projectCache {
		if cache.Mode == "" && cache.Size == "" && cache.Preload == "" {
			return nil
		}
		return &cache
	}
	optionalRayData := func(config projectRayData) *projectRayData {
		if config.Format == "" && config.Path == "" {
			return nil
		}
		return &config
	}
	optionalDatasetRef := func(reference domain.DatasetReference) *domain.DatasetReference {
		if reference.IsZero() {
			return nil
		}
		copy := reference
		return &copy
	}
	return starterProject{
		Name: value.Name, Image: value.Image, Entrypoint: value.Entrypoint, Engine: value.Engine, DataMode: value.DataMode,
		DatasetRef: optionalDatasetRef(value.DatasetRef), CachePolicy: value.CachePolicy,
		Workers: value.Workers, GPUsPerWorker: value.GPUsPerWorker, CPUPerWorker: value.CPUPerWorker,
		MemoryPerWorker: value.MemoryPerWorker, ExecutionMode: value.ExecutionMode,
		Cache:   optionalCache(value.Cache),
		RayData: optionalRayData(value.RayData),
		Input:   optional(value.Input), Checkpoint: optional(value.Checkpoint), Output: optional(value.Output),
	}
}

func writeProject(directory string, value project) error {
	path := filepath.Join(directory, projectFileName)
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists; edit it instead of running init again", projectFileName)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s: %w", projectFileName, err)
	}
	body, err := yaml.Marshal(newStarterProject(value))
	if err != nil {
		return fmt.Errorf("encode %s: %w", projectFileName, err)
	}
	document := projectFileHeader + string(body)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", projectFileName, err)
	}
	return nil
}

const projectFileHeader = `# spk-rayjob submission defaults for this repository.
#
# Commit this file so every teammate submits the same shape of job. Any value
# can still be overridden by a flag on a single run, for example:
#   spk-rayjob submit --gpus-per-worker 2 --name quick-check
#
# engine:
#   ray-ddp    兼容引擎：Ray Actor 编排 + 平台 torchrun
#   ray-train  托管引擎：Ray Train workers + 故障恢复/Checkpoint
# 省略 engine 时保持 ray-ddp 兼容行为；Ray 版本由平台根据镜像固化。
# dataMode: mount（直读）、cache（NVMe 预热）、ray-data-stage（Ray Data + NVMe）、
#           streaming（不可变数据集版本 + Ray Data 流式读取）。
# streaming 只传入公开的数据集引用，不填 TOS 或内部 manifest 路径：
# datasetRef:
#   dataset: <数据集 ID 或 slug>
#   version: <版本 ID 或 latest>
# cachePolicy: bounded  # auto | off | bounded
#
# executionMode:
#   single_gpu  1 worker x 1 GPU
#   torchrun    1 worker x N GPUs  (single machine, multi GPU)
#   ray_train   N workers x M GPUs (multi machine)
# Do not put torchrun in entrypoint: the platform adds it for you.
#
# 可选 runtime 临时缓存会随任务销毁。只配置 mode/size 不会自动缓存 /mnt/storage/public；
# 加 preload: input 后，平台自动预热所选具体输入目录。省略 cache 时缓存关闭：
# cache:
#   mode: runtime
#   size: <平台允许的容量>
#   preload: input
`

// projectRelativeName derives a stable default job name from the directory a
// user runs init in, so `spk-rayjob init` needs no arguments at all.
func projectRelativeName(directory string) string {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "training-job"
	}
	name := sanitizeJobName(filepath.Base(absolute))
	if name == "" {
		return "training-job"
	}
	return name
}

// sanitizeJobName reduces a directory name to the lowercase DNS label the
// platform requires, so init never produces a file that fails at submit time.
func sanitizeJobName(value string) string {
	lowered := strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, char := range lowered {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			builder.WriteRune(char)
		case char == '-' || char == '_' || char == '.' || char == ' ':
			builder.WriteByte('-')
		}
	}
	trimmed := strings.Trim(builder.String(), "-")
	for strings.Contains(trimmed, "--") {
		trimmed = strings.ReplaceAll(trimmed, "--", "-")
	}
	if len(trimmed) > 63 {
		trimmed = strings.Trim(trimmed[:63], "-")
	}
	return trimmed
}
