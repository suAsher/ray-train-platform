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
	ID                  string `gorm:"primaryKey"`
	Name                string
	SourceSpace         string `gorm:"column:source_space"`
	SourceRelativePath  string `gorm:"column:source_relative_path"`
	MirrorPrefix        string `gorm:"column:mirror_prefix"`
	Enabled             bool
	SyncIntervalMinutes int    `gorm:"column:sync_interval_minutes"`
	CreatedBy           string `gorm:"column:created_by"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}
type IDCDataSyncRunRecord struct {
	ID                    string `gorm:"primaryKey"`
	ConnectorID           string `gorm:"column:connector_id;index"`
	IdempotencyKey        string `gorm:"column:idempotency_key"`
	Mode                  string
	State                 string `gorm:"index"`
	RequestedBy           string `gorm:"column:requested_by"`
	InventorySHA256       string `gorm:"column:inventory_sha256"`
	InventoryObjectKey    string `gorm:"column:inventory_object_key"`
	SourceObjectCount     int64
	SourceBytes           int64
	NewObjectCount        int64
	ChangedObjectCount    int64
	ReusedObjectCount     int64
	TombstonedObjectCount int64
	FailureReason         string
	CreatedAt             time.Time
	StartedAt             *time.Time
	FinishedAt            *time.Time
}
type IDCDataSyncInventoryEntryRecord struct {
	RunID        string `gorm:"column:run_id;primaryKey"`
	RelativePath string `gorm:"column:relative_path;primaryKey"`
	SizeBytes    int64
	ModifiedAt   time.Time
	SHA256       string
	ObjectKey    string `gorm:"column:object_key"`
}

type IDCDataSyncObjectRefRecord struct {
	SHA256         string    `gorm:"column:sha256;primaryKey"`
	ObjectKey      string    `gorm:"column:object_key"`
	ReferenceCount int64     `gorm:"column:reference_count"`
	SizeBytes      int64     `gorm:"column:size_bytes"`
	FirstSeenAt    time.Time `gorm:"column:first_seen_at"`
	LastSeenAt     time.Time `gorm:"column:last_seen_at"`
}

func (IDCDataSyncConnectorRecord) TableName() string      { return "idc_sync_connectors" }
func (IDCDataSyncRunRecord) TableName() string            { return "idc_sync_runs" }
func (IDCDataSyncInventoryEntryRecord) TableName() string { return "idc_sync_inventory_entries" }
func (IDCDataSyncObjectRefRecord) TableName() string      { return "idc_sync_object_refs" }

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

func (r *GormRepository) UpdateIDCDataSyncConnector(ctx context.Context, item domain.IDCDataSyncConnector) (domain.IDCDataSyncConnector, error) {
	if err := item.Validate(); err != nil {
		return domain.IDCDataSyncConnector{}, fmt.Errorf("validate IDC sync connector: %w", err)
	}
	result := r.db.WithContext(ctx).Model(&IDCDataSyncConnectorRecord{}).Where("id = ?", item.ID).
		Updates(map[string]any{"enabled": item.Enabled, "sync_interval_minutes": item.SyncIntervalMinutes, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return domain.IDCDataSyncConnector{}, fmt.Errorf("update IDC sync connector: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return domain.IDCDataSyncConnector{}, ErrIDCDataSyncConnectorNotFound
	}
	var record IDCDataSyncConnectorRecord
	if err := r.db.WithContext(ctx).Where("id = ?", item.ID).First(&record).Error; err != nil {
		return domain.IDCDataSyncConnector{}, err
	}
	return record.connector()
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

// ListIDCDataSyncRuns returns the recent, control-plane-owned history for one
// connector. Inventory entries and object keys intentionally stay out of this
// summary: the administrative UI only needs lifecycle/provenance status.
func (r *GormRepository) ListIDCDataSyncRuns(ctx context.Context, connectorID string) ([]domain.IDCDataSyncRun, error) {
	if strings.TrimSpace(connectorID) == "" {
		return nil, ErrIDCDataSyncConnectorNotFound
	}
	var records []IDCDataSyncRunRecord
	if err := r.db.WithContext(ctx).Where("connector_id = ?", connectorID).Order("created_at DESC").Limit(100).Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list IDC sync runs: %w", err)
	}
	items := make([]domain.IDCDataSyncRun, 0, len(records))
	for _, record := range records {
		item, err := record.run()
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (r *GormRepository) GetIDCDataSyncRun(ctx context.Context, runID string) (domain.IDCDataSyncRun, bool, error) {
	if strings.TrimSpace(runID) == "" {
		return domain.IDCDataSyncRun{}, false, nil
	}
	var record IDCDataSyncRunRecord
	if err := r.db.WithContext(ctx).Where("id = ?", runID).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.IDCDataSyncRun{}, false, nil
		}
		return domain.IDCDataSyncRun{}, false, fmt.Errorf("get IDC sync run: %w", err)
	}
	run, err := record.run()
	if err != nil {
		return domain.IDCDataSyncRun{}, false, fmt.Errorf("decode IDC sync run: %w", err)
	}
	return run, true, nil
}

func (r *GormRepository) ListActiveIDCDataSyncRuns(ctx context.Context) ([]domain.IDCDataSyncRun, error) {
	var records []IDCDataSyncRunRecord
	if err := r.db.WithContext(ctx).Where("state IN ?", []string{string(domain.IDCDataSyncRunPending), string(domain.IDCDataSyncRunPlanning), string(domain.IDCDataSyncRunRunning)}).Order("created_at ASC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list active IDC sync runs: %w", err)
	}
	items := make([]domain.IDCDataSyncRun, 0, len(records))
	for _, record := range records {
		item, err := record.run()
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
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}
		var existing IDCDataSyncRunRecord
		if err := tx.Where("connector_id = ? AND idempotency_key = ?", run.ConnectorID, run.IdempotencyKey).First(&existing).Error; err != nil {
			return ErrIDCDataSyncConflict
		}
		if existing.ID == run.ID {
			return nil
		}
		return ErrIDCDataSyncConflict
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

// AppendIDCDataSyncInventory records one bounded worker callback. Repeating a
// successful callback is harmless: a run/path pair is immutable and the
// database rejects a conflicting rewrite.
func (r *GormRepository) AppendIDCDataSyncInventory(ctx context.Context, runID string, entries []domain.IDCDataSyncInventoryEntry) error {
	if len(entries) == 0 || len(entries) > 1000 {
		return ErrIDCDataSyncConflict
	}
	for _, entry := range entries {
		if entry.RunID != runID || entry.Validate() != nil {
			return ErrIDCDataSyncConflict
		}
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run IDCDataSyncRunRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", runID).First(&run).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrIDCDataSyncRunNotFound
			}
			return err
		}
		if run.State != string(domain.IDCDataSyncRunRunning) {
			return ErrIDCDataSyncConflict
		}
		for _, entry := range entries {
			record := entryRecord(entry)
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				var existing IDCDataSyncInventoryEntryRecord
				if err := tx.Where("run_id = ? AND relative_path = ?", runID, entry.RelativePath).First(&existing).Error; err != nil {
					return err
				}
				if existing.SHA256 != entry.SHA256 || existing.SizeBytes != entry.SizeBytes || existing.ObjectKey != entry.ObjectKey {
					return ErrIDCDataSyncConflict
				}
			}
		}
		return nil
	})
}

func (r *GormRepository) CompleteIDCDataSyncRun(ctx context.Context, runID string, completion domain.IDCDataSyncCompletion) (domain.IDCDataSyncRun, error) {
	if len(completion.InventorySHA256) != 64 || completion.NewObjectCount < 0 || completion.ChangedObjectCount < 0 || completion.ReusedObjectCount < 0 || completion.TombstonedObjectCount < 0 {
		return domain.IDCDataSyncRun{}, ErrIDCDataSyncConflict
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
		var entries []IDCDataSyncInventoryEntryRecord
		if err := tx.Where("run_id = ?", runID).Order("relative_path ASC").Find(&entries).Error; err != nil {
			return err
		}
		if len(entries) == 0 {
			return ErrIDCDataSyncConflict
		}
		if completion.NewObjectCount+completion.ChangedObjectCount+completion.ReusedObjectCount != int64(len(entries)) {
			return ErrIDCDataSyncConflict
		}
		current.State, current.FinishedAt, current.InventorySHA256, current.InventoryObjectKey, current.SourceObjectCount, current.SourceBytes = domain.IDCDataSyncRunSucceeded, &finished, completion.InventorySHA256, completion.InventoryObjectKey, int64(len(entries)), 0
		current.NewObjectCount, current.ChangedObjectCount, current.ReusedObjectCount, current.TombstonedObjectCount = completion.NewObjectCount, completion.ChangedObjectCount, completion.ReusedObjectCount, completion.TombstonedObjectCount
		for _, item := range entries {
			current.SourceBytes += item.SizeBytes
		}
		if err := current.Validate(); err != nil {
			return ErrIDCDataSyncConflict
		}
		for _, entry := range entries {
			ref := IDCDataSyncObjectRefRecord{SHA256: entry.SHA256, ObjectKey: entry.ObjectKey, ReferenceCount: 1, SizeBytes: entry.SizeBytes, FirstSeenAt: finished, LastSeenAt: finished}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "sha256"}},
				DoUpdates: clause.Assignments(map[string]any{"reference_count": gorm.Expr("idc_sync_object_refs.reference_count + 1"), "last_seen_at": finished}),
			}).Create(&ref).Error; err != nil {
				return err
			}
		}
		if result := tx.Model(&IDCDataSyncRunRecord{}).Where("id = ? AND state = ?", runID, string(domain.IDCDataSyncRunRunning)).Updates(map[string]any{"state": string(current.State), "inventory_sha256": current.InventorySHA256, "inventory_object_key": current.InventoryObjectKey, "source_object_count": current.SourceObjectCount, "source_bytes": current.SourceBytes, "new_object_count": current.NewObjectCount, "changed_object_count": current.ChangedObjectCount, "reused_object_count": current.ReusedObjectCount, "tombstoned_object_count": current.TombstonedObjectCount, "finished_at": finished}); result.Error != nil || result.RowsAffected != 1 {
			if result.Error != nil {
				return result.Error
			}
			return ErrIDCDataSyncConflict
		}
		completed = current
		return nil
	})
	return completed, err
}

func (r *GormRepository) LatestSuccessfulIDCDataSyncRun(ctx context.Context, connectorID string) (domain.IDCDataSyncRun, bool, error) {
	var record IDCDataSyncRunRecord
	err := r.db.WithContext(ctx).Where("connector_id = ? AND state = ?", connectorID, string(domain.IDCDataSyncRunSucceeded)).Order("finished_at DESC").First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.IDCDataSyncRun{}, false, nil
	}
	if err != nil {
		return domain.IDCDataSyncRun{}, false, fmt.Errorf("find latest IDC sync run: %w", err)
	}
	run, err := record.run()
	return run, err == nil, err
}

func (r *GormRepository) FailIDCDataSyncRun(ctx context.Context, runID, reason string, finishedAt time.Time) (domain.IDCDataSyncRun, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 512 {
		return domain.IDCDataSyncRun{}, ErrIDCDataSyncConflict
	}
	result := r.db.WithContext(ctx).Model(&IDCDataSyncRunRecord{}).
		Where("id = ? AND state IN ?", runID, []string{string(domain.IDCDataSyncRunPending), string(domain.IDCDataSyncRunPlanning), string(domain.IDCDataSyncRunRunning)}).
		Updates(map[string]any{"state": string(domain.IDCDataSyncRunFailed), "failure_reason": reason, "finished_at": finishedAt.UTC()})
	if result.Error != nil {
		return domain.IDCDataSyncRun{}, fmt.Errorf("fail IDC sync run: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return domain.IDCDataSyncRun{}, ErrIDCDataSyncConflict
	}
	var record IDCDataSyncRunRecord
	if err := r.db.WithContext(ctx).Where("id = ?", runID).First(&record).Error; err != nil {
		return domain.IDCDataSyncRun{}, err
	}
	return record.run()
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
	return IDCDataSyncConnectorRecord{ID: item.ID, Name: item.Name, SourceSpace: string(item.SourceSpace), SourceRelativePath: item.SourceRelativePath, MirrorPrefix: item.MirrorPrefix, Enabled: item.Enabled, SyncIntervalMinutes: item.SyncIntervalMinutes, CreatedBy: item.CreatedBy, CreatedAt: now, UpdatedAt: now}
}
func (record IDCDataSyncConnectorRecord) connector() (domain.IDCDataSyncConnector, error) {
	item := domain.IDCDataSyncConnector{ID: record.ID, Name: record.Name, SourceSpace: domain.DataSpaceID(record.SourceSpace), SourceRelativePath: record.SourceRelativePath, MirrorPrefix: record.MirrorPrefix, Enabled: record.Enabled, SyncIntervalMinutes: record.SyncIntervalMinutes, CreatedBy: record.CreatedBy, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
	return item, item.Validate()
}
func runRecord(item domain.IDCDataSyncRun, now time.Time) IDCDataSyncRunRecord {
	return IDCDataSyncRunRecord{ID: item.ID, ConnectorID: item.ConnectorID, IdempotencyKey: item.IdempotencyKey, Mode: string(item.Mode), State: string(item.State), RequestedBy: item.RequestedBy, InventorySHA256: item.InventorySHA256, InventoryObjectKey: item.InventoryObjectKey, SourceObjectCount: item.SourceObjectCount, SourceBytes: item.SourceBytes, NewObjectCount: item.NewObjectCount, ChangedObjectCount: item.ChangedObjectCount, ReusedObjectCount: item.ReusedObjectCount, TombstonedObjectCount: item.TombstonedObjectCount, FailureReason: item.FailureReason, CreatedAt: now, StartedAt: item.StartedAt, FinishedAt: item.FinishedAt}
}
func (record IDCDataSyncRunRecord) run() (domain.IDCDataSyncRun, error) {
	item := domain.IDCDataSyncRun{ID: record.ID, ConnectorID: record.ConnectorID, IdempotencyKey: record.IdempotencyKey, Mode: domain.IDCDataSyncRunMode(record.Mode), State: domain.IDCDataSyncRunState(record.State), RequestedBy: record.RequestedBy, InventorySHA256: record.InventorySHA256, InventoryObjectKey: record.InventoryObjectKey, SourceObjectCount: record.SourceObjectCount, SourceBytes: record.SourceBytes, NewObjectCount: record.NewObjectCount, ChangedObjectCount: record.ChangedObjectCount, ReusedObjectCount: record.ReusedObjectCount, TombstonedObjectCount: record.TombstonedObjectCount, FailureReason: record.FailureReason, CreatedAt: record.CreatedAt, StartedAt: record.StartedAt, FinishedAt: record.FinishedAt}
	return item, item.Validate()
}
func entryRecord(item domain.IDCDataSyncInventoryEntry) IDCDataSyncInventoryEntryRecord {
	return IDCDataSyncInventoryEntryRecord{RunID: item.RunID, RelativePath: item.RelativePath, SizeBytes: item.SizeBytes, ModifiedAt: item.ModifiedAt, SHA256: item.SHA256, ObjectKey: item.ObjectKey}
}
func (record IDCDataSyncInventoryEntryRecord) entry() (domain.IDCDataSyncInventoryEntry, error) {
	item := domain.IDCDataSyncInventoryEntry{RunID: record.RunID, RelativePath: record.RelativePath, SizeBytes: record.SizeBytes, ModifiedAt: record.ModifiedAt, SHA256: record.SHA256, ObjectKey: record.ObjectKey}
	return item, item.Validate()
}
