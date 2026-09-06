package spkrayjob

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseRejectsTruncatedPlatformHeaders(t *testing.T) {
	macho := make([]byte, 8)
	binary.LittleEndian.PutUint32(macho, 0xfeedfacf)
	binary.LittleEndian.PutUint32(macho[4:], 0x0100000c)
	pe := make([]byte, 128)
	copy(pe, "MZ")
	binary.LittleEndian.PutUint32(pe[0x3c:], 64)
	copy(pe[64:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(pe[68:], 0x8664)
	for _, tc := range []struct {
		system, arch string
		data         []byte
	}{{"darwin", "arm64", macho}, {"windows", "amd64", pe}} {
		t.Run(tc.system, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "binary")
			if err := os.WriteFile(p, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := verifyReleaseArtifactBinary(p, ReleaseArtifact{OS: tc.system, Arch: tc.arch}); err == nil {
				t.Fatal("truncated executable accepted")
			}
		})
	}
}

func TestWindowsUpgradePreservesPreviousBackup(t *testing.T) {
	target := filepath.Join(t.TempDir(), "spk.exe")
	if err := os.WriteFile(target+".previous", []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkUpgradeBackup(target, "windows"); err == nil {
		t.Fatal("existing Windows backup must require explicit preservation")
	}
	if err := checkUpgradeBackup(target, "linux"); err != nil {
		t.Fatal(err)
	}
}
