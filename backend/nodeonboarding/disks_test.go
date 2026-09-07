package nodeonboarding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeBlock(t *testing.T, root, dev, disk string, partition bool) {
	t.Helper()
	target := filepath.Join(root, "devices", "pci", disk)
	if err := os.MkdirAll(filepath.Join(target, "slaves"), 0755); err != nil {
		t.Fatal(err)
	}
	if partition {
		target = filepath.Join(target, disk+"1")
	}
	if err := os.MkdirAll(filepath.Join(target, "slaves"), 0755); err != nil {
		t.Fatal(err)
	}
	if partition {
		if err := os.WriteFile(filepath.Join(target, "partition"), []byte("1\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "dev", "block"), 0755); err != nil {
		t.Fatal(err)
	}
	link, err := filepath.Rel(filepath.Join(root, "dev", "block"), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(link, filepath.Join(root, "dev", "block", dev)); err != nil {
		t.Fatal(err)
	}
}
func TestPhysicalDisksRejectSharedPartitions(t *testing.T) {
	for _, test := range []struct {
		name, first, second string
		wantError           bool
	}{{"independent", "sdb", "sdc", false}, {"root sibling", "sda", "sdc", true}, {"same data disk", "sdb", "sdb", true}} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			fakeBlock(t, root, "8:1", "sda", true)
			fakeBlock(t, root, "8:16", test.first, true)
			fakeBlock(t, root, "8:32", test.second, true)
			err := ValidatePhysicalDisks(validMounts, root)
			if (err != nil) != test.wantError {
				t.Fatalf("physical independence: %v", err)
			}
		})
	}
}
func TestPhysicalDisksFailClosed(t *testing.T) {
	root := t.TempDir()
	if err := ValidatePhysicalDisks(validMounts, root); err == nil {
		t.Fatal("accepted missing sysfs")
	}
	if err := ValidatePhysicalDisks(strings.ReplaceAll(validMounts, "8:32", "8:16"), root); err == nil {
		t.Fatal("accepted shared block identity")
	}
}

func TestPhysicalDiskMapperAncestry(t *testing.T) {
	root := t.TempDir()
	fakeBlock(t, root, "8:1", "sda", true)
	fakeBlock(t, root, "8:16", "sdb", true)
	fakeBlock(t, root, "8:32", "sdc", true)
	mapper := filepath.Join(root, "devices", "virtual", "block", "dm-0")
	if err := os.MkdirAll(filepath.Join(mapper, "slaves"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mapper, filepath.Join(root, "dev", "block", "253:0")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "devices", "pci", "sda", "sda1"), filepath.Join(mapper, "slaves", "sda1")); err != nil {
		t.Fatal(err)
	}
	mounts := strings.ReplaceAll(validMounts, "8:1 / / ", "253:0 / / ")
	if err := ValidatePhysicalDisks(mounts, root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(mapper, "slaves", "sda1")); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePhysicalDisks(mounts, root); err == nil {
		t.Fatal("accepted unbacked virtual disk")
	}
	if err := os.Symlink(mapper, filepath.Join(mapper, "slaves", "cycle")); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePhysicalDisks(mounts, root); err == nil {
		t.Fatal("accepted cyclic ancestry")
	}
}
