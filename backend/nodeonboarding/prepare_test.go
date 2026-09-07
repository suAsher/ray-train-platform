package nodeonboarding

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareDirectoryContract(t *testing.T) {
	parent := t.TempDir()
	if err := prepareDirectory(parent, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(parent, "ray-cache"))
	if err != nil || info.Mode().Perm() != 0770 {
		t.Fatalf("directory mode: %v %v", info, err)
	}
	if err := prepareDirectory(parent, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(parent, "ray-cache"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := prepareDirectory(parent, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("accepted bad existing mode")
	}
}
func TestPrepareRejectsSymlinks(t *testing.T) {
	parent := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(parent, "ray-cache")); err != nil {
		t.Fatal(err)
	}
	if err := prepareDirectory(parent, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestPrepareRejectsReplaceableParent(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if err := prepareDirectory(parent, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("accepted parent permitting untrusted cache entry replacement")
	}
	if _, err := os.Stat(filepath.Join(parent, "ray-cache")); !os.IsNotExist(err) {
		t.Fatal("created directory under unsafe parent")
	}
}
