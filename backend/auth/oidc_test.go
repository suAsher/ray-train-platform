package auth

import (
	"context"
	"testing"
)

func TestNewValidatorRejectsIncompleteOIDCConfiguration(t *testing.T) {
	if _, err := NewValidator(context.Background(), "", "client", "audience", "platform/tenants/"); err == nil {
		t.Fatal("expected issuer validation error")
	}
	if _, err := NewValidator(context.Background(), "https://sso.example.com", "", "audience", "platform/tenants/"); err == nil {
		t.Fatal("expected client validation error")
	}
	if _, err := NewValidator(context.Background(), "https://sso.example.com", "client", "", "platform/tenants/"); err == nil {
		t.Fatal("expected audience validation error")
	}
}
