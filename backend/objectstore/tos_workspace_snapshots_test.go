package objectstore

import (
	"context"
	"reflect"
	"testing"
)

type fakeWorkspaceSnapshotClient struct {
	fakeTOSClient
	responses []tosArtifactListResponse
	requests  []tosArtifactListRequest
	copies    []tosCopyRequest
	markers   []string
}

func (client *fakeWorkspaceSnapshotClient) ListArtifacts(_ context.Context, request tosArtifactListRequest) (tosArtifactListResponse, error) {
	client.requests = append(client.requests, request)
	index := len(client.requests) - 1
	if index >= len(client.responses) {
		return tosArtifactListResponse{}, nil
	}
	return client.responses[index], nil
}

func (client *fakeWorkspaceSnapshotClient) CopyObject(_ context.Context, request tosCopyRequest) error {
	client.copies = append(client.copies, request)
	return nil
}

func (client *fakeWorkspaceSnapshotClient) PutDirectoryMarker(_ context.Context, _ string, key string) error {
	client.markers = append(client.markers, key)
	return nil
}

func TestTOSStoreSnapshotsOnlyWorkspaceFilesIntoNewImmutablePrefix(t *testing.T) {
	client := &fakeWorkspaceSnapshotClient{responses: []tosArtifactListResponse{{Objects: []tosArtifactObject{
		{Key: "ray-train/tenants/team-a/users/user-a/workspace/project/train.py"},
		{Key: "ray-train/tenants/team-a/users/user-a/workspace/project/.ray-train-keep"},
		{Key: "ray-train/tenants/team-a/users/user-b/workspace/project/private.py"},
	}}}}
	store, err := newTOSStoreWithClient(TOSConfig{Endpoint: "https://tos.example.com", Region: "cn", Bucket: "bucket", AccessKey: "ak", SecretKey: "sk"}, client)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := store.SnapshotWorkspace(context.Background(), "ray-train/tenants/team-a/users/user-a/workspace/", "project", "ray-train/tenants/team-a/users/user-a/snapshots/snapshot-a/")
	if err != nil {
		t.Fatal(err)
	}
	if copied != 1 || !reflect.DeepEqual(client.copies, []tosCopyRequest{{Bucket: "bucket", SourceKey: "ray-train/tenants/team-a/users/user-a/workspace/project/train.py", DestinationKey: "ray-train/tenants/team-a/users/user-a/snapshots/snapshot-a/train.py"}}) {
		t.Fatalf("copied=%d copies=%#v", copied, client.copies)
	}
	if !reflect.DeepEqual(client.markers, []string{"ray-train/tenants/team-a/users/user-a/snapshots/snapshot-a/.ray-train-snapshot"}) {
		t.Fatalf("markers=%#v", client.markers)
	}
	if _, err := store.SnapshotWorkspace(context.Background(), "ray-train/tenants/team-a/users/user-a/workspace/", "../other", "ray-train/tenants/team-a/users/user-a/snapshots/snapshot-b/"); err == nil {
		t.Fatal("traversal source was accepted")
	}
}
