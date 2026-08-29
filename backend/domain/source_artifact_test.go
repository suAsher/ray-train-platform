package domain

import (
	"strings"
	"testing"
	"time"
)

const artifactDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestNewSourceArtifactValidatesDigestSizeAndBuildsCanonicalKey(t *testing.T) {
	now := time.Date(2026, 8, 10, 1, 2, 3, 0, time.UTC)
	expires := now.Add(15 * time.Minute)
	artifact, err := NewSourceArtifact(SourceArtifactInput{
		ID: "artifact-1", TenantID: "tenant-a", UserID: "user_1", SHA256: artifactDigest, SizeBytes: MaxSourceArtifactSize,
	}, expires, now)
	if err != nil {
		t.Fatalf("new source artifact: %v", err)
	}
	wantKey := "ray-train/tenants/tenant-a/users/user_1/workspace/.ray-train-archives/" + artifactDigest + ".zip"
	if artifact.ObjectKey != wantKey || artifact.State != SourceArtifactPending || artifact.UploadExpiresAt != expires || artifact.CreatedAt != now {
		t.Fatalf("unexpected artifact fields: key=%q state=%q expires=%s created=%s", artifact.ObjectKey, artifact.State, artifact.UploadExpiresAt, artifact.CreatedAt)
	}
}

func TestNewSourceArtifactSeparatesOpaqueOwnerFromPersistedStorageRoot(t *testing.T) {
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	root, err := PersonalDataRootFor("local", "guofeng.su")
	if err != nil {
		t.Fatalf("personal root: %v", err)
	}
	artifact, err := NewSourceArtifact(SourceArtifactInput{
		ID: "artifact-storage-root", TenantID: "local", UserID: "opaque-subject-123", StorageRoot: root,
		SHA256: artifactDigest, SizeBytes: 10,
	}, now.Add(15*time.Minute), now)
	if err != nil {
		t.Fatalf("new source artifact: %v", err)
	}
	if artifact.UserID != "opaque-subject-123" || artifact.StorageRoot != root {
		t.Fatalf("owner and storage root must remain distinct: %+v", artifact)
	}
	want := root + "workspace/.ray-train-archives/" + artifactDigest + ".zip"
	if artifact.ObjectKey != want {
		t.Fatalf("artifact key=%q want=%q", artifact.ObjectKey, want)
	}
	if err := artifact.Validate(); err != nil {
		t.Fatalf("stored artifact should validate: %v", err)
	}
}

func TestNewRequestScopedSourceArtifactUsesServerArtifactIDInCanonicalKey(t *testing.T) {
	now := time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC)
	artifact, err := NewRequestScopedSourceArtifact(SourceArtifactInput{
		ID: "artifact-0123456789abcdef01234567", TenantID: "tenant-a", UserID: "user-a",
		SHA256: artifactDigest, SizeBytes: 10,
	}, now.Add(15*time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	want := "ray-train/tenants/tenant-a/users/user-a/workspace/.ray-train-archives/artifact-0123456789abcdef01234567/" + artifactDigest + ".zip"
	if artifact.ObjectKey != want {
		t.Fatalf("request artifact key=%q want=%q", artifact.ObjectKey, want)
	}
	if err := artifact.Validate(); err != nil {
		t.Fatalf("request-scoped artifact should validate: %v", err)
	}
	mounted, err := SourceArtifactMountedArchivePath("tenant-a", artifact.ObjectKey, artifact.ID, artifactDigest)
	if err != nil || mounted != "/mnt/platform-workspace-snapshot/workspace/.ray-train-archives/artifact-0123456789abcdef01234567/"+artifactDigest+".zip" {
		t.Fatalf("mounted request archive=%q err=%v", mounted, err)
	}
}

func TestNewRequestScopedSourceArtifactRejectsUnsafeServerArtifactID(t *testing.T) {
	now := time.Now().UTC()
	for _, id := range []string{"../artifact", "artifact/foreign", "artifact\nforeign", strings.Repeat("a", 129)} {
		if _, err := NewRequestScopedSourceArtifact(SourceArtifactInput{ID: id, TenantID: "tenant-a", UserID: "user-a", SHA256: artifactDigest, SizeBytes: 10}, now.Add(time.Minute), now); err == nil {
			t.Fatalf("unsafe server artifact ID accepted in object key: %q", id)
		}
	}
}

func TestNewSourceArtifactRejectsInvalidDigestAndSizeBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		digest string
		size   int64
	}{
		{name: "empty", digest: "", size: 1},
		{name: "uppercase", digest: strings.ToUpper(artifactDigest), size: 1},
		{name: "short", digest: artifactDigest[:63], size: 1},
		{name: "zero", digest: artifactDigest, size: 0},
		{name: "too large", digest: artifactDigest, size: MaxSourceArtifactSize + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewSourceArtifact(SourceArtifactInput{ID: "artifact", TenantID: "tenant", UserID: "user", SHA256: test.digest, SizeBytes: test.size}, time.Now().Add(time.Minute), time.Now())
			if err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestSourceArtifactObjectKeyRejectsUnsafeSegments(t *testing.T) {
	unsafe := []string{"", ".", "..", "a/b", `a\b`, "a%2fb", " a", "a ", "a\nb", "用户", strings.Repeat("a", 129)}
	for _, segment := range unsafe {
		t.Run(strings.ReplaceAll(segment, "/", "slash"), func(t *testing.T) {
			if _, err := SourceArtifactObjectKey(segment, "safe-user", artifactDigest); err == nil {
				t.Fatalf("unsafe tenant segment accepted: %q", segment)
			}
			if _, err := SourceArtifactObjectKey("safe-tenant", segment, artifactDigest); err == nil {
				t.Fatalf("unsafe user segment accepted: %q", segment)
			}
		})
	}
}

func TestSourceArtifactMarkReadyIsImmutableAndIdempotent(t *testing.T) {
	created := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	original, err := NewSourceArtifact(SourceArtifactInput{ID: "artifact", TenantID: "tenant", UserID: "user", SHA256: artifactDigest, SizeBytes: 10}, created.Add(15*time.Minute), created)
	if err != nil {
		t.Fatal(err)
	}
	completed := created.Add(time.Minute)
	ready, err := original.MarkReady(completed)
	if err != nil {
		t.Fatal(err)
	}
	if original.State != SourceArtifactPending || original.CompletedAt != nil {
		t.Fatal("mark ready mutated original artifact")
	}
	if ready.State != SourceArtifactReady || ready.CompletedAt == nil || !ready.CompletedAt.Equal(completed) {
		t.Fatalf("unexpected ready artifact: state=%q completed=%v", ready.State, ready.CompletedAt)
	}
	again, err := ready.MarkReady(completed.Add(time.Minute))
	if err != nil || again.CompletedAt == nil || !again.CompletedAt.Equal(completed) {
		t.Fatalf("repeated completion must preserve first completion: completed=%v err=%v", again.CompletedAt, err)
	}
}
