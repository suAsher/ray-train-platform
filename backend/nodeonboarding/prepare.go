package nodeonboarding

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

// PrepareHost is intentionally fixed: no user-supplied host path or mode exists.
func PrepareHost() error {
	metadata, err := os.ReadFile("/host-mountinfo")
	if err != nil {
		return err
	}
	if err := ValidateHostMounts(string(metadata)); err != nil {
		return err
	}
	if err := ValidatePhysicalDisks(string(metadata), "/host-sys"); err != nil {
		return err
	}
	for _, parent := range []string{"/data1", "/data2"} {
		if err := prepareDirectory(parent, 0, 0); err != nil {
			return fmt.Errorf("%s/ray-cache: %w", parent, err)
		}
	}
	return nil
}
func prepareDirectory(parent string, uid, gid int) error {
	fd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var parentStat unix.Stat_t
	if err := unix.Fstat(fd, &parentStat); err != nil {
		return err
	}
	if int(parentStat.Uid) != uid || uint32(parentStat.Mode)&0022 != 0 {
		return fmt.Errorf("parent mount must be owned by uid %d and not group/world writable", uid)
	}
	created := false
	if err := unix.Mkdirat(fd, "ray-cache", 0770); err == nil {
		created = true
	} else if err != unix.EEXIST {
		return err
	}
	directory, err := unix.Openat(fd, "ray-cache", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(directory)
	var st unix.Stat_t
	if err := unix.Fstat(directory, &st); err != nil {
		return err
	}
	if int(st.Uid) != uid || int(st.Gid) != gid {
		return fmt.Errorf("expected uid:gid %d:%d", uid, gid)
	}
	if created {
		if err := unix.Fchmod(directory, 0770); err != nil {
			return err
		}
		return nil
	}
	if uint32(st.Mode)&07777 != 0770 {
		return fmt.Errorf("existing directory must have mode 0770; refusing chmod")
	}
	return nil
}
