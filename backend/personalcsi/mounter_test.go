package personalcsi

import (
	"strings"
	"testing"
)

func TestPersonalStorageKernelMountIdentity(t *testing.T) {
	target := "/var/lib/kubelet/pods/pod/volumes/kubernetes.io~csi/pv/mount"
	id := VolumeID("owner", "binding", "tenant", 1)
	own := "52 21 0:99 / " + target + " rw - fuse.wellspiking personalstorage:" + id + " rw\n"
	tests := []struct {
		name, line, want string
		mounted, fail    bool
	}{
		{"own", own, id, true, false},
		{"none", own, "", false, false},
		{"foreign type", strings.Replace(own, "fuse.wellspiking", "ext4", 1), "foreign", true, false},
		{"malformed id", strings.Replace(own, id, "personal-"+strings.Repeat("x", 40), 1), "foreign", true, false},
		{"stacked mount", own + strings.Replace(own, "fuse.wellspiking", "ext4", 1), "", true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := target
			if test.name == "none" {
				query += "-elsewhere"
			}
			got, mounted, err := inspectMountInfo(strings.NewReader(test.line), query)
			if (err != nil) != test.fail || mounted != test.mounted || got != test.want {
				t.Fatalf("identity %q mounted %v error %v", got, mounted, err)
			}
		})
	}
}
