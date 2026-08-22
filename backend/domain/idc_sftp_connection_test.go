package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewPersonalIDCConnectionAcceptsScopedAccountAndHidesSecretInventory(t *testing.T) {
	connection, err := NewPersonalIDCConnection(
		"idc-conn-1", "tenant-a", "user-a", "guofeng.su",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest platform@ray", "idc-sftp-conn-1",
	)
	if err != nil {
		t.Fatalf("new personal IDC connection: %v", err)
	}
	if connection.State != IDCConnectionPending {
		t.Fatalf("state = %q, want pending", connection.State)
	}
	if connection.RemoteUsername != "guofeng.su" {
		t.Fatalf("remote username = %q", connection.RemoteUsername)
	}

	encoded, err := json.Marshal(connection.PublicView())
	if err != nil {
		t.Fatalf("marshal public view: %v", err)
	}
	if strings.Contains(string(encoded), connection.SecretName) {
		t.Fatalf("public connection view exposed Kubernetes Secret inventory: %s", encoded)
	}
	if !strings.Contains(string(encoded), "ssh-ed25519") {
		t.Fatalf("public connection view must expose the key users need to install: %s", encoded)
	}
}

func TestNewPersonalIDCConnectionRejectsUnsafeRemoteUsernameOrIdentity(t *testing.T) {
	for _, username := range []string{"", "../other", "alice/bob", "alice@host", "alice name", "alice\\name"} {
		t.Run(username, func(t *testing.T) {
			_, err := NewPersonalIDCConnection("idc-conn-1", "tenant-a", "user-a", username, "ssh-ed25519 AAAA test", "idc-sftp-conn-1")
			if err == nil {
				t.Fatalf("unsafe username %q was accepted", username)
			}
		})
	}
	if _, err := NewPersonalIDCConnection("idc-conn-1", "tenant-a", "../user", "alice", "ssh-ed25519 AAAA test", "idc-sftp-conn-1"); err == nil {
		t.Fatal("unsafe platform identity was accepted")
	}
}
