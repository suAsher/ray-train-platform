package observability

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	ml "ray-train-platform-backend/modellifecycle"
)

func TestModelRegistryRealMLflowSmoke(t *testing.T) {
	base := strings.TrimSpace(os.Getenv("MLFLOW_REGISTRY_SMOKE_URL"))
	if base == "" {
		t.Skip("set MLFLOW_REGISTRY_SMOKE_URL to an isolated MLflow service")
	}
	validateTrackingSmokeURL(t, base)
	data := []byte("RayTrain checkpoint registry protocol smoke; not a trained model")
	sum := sha256.Sum256(data)
	model := ml.Model{ID: uuid.NewString(), Name: "Registry protocol smoke"}
	version := ml.Version{ID: uuid.NewString(), ModelID: model.ID, State: ml.Ready, FileName: "smoke.bin", SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	client := &MLflowClient{BaseURL: base, ProvenanceKey: bytes.Repeat([]byte("s"), 32)}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	open := func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	link, err := client.EnsureVersion(ctx, model, version, open)
	if err != nil {
		t.Fatalf("register copied checkpoint: %v", err)
	}
	again, err := client.EnsureVersion(ctx, model, version, open)
	if err != nil || link != again {
		t.Fatalf("repeated association: %#v %v", again, err)
	}
	if link.Version != "1" || !strings.HasPrefix(link.SourceURI, "mlflow-artifacts:/") {
		t.Fatalf("invalid registry link %#v", link)
	}
	t.Logf("registered checkpoint %s version %s; copied bytes verified before registration", link.RegisteredName, link.Version)
}
