package warehousesync

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	fw "ray-train-platform-backend/functionwarehouse"
)

func TestCredentialsBoundToOperationAndNeverSerialized(t *testing.T) {
	box, err := newCredentialBox(bytes.Repeat([]byte("p"), 32))
	if err != nil {
		t.Fatal(err)
	}
	op := Operation{ID: "op", OwnerID: "owner", TenantID: "tenant", Environment: fw.Development}
	op.Credential, err = box.seal(op, "private-oauth-token")
	if err != nil {
		t.Fatal(err)
	}
	got, err := box.open(op)
	if err != nil || got != "private-oauth-token" {
		t.Fatalf("decrypt failed: %v", err)
	}
	changed := []Operation{op, op, op, op}
	changed[0].ID = "other"
	changed[1].OwnerID = "other"
	changed[2].TenantID = "other"
	changed[3].Environment = fw.Production
	for _, candidate := range changed {
		if _, err := box.open(candidate); err == nil {
			t.Fatal("AAD mismatch accepted")
		}
	}
	wire, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "Credential") || strings.Contains(string(wire), "private-oauth-token") || bytes.Contains(op.Credential, []byte("private-oauth-token")) {
		t.Fatal("credential leaked")
	}
	other, _ := newCredentialBox(bytes.Repeat([]byte("q"), 32))
	if _, err := other.open(op); err == nil {
		t.Fatal("wrong key accepted")
	}
	op.Credential[0] ^= 1
	if _, err := box.open(op); err == nil {
		t.Fatal("tampering accepted")
	}
}

func TestCredentialsRejectInvalidConfigurationAndTokens(t *testing.T) {
	if _, err := newCredentialBox([]byte("short")); err == nil {
		t.Fatal("short pepper accepted")
	}
	box, _ := newCredentialBox(bytes.Repeat([]byte("p"), 32))
	for _, token := range []string{"", "a\nb", strings.Repeat("x", 32769)} {
		if _, err := box.seal(Operation{}, token); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
	if _, err := box.open(Operation{Credential: []byte("bad")}); err == nil {
		t.Fatal("invalid ciphertext accepted")
	}
}
