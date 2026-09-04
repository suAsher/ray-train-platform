package objectstore

import (
	"context"
	"io"
	"strings"
	"testing"
)

type fakeMultipartClient struct {
	fakeTOSClient
	created   tosMultipartCreateRequest
	uploaded  tosMultipartPartRequest
	completed tosMultipartCompleteRequest
	aborted   tosMultipartAbortRequest
}

func (client *fakeMultipartClient) CreateDataMultipart(_ context.Context, request tosMultipartCreateRequest) (string, error) {
	client.created = request
	return "provider-secret", nil
}
func (client *fakeMultipartClient) UploadDataPart(_ context.Context, request tosMultipartPartRequest) (string, error) {
	client.uploaded = request
	_, _ = io.ReadAll(request.Body)
	return "etag-1", nil
}
func (client *fakeMultipartClient) CompleteDataMultipart(_ context.Context, request tosMultipartCompleteRequest) error {
	client.completed = request
	return nil
}
func (client *fakeMultipartClient) AbortDataMultipart(_ context.Context, request tosMultipartAbortRequest) error {
	client.aborted = request
	return nil
}

func TestTOSMultipartKeepsEveryOperationInsideAuthorizedRoot(t *testing.T) {
	client := &fakeMultipartClient{}
	store, err := newTOSStoreWithClient(TOSConfig{Endpoint: "https://tos.example.com", Region: "cn", Bucket: "bucket", AccessKey: "ak", SecretKey: "sk"}, client)
	if err != nil {
		t.Fatal(err)
	}
	root, relative := "ray-train/tenants/a/users/u/files/", "models/model.pth"
	uploadID, err := store.CreateDataMultipart(context.Background(), root, relative, "application/octet-stream")
	if err != nil || uploadID != "provider-secret" {
		t.Fatalf("create id=%q err=%v", uploadID, err)
	}
	if _, err := store.UploadDataPart(context.Background(), root, relative, uploadID, 1, 7, strings.NewReader("payload")); err != nil {
		t.Fatal(err)
	}
	parts := []MultipartPart{{PartNumber: 1, SizeBytes: 7, ETag: "etag-1"}}
	if err := store.CompleteDataMultipart(context.Background(), root, relative, uploadID, parts); err != nil {
		t.Fatal(err)
	}
	if client.created.Key != root+relative || client.uploaded.Key != root+relative || client.completed.Key != root+relative {
		t.Fatalf("operations escaped root: create=%+v upload=%+v complete=%+v", client.created, client.uploaded, client.completed)
	}
	if err := store.AbortDataMultipart(context.Background(), root, relative, uploadID); err != nil || client.aborted.Key != root+relative {
		t.Fatalf("abort=%+v err=%v", client.aborted, err)
	}
	if _, err := store.CreateDataMultipart(context.Background(), root, "../other/secret", "application/octet-stream"); err == nil {
		t.Fatal("traversal was accepted")
	}
}

func TestTOSMultipartRejectsMalformedCompletion(t *testing.T) {
	client := &fakeMultipartClient{}
	store, _ := newTOSStoreWithClient(TOSConfig{Endpoint: "https://tos.example.com", Region: "cn", Bucket: "bucket", AccessKey: "ak", SecretKey: "sk"}, client)
	if err := store.CompleteDataMultipart(context.Background(), "safe/root/", "file", "upload", []MultipartPart{{PartNumber: 2, SizeBytes: 1, ETag: "etag"}}); err == nil {
		t.Fatal("non-contiguous parts were accepted")
	}
}
