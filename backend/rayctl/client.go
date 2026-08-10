package rayctl

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

type apiEnvelope[T any] struct {
	Success bool `json:"success"`
	Data    T    `json:"data"`
	Error   *struct {
		Code string `json:"code"`
	} `json:"error"`
}

func NewClient(options ClientOptions) (*Client, error) {
	server, err := url.Parse(strings.TrimSpace(options.ServerURL))
	if err != nil || server.Scheme != "https" || server.Host == "" || server.User != nil || server.RawQuery != "" || server.Fragment != "" {
		return nil, fmt.Errorf("invalid platform server URL")
	}
	if strings.TrimSpace(options.Token) == "" {
		return nil, fmt.Errorf("platform token is required")
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	if caFile := strings.TrimSpace(options.CAFile); caFile != "" {
		configured, err := clientWithCA(httpClient, caFile)
		if err != nil {
			return nil, err
		}
		httpClient = configured
	}
	return &Client{server: server, token: options.Token, httpClient: httpClient, debugWriter: options.DebugWriter}, nil
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
	archive, err := BuildArchive(directory)
	if err != nil {
		return Job{}, err
	}
	defer os.Remove(archive.Path)
	artifact, err := client.CreateArtifact(ctx, archive)
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
	spec.Source = domain.CodeSource{Type: "artifact", ArtifactID: artifact.ArtifactID}
	return client.Submit(ctx, spec)
}

func (client *Client) CreateArtifact(ctx context.Context, archive Archive) (Artifact, error) {
	body, err := json.Marshal(map[string]any{"sha256": archive.SHA256, "sizeBytes": archive.SizeBytes})
	if err != nil {
		return Artifact{}, err
	}
	data, err := client.request(ctx, http.MethodPost, "/api/v1/source-artifacts", body, nil)
	if err != nil {
		return Artifact{}, err
	}
	var artifact Artifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return Artifact{}, fmt.Errorf("decode source artifact response")
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
	body, err := json.Marshal(struct {
		Spec domain.JobSpec `json:"spec"`
	}{Spec: spec})
	if err != nil {
		return Job{}, err
	}
	data, err := client.request(ctx, http.MethodPost, "/api/v1/jobs", body, nil)
	if err != nil {
		return Job{}, err
	}
	return decodeJob(data)
}

func (client *Client) Status(ctx context.Context, jobID string) (Job, error) {
	data, err := client.request(ctx, http.MethodGet, "/api/v1/jobs/"+url.PathEscape(jobID), nil, nil)
	if err != nil {
		return Job{}, err
	}
	return decodeJob(data)
}

func (client *Client) Logs(ctx context.Context, jobID string, limit int) (json.RawMessage, error) {
	path := "/api/v1/jobs/" + url.PathEscape(jobID) + "/logs"
	if limit > 0 {
		path += "?limit=" + fmt.Sprintf("%d", limit)
	}
	return client.request(ctx, http.MethodGet, path, nil, nil)
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
	contents, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read platform response")
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
		_, _ = fmt.Fprintf(client.debugWriter, "rayctl: "+format+"\n", arguments...)
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
	if strings.TrimSpace(configPath) == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return configFile{}, fmt.Errorf("locate rayctl config: %w", err)
		}
		configPath = filepath.Join(base, "rayctl", "config.json")
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return configFile{}, fmt.Errorf("read rayctl config: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return configFile{}, fmt.Errorf("rayctl config must be owner-only")
	}
	contents, err := os.ReadFile(configPath)
	if err != nil {
		return configFile{}, fmt.Errorf("read rayctl config: %w", err)
	}
	var config configFile
	if err := json.Unmarshal(contents, &config); err != nil {
		return configFile{}, fmt.Errorf("invalid rayctl config")
	}
	return config, nil
}
