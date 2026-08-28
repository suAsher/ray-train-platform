package spkrayjob

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ray-train-platform-backend/domain"
)

type ClientOptions struct {
	ServerURL   string
	Token       string
	CAFile      string
	HTTPClient  *http.Client
	DebugWriter io.Writer
}

type Client struct {
	server      *url.URL
	token       string
	httpClient  *http.Client
	debugWriter io.Writer
}

const (
	maxGenericPlatformResponseBytes int64 = 1 << 20
	// Model summaries and multi-worker logs routinely exceed the generic API
	// envelope size. Keep this bounded, but large enough for the documented
	// 10,000-line query contract.
	maxLogPlatformResponseBytes int64 = 32 << 20
)

var stableSourceRequestID = regexp.MustCompile(`^source-request-[0-9a-f]{24}$`)

type localLogin struct {
	Token    string `json:"token"`
	Username string `json:"username"`
}

type Artifact struct {
	ArtifactID      string            `json:"artifactId"`
	State           string            `json:"state"`
	SHA256          string            `json:"sha256"`
	SizeBytes       int64             `json:"sizeBytes"`
	UploadURL       string            `json:"uploadUrl"`
	RequiredHeaders map[string]string `json:"requiredHeaders"`
	ContentLength   int64             `json:"contentLength"`
	UploadRequired  bool              `json:"uploadRequired"`
}

type Job struct {
	ID            string          `json:"id"`
	ObservedState domain.State    `json:"observedState"`
	Raw           json.RawMessage `json:"-"`
}

type JobCheckpointPage struct {
	JobID string                      `json:"jobId"`
	Items []domain.TrainingCheckpoint `json:"items"`
}

type LogEntry struct {
	Timestamp string            `json:"timestamp"`
	Line      string            `json:"line"`
	Stream    map[string]string `json:"stream,omitempty"`
}

type LogPageMeta struct {
	Direction  string `json:"direction"`
	Limit      int    `json:"limit"`
	HasMore    bool   `json:"hasMore"`
	NextCursor string `json:"nextCursor"`
}

type LogPage struct {
	JobID               string      `json:"jobId"`
	Items               []LogEntry  `json:"items"`
	Page                LogPageMeta `json:"page"`
	PaginationAvailable bool        `json:"-"`
}

type LogPageOptions struct {
	Limit     int
	Direction string
	Cursor    string
}

type PlatformLimits struct {
	Cache   PlatformCacheLimits   `json:"cache"`
	Runtime PlatformRuntimeLimits `json:"runtime"`
}

type PlatformRuntimeLimits struct {
	AvailableEngines     []string `json:"availableEngines"`
	ProductionRayVersion string   `json:"productionRayVersion"`
	CanaryRayVersion     string   `json:"canaryRayVersion"`
	ManagedEnabled       bool     `json:"managedEnabled"`
	CanaryEnabled        bool     `json:"canaryEnabled"`
}

func (limits PlatformRuntimeLimits) ManagedAvailable() bool {
	return limits.ManagedEnabled && containsTrimmed(limits.AvailableEngines, string(domain.TrainingEngineRayDDP)) && containsTrimmed(limits.AvailableEngines, string(domain.TrainingEngineRayTrain))
}

type PlatformCacheLimits struct {
	Enabled      bool     `json:"enabled"`
	Modes        []string `json:"modes"`
	AllowedSizes []string `json:"allowedSizes"`
	DefaultSize  string   `json:"defaultSize"`
	MaxSize      string   `json:"maxSize"`
}

type apiEnvelope[T any] struct {
	Success bool `json:"success"`
	Data    T    `json:"data"`
	Error   *struct {
		Code string `json:"code"`
	} `json:"error"`
}

func NewClient(options ClientOptions) (*Client, error) {
	if strings.TrimSpace(options.Token) == "" {
		return nil, fmt.Errorf("platform token is required")
	}
	server, httpClient, err := openPlatformConnection(options.ServerURL, options.CAFile, options.HTTPClient)
	if err != nil {
		return nil, err
	}
	return &Client{server: server, token: options.Token, httpClient: httpClient, debugWriter: options.DebugWriter}, nil
}

