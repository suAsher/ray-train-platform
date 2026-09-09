package repositories

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
)

var (
	ErrIDCDataSyncConnectorNotFound = errors.New("IDC sync connector not found")
	ErrIDCDataSyncRunNotFound       = errors.New("IDC sync run not found")
	ErrIDCDataSyncConflict          = errors.New("IDC sync conflict")
	ErrIDCDataSyncActiveRun         = errors.New("IDC sync connector already has an active run")
)

type IDCDataSyncConnectorRecord struct {
	ID                 string `gorm:"primaryKey"`
	Name               string
	SourceSpace        string `gorm:"column:source_space"`
	SourceRelativePath string `gorm:"column:source_relative_path"`
	MirrorPrefix       string `gorm:"column:mirror_prefix"`
	Enabled            bool
	CreatedBy          string `gorm:"column:created_by"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}
type IDCDataSyncRunRecord struct {
	ID                 string `gorm:"primaryKey"`
	ConnectorID        string `gorm:"column:connector_id;index"`
	Mode               string
	State              string `gorm:"index"`
	RequestedBy        string `gorm:"column:requested_by"`
	InventorySHA256    string `gorm:"column:inventory_sha256"`
	SourceObjectCount  int64
	SourceBytes        int64
	NewObjectCount     int64
	ChangedObjectCount int64
	ReusedObjectCount  int64
	FailureReason      string
	CreatedAt          time.Time
	StartedAt          *time.Time
	FinishedAt         *time.Time
}
type IDCDataSyncInventoryEntryRecord struct {
	RunID        string `gorm:"column:run_id;primaryKey"`
	RelativePath string `gorm:"column:relative_path;primaryKey"`
	SizeBytes    int64
	ModifiedAt   time.Time
	SHA256       string
	ObjectKey    string `gorm:"column:object_key"`
}

func (IDCDataSyncConnectorRecord) TableName() string      { return "idc_sync_connectors" }
func (IDCDataSyncRunRecord) TableName() string            { return "idc_sync_runs" }
func (IDCDataSyncInventoryEntryRecord) TableName() string { return "idc_sync_inventory_entries" }

func (r *GormRepository) CreateIDCDataSyncConnector(ctx context.Context, item domain.IDCDataSyncConnector) error {
	if err := item.Validate(); err != nil {
		return fmt.Errorf("validate IDC sync connector: %w", err)
	}
	now := time.Now().UTC()
	record := connectorRecord(item, now)
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if result.Error != nil {
		return fmt.Errorf("create IDC sync connector: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrIDCDataSyncConflict
	}
	return nil
}
func (r *GormRepository) ListIDCDataSyncConnectors(ctx context.Context) ([]domain.IDCDataSyncConnector, error) {
	var records []IDCDataSyncConnectorRecord
	if err := r.db.WithContext(ctx).Order("id ASC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list IDC sync connectors: %w", err)
	}
	items := make([]domain.IDCDataSyncConnector, 0, len(records))
	for _, record := range records {
		item, err := record.connector()
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}
func (r *GormRepository) CreateIDCDataSyncRun(ctx context.Context, run domain.IDCDataSyncRun) error {
	if err := run.Validate(); err != nil || run.State != domain.IDCDataSyncRunPending {
		return ErrIDCDataSyncConflict
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var connector IDCDataSyncConnectorRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", run.ConnectorID).First(&connector).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrIDCDataSyncConnectorNotFound
			}
			return err
		}
		if !connector.Enabled {
			return ErrIDCDataSyncConflict
		}
		var active int64
		if err := tx.Model(&IDCDataSyncRunRecord{}).Where("connector_id = ? AND state IN ?", run.ConnectorID, []string{string(domain.IDCDataSyncRunPending), string(domain.IDCDataSyncRunPlanning), string(domain.IDCDataSyncRunRunning)}).Count(&active).Error; err != nil {
			return err
		}
		if active != 0 {
			return ErrIDCDataSyncActiveRun
		}
		record := runRecord(run, time.Now().UTC())
		return tx.Create(&record).Error
	})
}
func (r *GormRepository) ClaimIDCDataSyncRun(ctx context.Context, runID string, now time.Time) (domain.IDCDataSyncRun, bool, error) {
	if strings.TrimSpace(runID) == "" {
		return domain.IDCDataSyncRun{}, false, ErrIDCDataSyncConflict
	}
	var claimed domain.IDCDataSyncRun
	didClaim := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record IDCDataSyncRunRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", runID).First(&record).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrIDCDataSyncRunNotFound
			}
			return err
		}
		current, err := record.run()
		if err != nil {
			return ErrIDCDataSyncConflict
		}
		if current.State != domain.IDCDataSyncRunPending {
			claimed = current
			return nil
		}
		if result := tx.Model(&IDCDataSyncRunRecord{}).Where("id = ? AND state = ?", runID, string(domain.IDCDataSyncRunPending)).Updates(map[string]any{"state": string(domain.IDCDataSyncRunRunning), "started_at": now, "failure_reason": ""}); result.Error != nil || result.RowsAffected != 1 {
			return ErrIDCDataSyncConflict
		}
		current.State, current.StartedAt = domain.IDCDataSyncRunRunning, &now
		claimed, didClaim = current, true
		return nil
	})
	return claimed, didClaim, err
}
func (r *GormRepository) CompleteIDCDataSyncRun(ctx context.Context, runID, inventory string, entries []domain.IDCDataSyncInventoryEntry) (domain.IDCDataSyncRun, error) {
	if len(entries) == 0 || len(inventory) != 64 {
		return domain.IDCDataSyncRun{}, ErrIDCDataSyncConflict
	}
	for _, item := range entries {
		if item.RunID != runID || item.Validate() != nil {
			return domain.IDCDataSyncRun{}, ErrIDCDataSyncConflict
		}
	}
	var completed domain.IDCDataSyncRun
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record IDCDataSyncRunRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", runID).First(&record).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrIDCDataSyncRunNotFound
			}
			return err
		}
		current, err := record.run()
		if err != nil || current.State != domain.IDCDataSyncRunRunning {
			return ErrIDCDataSyncConflict
		}
		finished := time.Now().UTC()
		current.State, current.FinishedAt, current.InventorySHA256, current.SourceObjectCount, current.SourceBytes = domain.IDCDataSyncRunSucceeded, &finished, inventory, int64(len(entries)), 0
		for _, item := range entries {
			current.SourceBytes += item.SizeBytes
		}
		if err := current.Validate(); err != nil {
			return ErrIDCDataSyncConflict
		}
		records := make([]IDCDataSyncInventoryEntryRecord, 0, len(entries))
		for _, item := range entries {
			records = append(records, entryRecord(item))
		}
		if err := tx.Create(&records).Error; err != nil {
			return err
		}
		if err := tx.Model(&IDCDataSyncRunRecord{}).Where("id = ? AND state = ?", runID, string(domain.IDCDataSyncRunRunning)).Updates(map[string]any{"state": string(current.State), "inventory_sha256": current.InventorySHA256, "source_object_count": current.SourceObjectCount, "source_bytes": current.SourceBytes, "finished_at": finished}).Error; err != nil {
			return err
		}
		completed = current
		return nil
	})
	return completed, err
}
func (r *GormRepository) ListIDCDataSyncInventory(ctx context.Context, runID string) ([]domain.IDCDataSyncInventoryEntry, error) {
	var records []IDCDataSyncInventoryEntryRecord
	if err := r.db.WithContext(ctx).Where("run_id = ?", runID).Order("relative_path ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	items := make([]domain.IDCDataSyncInventoryEntry, 0, len(records))
	for _, record := range records {
		item, err := record.entry()
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}
func connectorRecord(item domain.IDCDataSyncConnector, now time.Time) IDCDataSyncConnectorRecord {
	return IDCDataSyncConnectorRecord{ID: item.ID, Name: item.Name, SourceSpace: string(item.SourceSpace), SourceRelativePath: item.SourceRelativePath, MirrorPrefix: item.MirrorPrefix, Enabled: item.Enabled, CreatedBy: item.CreatedBy, CreatedAt: now, UpdatedAt: now}
}
func (record IDCDataSyncConnectorRecord) connector() (domain.IDCDataSyncConnector, error) {
	item := domain.IDCDataSyncConnector{ID: record.ID, Name: record.Name, SourceSpace: domain.DataSpaceID(record.SourceSpace), SourceRelativePath: record.SourceRelativePath, MirrorPrefix: record.MirrorPrefix, Enabled: record.Enabled, CreatedBy: record.CreatedBy, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
	return item, item.Validate()
}
func runRecord(item domain.IDCDataSyncRun, now time.Time) IDCDataSyncRunRecord {
	return IDCDataSyncRunRecord{ID: item.ID, ConnectorID: item.ConnectorID, Mode: string(item.Mode), State: string(item.State), RequestedBy: item.RequestedBy, InventorySHA256: item.InventorySHA256, SourceObjectCount: item.SourceObjectCount, SourceBytes: item.SourceBytes, NewObjectCount: item.NewObjectCount, ChangedObjectCount: item.ChangedObjectCount, ReusedObjectCount: item.ReusedObjectCount, FailureReason: item.FailureReason, CreatedAt: now, StartedAt: item.StartedAt, FinishedAt: item.FinishedAt}
}
func (record IDCDataSyncRunRecord) run() (domain.IDCDataSyncRun, error) {
	item := domain.IDCDataSyncRun{ID: record.ID, ConnectorID: record.ConnectorID, Mode: domain.IDCDataSyncRunMode(record.Mode), State: domain.IDCDataSyncRunState(record.State), RequestedBy: record.RequestedBy, InventorySHA256: record.InventorySHA256, SourceObjectCount: record.SourceObjectCount, SourceBytes: record.SourceBytes, NewObjectCount: record.NewObjectCount, ChangedObjectCount: record.ChangedObjectCount, ReusedObjectCount: record.ReusedObjectCount, FailureReason: record.FailureReason, CreatedAt: record.CreatedAt, StartedAt: record.StartedAt, FinishedAt: record.FinishedAt}
	return item, item.Validate()
}
func entryRecord(item domain.IDCDataSyncInventoryEntry) IDCDataSyncInventoryEntryRecord {
	return IDCDataSyncInventoryEntryRecord{RunID: item.RunID, RelativePath: item.RelativePath, SizeBytes: item.SizeBytes, ModifiedAt: item.ModifiedAt, SHA256: item.SHA256, ObjectKey: item.ObjectKey}
}
func (record IDCDataSyncInventoryEntryRecord) entry() (domain.IDCDataSyncInventoryEntry, error) {
	item := domain.IDCDataSyncInventoryEntry{RunID: record.RunID, RelativePath: record.RelativePath, SizeBytes: record.SizeBytes, ModifiedAt: record.ModifiedAt, SHA256: record.SHA256, ObjectKey: record.ObjectKey}
	return item, item.Validate()
}
