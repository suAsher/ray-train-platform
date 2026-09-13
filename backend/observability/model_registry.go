package observability

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"ray-train-platform-backend/mlflowtracking"
	ml "ray-train-platform-backend/modellifecycle"
	"ray-train-platform-backend/modelregistry"
)

var _ modelregistry.Provider = (*MLflowClient)(nil)
var registryUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var registrySHA = regexp.MustCompile(`^[0-9a-f]{64}$`)
var registryNumber = regexp.MustCompile(`^[1-9][0-9]*$`)

type registryVersion struct {
	Name    string           `json:"name"`
	Version string           `json:"version"`
	RunID   string           `json:"run_id"`
	Source  string           `json:"source"`
	Status  string           `json:"status"`
	Tags    []MLflowKeyValue `json:"tags"`
}

// EnsureVersion is serialized by the caller's durable, renewable lease. A lost
// response is reconciled by signed identity tags before another create attempt.
// It copies only the immutable checkpoint; no arbitrary MLmodel is fabricated.
func (c *MLflowClient) EnsureVersion(ctx context.Context, model ml.Model, version ml.Version, open modelregistry.OpenSnapshot) (modelregistry.Link, error) {
	var zero modelregistry.Link
	if !registryUUID.MatchString(model.ID) || !registryUUID.MatchString(version.ID) || version.ModelID != model.ID || version.State != ml.Ready || !registrySHA.MatchString(version.SHA256) || version.SizeBytes <= 0 || version.SizeBytes > ml.MaxFileSize || open == nil {
		return zero, modelregistry.ErrInvalid
	}
	if c == nil || len(c.ProvenanceKey) < 32 {
		return zero, modelregistry.ErrUnavailable
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return zero, modelregistry.ErrUnavailable
	}
	copyClient := *c
	copyClient.HTTPClient = registryHTTPClient(c.HTTPClient, 2*time.Minute)
	c = &copyClient
	ctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	name := "raytrain-model-" + model.ID
	operation := registryOperation(model, version)
	experiment, err := c.registryExperiment(ctx, name)
	if err != nil {
		return zero, err
	}
	runID, err := c.CreateRun(ctx, experiment, operation, "Model checkpoint "+version.ID)
	if err != nil {
		return zero, registryTrackingError(err)
	}
	artifactRoot, err := c.registryRunArtifactRoot(ctx, experiment, runID, operation)
	if err != nil {
		return zero, err
	}
	file := "checkpoint/" + version.SHA256 + registryExtension(version.FileName)
	source := artifactRoot + "/" + file
	tags := c.registryTags(model, version)
	if err = c.ensureRegisteredModel(ctx, name, model.ID); err != nil {
		return zero, err
	}
	if found, ok, findErr := c.findRegistryVersion(ctx, name, runID, source, tags); findErr != nil {
		return zero, findErr
	} else if ok {
		// Shared MLflow clients may alter artifacts outside this bridge. A retry
		// must detect that drift without replacing an already registered object.
		endpoint, endpointErr := c.endpoint("/api/2.0/mlflow-artifacts/artifacts" + strings.TrimPrefix(source, "mlflow-artifacts:"))
		if endpointErr != nil {
			return zero, modelregistry.ErrUnavailable
		}
		if _, checkErr := checkRegistryArtifact(ctx, registryHTTPClient(c.HTTPClient, 30*time.Minute), endpoint.String(), version); checkErr != nil {
			return zero, checkErr
		}
		return found, nil
	}
	if err = c.ensureRegistryArtifact(ctx, artifactRoot, file, version, open); err != nil {
		return zero, err
	}
	if err = c.FinishRun(ctx, experiment, runID, operation, "FINISHED", 0); err != nil {
		return zero, registryTrackingError(err)
	}
	var result struct {
		Version registryVersion `json:"model_version"`
	}
	body := map[string]any{"name": name, "run_id": runID, "source": source, "tags": tags, "description": "Immutable RayTrain checkpoint. Requires matching inference code; not an MLflow flavor."}
	_, _ = c.registryJSON(ctx, http.MethodPost, "/api/2.0/mlflow/model-versions/create", nil, body, &result)
	// Always search, including a successful response: this verifies the durable
	// association and detects duplicate writes after an expired external lease.
	link, found, findErr := c.findRegistryVersion(ctx, name, runID, source, tags)
	if findErr != nil {
		return zero, findErr
	}
	if found {
		return link, nil
	}
	return zero, modelregistry.ErrUnavailable
}

