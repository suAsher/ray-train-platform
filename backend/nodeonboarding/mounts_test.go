package nodeonboarding

import (
	"strings"
	"testing"
)

const validMounts = `24 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw
25 24 8:16 / /data1 rw,relatime shared:2 - xfs /dev/sdb rw,attr2
26 24 8:32 / /data2 rw,relatime - ext4 /dev/sdc rw
27 24 0:1 / /proc rw - proc proc rw
`

func TestValidateHostMounts(t *testing.T) {
	if err := ValidateHostMounts(validMounts); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"empty": "", "missing disk": strings.ReplaceAll(validMounts, "26 24 8:32 / /data2 rw,relatime - ext4 /dev/sdc rw\n", ""),
		"duplicate":           validMounts + "28 24 8:48 / /data1 rw - ext4 /dev/sdd rw\n",
		"same device":         strings.ReplaceAll(validMounts, "8:32", "8:16"),
		"root device":         strings.ReplaceAll(validMounts, "8:32", "8:1"),
		"bind":                strings.ReplaceAll(validMounts, "8:16 / /data1", "8:16 /subdir /data1"),
		"read only":           strings.ReplaceAll(validMounts, "/data1 rw,relatime", "/data1 ro,relatime"),
		"super readonly":      strings.ReplaceAll(validMounts, "/dev/sdb rw,attr2", "/dev/sdb ro,attr2"),
		"network":             strings.ReplaceAll(validMounts, "/dev/sdb", "server:/export"),
		"wrong fs":            strings.ReplaceAll(validMounts, "- xfs", "- nfs4"),
		"malformed":           validMounts + "junk\n",
		"escape":              strings.ReplaceAll(validMounts, "/data1", `/data\061`),
		"invalid dev":         strings.ReplaceAll(validMounts, "8:16", "invalid"),
		"zero dev":            strings.ReplaceAll(validMounts, "8:16", "0:16"),
		"conflicting options": strings.ReplaceAll(validMounts, "/data1 rw,relatime", "/data1 rw,ro"),
		"root absent":         strings.ReplaceAll(validMounts, "/ / rw,relatime", "/ /other rw,relatime"),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateHostMounts(input); err == nil {
				t.Fatal("accepted unsafe mounts")
			}
		})
	}
}