func openPlatformConnection(serverURL, caFile string, httpClient *http.Client) (*url.URL, *http.Client, error) {
	server, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil || server.Scheme != "https" || server.Host == "" || server.User != nil || server.RawQuery != "" || server.Fragment != "" {
		return nil, nil, fmt.Errorf("invalid platform server URL")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	if caFile := strings.TrimSpace(caFile); caFile != "" {
		configured, err := clientWithCA(httpClient, caFile)
		if err != nil {
			return nil, nil, err
		}
		httpClient = configured
	}
	return server, httpClient, nil
}

// LoginWithLocalCredentials exchanges a platform username and a password read
// from stdin for the same short-lived local session that the Portal uses. The
// password is intentionally never stored or written to a command argument.
func LoginWithLocalCredentials(ctx context.Context, serverURL, username, password, caFile string, httpClient *http.Client) (localLogin, error) {
	server, client, err := openPlatformConnection(serverURL, caFile, httpClient)
	if err != nil {
		return localLogin{}, err
	}
	if strings.TrimSpace(username) == "" || password == "" {
		return localLogin{}, fmt.Errorf("platform username and password are required")
	}
	body, err := json.Marshal(map[string]string{"username": strings.TrimSpace(username), "password": password})
	if err != nil {
		return localLogin{}, fmt.Errorf("encode platform login request")
	}
	endpoint := server.ResolveReference(&url.URL{Path: "/api/v1/auth/login"})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return localLogin{}, fmt.Errorf("create platform login request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return localLogin{}, fmt.Errorf("platform login request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return localLogin{}, fmt.Errorf("platform login rejected")
	}
	var envelope apiEnvelope[localLogin]
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return localLogin{}, fmt.Errorf("decode platform login response")
	}
	if !envelope.Success || strings.TrimSpace(envelope.Data.Token) == "" {
		return localLogin{}, fmt.Errorf("platform login rejected")
	}
	return envelope.Data, nil
}

