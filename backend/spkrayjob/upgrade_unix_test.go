//go:build !windows

package spkrayjob

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicReplacementPreservesExistingBackup(t *testing.T) {
	target := filepath.Join(t.TempDir(), "spk-rayjob")
	os.WriteFile(target, []byte("old"), 0700)
	backup := fmt.Sprintf("%s.previous.%d", target, os.Getpid())
	os.WriteFile(backup, []byte("older backup"), 0700)
	if _, err := replaceExecutable(target+".missing", target, target+".upgrade-lock"); err == nil {
		t.Fatal("missing staged file accepted")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "old" {
		t.Fatal("target lost after failure")
	}
	b, _ = os.ReadFile(backup)
	if string(b) != "older backup" {
		t.Fatal("preexisting backup overwritten")
	}
}
