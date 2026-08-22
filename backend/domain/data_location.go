package domain

import (
	"fmt"
	"net/url"
	"strings"
)

// DataLocation is the only new storage selection format accepted from the
// Portal. Space is logical and RelativePath is relative to that space; neither
// field can select a bucket, PVC, absolute path, or another user's prefix.
type DataLocation struct {
	Space        DataSpaceID `json:"space,omitempty"`
	RelativePath string      `json:"relativePath,omitempty"`
}

func NewDataLocation(space DataSpaceID, relativePath string) (DataLocation, error) {
	if !IsKnownDataSpace(space) {
		return DataLocation{}, fmt.Errorf("unknown data space %q", space)
	}
	path, err := NormalizeStorageRelativePath(relativePath)
	if err != nil {
		return DataLocation{}, fmt.Errorf("data location path: %w", err)
	}
	return DataLocation{Space: space, RelativePath: path}, nil
}

func (location DataLocation) Validate() error {
	_, err := NewDataLocation(location.Space, location.RelativePath)
	return err
}

func ValidateOutputSpace(space DataSpaceID) error {
	if !IsKnownDataSpace(space) {
		return fmt.Errorf("unknown output data space %q", space)
	}
	if !IsTrainingOutputSpace(space) {
		return fmt.Errorf("only %q can be a training output", DataSpaceMyRuns)
	}
	return nil
}

func validateLogicalDataLocations(spec JobSpec) error {
	locations := []struct {
		field    string
		location DataLocation
		isOutput bool
	}{
		{field: "input", location: spec.Input},
		{field: "checkpoint", location: spec.Checkpoint},
		{field: "output", location: spec.Output, isOutput: true},
	}
	for _, item := range locations {
		if item.location.Space == "" && item.location.RelativePath == "" {
			continue
		}
		if err := item.location.Validate(); err != nil {
			return fmt.Errorf("%s: %w", item.field, err)
		}
		if item.isOutput {
			if err := ValidateOutputSpace(item.location.Space); err != nil {
				return fmt.Errorf("%s: %w", item.field, err)
			}
		}
	}
	if hasLogicalDataLocation(spec) && hasLegacyStorageSelection(spec) {
		return fmt.Errorf("logical data locations cannot be combined with legacy storage selections")
	}
	if hasLogicalDataLocation(spec) && hasLegacyDataURI(spec) {
		return fmt.Errorf("logical data locations cannot be combined with legacy storage URIs")
	}
	return nil
}

func hasLogicalDataLocation(spec JobSpec) bool {
	return spec.Input.Space != "" || spec.Input.RelativePath != "" ||
		spec.Checkpoint.Space != "" || spec.Checkpoint.RelativePath != "" ||
		spec.Output.Space != "" || spec.Output.RelativePath != ""
}

func hasLegacyStorageSelection(spec JobSpec) bool {
	return spec.DatasetStorage.AssetID != "" || spec.DatasetStorage.RelativePath != "" ||
		spec.CheckpointStorage.AssetID != "" || spec.CheckpointStorage.RelativePath != "" ||
		spec.OutputStorage.AssetID != "" || spec.OutputStorage.RelativePath != ""
}

func hasLegacyDataURI(spec JobSpec) bool {
	return strings.TrimSpace(spec.DatasetURI) != "" || strings.TrimSpace(spec.CheckpointURI) != "" || strings.TrimSpace(spec.OutputURI) != ""
}

type dataLocation struct {
	field string
	value string
}

func validateDataLocations(locations ...dataLocation) error {
	for _, location := range locations {
		if err := validateDataLocation(location.field, location.value); err != nil {
			return err
		}
	}
	return nil
}

func validateDataLocation(field, raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must be a supported storage URI without credentials, query, or fragment", field)
	}

	switch parsed.Scheme {
	case "tos":
		if strings.TrimSpace(parsed.Host) == "" || invalidStoragePath(parsed.Path) {
			return fmt.Errorf("%s must be a tos://bucket/path URI", field)
		}
	case "idc":
		location := strings.Trim(strings.Join([]string{parsed.Host, parsed.Path}, "/"), "/")
		if location == "" || invalidStoragePath(location) {
			return fmt.Errorf("%s must be an idc:///path URI", field)
		}
	default:
		return fmt.Errorf("%s must use tos:// or idc:// storage", field)
	}
	return nil
}

func invalidStoragePath(value string) bool {
	path := strings.TrimSpace(value)
	if path == "" {
		return true
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func validateStorageSelections(spec JobSpec) error {
	selections := []struct {
		field          string
		legacyLocation string
		selection      StorageSelection
		output         bool
	}{
		{field: "datasetStorage", legacyLocation: spec.DatasetURI, selection: spec.DatasetStorage},
		{field: "checkpointStorage", legacyLocation: spec.CheckpointURI, selection: spec.CheckpointStorage},
		{field: "outputStorage", legacyLocation: spec.OutputURI, selection: spec.OutputStorage, output: true},
	}
	for _, item := range selections {
		if strings.TrimSpace(item.legacyLocation) != "" && strings.TrimSpace(item.selection.AssetID) != "" {
			return fmt.Errorf("%s cannot be combined with its legacy URI", item.field)
		}
		if strings.TrimSpace(item.selection.AssetID) == "" {
			if strings.TrimSpace(item.selection.RelativePath) != "" {
				return fmt.Errorf("%s relative path requires a storage asset", item.field)
			}
			continue
		}
		if item.selection.AssetID != strings.TrimSpace(item.selection.AssetID) {
			return fmt.Errorf("%s asset id must not contain surrounding whitespace", item.field)
		}
		path, err := NormalizeStorageRelativePath(item.selection.RelativePath)
		if err != nil {
			return fmt.Errorf("%s: %w", item.field, err)
		}
		if item.output && path != "" {
			return fmt.Errorf("outputStorage relative path is platform generated")
		}
	}
	return nil
}
