package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPersonalDataSpacesUseStableSubjectPrefix(t *testing.T) {
	spaces := PersonalDataSpaces("local", "kc-7f3a")
	space, ok := FindDataSpace(spaces, DataSpaceMyFiles)
	if !ok {
		t.Fatal("my-files space is missing")
	}
	if got, want := space.RootPrefix, "ray-train/tenants/local/users/kc-7f3a/files/"; got != want {
		t.Fatalf("root prefix = %q, want %q", got, want)
	}
	if space.ReadOnly {
		t.Fatal("my-files must be writable")
	}
	if got, want := space.MountPath, "/mnt/storage/me/files"; got != want {
		t.Fatalf("my-files mount path = %q, want %q", got, want)
	}
}

func TestPersonalDataSpacesExposeTheRealPersonalStorageRoot(t *testing.T) {
	spaces := PersonalDataSpaces("local", "kc-7f3a")
	space, ok := FindDataSpace(spaces, DataSpaceID("my-storage"))
	if !ok {
		t.Fatal("my-storage space is missing")
	}
	if got, want := space.RootPrefix, "ray-train/tenants/local/users/kc-7f3a/"; got != want {
		t.Fatalf("root prefix = %q, want %q", got, want)
	}
	if got, want := space.MountPath, "/mnt/storage/me"; got != want {
		t.Fatalf("mount path = %q, want %q", got, want)
	}
	if space.ReadOnly || !space.BrowseEnabled {
		t.Fatalf("personal storage root must be writable and browsable: %#v", space)
	}
}

func TestPersonalDataSpacesCanUseAStableStorageKeyRootWithoutChangingOwnerIdentity(t *testing.T) {
	spaces, err := PersonalDataSpacesForRoot("local", "ray-train/tenants/local/users/guofeng.su/")
	if err != nil {
		t.Fatal(err)
	}
	space, ok := FindDataSpace(spaces, DataSpaceMyFiles)
	if !ok || space.RootPrefix != "ray-train/tenants/local/users/guofeng.su/files/" {
		t.Fatalf("storage-key space = %#v", space)
	}
	if _, err := PersonalDataSpacesForRoot("local", "ray-train/tenants/other/users/guofeng.su/"); err == nil {
		t.Fatal("foreign tenant root was accepted")
	}
}

func TestDataSpacesDoNotExposeInfrastructureRootsInJSON(t *testing.T) {
	space, ok := FindDataSpace(PersonalDataSpaces("local", "kc-7f3a"), DataSpaceWorkspace)
	if !ok {
		t.Fatal("workspace is missing")
	}
	encoded, err := json.Marshal(space)
	if err != nil {
		t.Fatalf("marshal data space: %v", err)
	}
	if string(encoded) == "" || strings.Contains(string(encoded), `"rootPrefix"`) {
		t.Fatalf("data-space response leaks root prefix: %s", encoded)
	}
}

func TestPersonalRunSpaceDoesNotAdvertiseDataDownload(t *testing.T) {
	space, ok := FindDataSpace(PersonalDataSpaces("local", "kc-7f3a"), DataSpaceMyRuns)
	if !ok {
		t.Fatal("my-runs space is missing")
	}
	if strings.Contains(space.Description, "下载") {
		t.Fatalf("my-runs description must not advertise downloading data: %q", space.Description)
	}
}

func TestDataSpaceRejectsUnsafeTenantOrSubject(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tenantID string
		subject  string
	}{
		{name: "tenant traversal", tenantID: "../other", subject: "kc-7f3a"},
		{name: "subject path", tenantID: "local", subject: "user/other"},
		{name: "subject backslash", tenantID: "local", subject: `user\\other`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PersonalDataSpacesFor(tc.tenantID, tc.subject); err == nil {
				t.Fatal("unsafe data-space identity was accepted")
			}
		})
	}
}

func TestConfiguredPublicDataRootIsTenantConfinedAndVisibleInTheLogicalCatalog(t *testing.T) {
	root, err := NormalizePublicDataRoot("ray-train/tenants/local/datasets/public")
	if err != nil {
		t.Fatalf("normalize temporary public root: %v", err)
	}
	spaces, err := PersonalDataSpacesForRoots("local", "ray-train/tenants/local/users/guofeng.su/", root)
	if err != nil {
		t.Fatalf("build data-space catalog: %v", err)
	}
	public, ok := FindDataSpace(spaces, DataSpacePublic)
	if !ok || public.RootPrefix != "ray-train/tenants/local/datasets/public/" {
		t.Fatalf("public root = %#v, want the configured tenant public root", public)
	}
	if _, err := PersonalDataSpacesForRoots("other", "ray-train/tenants/other/users/alice/", root); err == nil {
		t.Fatal("another tenant must not be able to adopt local's temporary public root")
	}
}

func TestNormalizePublicDataRootRejectsUngovernedPrefixes(t *testing.T) {
	for _, value := range []string{
		"ray-train/tenants/other/users/alice/files",
		"ray-train/tenants/local/shared",
		"other-bucket/public",
		"ray-train/public/../../users/alice",
	} {
		if _, err := NormalizePublicDataRoot(value); err == nil {
			t.Fatalf("unsafe public root %q was accepted", value)
		}
	}
}
