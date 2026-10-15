package personalcsi

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"

	"ray-train-platform-backend/personalfs"
	"ray-train-platform-backend/personalstorage"
)

// FuseMounter identifies mounts from kernel metadata, never local marker files.
type FuseMounter struct {
	mu     sync.Mutex
	mounts map[string]*personalfs.MountHandle
}

func NewFuseMounter() *FuseMounter { return &FuseMounter{mounts: map[string]*personalfs.MountHandle{}} }
func (m *FuseMounter) Mount(ctx context.Context, target, id string, store personalstorage.Store) (Mounted, error) {
	handle, err := personalfs.NewMount(ctx, target, id, store)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.mounts[target] = handle
	m.mu.Unlock()
	return handle, nil
}
func unescapeMount(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, "\\").Replace(s)
}
func (m *FuseMounter) Inspect(target string) (string, bool, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	return inspectMountInfo(file, target)
}
func inspectMountInfo(reader io.Reader, target string) (string, bool, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	result := ""
	mounted := false
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 || unescapeMount(fields[4]) != target {
			continue
		}
		if mounted {
			return "", true, errors.New("ambiguous stacked mounts at personal publish target")
		}
		mounted = true
		if len(fields) < 10 {
			return "", true, errors.New("malformed mount metadata")
		}
		separator := -1
		for i, field := range fields {
			if field == "-" {
				separator = i
				break
			}
		}
		if separator < 0 || len(fields) < separator+4 {
			return "", true, errors.New("malformed mount metadata")
		}
		if fields[separator+1] != "fuse.wellspiking" || !strings.HasPrefix(fields[separator+2], "personalstorage:personal-") {
			result = "foreign"
			continue
		}
		id := strings.TrimPrefix(fields[separator+2], "personalstorage:")
		if !volumeHandle.MatchString(id) {
			result = "foreign"
			continue
		}
		result = id
	}
	if err := scanner.Err(); err != nil {
		return "", mounted, err
	}
	return result, mounted, nil
}
func (m *FuseMounter) Unmount(target string) error {
	m.mu.Lock()
	handle := m.mounts[target]
	m.mu.Unlock()
	if handle != nil {
		if err := handle.Close(); err != nil {
			return err
		}
		m.mu.Lock()
		delete(m.mounts, target)
		m.mu.Unlock()
		return nil
	}
	// Restart cleanup is permitted only after Driver validated the exact kernel
	// filesystem source and root-owned target ancestry for this volume.
	return syscall.Unmount(target, 0)
}