func registryOperation(model ml.Model, version ml.Version) string {
	sum := sha256.Sum256([]byte("raytrain-registry:" + model.ID + ":" + version.ID + ":" + version.SHA256))
	return hex.EncodeToString(sum[:16])
}

func registryExtension(name string) string {
	switch ext := strings.ToLower(path.Ext(name)); ext {
	case ".safetensors", ".pt", ".pth", ".ckpt", ".bin", ".onnx":
		return ext
	default:
		return ".bin"
	}
}

func (c *MLflowClient) registryTags(model ml.Model, version ml.Version) []MLflowKeyValue {
	identity := model.ID + ":" + version.ID + ":" + version.SHA256
	mac := hmac.New(sha256.New, c.ProvenanceKey)
	_, _ = mac.Write([]byte("raytrain-registry-version:" + identity))
	return []MLflowKeyValue{{Key: "platform.model_id", Value: model.ID}, {Key: "platform.version_id", Value: version.ID}, {Key: "platform.sha256", Value: version.SHA256}, {Key: "platform.provenance", Value: hex.EncodeToString(mac.Sum(nil))}, {Key: "platform.artifact_kind", Value: "checkpoint"}, {Key: "platform.training_run_id", Value: version.RunID}}
}

func (c *MLflowClient) registryExperiment(ctx context.Context, name string) (string, error) {
	if id, found, err := c.experimentID(ctx, name); err != nil {
		return "", modelregistry.ErrUnavailable
	} else if found {
		return id, nil
	}
	var result struct {
		ID string `json:"experiment_id"`
	}
	_, _ = c.registryJSON(ctx, http.MethodPost, "/api/2.0/mlflow/experiments/create", nil, map[string]any{"name": name}, &result)
	if id, found, err := c.experimentID(ctx, name); err == nil && found && validMLflowExperimentID(id) {
		return id, nil
	}
	return "", modelregistry.ErrUnavailable
}

func (c *MLflowClient) registryRunArtifactRoot(ctx context.Context, experiment, runID, operation string) (string, error) {
	if _, err := c.trackingRun(ctx, experiment, runID, operation); err != nil {
		return "", registryTrackingError(err)
	}
	var result struct {
		Run struct {
			Info struct {
				ID  string `json:"run_id"`
				URI string `json:"artifact_uri"`
			} `json:"info"`
		} `json:"run"`
	}
	_, err := c.registryJSON(ctx, http.MethodGet, "/api/2.0/mlflow/runs/get", url.Values{"run_id": {runID}}, nil, &result)
	if err != nil {
		return "", err
	}
	expected := "mlflow-artifacts:/" + experiment + "/" + runID + "/artifacts"
	if result.Run.Info.ID != runID || strings.TrimRight(result.Run.Info.URI, "/") != expected {
		return "", modelregistry.ErrConflict
	}
	return expected, nil
}

