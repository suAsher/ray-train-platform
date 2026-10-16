package registryauth

import (
	"archive/tar"
	"context"
	"testing"
)

func TestEnvironmentLayerAllowsOnlyReadOnlyRootProfile(t *testing.T) {
	profile := &tar.Header{Name: "etc/raytrain/environment-profile.json", Typeflag: tar.TypeReg, Mode: 0444}
	if err := validateEnvironmentTar(context.Background(), writeEnvironmentTar(t, []*tar.Header{profile})); err != nil {
		t.Fatalf("trusted runtime profile rejected: %v", err)
	}
	for _, test := range []struct {
		name string
		change func(*tar.Header)
	}{
		{"writable", func(h *tar.Header) { h.Mode = 0644 }},
		{"executable", func(h *tar.Header) { h.Mode = 0555 }},
		{"foreign-owner", func(h *tar.Header) { h.Uid = 1000 }},
		{"foreign-group", func(h *tar.Header) { h.Gid = 1000 }},
		{"symlink", func(h *tar.Header) { h.Typeflag = tar.TypeSymlink; h.Mode = 0777; h.Linkname = "../other.json" }},
		{"directory", func(h *tar.Header) { h.Typeflag = tar.TypeDir; h.Mode = 0755 }},
		{"sibling", func(h *tar.Header) { h.Name = "etc/raytrain/other.json" }},
		{"read-only-elsewhere", func(h *tar.Header) { h.Name = "opt/raytrain/environment/something" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := *profile
			test.change(&header)
			if err := validateEnvironmentTar(context.Background(), writeEnvironmentTar(t, []*tar.Header{&header})); err == nil {
				t.Fatal("unsafe runtime profile layer accepted")
			}
		})
	}
}
