package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"ray-train-platform-backend/trackingartifacts"
)

func TestTrackingArtifactsUseOnlyServerOwnedImmutablePrefix(t *testing.T) {
	client := &trackingObjectClient{recordingPublicationClient: recordingPublicationClient{objects: map[string][]byte{}}}
	store, err := newTOSStoreWithClient(TOSConfig{Endpoint: "https://tos.example.com", Region: "cn-test", Bucket: "private-bucket", AccessKey: "ak", SecretKey: "sk"}, client)
	if err != nil {
		t.Fatal(err)
	}
	objects := store.TrackingArtifacts()
	id := "0123456789abcdef0123456789abcdef"
	payload := []byte("safe-model")
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := objects.Put(ctx, id, 1, hash, payload); err != nil {
			t.Fatal(err)
		}
	}
	changed := []byte("different!")
	different := sha256.Sum256(changed)
	if err := objects.Put(ctx, id, 1, hex.EncodeToString(different[:]), changed); !errors.Is(err, trackingartifacts.ErrConflict) {
		t.Fatalf("immutable overwrite: %v", err)
	}
	key := "raytrain-mlflow-artifacts/" + id + "/parts/1"
	if string(client.objects[key]) != string(payload) {
		t.Fatalf("wrong scope: %v", client.objects)
	}
	body, size, err := objects.Get(ctx, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	body.Close()
	if err != nil || size != int64(len(payload)) || string(got) != string(payload) {
		t.Fatalf("get: %d %v", size, err)
	}
	for _, bad := range []string{"../personal", id + "/../secret", strings.ToUpper(id), ""} {
		if err := objects.Put(ctx, bad, 1, hash, payload); !errors.Is(err, trackingartifacts.ErrInvalid) {
			t.Errorf("unsafe id %q %v", bad, err)
		}
	}
	for _, index := range []int{-1, 0, 2561} {
		if _, _, err := objects.Get(ctx, id, index); !errors.Is(err, trackingartifacts.ErrInvalid) {
			t.Errorf("unsafe part %d %v", index, err)
		}
	}
	if err := objects.Delete(ctx, id, 1); err != nil {
		t.Fatal(err)
	}
	if _, ok := client.objects[key]; ok {
		t.Fatal("cancel did not delete owned key")
	}
}

type trackingObjectClient struct{ recordingPublicationClient }

func (c *trackingObjectClient) Head(_ context.Context, _, key string) (ObjectInfo, error) {
	b, ok := c.objects[key]
	if !ok {
		return ObjectInfo{}, ErrNotFound
	}
	sum := sha256.Sum256(b)
	return ObjectInfo{SizeBytes: int64(len(b)), Metadata: map[string]string{"sha256": hex.EncodeToString(sum[:])}}, nil
}
