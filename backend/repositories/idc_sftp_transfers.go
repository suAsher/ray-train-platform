package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
)

var (
	ErrIDCConnectionNotFound = errors.New("IDC SFTP connection not found")
	ErrDataTransferNotFound  = errors.New("data transfer not found")
)

// IDCConnectionRecord contains only public connection metadata and a name for
// the namespace-scoped Kubernetes Secret. The generated private key never
// enters this database model.
type IDCConnectionRecord struct {
	ID             string `gorm:"primaryKey"`
	TenantID       string `gorm:"column:tenant_id;index;uniqueIndex:idx_idc_connection_owner"`
	UserID         string `gorm:"column:user_id;index;uniqueIndex:idx_idc_connection_owner"`
	RemoteUsername string `gorm:"column:remote_username"`
	PublicKey      string `gorm:"column:public_key"`
	SecretName     string `gorm:"column:secret_name"`
	State          string `gorm:"index"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (IDCConnectionRecord) TableName() string { return "idc_sftp_connections" }

// DataTransferRecord is immutable request metadata plus durable state. The
// backing SFTP server and TOS root are deployment/user binding details, so
// neither raw infrastructure location appears here.
type DataTransferRecord struct {
	ID              string `gorm:"primaryKey"`
	TenantID        string `gorm:"column:tenant_id;index"`
	UserID          string `gorm:"column:user_id;index"`
	Direction       string `gorm:"index"`
	IDCRelativePath string `gorm:"column:idc_relative_path"`
	TOSSpace        string `gorm:"column:tos_space"`
	TOSRelativePath string `gorm:"column:tos_relative_path"`
	State           string `gorm:"index"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (DataTransferRecord) TableName() string { return "data_transfers" }

func (r *GormRepository) EnsurePersonalIDCConnection(ctx context.Context, requested domain.PersonalIDCConnection) (domain.PersonalIDCConnection, error) {
	if err := requested.Validate(); err != nil {
		return domain.PersonalIDCConnection{}, err
	}
	var existing IDCConnectionRecord
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ?", requested.TenantID, requested.UserID).First(&existing).Error
	switch {
	case err == nil:
		return existing.toDomain()
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return domain.PersonalIDCConnection{}, fmt.Errorf("look up personal IDC connection: %w", err)
	}
	now := time.Now().UTC()
	record := idcConnectionRecordFromDomain(requested, now)
	if err := r.db.WithContext(ctx).Create(&record).Error; err != nil {
		// A concurrent enrollment for the same owner resolves to the one durable
		// Secret inventory record rather than creating a replacement key.
		if lookupErr := r.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ?", requested.TenantID, requested.UserID).First(&existing).Error; lookupErr == nil {
			return existing.toDomain()
		}
		return domain.PersonalIDCConnection{}, fmt.Errorf("create personal IDC connection: %w", err)
	}
	return record.toDomain()
}

func (r *GormRepository) GetPersonalIDCConnection(ctx context.Context, tenantID, userID string) (domain.PersonalIDCConnection, error) {
	var record IDCConnectionRecord
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ?", tenantID, userID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.PersonalIDCConnection{}, ErrIDCConnectionNotFound
	}
	if err != nil {
		return domain.PersonalIDCConnection{}, fmt.Errorf("get personal IDC connection: %w", err)
	}
	return record.toDomain()
}

func (r *GormRepository) CreateDataTransfer(ctx context.Context, transfer domain.DataTransfer) error {
	if err := transfer.Validate(); err != nil {
		return err
	}
	record := dataTransferRecordFromDomain(transfer, time.Now().UTC())
	if err := r.db.WithContext(ctx).Create(&record).Error; err != nil {
		return fmt.Errorf("create data transfer: %w", err)
	}
	return nil
}

func (r *GormRepository) ListDataTransfers(ctx context.Context, tenantID, userID string, limit int) ([]domain.DataTransfer, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var records []DataTransferRecord
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ?", tenantID, userID).Order("created_at DESC, id DESC").Limit(limit).Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list data transfers: %w", err)
	}
	transfers := make([]domain.DataTransfer, 0, len(records))
	for _, record := range records {
		transfer, err := record.toDomain()
		if err != nil {
			return nil, err
		}
		transfers = append(transfers, transfer)
	}
	return transfers, nil
}

func (r *GormRepository) GetDataTransfer(ctx context.Context, tenantID, userID, id string) (domain.DataTransfer, error) {
	var record DataTransferRecord
	err := r.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND user_id = ?", id, tenantID, userID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.DataTransfer{}, ErrDataTransferNotFound
	}
	if err != nil {
		return domain.DataTransfer{}, fmt.Errorf("get data transfer: %w", err)
	}
	return record.toDomain()
}

func idcConnectionRecordFromDomain(connection domain.PersonalIDCConnection, now time.Time) IDCConnectionRecord {
	return IDCConnectionRecord{
		ID: connection.ID, TenantID: connection.TenantID, UserID: connection.UserID,
		RemoteUsername: connection.RemoteUsername, PublicKey: connection.PublicKey,
		SecretName: connection.SecretName, State: string(connection.State), CreatedAt: now, UpdatedAt: now,
	}
}

func (record IDCConnectionRecord) toDomain() (domain.PersonalIDCConnection, error) {
	connection := domain.PersonalIDCConnection{
		ID: record.ID, TenantID: record.TenantID, UserID: record.UserID,
		RemoteUsername: record.RemoteUsername, PublicKey: record.PublicKey,
		SecretName: record.SecretName, State: domain.IDCConnectionState(record.State),
	}
	if err := connection.Validate(); err != nil {
		return domain.PersonalIDCConnection{}, fmt.Errorf("invalid stored IDC connection: %w", err)
	}
	return connection, nil
}

func dataTransferRecordFromDomain(transfer domain.DataTransfer, now time.Time) DataTransferRecord {
	return DataTransferRecord{
		ID: transfer.ID, TenantID: transfer.TenantID, UserID: transfer.UserID,
		Direction: string(transfer.Direction), IDCRelativePath: transfer.IDCRelativePath,
		TOSSpace: string(transfer.TOSLocation.Space), TOSRelativePath: transfer.TOSLocation.RelativePath,
		State: string(transfer.State), CreatedAt: now, UpdatedAt: now,
	}
}

func (record DataTransferRecord) toDomain() (domain.DataTransfer, error) {
	transfer := domain.DataTransfer{
		ID: record.ID, TenantID: record.TenantID, UserID: record.UserID,
		Direction: domain.DataTransferDirection(record.Direction), IDCRelativePath: record.IDCRelativePath,
		TOSLocation: domain.DataLocation{Space: domain.DataSpaceID(record.TOSSpace), RelativePath: record.TOSRelativePath},
		State:       domain.DataTransferState(record.State),
	}
	if err := transfer.Validate(); err != nil {
		return domain.DataTransfer{}, fmt.Errorf("invalid stored data transfer: %w", err)
	}
	return transfer, nil
}
