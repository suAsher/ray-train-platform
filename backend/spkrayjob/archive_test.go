package spkrayjob

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestBuildArchiveIsDeterministicAndHonorsIgnoreFiles(t *testing.T) {
	root := t.TempDir()
	writeArchiveTestFile(t, root, "z.txt", "z")
	writeArchiveTestFile(t, root, "nested/a.txt", "a")
	writeArchiveTestFile(t, root, "ignored.tmp", "ignored")
	writeArchiveTestFile(t, root, "ray-ignored.txt", "ignored")
	writeArchiveTestFile(t, root, ".git/config", "never include")
	writeArchiveTestFile(t, root, ".gitignore", "*.tmp\n")
	writeArchiveTestFile(t, root, ".rayignore", "ray-ignored.txt\n")

	first, err := BuildArchive(root)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(first.Path)
	second, err := BuildArchive(root)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(second.Path)
	firstBytes, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(second.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != string(secondBytes) {
		t.Fatal("archives differ for identical input")
	}
	sum := sha256.Sum256(firstBytes)
	if first.SHA256 != hex.EncodeToString(sum[:]) || first.SizeBytes != int64(len(firstBytes)) {
		t.Fatalf("archive metadata=%+v", first)
	}

	reader, err := zip.OpenReader(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
		if !file.Modified.Equal(zipEpoch) {
			t.Fatalf("entry %s timestamp=%s", file.Name, file.Modified)
		}
	}
	want := []string{".gitignore", ".rayignore", "nested/a.txt", "z.txt"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("archive names=%v want=%v", names, want)
	}
}

func TestBuildArchivePreservesRootAnchoringInIgnoreRules(t *testing.T) {
	root := t.TempDir()
	writeArchiveTestFile(t, root, ".gitignore", "/run*/\n")
	writeArchiveTestFile(t, root, "run-output/result.txt", "ignored at repository root")
	writeArchiveTestFile(t, root, "mmdet3d/runner/__init__.py", "must remain in source package")

	archive, err := BuildArchive(root)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(archive.Path)

	reader, err := zip.OpenReader(archive.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
	}
	want := []string{".gitignore", "mmdet3d/runner/__init__.py"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("archive names=%v want=%v", names, want)
	}
}

func TestBuildArchiveKeepsUnanchoredDirectoryRulesRecursive(t *testing.T) {
	root := t.TempDir()
	writeArchiveTestFile(t, root, ".gitignore", "run*/\n")
	writeArchiveTestFile(t, root, "mmdet3d/runner/__init__.py", "ignored by recursive rule")

	archive, err := BuildArchive(root)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(archive.Path)

	reader, err := zip.OpenReader(archive.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
	}
	want := []string{".gitignore"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("archive names=%v want=%v", names, want)
	}
}

func TestBuildArchiveRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(external, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildArchive(root); err == nil {
		t.Fatal("escaping symlink was accepted")
	}
}

func TestBuildArchiveRejectsLogicalContentAboveLimit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.bin")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxLogicalArchiveSize + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildArchive(root); err == nil {
		t.Fatal("archive above logical size limit was accepted")
	}
}

func writeArchiveTestFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

var _ = time.Time{}
