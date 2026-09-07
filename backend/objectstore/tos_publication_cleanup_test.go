package objectstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFailedPublicationCleanupOnlyDeletesServerScopedOutputs(t *testing.T) {
	c := newRecordingPublicationClient()
	root := "ray-train/platform/datasets"
	manifest := root + "/data/manifests/version-one.parquet"
	c.objects[manifest] = []byte("x")
	c.pages[""] = tosArtifactListResponse{Objects: []tosArtifactObject{{Key: root + "/data/publication/version-one/partitions/00001.json", SizeBytes: 1, ETag: "a", LastModified: time.Now()}}}
	s := &TOSStore{client: c, bucket: "target"}
	// A backend returning objects outside a requested prefix must never cause deletion.
	if _, err := s.PurgeFailedPublicationObjects(context.Background(), "target", root, "data", "version-one", []string{"run-one"}); err == nil {
		t.Fatal("expected prefix mismatch")
	}
	for _, op := range c.ops {
		if strings.HasPrefix(op, "delete ") {
			t.Fatalf("deleted before full validation: %s", op)
		}
	}
}

func TestFailedPublicationCleanupDeletesExactManifestAndPreservesSharedObjects(t *testing.T) {
	c := newRecordingPublicationClient()
	root := "ray-train/platform/datasets"
	manifest := root + "/data/manifests/version-one.parquet"
	shared := root + "/data/shards/sha256-shared.tar"
	source := "ray-train/public/labeled/source.pkl"
	for _, key := range []string{manifest, shared, source} {
		c.objects[key] = []byte("x")
	}
	s := &TOSStore{client: c, bucket: "target"}
	n, err := s.PurgeFailedPublicationObjects(context.Background(), "target", root, "data", "version-one", nil)
	if err != nil || n != 1 {
		t.Fatalf("deleted=%d err=%v", n, err)
	}
	if _, exists := c.objects[manifest]; exists {
		t.Fatal("manifest retained")
	}
	for _, key := range []string{shared, source} {
		if _, exists := c.objects[key]; !exists {
			t.Fatalf("deleted protected key %s", key)
		}
	}
	n, err = s.PurgeFailedPublicationObjects(context.Background(), "target", root, "data", "version-one", nil)
	if err != nil || n != 0 {
		t.Fatalf("retry deleted=%d err=%v", n, err)
	}
}

func TestFailedPublicationCleanupRejectsUnsafeScope(t *testing.T) {
	for _, id := range []string{"", "..", "a/b", "a%2fb"} {
		c := newRecordingPublicationClient()
		s := &TOSStore{client: c, bucket: "target"}
		if _, err := s.PurgeFailedPublicationObjects(context.Background(), "target", "ray-train/platform/datasets", id, "version-one", nil); err == nil {
			t.Fatalf("accepted %q", id)
		}
		if len(c.ops) != 0 {
			t.Fatal("unsafe request touched objects")
		}
	}
	s := &TOSStore{client: newRecordingPublicationClient(), bucket: "other"}
	if _, err := s.PurgeFailedPublicationObjects(context.Background(), "target", "ray-train/platform/datasets", "data", "version-one", nil); err == nil {
		t.Fatal("accepted wrong bucket")
	}
}

func TestFailedPublicationCleanupFailClosedOnListingAndDeletionErrors(t *testing.T) {
	for _, scenario := range []string{"loop", "directory", "delete", "cancel", "root", "runs"} {
		t.Run(scenario, func(t *testing.T) {
			c := newRecordingPublicationClient()
			s := &TOSStore{client: c, bucket: "target"}
			root := "ray-train/platform/datasets"
			runs := []string{}
			c.objects[root+"/data/manifests/version-one.parquet"] = []byte("x")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "loop":
				c.pages[""] = tosArtifactListResponse{NextContinuationToken: "next"}
				c.pages["next"] = tosArtifactListResponse{NextContinuationToken: "next"}
			case "directory":
				c.pages[""] = tosArtifactListResponse{Directories: []string{"unexpected"}}
			case "delete":
				c.deleteErr = errors.New("secret")
			case "cancel":
				cancel()
			case "root":
				root = "ray-train/public"
			case "runs":
				for i := 0; i < 101; i++ {
					runs = append(runs, "run")
				}
			}
			if _, err := s.PurgeFailedPublicationObjects(ctx, "target", root, "data", "version-one", runs); err == nil {
				t.Fatal("error not propagated")
			}
			if _, ok := c.objects["ray-train/platform/datasets/data/manifests/version-one.parquet"]; !ok {
				t.Fatal("failure removed manifest")
			}
		})
	}
}
