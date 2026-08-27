package spkrayjob

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The daily loop is "edit code, submit". Retyping image digests, GPU counts and
// data paths on every submit is the friction this file removes: the repository
// carries its own submission defaults and `spk-rayjob submit` takes no flags.
func TestProjectFileSuppliesSubmitDefaults(t *testing.T) {
	root := t.TempDir()
	contents := `name: bevfusion-lidar
image: harbor.example.com/team/bevfusion@sha256:1111111111111111111111111111111111111111111111111111111111111111
entrypoint: python tools/westwell_train.py configs/x.yaml --launcher pytorch
workers: 1
gpusPerWorker: 8
cpuPerWorker: 32
memoryPerWorker: 128Gi
executionMode: torchrun
engine: ray-train
cache:
  mode: runtime
  size: 200Gi
  preload: input
input:
  space: public
  path: bevfusion/2026-08-0429
output:
  path: bevfusion-lidar
`
	if err := os.WriteFile(filepath.Join(root, projectFileName), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := loadProject(root)
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	if project.Name != "bevfusion-lidar" || project.GPUsPerWorker != 8 || project.ExecutionMode != "torchrun" || project.Engine != "ray-train" {
		t.Fatalf("unexpected project: %+v", project)
	}
	if project.Input.Space != "public" || project.Input.Path != "bevfusion/2026-08-0429" {
		t.Fatalf("unexpected input: %+v", project.Input)
	}
	if project.MemoryPerWorker != "128Gi" || project.CPUPerWorker != 32 {
		t.Fatalf("unexpected resources: %+v", project)
	}
	if project.Cache.Mode != "runtime" || project.Cache.Size != "200Gi" || project.Cache.Preload != "input" {
		t.Fatalf("unexpected cache defaults: %+v", project.Cache)
	}
}

// An absent project file is normal for a one-off submission: every value can
// still come from flags.
func TestLoadProjectReturnsEmptyDefaultsWhenAbsent(t *testing.T) {
	project, err := loadProject(t.TempDir())
	if err != nil {
		t.Fatalf("an absent project file must not be an error: %v", err)
	}
	if project.Name != "" || project.Workers != 0 {
		t.Fatalf("expected zero defaults, got %+v", project)
	}
}

func TestProjectFileStrictlyAcceptsUnquotedOffCacheMode(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, projectFileName), []byte("cache:\n  mode: off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadProject(root)
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	if loaded.Cache.Mode != "off" || loaded.Cache.Size != "" {
		t.Fatalf("unexpected off cache shape: %+v", loaded.Cache)
	}
}

func TestProjectFileRejectsBooleanLikeCacheModes(t *testing.T) {
	for _, mode := range []string{"false", "no"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			contents := "cache:\n  mode: " + mode + "\n"
			if err := os.WriteFile(filepath.Join(root, projectFileName), []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadProject(root)
			if err == nil || !strings.Contains(err.Error(), "cache.mode") {
				t.Fatalf("unquoted %q must be rejected clearly, got %v", mode, err)
			}
		})
	}
}

// Explicit flags always win: a project file is a default, not a lock.
func TestExplicitFlagsOverrideProjectDefaults(t *testing.T) {
	project := project{
		Name: "from-file", Workers: 1, GPUsPerWorker: 8, ExecutionMode: "torchrun", Engine: "ray-train",
		Cache: projectCache{Mode: "runtime", Size: "100Gi", Preload: "input"},
	}
	resolved := project.merge(submitOverrides{
		Name: "from-flag", GPUsPerWorker: 2, Engine: "ray-ddp", Cache: projectCache{Size: "200Gi"},
		providedGPUs: true, providedName: true, providedEngine: true, providedCacheSize: true,
	})

	if resolved.Name != "from-flag" || resolved.GPUsPerWorker != 2 {
		t.Fatalf("flags must win, got %+v", resolved)
	}
	if resolved.Workers != 1 || resolved.ExecutionMode != "torchrun" {
		t.Fatalf("unset flags must keep project defaults, got %+v", resolved)
	}
	if resolved.Engine != "ray-ddp" {
		t.Fatalf("an explicit engine flag must override the project default, got %+v", resolved)
	}
	if resolved.Cache.Mode != "runtime" || resolved.Cache.Size != "200Gi" {
		t.Fatalf("cache flags must override project defaults independently, got %+v", resolved.Cache)
	}

	modeOnly := project.merge(submitOverrides{
		Cache: projectCache{Mode: "off"}, providedCacheMode: true,
	})
	if modeOnly.Cache.Mode != "off" || modeOnly.Cache.Size != "" {
		t.Fatalf("an explicit off mode must clear the inherited size, got %+v", modeOnly.Cache)
	}
	if modeOnly.Cache.Preload != "" {
		t.Fatalf("an explicit off mode must clear inherited preload, got %+v", modeOnly.Cache)
	}

	preloadOnly := project.merge(submitOverrides{
		Cache: projectCache{Preload: ""}, providedCachePreload: true,
	})
	if preloadOnly.Cache.Mode != "runtime" || preloadOnly.Cache.Size != "100Gi" || preloadOnly.Cache.Preload != "" {
		t.Fatalf("an explicit empty preload must disable only automatic staging, got %+v", preloadOnly.Cache)
	}

	runtimeModeOnly := project.merge(submitOverrides{
		Cache: projectCache{Mode: "runtime"}, providedCacheMode: true,
	})
	if runtimeModeOnly.Cache.Mode != "runtime" || runtimeModeOnly.Cache.Size != "100Gi" {
		t.Fatalf("runtime mode alone must preserve the project size, got %+v", runtimeModeOnly.Cache)
	}
}

