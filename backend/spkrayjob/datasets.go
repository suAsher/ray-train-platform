package spkrayjob

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"ray-train-platform-backend/domain"
)

// DatasetCatalogItem contains only logical catalogue fields. Object-store
// keys, PVC names and credentials never enter the CLI model.
type DatasetCatalogItem struct {
	ID                 string                   `json:"id"`
	Slug               string                   `json:"slug"`
	Name               string                   `json:"name"`
	Description        string                   `json:"description,omitempty"`
	SourceSpace        domain.DataSpaceID       `json:"sourceSpace"`
	SourceRelativePath string                   `json:"sourceRelativePath"`
	OwnerTenantID      string                   `json:"ownerTenantId,omitempty"`
	Visibility         domain.DatasetVisibility `json:"visibility"`
	SchemaVersion      string                   `json:"schemaVersion"`
}

type DatasetVersionCatalogItem struct {
	ID                string                     `json:"id"`
	DatasetID         string                     `json:"datasetId"`
	Version           string                     `json:"version"`
	State             domain.DatasetVersionState `json:"state"`
	ManifestSHA256    string                     `json:"manifestSha256,omitempty"`
	SchemaVersion     string                     `json:"schemaVersion"`
	TrainSamples      int64                      `json:"trainSamples"`
	ValSamples        int64                      `json:"valSamples"`
	TestSamples       int64                      `json:"testSamples"`
	SourceObjectCount int64                      `json:"sourceObjectCount"`
	LogicalBytes      int64                      `json:"logicalBytes"`
	PackedBytes       int64                      `json:"packedBytes"`
}

type DatasetPreflightSummary struct {
	Sites               domain.DatasetSites       `json:"sites,omitempty"`
	SelectionValidation string                    `json:"selectionValidation,omitempty"`
	DatasetID           string                    `json:"datasetId"`
	DatasetSlug         string                    `json:"datasetSlug"`
	VersionID           string                    `json:"versionId"`
	Version             string                    `json:"version"`
	ManifestSHA256      string                    `json:"manifestSha256"`
	SchemaVersion       string                    `json:"schemaVersion"`
	TrainSamples        int64                     `json:"trainSamples"`
	ValSamples          int64                     `json:"valSamples"`
	TestSamples         int64                     `json:"testSamples"`
	LogicalBytes        int64                     `json:"logicalBytes"`
	PackedBytes         int64                     `json:"packedBytes"`
	DataMode            domain.DataMode           `json:"dataMode"`
	CachePolicy         domain.DatasetCachePolicy `json:"cachePolicy"`
}

type SubmissionPreflightResult struct {
	Image          string                   `json:"image"`
	TrainingEngine domain.TrainingEngine    `json:"trainingEngine"`
	RayVersion     string                   `json:"rayVersion"`
	RequestedGPUs  int                      `json:"requestedGpus"`
	Dataset        *DatasetPreflightSummary `json:"dataset,omitempty"`
}

func (client *Client) Datasets(ctx context.Context) ([]DatasetCatalogItem, error) {
	raw, err := client.request(ctx, http.MethodGet, "/api/v1/datasets", nil, nil)
	if err != nil {
		return nil, err
	}
	var items []DatasetCatalogItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("decode dataset catalogue")
	}
	if items == nil {
		items = make([]DatasetCatalogItem, 0)
	}
	return append([]DatasetCatalogItem(nil), items...), nil
}

func (client *Client) DatasetVersions(ctx context.Context, datasetToken string) (DatasetCatalogItem, []DatasetVersionCatalogItem, error) {
	datasets, err := client.Datasets(ctx)
	if err != nil {
		return DatasetCatalogItem{}, nil, err
	}
	dataset, err := selectCatalogDataset(datasets, datasetToken)
	if err != nil {
		return DatasetCatalogItem{}, nil, err
	}
	raw, err := client.request(ctx, http.MethodGet, "/api/v1/datasets/"+url.PathEscape(dataset.ID)+"/versions", nil, nil)
	if err != nil {
		return DatasetCatalogItem{}, nil, err
	}
	var versions []DatasetVersionCatalogItem
	if err := json.Unmarshal(raw, &versions); err != nil {
		return DatasetCatalogItem{}, nil, fmt.Errorf("decode dataset versions")
	}
	if versions == nil {
		versions = make([]DatasetVersionCatalogItem, 0)
	}
	return dataset, append([]DatasetVersionCatalogItem(nil), versions...), nil
}