func clientWithCA(base *http.Client, caFile string) (*http.Client, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA file: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CA file contains no certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if current, ok := base.Transport.(*http.Transport); ok && current != nil {
		transport = current.Clone()
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	copy := *base
	copy.Transport = transport
	return &copy, nil
}

func (client *Client) LoginCheck(ctx context.Context) (json.RawMessage, error) {
	return client.request(ctx, http.MethodGet, "/api/v1/jobs?limit=1", nil, nil)
}

func (client *Client) SubmitDirectory(ctx context.Context, directory string, spec domain.JobSpec) (Job, error) {
	return client.SubmitDirectoryWithRequestID(ctx, directory, spec, "")
}

func (client *Client) SubmitDirectoryWithRequestID(ctx context.Context, directory string, spec domain.JobSpec, clientRequestID string) (Job, error) {
	if clientRequestID != "" && !stableSourceRequestID.MatchString(clientRequestID) {
		return Job{}, fmt.Errorf("source request ID must match source-request- followed by 24 lowercase hexadecimal characters")
	}
	if err := validateArchiveJobSpec(spec); err != nil {
		return Job{}, err
	}
	resolved, err := client.preflightManagedImage(ctx, spec)
	if err != nil {
		return Job{}, err
	}
	spec = resolved
	archive, err := BuildArchive(directory)
	if err != nil {
		return Job{}, err
	}
	defer os.Remove(archive.Path)
	return client.submitArchiveWithRequestID(ctx, archive, spec, clientRequestID)
}

func (client *Client) preflightManagedImage(ctx context.Context, spec domain.JobSpec) (domain.JobSpec, error) {
	if spec.TrainingEngine.Resolved() != domain.TrainingEngineRayTrain {
		return spec, nil
	}
	limits, err := client.PlatformLimits(ctx)
	if err != nil {
		return domain.JobSpec{}, fmt.Errorf("read managed runtime capabilities: %w", err)
	}
	if !limits.Runtime.ManagedAvailable() {
		return domain.JobSpec{}, fmt.Errorf("Ray Train managed engine is not available")
	}
	images, err := client.TrainingImages(ctx)
	if err != nil {
		return domain.JobSpec{}, err
	}
	selected, err := managedImage(images, spec.Image, limits.Runtime)
	if err != nil {
		return domain.JobSpec{}, err
	}
	resolved := spec
	resolved.Image = selected.Reference
	return resolved, nil
}

func (client *Client) submitArchive(ctx context.Context, archive Archive, spec domain.JobSpec) (Job, error) {
	return client.submitArchiveWithRequestID(ctx, archive, spec, "")
}

func (client *Client) submitArchiveWithRequestID(ctx context.Context, archive Archive, spec domain.JobSpec, clientRequestID string) (Job, error) {
	if err := validateArchiveJobSpec(spec); err != nil {
		return Job{}, err
	}
	artifact, err := client.CreateArtifactWithRequestID(ctx, archive, clientRequestID)
	if err != nil {
		return Job{}, err
	}
	if artifact.UploadRequired {
		if err := client.Upload(ctx, artifact, archive); err != nil {
			return Job{}, err
		}
		artifact, err = client.CompleteArtifact(ctx, artifact.ArtifactID)
		if err != nil {
			return Job{}, err
		}
	}
	if artifact.ArtifactID == "" || artifact.State != "READY" {
		return Job{}, fmt.Errorf("source artifact was not ready")
	}
	spec.Source = domain.CodeSource{Type: "workspace-archive", ArtifactID: artifact.ArtifactID}
	return client.submit(ctx, spec, domain.SubmissionOriginRayCLI)
}

func (client *Client) CreateArtifact(ctx context.Context, archive Archive) (Artifact, error) {
	return client.CreateArtifactWithRequestID(ctx, archive, "")
}

func (client *Client) CreateArtifactWithRequestID(ctx context.Context, archive Archive, clientRequestID string) (Artifact, error) {
	if clientRequestID != "" && !stableSourceRequestID.MatchString(clientRequestID) {
		return Artifact{}, fmt.Errorf("source request ID must match source-request- followed by 24 lowercase hexadecimal characters")
	}
	body, err := json.Marshal(struct {
		ClientRequestID string `json:"clientRequestId,omitempty"`
		SHA256          string `json:"sha256"`
		SizeBytes       int64  `json:"sizeBytes"`
	}{ClientRequestID: clientRequestID, SHA256: archive.SHA256, SizeBytes: archive.SizeBytes})
	if err != nil {
		return Artifact{}, err
	}
	data, err := client.request(ctx, http.MethodPost, "/api/v1/source-artifacts", body, nil)
	if err != nil && clientRequestID != "" && ctx.Err() == nil {
		// An owner-scoped idempotency key makes one bounded retry safe when the server
		// committed creation but its response was lost in transit.
		data, err = client.request(ctx, http.MethodPost, "/api/v1/source-artifacts", body, nil)
	}
	if err != nil {
		return Artifact{}, err
	}
	var artifact Artifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return Artifact{}, fmt.Errorf("decode source artifact response")
	}
	return artifact, nil
}

func (client *Client) ResolveArtifactRequest(ctx context.Context, clientRequestID string) (Artifact, error) {
	if !stableSourceRequestID.MatchString(clientRequestID) {
		return Artifact{}, fmt.Errorf("source request ID must match source-request- followed by 24 lowercase hexadecimal characters")
	}
	data, err := client.request(ctx, http.MethodGet, "/api/v1/source-artifact-requests/"+url.PathEscape(clientRequestID), nil, nil)
	if err != nil {
		return Artifact{}, err
	}
	var artifact Artifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return Artifact{}, fmt.Errorf("decode source artifact request response")
	}
	if strings.TrimSpace(artifact.ArtifactID) == "" {
		return Artifact{}, fmt.Errorf("source artifact request response had no artifact ID")
	}
	return artifact, nil
}

