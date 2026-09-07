package nodeonboarding

import (
	"fmt"
	"path"
	"strconv"
	"strings"
)

// ValidateHostMounts validates Linux /proc/1/mountinfo obtained from the host's
// mount namespace. The caller must establish that provenance. It requires three
// distinct writable block filesystems at /, /data1 and /data2. It rejects escaped
// fields rather than guessing their decoded meaning, including on unrelated lines.
// This is not a write probe, capacity check, or complete node readiness check.
func ValidateHostMounts(mountinfo string) error {
	seen := make(map[string]bool)
	devices := make(map[string]bool)
	for _, line := range strings.Split(mountinfo, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if strings.Contains(line, `\`) || len(fields) < 10 {
			return fmt.Errorf("malformed or escaped mountinfo")
		}
		separator := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				separator = i
				break
			}
		}
		if separator < 6 || len(fields) != separator+4 {
			return fmt.Errorf("malformed mountinfo separator")
		}
		target := fields[4]
		if target != "/" && target != "/data1" && target != "/data2" {
			continue
		}
		if seen[target] {
			return fmt.Errorf("ambiguous mount at %s", target)
		}
		if fields[3] != "/" {
			return fmt.Errorf("mount %s uses a filesystem subdirectory", target)
		}
		device, err := blockDevice(fields[2])
		if err != nil {
			return fmt.Errorf("mount %s: %w", target, err)
		}
		if devices[device] {
			return fmt.Errorf("mount %s shares a device", target)
		}
		fs, source := fields[separator+1], fields[separator+2]
		if fs != "ext4" && fs != "xfs" {
			return fmt.Errorf("mount %s has unsupported filesystem", target)
		}
		if !strings.HasPrefix(source, "/dev/") || source == "/dev/" || path.Clean(source) != source {
			return fmt.Errorf("mount %s is not a block source", target)
		}
		if !writable(fields[5]) || !writable(fields[separator+3]) {
			return fmt.Errorf("mount %s is not writable", target)
		}
		seen[target] = true
		devices[device] = true
	}
	for _, target := range []string{"/", "/data1", "/data2"} {
		if !seen[target] {
			return fmt.Errorf("missing exact mount %s", target)
		}
	}
	return nil
}

func writable(options string) bool {
	hasRW := false
	for _, option := range strings.Split(options, ",") {
		if option == "ro" {
			return false
		}
		if option == "rw" {
			hasRW = true
		}
	}
	return hasRW
}

func blockDevice(raw string) (string, error) {
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid block device")
	}
	major, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil || major == 0 {
		return "", fmt.Errorf("invalid block major number")
	}
	minor, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return "", fmt.Errorf("invalid block minor number")
	}
	return fmt.Sprintf("%d:%d", major, minor), nil
}
