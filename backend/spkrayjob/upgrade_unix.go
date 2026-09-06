//go:build !windows

package spkrayjob

import (
	"fmt"
	"os"
)

func replaceExecutable(staged, target, lock string) (bool, error) {
	backup := ""
	for attempt := 0; attempt < 100; attempt++ {
		candidate := fmt.Sprintf("%s.previous.%d", target, os.Getpid())
		if attempt > 0 {
			candidate = fmt.Sprintf("%s.%d", candidate, attempt)
		}
		// Link without removing target; O_EXCL semantics preserve prior backups.
		if err := os.Link(target, candidate); err == nil {
			backup = candidate
			break
		} else if !os.IsExist(err) {
			return false, err
		}
	}
	if backup == "" {
		return false, fmt.Errorf("cannot allocate rollback file")
	}
	// A single rename keeps target continuously present, including on failure.
	if err := os.Rename(staged, target); err != nil {
		return false, err
	}
	return false, nil
}
