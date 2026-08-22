package objectstore

import (
	"context"
	"reflect"
	"testing"
	"time"
)

type fakeArtifactClient struct {
	fakeTOSClient
	request  tosArtifactListRequest
	response tosArtifactListResponse
	err      error
}

func (client *fakeArtifactClient) ListArtifacts(_ context.Context, request tosArtifactListRequest) (tosArtifactListResponse, error) {
	client.request = request
	return client.response, client.err
}

func TestTOSStoreListArtifactEntriesKeepsEntriesInsideTaskRoot(t *testing.T) {
	modified := time.Date(2026, 8, 12, 4, 0, 0, 0, time.UTC)
	client := &fakeArtifactClient{response: tosArtifactListResponse{
		Directories: []string{
			"outputs/runs/job-a/checkpoints/",
			"outputs/runs/job-a/logs/",
			"outputs/runs/job-b/private/",
		},
		Objects: []tosArtifactObject{
			{Key: "outputs/runs/job-a/metrics.json", SizeBytes: 123, LastModified: modified},
			{Key: "outputs/runs/job-a/nested/part.bin", SizeBytes: 456, LastModified: modified},
			{Key: "outputs/runs/job-b/secret.txt", SizeBytes: 789, LastModified: modified},
		},
		NextContinuationToken: "opaque-next-token",
	}}
	store, err := newTOSStoreWithClient(TOSConfig{
		Endpoint: "https://tos.example.com", Region: "cn-test", Bucket: "private-bucket", AccessKey: "ak", SecretKey: "sk",
	}, client)
	if err != nil {
		t.Fatal(err)
	}

	page, err := store.ListArtifactEntries(context.Background(), "outputs/runs/job-a", "", "", 50)
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	want := []ArtifactEntry{
		{Name: "checkpoints", Type: ArtifactDirectory},
		{Name: "logs", Type: ArtifactDirectory},
		{Name: "metrics.json", Type: ArtifactFile, SizeBytes: 123, LastModified: modified},
	}
	if !reflect.DeepEqual(page.Entries, want) || page.NextCursor != "opaque-next-token" {
		t.Fatalf("unexpected artifact page: %#v", page)
	}
	if client.request.Bucket != "private-bucket" || client.request.Prefix != "outputs/runs/job-a/" || client.request.Delimiter != "/" || client.request.MaxKeys != 50 {
		t.Fatalf("unsafe TOS list request: %#v", client.request)
	}
	if _, err := store.ListArtifactEntries(context.Background(), "outputs/runs/job-a", "../job-b", "", 50); err == nil {
		t.Fatal("artifact traversal was accepted")
	}
}