func (client *Client) Upload(ctx context.Context, artifact Artifact, archive Archive) error {
	uploadURL, err := url.Parse(artifact.UploadURL)
	if err != nil || uploadURL.Scheme != "https" && uploadURL.Scheme != "http" || uploadURL.Host == "" || uploadURL.User != nil {
		return fmt.Errorf("invalid source upload URL")
	}
	if artifact.ContentLength > 0 && artifact.ContentLength != archive.SizeBytes {
		return fmt.Errorf("source upload size does not match declaration")
	}
	file, err := os.Open(archive.Path)
	if err != nil {
		return fmt.Errorf("open source archive: %w", err)
	}
	defer file.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL.String(), file)
	if err != nil {
		return fmt.Errorf("create source upload request")
	}
	request.ContentLength = archive.SizeBytes
	for key, value := range artifact.RequiredHeaders {
		if !validUploadHeader(key, value) {
			return fmt.Errorf("invalid required source upload header")
		}
		request.Header.Set(key, value)
	}
	if request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/zip")
	}
	client.debugf("PUT %s", redactedURL(uploadURL))
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("source upload request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("source upload failed with status %d", response.StatusCode)
	}
	return nil
}

func validUploadHeader(key, value string) bool {
	key = http.CanonicalHeaderKey(strings.TrimSpace(key))
	return key != "" && key != "Authorization" && key != "Host" && !strings.ContainsAny(key, "\r\n") && !strings.ContainsAny(value, "\r\n")
}

func (client *Client) CompleteArtifact(ctx context.Context, artifactID string) (Artifact, error) {
	if strings.TrimSpace(artifactID) == "" {
		return Artifact{}, fmt.Errorf("source artifact ID is required")
	}
	data, err := client.request(ctx, http.MethodPost, "/api/v1/source-artifacts/"+url.PathEscape(artifactID)+"/complete", nil, nil)
	if err != nil {
		return Artifact{}, err
	}
	var artifact Artifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return Artifact{}, fmt.Errorf("decode source artifact completion")
	}
	return artifact, nil
}

func (client *Client) Submit(ctx context.Context, spec domain.JobSpec) (Job, error) {
	if err := validateFinalJobSpec(spec); err != nil {
		return Job{}, err
	}
	resolved, err := client.preflightManagedImage(ctx, spec)
	if err != nil {
		return Job{}, err
	}
	return client.submit(ctx, resolved, "")
}

func (client *Client) submit(ctx context.Context, spec domain.JobSpec, origin domain.SubmissionOrigin) (Job, error) {
	if err := validateFinalJobSpec(spec); err != nil {
		return Job{}, err
	}
	body, err := json.Marshal(struct {
		Spec   domain.JobSpec          `json:"spec"`
		Origin domain.SubmissionOrigin `json:"origin,omitempty"`
	}{Spec: spec, Origin: origin})
	if err != nil {
		return Job{}, err
	}
	data, err := client.request(ctx, http.MethodPost, "/api/v1/jobs", body, nil)
	if err != nil {
		return Job{}, err
	}
	return decodeJob(data)
}

// TrainingImages returns the administrator-approved training environments so
// the CLI can pick the default one instead of asking the user for a reference.
func (client *Client) TrainingImages(ctx context.Context) ([]catalogImage, error) {
	raw, err := client.request(ctx, http.MethodGet, "/api/v1/images?kind=training", nil, nil)
	if err != nil {
		return nil, err
	}
	var images []catalogImage
	if err := json.Unmarshal(raw, &images); err != nil {
		return nil, fmt.Errorf("decode image catalogue: %w", err)
	}
	normalized := make([]catalogImage, 0, len(images))
	for _, image := range images {
		validated, err := normalizeCatalogImage(image)
		if err != nil {
			return nil, fmt.Errorf("decode image catalogue: %w", err)
		}
		normalized = append(normalized, validated)
	}
	return normalized, nil
}

