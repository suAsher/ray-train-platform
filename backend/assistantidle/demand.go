package assistantidle

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const demandURL = "https://raytrain.wellspiking.ai/api/v1/internal/assistant-idle/training-demand"

type DemandObserver interface {
	Pending(context.Context) (bool, error)
}

type HTTPDemandObserver struct {
	client *http.Client
	token  string
	url    string
	now    func() time.Time
}

func NewHTTPDemandObserver(tokenFile, caFile string) (*HTTPDemandObserver, error) {
	tokenBytes, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, errors.New("assistant demand credential is unavailable")
	}
	token := strings.TrimSpace(string(tokenBytes))
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(token) {
		return nil, errors.New("assistant demand credential is invalid")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		data, err := os.ReadFile(caFile)
		if err != nil {
			return nil, errors.New("assistant demand CA is unavailable")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(data) {
			return nil, errors.New("assistant demand CA is invalid")
		}
		tlsConfig.RootCAs = roots
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = tlsConfig
	return &HTTPDemandObserver{client: &http.Client{Transport: transport, Timeout: 1500 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, token: token, url: demandURL, now: time.Now}, nil
}

func (d *HTTPDemandObserver) Pending(ctx context.Context) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return false, errors.New("assistant demand request is invalid")
	}
	request.Header.Set("Authorization", "Bearer "+d.token)
	response, err := d.client.Do(request)
	if err != nil {
		return false, errors.New("assistant demand observation is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, errors.New("assistant demand observation was refused")
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    *struct {
			HasDemand        *bool     `json:"hasDemand"`
			GPUCount         *int64    `json:"gpuCount"`
			TrainingJobCount *int64    `json:"trainingJobCount"`
			WorkspaceCount   *int64    `json:"workspaceCount"`
			ObservedAt       time.Time `json:"observedAt"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4097))
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || !envelope.Success || envelope.Data == nil || envelope.Data.HasDemand == nil {
		return false, errors.New("assistant demand response is invalid")
	}
	data := envelope.Data
	age := d.now().Sub(data.ObservedAt)
	if data.ObservedAt.IsZero() || age > 3*time.Second || age < -time.Second || data.GPUCount == nil || data.TrainingJobCount == nil || data.WorkspaceCount == nil || *data.GPUCount < 0 || *data.TrainingJobCount < 0 || *data.WorkspaceCount < 0 {
		return false, errors.New("assistant demand response is stale or invalid")
	}
	if !*data.HasDemand && (*data.GPUCount > 0 || *data.TrainingJobCount > 0 || *data.WorkspaceCount > 0) {
		return false, errors.New("assistant demand response is inconsistent")
	}
	return *data.HasDemand, nil
}
