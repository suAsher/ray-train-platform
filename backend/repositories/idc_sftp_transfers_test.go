package repositories

import (
	"context"
	"errors"
	"testing"

	"ray-train-platform-backend/domain"
)

func idcSFTPTransferRepo(t *testing.T) *GormRepository {
	t.Helper()
	repo := testRepository(t)
	if err := repo.db.AutoMigrate(&IDCConnectionRecord{}, &DataTransferRecord{}); err != nil {
		t.Fatalf("migrate IDC SFTP records: %v", err)
	}
	return repo
}

func testIDCConnection(t *testing.T, id, tenantID, userID, username string) domain.PersonalIDCConnection {
	t.Helper()
	connection, err := domain.NewPersonalIDCConnection(id, tenantID, userID, username, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest platform@ray", "idc-sftp-"+id)
	if err != nil {
		t.Fatalf("new IDC connection: %v", err)
	}
	return connection
}

func testDataTransfer(t *testing.T, id, tenantID, userID string) domain.DataTransfer {
	t.Helper()
	transfer, err := domain.NewDataTransfer(id, tenantID, userID, domain.DataTransferIDCToTOS, "projects/demo", domain.DataLocation{Space: domain.DataSpaceMyFiles, RelativePath: "datasets/demo"})
	if err != nil {
		t.Fatalf("new transfer: %v", err)
	}
	return transfer
}

func TestEnsurePersonalIDCConnectionIsIdempotentPerOwner(t *testing.T) {
	repo := idcSFTPTransferRepo(t)
	ctx := context.Background()
	ensureDataBindingIdentity(t, repo, "tenant-a", "user-a")

	first, err := repo.EnsurePersonalIDCConnection(ctx, testIDCConnection(t, "connection-a", "tenant-a", "user-a", "guofeng.su"))
	if err != nil {
		t.Fatalf("ensure first connection: %v", err)
	}
	second, err := repo.EnsurePersonalIDCConnection(ctx, testIDCConnection(t, "connection-retry", "tenant-a", "user-a", "guofeng.su"))
	if err != nil {
		t.Fatalf("ensure second connection: %v", err)
	}
	if first.ID != second.ID || first.SecretName != second.SecretName {
		t.Fatalf("connection retry changed the stored key inventory: first=%#v second=%#v", first, second)
	}
}

func TestIDCTransferQueriesNeverLeakAnotherUsersRecords(t *testing.T) {
	repo := idcSFTPTransferRepo(t)
	ctx := context.Background()
	ensureDataBindingIdentity(t, repo, "tenant-a", "user-a")
	ensureDataBindingIdentity(t, repo, "tenant-a", "user-b")
	ensureDataBindingIdentity(t, repo, "tenant-b", "user-c")

	for _, connection := range []domain.PersonalIDCConnection{
		testIDCConnection(t, "connection-a", "tenant-a", "user-a", "alice"),
		testIDCConnection(t, "connection-b", "tenant-a", "user-b", "bob"),
		testIDCConnection(t, "connection-c", "tenant-b", "user-c", "carol"),
	} {
		if _, err := repo.EnsurePersonalIDCConnection(ctx, connection); err != nil {
			t.Fatalf("ensure connection %s: %v", connection.ID, err)
		}
	}
	for _, transfer := range []domain.DataTransfer{
		testDataTransfer(t, "transfer-a", "tenant-a", "user-a"),
		testDataTransfer(t, "transfer-b", "tenant-a", "user-b"),
		testDataTransfer(t, "transfer-c", "tenant-b", "user-c"),
	} {
		if err := repo.CreateDataTransfer(ctx, transfer); err != nil {
			t.Fatalf("create transfer %s: %v", transfer.ID, err)
		}
	}

	transfers, err := repo.ListDataTransfers(ctx, "tenant-a", "user-a", 20)
	if err != nil {
		t.Fatalf("list own transfers: %v", err)
	}
	if len(transfers) != 1 || transfers[0].ID != "transfer-a" {
		t.Fatalf("cross-user transfer leaked: %#v", transfers)
	}
	if _, err := repo.GetDataTransfer(ctx, "tenant-a", "user-a", "transfer-b"); !errors.Is(err, ErrDataTransferNotFound) {
		t.Fatalf("get another user's transfer: %v", err)
	}
	if _, err := repo.GetPersonalIDCConnection(ctx, "tenant-a", "user-a"); err != nil {
		t.Fatalf("get own connection: %v", err)
	}
	if _, err := repo.GetPersonalIDCConnection(ctx, "tenant-a", "missing-user"); !errors.Is(err, ErrIDCConnectionNotFound) {
		t.Fatalf("get unknown connection: %v", err)
	}
}