func (client *Client) PlatformLimits(ctx context.Context) (PlatformLimits, error) {
	raw, err := client.request(ctx, http.MethodGet, "/api/v1/limits", nil, nil)
	if err != nil {
		return PlatformLimits{}, err
	}
	var limits PlatformLimits
	if err := json.Unmarshal(raw, &limits); err != nil {
		return PlatformLimits{}, fmt.Errorf("decode platform limits: %w", err)
	}
	limits.Cache.Modes = append([]string(nil), limits.Cache.Modes...)
	limits.Cache.AllowedSizes = append([]string(nil), limits.Cache.AllowedSizes...)
	runtime, err := normalizeRuntimeLimits(limits.Runtime)
	if err != nil {
		return PlatformLimits{}, fmt.Errorf("decode platform runtime limits: %w", err)
	}
	limits.Runtime = runtime
	return limits, nil
}

func normalizeRuntimeLimits(limits PlatformRuntimeLimits) (PlatformRuntimeLimits, error) {
	normalized := limits
	normalized.AvailableEngines = make([]string, 0, len(limits.AvailableEngines))
	seen := make(map[string]struct{}, len(limits.AvailableEngines))
	for _, raw := range limits.AvailableEngines {
		engine := strings.TrimSpace(raw)
		if engine != string(domain.TrainingEngineRayDDP) && engine != string(domain.TrainingEngineRayTrain) {
			return PlatformRuntimeLimits{}, fmt.Errorf("unsupported engine %q", raw)
		}
		if _, duplicate := seen[engine]; duplicate {
			return PlatformRuntimeLimits{}, fmt.Errorf("duplicate engine %q", engine)
		}
		seen[engine] = struct{}{}
		normalized.AvailableEngines = append(normalized.AvailableEngines, engine)
	}
	if limits.ManagedEnabled && !normalized.ManagedAvailable() {
		return PlatformRuntimeLimits{}, fmt.Errorf("managed engine capability is inconsistent")
	}
	if limits.CanaryEnabled && !limits.ManagedEnabled {
		return PlatformRuntimeLimits{}, fmt.Errorf("canary is enabled while managed engine is disabled")
	}
	if limits.ManagedEnabled && (strings.TrimSpace(limits.ProductionRayVersion) != domain.RayVersionProduction || strings.TrimSpace(limits.CanaryRayVersion) != domain.RayVersionCanary) {
		return PlatformRuntimeLimits{}, fmt.Errorf("managed Ray version capability is inconsistent")
	}
	if !limits.ManagedEnabled && containsTrimmed(normalized.AvailableEngines, string(domain.TrainingEngineRayTrain)) {
		return PlatformRuntimeLimits{}, fmt.Errorf("ray-train is advertised while managed engine is disabled")
	}
	return normalized, nil
}

func (client *Client) Status(ctx context.Context, jobID string) (Job, error) {
	data, err := client.request(ctx, http.MethodGet, "/api/v1/jobs/"+url.PathEscape(jobID), nil, nil)
	if err != nil {
		return Job{}, err
	}
	return decodeJob(data)
}

// Checkpoints reads the server-authorized checkpoint list for one job. The
// endpoint applies tenant and owner scope; selection and path validation remain
// client-side so a malformed response cannot become a submitted storage path.
func (client *Client) Checkpoints(ctx context.Context, jobID string) (JobCheckpointPage, error) {
	data, err := client.request(ctx, http.MethodGet, "/api/v1/jobs/"+url.PathEscape(jobID)+"/checkpoints", nil, nil)
	if err != nil {
		return JobCheckpointPage{}, err
	}
	var page JobCheckpointPage
	if err := json.Unmarshal(data, &page); err != nil {
		return JobCheckpointPage{}, fmt.Errorf("decode checkpoint response")
	}
	page.Items = append([]domain.TrainingCheckpoint(nil), page.Items...)
	return page, nil
}

