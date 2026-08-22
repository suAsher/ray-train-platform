package api

import (
	"testing"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func TestSharedDataMountBindingsAreVisibleThroughTenantLocalPublicMirror(t *testing.T) {
	principal := auth.Principal{TenantID: "tenant-a", Subject: "user-a"}
	publicMirror, err := domain.NewSharedDataMountBinding(
		"public-tenant-a", principal.TenantID, domain.DataSpacePublic, "data-public-tenant-a",
		`{"type":"TOS","bucket":"bucket","server":"server","region":"region"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bindingVisibleToPrincipal(publicMirror, principal) {
		t.Fatalf("tenant-local public mirror must be visible: %#v", publicMirror)
	}
}
