package objectstore

import (
	"context"
	"testing"
	"time"
)

func TestNewTOSStoreRejectsEndpointWithPath(t *testing.T) {
	_, err := NewTOSStore(TOSConfig{
		Endpoint: "https://tos-cn-beijing.volces.com/unexpected/path",
		Region:   "cn-beijing", Bucket: "bucket", AccessKey: "ak", SecretKey: "sk",
	})
	if err == nil {
		t.Fatal("TOS endpoint with a path must be rejected")
	}
}

func TestTOSStorePresignPutRejectsInvalidDigestAtStorageBoundary(t *testing.T) {
	store, err := NewTOSStore(TOSConfig{
		Endpoint: "https://tos-cn-beijing.volces.com", Region: "cn-beijing",
		Bucket: "bucket", AccessKey: "ak", SecretKey: "sk",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.PresignPut(context.Background(), "safe-key", "NOT-A-SHA256", 1, 15*time.Minute)
	if err == nil {
		t.Fatal("invalid digest must be rejected before signing")
	}
}
