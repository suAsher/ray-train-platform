package objectstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTOSStorePresignResponsePathIsBoundToRequestedObjectKey(t *testing.T) {
	objectKey := "tenants/tenant-a/users/user-a/sha256/" + testDigest + ".zip"
	otherKey := "tenants/tenant-a/users/user-a/sha256/" + strings.Repeat("f", 64) + ".zip"
	tests := []struct {
		name    string
		baseURL string
		wantErr bool
	}{
		{name: "virtual host canonical", baseURL: "https://private-bucket.tos.example.com/" + objectKey},
		{name: "path style canonical", baseURL: "https://tos.example.com/private-bucket/" + objectKey},
		{name: "virtual host other key", baseURL: "https://private-bucket.tos.example.com/" + otherKey, wantErr: true},
		{name: "path style other key", baseURL: "https://tos.example.com/private-bucket/" + otherKey, wantErr: true},
		{name: "extra prefix", baseURL: "https://private-bucket.tos.example.com/prefix/" + objectKey, wantErr: true},
		{name: "extra suffix", baseURL: "https://private-bucket.tos.example.com/" + objectKey + ".bak", wantErr: true},
		{name: "double slash", baseURL: "https://private-bucket.tos.example.com/tenants//tenant-a/users/user-a/sha256/" + testDigest + ".zip", wantErr: true},
		{name: "encoded path ambiguity", baseURL: "https://private-bucket.tos.example.com/tenants%2Ftenant-a/users/user-a/sha256/" + testDigest + ".zip", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeTOSClient{presignResponse: validPresignResponse(test.baseURL, fullUploadHeaders(1234, testDigest))}
			store, err := newTOSStoreWithClient(TOSConfig{
				Endpoint: "https://tos.example.com", Region: "cn", Bucket: "private-bucket",
				AccessKey: "ak", SecretKey: "sk",
			}, client)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.PresignPut(context.Background(), objectKey, testDigest, 1234, 15*time.Minute)
			if test.wantErr && err == nil {
				t.Fatal("presign response path was not bound to the requested object key")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("canonical presign path rejected: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), objectKey) {
				t.Fatal("presign validation error leaked object key")
			}
		})
	}
}
