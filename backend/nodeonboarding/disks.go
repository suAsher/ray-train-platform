package nodeonboarding

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ValidatePhysicalDisks proves that the three mount devices have disjoint backing
// disks using read-only host sysfs. Partitions collapse to their parent disk;
// device-mapper/RAID devices expand through slaves. Unknown virtual leaves fail.
func ValidatePhysicalDisks(mountinfo, sysRoot string) error {
	if err := ValidateHostMounts(mountinfo); err != nil {
		return err
	}
	canonicalRoot, err := filepath.EvalSymlinks(sysRoot)
	if err != nil {
		return err
	}
	used := map[string]string{}
	for _, line := range strings.Split(mountinfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		target := fields[4]
		if target != "/" && target != "/data1" && target != "/data2" {
			continue
		}
		device, err := blockDevice(fields[2])
		if err != nil {
			return err
		}
		leaves, err := physicalLeaves(canonicalRoot, filepath.Join(canonicalRoot, "dev", "block", device), map[string]bool{}, 0)
		if err != nil {
			return fmt.Errorf("mount %s physical device: %w", target, err)
		}
		for _, leaf := range leaves {
			if previous := used[leaf]; previous != "" && previous != target {
				return fmt.Errorf("mounts %s and %s share physical disk %s", previous, target, filepath.Base(leaf))
			}
			used[leaf] = target
		}
	}
	return nil
}

func physicalLeaves(root, entry string, seen map[string]bool, depth int) ([]string, error) {
	if depth > 16 {
		return nil, fmt.Errorf("block ancestry exceeds bound")
	}
	resolved, err := filepath.EvalSymlinks(entry)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(resolved, filepath.Join(root, "devices")+string(os.PathSeparator)) {
		return nil, fmt.Errorf("block ancestry escapes host sysfs devices")
	}
	if seen[resolved] {
		return nil, fmt.Errorf("cyclic block ancestry")
	}
	nextSeen := make(map[string]bool, len(seen)+1)
	for name, value := range seen {
		nextSeen[name] = value
	}
	nextSeen[resolved] = true
	partition, err := os.ReadFile(filepath.Join(resolved, "partition"))
	if err == nil {
		number, err := strconv.ParseUint(strings.TrimSpace(string(partition)), 10, 32)
		if err != nil || number == 0 {
			return nil, fmt.Errorf("invalid sysfs partition metadata")
		}
		return physicalLeaves(root, filepath.Dir(resolved), nextSeen, depth+1)
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	slaves, err := os.ReadDir(filepath.Join(resolved, "slaves"))
	if err != nil {
		return nil, err
	}
	if len(slaves) > 64 {
		return nil, fmt.Errorf("too many backing block devices")
	}
	if len(slaves) == 0 {
		if strings.HasPrefix(resolved, filepath.Join(root, "devices", "virtual")+string(os.PathSeparator)) {
			return nil, fmt.Errorf("unsupported virtual block leaf")
		}
		return []string{resolved}, nil
	}
	var leaves []string
	for _, slave := range slaves {
		backing, err := physicalLeaves(root, filepath.Join(resolved, "slaves", slave.Name()), nextSeen, depth+1)
		if err != nil {
			return nil, err
		}
		leaves = append(leaves, backing...)
	}
	return leaves, nil
}