func TestWriteProjectProducesAReadableStarterFile(t *testing.T) {
	root := t.TempDir()
	if err := writeProject(root, project{
		Name: "my-training", Image: "registry.example/img@sha256:2222222222222222222222222222222222222222222222222222222222222222",
		Entrypoint: "python train.py", Workers: 1, GPUsPerWorker: 8, CPUPerWorker: 32,
		MemoryPerWorker: "128Gi", ExecutionMode: "torchrun", Engine: "ray-train", Cache: projectCache{Mode: "runtime", Size: "200Gi", Preload: "input"},
	}); err != nil {
		t.Fatalf("write project: %v", err)
	}
	written, err := os.ReadFile(filepath.Join(root, projectFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"name: my-training", "gpusPerWorker: 8", "executionMode: torchrun", "engine: ray-train", "cache:", "mode: runtime", "size: 200Gi", "preload: input"} {
		if !strings.Contains(string(written), expected) {
			t.Fatalf("expected %q in the starter file:\n%s", expected, written)
		}
	}
	reloaded, err := loadProject(root)
	if err != nil || reloaded.Name != "my-training" || reloaded.GPUsPerWorker != 8 || reloaded.Engine != "ray-train" || reloaded.Cache.Mode != "runtime" || reloaded.Cache.Size != "200Gi" {
		t.Fatalf("the starter file must round-trip, got %+v (%v)", reloaded, err)
	}
}

func TestProjectJobSpecDefaultsEngineWithoutForgingRayVersion(t *testing.T) {
	value := project{
		Name: "legacy", Image: "registry.example/img@sha256:" + strings.Repeat("4", 64),
		Entrypoint: "python train.py", Workers: 1, GPUsPerWorker: 1,
	}
	spec, err := value.jobSpec()
	if err != nil {
		t.Fatal(err)
	}
	if spec.TrainingEngine != "ray-ddp" || spec.RayVersion != "" || spec.Managed.MaxFailures != 0 {
		t.Fatalf("legacy project must use the compatibility engine without a client Ray version: %+v", spec)
	}

	value.Engine = "ray-train"
	spec, err = value.jobSpec()
	if err != nil {
		t.Fatal(err)
	}
	if spec.TrainingEngine != "ray-train" || spec.RayVersion != "" || spec.Managed.MaxFailures != 2 {
		t.Fatalf("managed project must use the design recovery default without a client Ray version: %+v", spec)
	}
}

func TestProjectJobSpecRejectsUnknownEngine(t *testing.T) {
	_, err := (project{
		Name: "bad-engine", Image: "registry.example/img@sha256:" + strings.Repeat("5", 64),
		Entrypoint: "python train.py", Engine: "ray-magic", Workers: 1, GPUsPerWorker: 1,
	}).jobSpec()
	if err == nil || !strings.Contains(err.Error(), "ray-ddp") || !strings.Contains(err.Error(), "ray-train") {
		t.Fatalf("unknown engine must fail with the supported values, got %v", err)
	}
}

func TestStarterAndHelpExplainOptionalDisposableRuntimeCache(t *testing.T) {
	root := t.TempDir()
	if err := writeProject(root, project{Name: "my-training"}); err != nil {
		t.Fatalf("write project: %v", err)
	}
	written, err := os.ReadFile(filepath.Join(root, projectFileName))
	if err != nil {
		t.Fatal(err)
	}
	for label, text := range map[string]string{"starter": string(written), "help": helpText} {
		for _, expected := range []string{"可选", "临时", "/mnt/storage/public", "不会自动缓存"} {
			if !strings.Contains(text, expected) {
				t.Fatalf("%s must explain %q:\n%s", label, expected, text)
			}
		}
	}
}

// Refusing to clobber an existing file keeps `init` safe to run in a directory
// somebody already configured.
func TestWriteProjectRefusesToOverwriteAnExistingFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, projectFileName), []byte("name: existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeProject(root, project{Name: "replacement"}); err == nil {
		t.Fatal("expected an error rather than silently replacing the project file")
	}
	written, _ := os.ReadFile(filepath.Join(root, projectFileName))
	if !strings.Contains(string(written), "existing") {
		t.Fatalf("the original file must survive, got %q", written)
	}
}

// A starter file that greets the user with `input: {}` reads like a broken
// template. Unset optional sections are omitted entirely.
func TestStarterFileOmitsUnsetOptionalSections(t *testing.T) {
	root := t.TempDir()
	if err := writeProject(root, project{
		Name: "my-training", Image: "registry.example/img@sha256:3333333333333333333333333333333333333333333333333333333333333333",
		Entrypoint: "python train.py", Workers: 1, GPUsPerWorker: 8, ExecutionMode: "torchrun",
		Output: projectLocation{Path: "my-training"},
	}); err != nil {
		t.Fatalf("write project: %v", err)
	}
	written, err := os.ReadFile(filepath.Join(root, projectFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, noise := range []string{"input: {}", "checkpoint: {}"} {
		if strings.Contains(string(written), noise) {
			t.Fatalf("the starter file must not contain %q:\n%s", noise, written)
		}
	}
	if !strings.Contains(string(written), "path: my-training") {
		t.Fatalf("a set section must still be written:\n%s", written)
	}
}
