package observability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"time"

	ml "ray-train-platform-backend/modellifecycle"
	"ray-train-platform-backend/modelregistry"
)

// ensureRegistryArtifact never trusts the server's success status or artifact
// tags alone. The object must download with the immutable snapshot's size/hash.
// An incomplete previous upload is replaced only in this dedicated signed run.
func (c *MLflowClient) ensureRegistryArtifact(ctx context.Context, root, file string, version ml.Version, open modelregistry.OpenSnapshot) error {
	apiPath := "/api/2.0/mlflow-artifacts/artifacts" + strings.TrimPrefix(root, "mlflow-artifacts:") + "/" + file
	endpoint, err := c.endpoint(apiPath)
	if err != nil {
		return modelregistry.ErrUnavailable
	}
	client := registryHTTPClient(c.HTTPClient, 30*time.Minute)
	if status, checkErr := checkRegistryArtifact(ctx, client, endpoint.String(), version); checkErr == nil {
		return nil
	} else if status != http.StatusNotFound && checkErr != modelregistry.ErrIntegrity {
		return checkErr
	}
	body, err := open(ctx)
	if err != nil || body == nil {
		return modelregistry.ErrUnavailable
	}
	defer body.Close()
	hash := sha256.New()
	limited := &io.LimitedReader{R: body, N: version.SizeBytes + 1}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint.String(), io.TeeReader(limited, hash))
	if err != nil {
		return modelregistry.ErrUnavailable
	}
	request.ContentLength = version.SizeBytes
	request.Header.Set("Content-Type", "application/octet-stream")
	response, sendErr := client.Do(request)
	if response != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
	}
	if limited.N != 1 || hex.EncodeToString(hash.Sum(nil)) != version.SHA256 {
		return modelregistry.ErrIntegrity
	}
	// Detect oversized readers even if a transport stopped at ContentLength.
	var extra [1]byte
	if n, readErr := io.ReadFull(body, extra[:]); n != 0 || readErr != io.EOF {
		return modelregistry.ErrIntegrity
	}
	if sendErr != nil || response == nil || response.StatusCode/100 != 2 {
		return modelregistry.ErrUnavailable
	}
	_, err = checkRegistryArtifact(ctx, client, endpoint.String(), version)
	return err
}

func checkRegistryArtifact(ctx context.Context, client *http.Client, endpoint string, version ml.Version) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, modelregistry.ErrUnavailable
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, modelregistry.ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return response.StatusCode, modelregistry.ErrUnavailable
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(response.Body, version.SizeBytes+1))
	if err != nil {
		return response.StatusCode, modelregistry.ErrUnavailable
	}
	if size != version.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != version.SHA256 {
		return response.StatusCode, modelregistry.ErrIntegrity
	}
	return response.StatusCode, nil
}