func (c *MLflowClient) registeredModelProof(modelID string) string {
	mac := hmac.New(sha256.New, c.ProvenanceKey)
	_, _ = mac.Write([]byte("raytrain-registry-model:" + modelID))
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *MLflowClient) ensureRegisteredModel(ctx context.Context, name, modelID string) error {
	tags := []MLflowKeyValue{{Key: "platform.model_id", Value: modelID}, {Key: "platform.provenance", Value: c.registeredModelProof(modelID)}}
	check := func() (bool, error) {
		var result struct {
			Model struct {
				Name string           `json:"name"`
				Tags []MLflowKeyValue `json:"tags"`
			} `json:"registered_model"`
		}
		status, err := c.registryJSON(ctx, http.MethodGet, "/api/2.0/mlflow/registered-models/get", url.Values{"name": {name}}, nil, &result)
		if status == 404 {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if result.Model.Name != name || !registryTagsMatch(result.Model.Tags, tags) {
			return false, modelregistry.ErrConflict
		}
		return true, nil
	}
	if found, err := check(); err != nil || found {
		return err
	}
	var result map[string]any
	_, _ = c.registryJSON(ctx, http.MethodPost, "/api/2.0/mlflow/registered-models/create", nil, map[string]any{"name": name, "tags": tags}, &result)
	if found, err := check(); err != nil {
		return err
	} else if found {
		return nil
	}
	return modelregistry.ErrUnavailable
}

func (c *MLflowClient) findRegistryVersion(ctx context.Context, name, runID, source string, tags []MLflowKeyValue) (modelregistry.Link, bool, error) {
	var zero modelregistry.Link
	var match *registryVersion
	token := ""
	for page := 0; page < 100; page++ {
		query := url.Values{"filter": {"name = '" + name + "'"}, "max_results": {"100"}}
		if token != "" {
			query.Set("page_token", token)
		}
		var result struct {
			Versions []registryVersion `json:"model_versions"`
			Next     string            `json:"next_page_token"`
		}
		if _, err := c.registryJSON(ctx, http.MethodGet, "/api/2.0/mlflow/model-versions/search", query, nil, &result); err != nil {
			return zero, false, err
		}
		for _, item := range result.Versions {
			if !registryHasTag(item.Tags, "platform.version_id", tags[1].Value) && item.RunID != runID && item.Source != source {
				continue
			}
			if match != nil || item.Name != name || item.RunID != runID || item.Source != source || !registryTagsMatch(item.Tags, tags) || !registryNumber.MatchString(item.Version) {
				return zero, false, modelregistry.ErrConflict
			}
			copyItem := item
			match = &copyItem
		}
		if result.Next == "" {
			if match == nil {
				return zero, false, nil
			}
			if match.Status != "READY" {
				return zero, false, modelregistry.ErrUnavailable
			}
			return modelregistry.Link{RegisteredName: name, Version: match.Version, RunID: runID, SourceURI: source}, true, nil
		}
		if result.Next == token {
			return zero, false, modelregistry.ErrUnavailable
		}
		token = result.Next
	}
	return zero, false, modelregistry.ErrUnavailable
}

func registryHasTag(tags []MLflowKeyValue, key, value string) bool {
	for _, tag := range tags {
		if tag.Key == key && tag.Value == value {
			return true
		}
	}
	return false
}

func registryTagsMatch(actual, expected []MLflowKeyValue) bool {
	seen := map[string]string{}
	for _, tag := range actual {
		if _, ok := seen[tag.Key]; ok {
			return false
		}
		seen[tag.Key] = tag.Value
	}
	for _, tag := range expected {
		if !hmac.Equal([]byte(seen[tag.Key]), []byte(tag.Value)) {
			return false
		}
	}
	return true
}

func (c *MLflowClient) registryJSON(ctx context.Context, method, apiPath string, query url.Values, body, target any) (int, error) {
	endpoint, err := c.endpoint(apiPath)
	if err != nil {
		return 0, modelregistry.ErrUnavailable
	}
	if query != nil {
		endpoint.RawQuery = query.Encode()
	}
	status, err := c.doJSON(ctx, method, endpoint, body, target)
	if err != nil {
		return status, modelregistry.ErrUnavailable
	}
	return status, nil
}

func registryHTTPClient(original *http.Client, timeout time.Duration) *http.Client {
	client := http.Client{}
	if original != nil {
		client = *original
	}
	client.Timeout = timeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

func registryTrackingError(err error) error {
	if errors.Is(err, mlflowtracking.ErrConflict) || errors.Is(err, mlflowtracking.ErrNotFound) {
		return modelregistry.ErrConflict
	}
	return modelregistry.ErrUnavailable
}