func selectCatalogDataset(items []DatasetCatalogItem, raw string) (DatasetCatalogItem, error) {
	token := strings.TrimSpace(raw)
	if token == "" || strings.Contains(token, "://") || strings.ContainsAny(token, "/\\?#%") {
		return DatasetCatalogItem{}, fmt.Errorf("dataset must be an ID or slug from spk-rayjob datasets")
	}
	matches := make([]DatasetCatalogItem, 0, 1)
	for _, item := range items {
		if item.ID == token || item.Slug == token {
			matches = append(matches, item)
		}
	}
	if len(matches) != 1 {
		return DatasetCatalogItem{}, fmt.Errorf("dataset %q was not found or is ambiguous; run spk-rayjob datasets", token)
	}
	return matches[0], nil
}

// PreflightStreaming resolves latest to an immutable version before source
// packaging starts and returns only safe logical metadata.
func (client *Client) PreflightStreaming(ctx context.Context, spec domain.JobSpec) (domain.JobSpec, SubmissionPreflightResult, error) {
	if spec.DataMode != domain.DataModeStreaming {
		return spec, SubmissionPreflightResult{}, nil
	}
	body, err := json.Marshal(struct {
		Spec   domain.JobSpec          `json:"spec"`
		Origin domain.SubmissionOrigin `json:"origin"`
	}{Spec: spec, Origin: domain.SubmissionOriginRayCLI})
	if err != nil {
		return domain.JobSpec{}, SubmissionPreflightResult{}, fmt.Errorf("encode submission preflight")
	}
	raw, err := client.request(ctx, http.MethodPost, "/api/v1/jobs/preflight", body, nil)
	if err != nil {
		return domain.JobSpec{}, SubmissionPreflightResult{}, err
	}
	var result SubmissionPreflightResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return domain.JobSpec{}, SubmissionPreflightResult{}, fmt.Errorf("decode submission preflight")
	}
	resolved, err := validateStreamingPreflight(spec, result)
	if err != nil {
		return domain.JobSpec{}, SubmissionPreflightResult{}, err
	}
	return resolved, result, nil
}

func validateStreamingPreflight(spec domain.JobSpec, result SubmissionPreflightResult) (domain.JobSpec, error) {
	if result.Dataset == nil {
		return domain.JobSpec{}, fmt.Errorf("submission preflight returned no dataset version")
	}
	dataset := result.Dataset
	provenance := domain.DatasetProvenance{
		Sites:     dataset.Sites,
		DatasetID: dataset.DatasetID, DatasetVersionID: dataset.VersionID,
		ManifestSHA256: dataset.ManifestSHA256, DataMode: dataset.DataMode, CachePolicy: dataset.CachePolicy,
	}
	if err := provenance.Validate(); err != nil || dataset.VersionID == "latest" || dataset.TrainSamples <= 0 ||
		dataset.ValSamples < 0 || dataset.TestSamples < 0 || dataset.LogicalBytes < 0 || dataset.PackedBytes <= 0 {
		return domain.JobSpec{}, fmt.Errorf("submission preflight returned an invalid dataset version")
	}
	requestedGPUs := spec.Resources.WorkerReplicas * spec.Resources.GPUsPerWorker
	if result.TrainingEngine != domain.TrainingEngineRayTrain || result.RayVersion != domain.RayVersionCanary ||
		result.RequestedGPUs != requestedGPUs || result.Image != spec.Image || dataset.DataMode != domain.DataModeStreaming ||
		dataset.CachePolicy != spec.CachePolicy || dataset.Sites != spec.DatasetRef.Sites {
		return domain.JobSpec{}, fmt.Errorf("submission preflight is inconsistent with the requested runtime")
	}
	resolved := spec
	resolved.DatasetRef = domain.DatasetReference{Dataset: dataset.DatasetID, Version: dataset.VersionID, Sites: dataset.Sites}
	if err := resolved.DatasetRef.Validate(); err != nil {
		return domain.JobSpec{}, fmt.Errorf("submission preflight returned an invalid dataset reference")
	}
	return resolved, nil
}
