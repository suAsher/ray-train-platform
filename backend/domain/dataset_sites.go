package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

// DatasetSites stores a canonical JSON array while retaining value semantics.
// Empty means all sites. It is never inferred from sample tokens or paths.
type DatasetSites string

var datasetSitePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func NewDatasetSites(values []string) (DatasetSites, error) {
	if len(values) > 256 {
		return "", fmt.Errorf("at most 256 dataset sites may be selected")
	}
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !datasetSitePattern.MatchString(value) {
			return "", fmt.Errorf("invalid dataset site ID %q", value)
		}
		unique[value] = struct{}{}
	}
	if len(unique) == 0 {
		return "", nil
	}
	canonical := make([]string, 0, len(unique))
	for value := range unique {
		canonical = append(canonical, value)
	}
	sort.Strings(canonical)
	encoded, _ := json.Marshal(canonical)
	return DatasetSites(encoded), nil
}

func (sites DatasetSites) JSON() string {
	if sites == "" {
		return "[]"
	}
	return string(sites)
}

func (sites DatasetSites) Validate() error {
	var values []string
	if err := json.Unmarshal([]byte(sites.JSON()), &values); err != nil {
		return fmt.Errorf("invalid dataset sites: %w", err)
	}
	canonical, err := NewDatasetSites(values)
	if err != nil {
		return err
	}
	if canonical != sites {
		return fmt.Errorf("dataset sites must be canonical")
	}
	return nil
}

func (sites DatasetSites) MarshalJSON() ([]byte, error) {
	if err := sites.Validate(); err != nil {
		return nil, err
	}
	return []byte(sites.JSON()), nil
}

func (sites *DatasetSites) UnmarshalJSON(data []byte) error {
	var values []string
	if len(data) == 0 || data[0] != '[' {
		return fmt.Errorf("dataset sites must be an array")
	}
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	canonical, err := NewDatasetSites(values)
	if err == nil {
		*sites = canonical
	}
	return err
}
