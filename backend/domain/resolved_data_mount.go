package domain

import (
	"fmt"
	"strings"
)

const (
	DataMountInputPath      = "/mnt/data/input"
	DataMountCheckpointPath = "/mnt/data/checkpoints"
	DataMountOutputPath     = "/mnt/data/output"
)

// ResolvedDataMount is a control-plane-generated PVC mount. It is persisted
// with a job after authorization, but it is never trusted from a submission
// request and never returned through the public job API.
type ResolvedDataMount struct {
	Space        DataSpaceID `json:"space"`
	BindingSpace DataSpaceID `json:"bindingSpace"`
	ClaimName    string      `json:"claimName"`
	SubPath      string      `json:"subPath,omitempty"`
	MountPath    string      `json:"mountPath"`
	ReadOnly     bool        `json:"readOnly"`
}

// ResolvedDataSpaceMounts is deliberately separate from the legacy storage
// catalogue contract. A job uses one or the other, never both.
type ResolvedDataSpaceMounts struct {
	Input      *ResolvedDataMount `json:"input,omitempty"`
	Checkpoint *ResolvedDataMount `json:"checkpoint,omitempty"`
	Output     *ResolvedDataMount `json:"output,omitempty"`
}

func (mounts ResolvedDataSpaceMounts) Validate() error {
	checks := []struct {
		name      string
		mount     *ResolvedDataMount
		mountPath string
		readOnly  bool
		output    bool
	}{
		{name: "input", mount: mounts.Input, mountPath: DataMountInputPath, readOnly: true},
		{name: "checkpoint", mount: mounts.Checkpoint, mountPath: DataMountCheckpointPath, readOnly: true},
		{name: "output", mount: mounts.Output, mountPath: DataMountOutputPath, readOnly: false, output: true},
	}
	for _, check := range checks {
		if check.mount == nil {
			continue
		}
		if err := check.mount.validate(check.name, check.mountPath, check.readOnly, check.output); err != nil {
			return err
		}
	}
	return nil
}

func (mount ResolvedDataMount) validate(name, mountPath string, readOnly, output bool) error {
	if !IsKnownDataSpace(mount.Space) || mount.BindingSpace != dataSpaceBindingSpace(mount.Space) {
		return fmt.Errorf("resolved %s data mount has an invalid data space binding", name)
	}
	if strings.TrimSpace(mount.ClaimName) == "" || !dnsLabel.MatchString(mount.ClaimName) {
		return fmt.Errorf("resolved %s data mount has an invalid claim", name)
	}
	if mount.MountPath != mountPath || mount.ReadOnly != readOnly {
		return fmt.Errorf("resolved %s data mount has an invalid mount contract", name)
	}
	subPath, err := NormalizeStorageRelativePath(mount.SubPath)
	if err != nil {
		return fmt.Errorf("resolved %s data mount path: %w", name, err)
	}
	if output {
		if mount.Space != DataSpaceMyRuns || !isResolvedRunsSubPath(subPath) {
			return fmt.Errorf("resolved output data mount must stay below the personal runs directory")
		}
	}
	return nil
}

func isResolvedRunsSubPath(subPath string) bool {
	if strings.HasPrefix(subPath, "runs/") && strings.TrimPrefix(subPath, "runs/") != "" {
		return true
	}
	// The tenant-root PVC carries a physical personal prefix. It is still
	// constrained to the owner's runs directory rather than an arbitrary
	// directory below ray-train/.
	parts := strings.Split(subPath, "/")
	return len(parts) >= 6 && parts[0] == "tenants" && parts[2] == "users" && parts[4] == "runs" && strings.Join(parts[5:], "/") != ""
}

func dataSpaceBindingSpace(space DataSpaceID) DataSpaceID {
	if space == DataSpaceMyFiles || space == DataSpaceMyRuns {
		return DataSpaceWorkspace
	}
	return space
}
