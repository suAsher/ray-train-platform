package api

import (
	"testing"

	"ray-train-platform-backend/auth"
)

func TestNewPersonalDataBindingUsesUniquePlatformBindingIDForClaimName(t *testing.T) {
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpacesEnabled: true, DataSpacesFSXAttributes: `{"type":"TOS","bucket":"b","server":"s","region":"r"}`})
	principal := auth.Principal{TenantID: "tenant-a", Subject: "user.a", AuthType: auth.AuthTypeLocal}
	binding, err := handler.newPersonalDataBinding("mount-abc123", principal)
	if err != nil {
		t.Fatal(err)
	}
	if binding.ClaimName != "data-mount-abc123" {
		t.Fatalf("claim=%q, want a name derived from the unique binding id", binding.ClaimName)
	}
}