// ListJobs returns the caller's visible jobs. The server owns tenant scoping;
// the state filter is passed through so `spk-rayjob jobs --state RUNNING` does
// not download a full history to discard it locally.
func (client *Client) ListJobs(ctx context.Context, state string, limit int) (json.RawMessage, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if trimmed := strings.TrimSpace(state); trimmed != "" {
		query.Set("status", strings.ToUpper(trimmed))
	}
	path := "/api/v1/jobs"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return client.request(ctx, http.MethodGet, path, nil, nil)
}

func (client *Client) Logs(ctx context.Context, jobID string, limit int) (json.RawMessage, error) {
	path := "/api/v1/jobs/" + url.PathEscape(jobID) + "/logs"
	if limit > 0 {
		path += "?limit=" + fmt.Sprintf("%d", limit)
	}
	return client.request(ctx, http.MethodGet, path, nil, nil)
}

// LogsPage reads one bounded page. Forward pages are used for complete exports
// and follow; backward pages let interactive clients open at the newest lines.
func (client *Client) LogsPage(ctx context.Context, jobID string, options LogPageOptions) (LogPage, error) {
	direction := strings.ToLower(strings.TrimSpace(options.Direction))
	if direction == "" {
		direction = "backward"
	}
	if direction != "forward" && direction != "backward" {
		return LogPage{}, fmt.Errorf("log direction must be forward or backward")
	}
	query := url.Values{}
	query.Set("direction", direction)
	if options.Limit > 0 {
		query.Set("limit", strconv.Itoa(options.Limit))
	}
	if cursor := strings.TrimSpace(options.Cursor); cursor != "" {
		if direction == "forward" {
			query.Set("after", cursor)
		} else {
			query.Set("before", cursor)
		}
	}
	path := "/api/v1/jobs/" + url.PathEscape(jobID) + "/logs?" + query.Encode()
	raw, err := client.request(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return LogPage{}, err
	}
	var payload struct {
		JobID string       `json:"jobId"`
		Items []LogEntry   `json:"items"`
		Page  *LogPageMeta `json:"page"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return LogPage{}, fmt.Errorf("decode job logs")
	}
	page := LogPage{JobID: payload.JobID, Items: payload.Items, PaginationAvailable: payload.Page != nil}
	if payload.Page != nil {
		page.Page = *payload.Page
	}
	if page.Items == nil {
		page.Items = make([]LogEntry, 0)
	}
	if page.JobID == "" {
		page.JobID = jobID
	}
	if page.Page.Direction == "" {
		page.Page.Direction = direction
	}
	if page.Page.Limit == 0 {
		page.Page.Limit = options.Limit
	}
	// This fallback keeps a newly installed CLI compatible with a rolling
	// backend upgrade. Old responses had no page block, but their timestamp is
	// still a safe follow cursor.
	if page.Page.NextCursor == "" && len(page.Items) > 0 {
		if direction == "backward" {
			page.Page.NextCursor = page.Items[0].Timestamp
		} else {
			page.Page.NextCursor = page.Items[len(page.Items)-1].Timestamp
		}
	}
	return page, nil
}

func (client *Client) Cancel(ctx context.Context, jobID string) (json.RawMessage, error) {
	return client.request(ctx, http.MethodPost, "/api/v1/jobs/"+url.PathEscape(jobID)+"/cancel", nil, nil)
}

func decodeJob(data json.RawMessage) (Job, error) {
	var job Job
	if err := json.Unmarshal(data, &job); err != nil {
		return Job{}, fmt.Errorf("decode job response")
	}
	job.Raw = append(json.RawMessage(nil), data...)
	return job, nil
}

func (client *Client) request(ctx context.Context, method, relativePath string, body []byte, headers map[string]string) (json.RawMessage, error) {
	target := *client.server
	relative, err := url.Parse(relativePath)
	if err != nil {
		return nil, fmt.Errorf("invalid platform request")
	}
	target.Path = relative.Path
	target.RawPath = relative.EscapedPath()
	target.RawQuery = relative.RawQuery
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create platform request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.token)
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	client.debugf("%s %s", method, redactedURL(&target))
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("platform request failed")
	}
	defer response.Body.Close()
	responseLimit := maxGenericPlatformResponseBytes
	if strings.HasPrefix(relative.Path, "/api/v1/jobs/") && strings.HasSuffix(relative.Path, "/logs") {
		responseLimit = maxLogPlatformResponseBytes
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read platform response")
	}
	if int64(len(contents)) > responseLimit {
		return nil, fmt.Errorf("platform response exceeds %d MiB; use --follow or a smaller --limit", responseLimit>>20)
	}
	var envelope apiEnvelope[json.RawMessage]
	if err := json.Unmarshal(contents, &envelope); err != nil {
		return nil, fmt.Errorf("invalid platform response")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices || !envelope.Success {
		code := "REQUEST_REJECTED"
		if envelope.Error != nil && envelope.Error.Code != "" {
			code = envelope.Error.Code
		}
		return nil, fmt.Errorf("platform request failed: %s", code)
	}
	return envelope.Data, nil
}

func (client *Client) debugf(format string, arguments ...any) {
	if client.debugWriter != nil {
		_, _ = fmt.Fprintf(client.debugWriter, "spk-rayjob: "+format+"\n", arguments...)
	}
}

func redactedURL(value *url.URL) string {
	copy := *value
	query := copy.Query()
	for key := range query {
		query.Set(key, "REDACTED")
	}
	copy.RawQuery = query.Encode()
	copy.User = nil
	return copy.String()
}

type configFile struct {
	Token  string `json:"token"`
	Server string `json:"server"`
}

func LoadToken(environmentToken, configPath string) (string, error) {
	if token := strings.TrimSpace(environmentToken); token != "" {
		return token, nil
	}
	config, err := loadConfig(configPath)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(config.Token) == "" {
		return "", fmt.Errorf("platform token is required")
	}
	return config.Token, nil
}

func loadConfig(configPath string) (configFile, error) {
	resolved, err := resolveConfigPath(configPath)
	if err != nil {
		return configFile{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return configFile{}, fmt.Errorf("read spk-rayjob config: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return configFile{}, fmt.Errorf("spk-rayjob config must be owner-only")
	}
	contents, err := os.ReadFile(resolved)
	if err != nil {
		return configFile{}, fmt.Errorf("read spk-rayjob config: %w", err)
	}
	var config configFile
	if err := json.Unmarshal(contents, &config); err != nil {
		return configFile{}, fmt.Errorf("invalid spk-rayjob config")
	}
	return config, nil
}

func writeConfig(configPath string, config configFile) error {
	if strings.TrimSpace(config.Server) == "" || strings.TrimSpace(config.Token) == "" {
		return fmt.Errorf("spk-rayjob config requires server and token")
	}
	resolved, err := resolveConfigPath(configPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o700); err != nil {
		return fmt.Errorf("create spk-rayjob config directory: %w", err)
	}
	contents, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode spk-rayjob config: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(resolved), ".config-")
	if err != nil {
		return fmt.Errorf("create spk-rayjob config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure spk-rayjob config: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write spk-rayjob config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync spk-rayjob config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close spk-rayjob config: %w", err)
	}
	if err := os.Rename(temporaryPath, resolved); err != nil {
		return fmt.Errorf("save spk-rayjob config: %w", err)
	}
	return nil
}

func resolveConfigPath(configPath string) (string, error) {
	if strings.TrimSpace(configPath) != "" {
		return configPath, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate spk-rayjob config: %w", err)
	}
	return filepath.Join(base, "spk-rayjob", "config.json"), nil
}
