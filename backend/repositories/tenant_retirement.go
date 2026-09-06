package repositories

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrTenantRetirementBlocked = errors.New("tenant retirement blocked")

type TenantRetirementPreflight struct {
	TenantID  string           `json:"tenantId"`
	CanRetire bool             `json:"canRetire"`
	Blockers  []string         `json:"blockers"`
	Counts    map[string]int64 `json:"counts"`
	RetiredAt *time.Time       `json:"retiredAt"`
	RetiredBy string           `json:"retiredBy"`
}

func (r *GormRepository) TenantRetirementPreflight(ctx context.Context, id string) (TenantRetirementPreflight, error) {
	return tenantRetirementPreflight(r.db.WithContext(ctx), id)
}

func tenantRetirementPreflight(tx *gorm.DB, id string) (TenantRetirementPreflight, error) {
	result := TenantRetirementPreflight{TenantID: id, Blockers: []string{}, Counts: map[string]int64{}}
	var tenant TenantRecord
	if err := tx.First(&tenant, "id = ?", id).Error; err != nil {
		return result, err
	}
	result.RetiredAt, result.RetiredBy = tenant.RetiredAt, tenant.RetiredBy
	if tenant.RetiredAt != nil {
		result.Blockers = append(result.Blockers, "already_retired")
	}
	queries := []struct{ key, sql string }{
		{"members", "SELECT count(*) FROM users WHERE tenant_id = ?"},
		{"historicalJobs", "SELECT count(*) FROM training_jobs WHERE tenant_id = ? AND observed_state IN ('SUCCEEDED','FAILED','CANCELED','TIMED_OUT')"},
		{"sessions", "SELECT count(*) FROM local_sessions WHERE tenant_id = ?"},
		{"tokens", "SELECT count(*) FROM personal_access_tokens WHERE tenant_id = ?"},
		{"datasets", "SELECT count(*) FROM datasets WHERE owner_tenant_id = ?"},
		{"images", "SELECT count(*) FROM platform_images WHERE tenant_id = ?"},
		{"mounts", "SELECT count(*) FROM data_mount_bindings WHERE tenant_id = ?"},
		{"storageAssets", "SELECT count(*) FROM storage_assets WHERE tenant_id = ?"},
		{"jobs", "SELECT count(*) FROM training_jobs WHERE tenant_id = ? AND observed_state NOT IN ('SUCCEEDED','FAILED','CANCELED','TIMED_OUT')"},
		{"workspaces", "SELECT count(*) FROM dev_workspaces WHERE tenant_id = ? AND observed_state NOT IN ('STOPPED','FAILED')"},
		{"publications", "SELECT count(*) FROM dataset_publication_runs p JOIN datasets d ON d.id = p.dataset_id WHERE (d.owner_tenant_id = ? OR d.owner_tenant_id IS NULL) AND p.state NOT IN ('READY','FAILED','DEPRECATED','RETIRED')"},
		{"uploads", "SELECT (SELECT count(*) FROM source_artifacts WHERE tenant_id = ? AND state = 'PENDING') + (SELECT count(*) FROM data_space_uploads WHERE tenant_id = ? AND state NOT IN ('COMPLETED','ABORTED'))"},
		{"transfers", "SELECT count(*) FROM data_transfers WHERE tenant_id = ? AND state NOT IN ('succeeded','failed','cancelled')"},
	}
	for _, q := range queries {
		args := []any{id}
		if q.key == "uploads" {
			args = append(args, id)
		}
		var count int64
		if err := tx.Raw(q.sql, args...).Scan(&count).Error; err != nil {
			return result, fmt.Errorf("inventory %s: %w", q.key, err)
		}
		result.Counts[q.key] = count
		active := q.key == "jobs" || q.key == "workspaces" || q.key == "publications" || q.key == "uploads" || q.key == "transfers"
		if count > 0 && active {
			result.Blockers = append(result.Blockers, "active_"+q.key)
		}
	}
	result.CanRetire = len(result.Blockers) == 0
	return result, nil
}

// WithActiveTenantWrite holds the same PostgreSQL advisory fence as retirement
// while the caller performs database and external side effects. The callback
// must not attempt retirement itself.
func (r *GormRepository) WithActiveTenantWrite(ctx context.Context, id string, fn func() error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock_shared(hashtextextended(?, 34781))", id).Error; err != nil {
				return err
			}
		}
		var tenant TenantRecord
		if err := tx.First(&tenant, "id = ?", id).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return fn()
		} else if err != nil {
			return err
		}
		if tenant.RetiredAt != nil {
			return ErrTenantRetirementBlocked
		}
		return fn()
	})
}

// RetireTenant preserves every resource. Both inventories are repeated under
// the exclusive write fence before credentials and lifecycle state change.
func (r *GormRepository) RetireTenant(ctx context.Context, id, actor string, clusterCheck func(context.Context, string) ([]string, error)) (TenantRetirementPreflight, error) {
	var result TenantRetirementPreflight
	if strings.TrimSpace(actor) == "" || clusterCheck == nil {
		return result, ErrTenantRetirementBlocked
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			var acquired bool
			if err := tx.Raw("SELECT pg_try_advisory_xact_lock(hashtextextended(?, 34781))", id).Scan(&acquired).Error; err != nil {
				return err
			}
			// Never queue an exclusive fence behind a request's shared fence: that
			// request may acquire another shared fence on a second DB connection.
			if !acquired {
				result = TenantRetirementPreflight{TenantID: id, Blockers: []string{"writes_in_flight"}, Counts: map[string]int64{}}
				return ErrTenantRetirementBlocked
			}
		}
		var tenant TenantRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&tenant, "id = ?", id).Error; err != nil {
			return err
		}
		var err error
		result, err = tenantRetirementPreflight(tx, id)
		if err != nil {
			return err
		}
		if tenant.RetiredAt != nil {
			return nil
		}
		blockers, err := clusterCheck(ctx, tenant.Namespace)
		if err != nil {
			result.Blockers = append(result.Blockers, "k8s_state_unknown")
		} else {
			result.Blockers = append(result.Blockers, blockers...)
		}
		result.CanRetire = len(result.Blockers) == 0
		if !result.CanRetire {
			return ErrTenantRetirementBlocked
		}
		now := time.Now().UTC()
		for _, table := range []string{"personal_access_tokens", "local_sessions"} {
			if err := tx.Table(table).Where("tenant_id = ? AND revoked_at IS NULL", id).Update("revoked_at", now).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&TenantRecord{}).Where("id = ? AND retired_at IS NULL", id).Updates(map[string]any{"retired_at": now, "retired_by": actor, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Create(&AuditLogRecord{TenantID: id, UserID: actor, Action: "tenant.retired", ResourceType: "tenant", ResourceID: id, PayloadJSON: `{"outcome":"success","resourcesPreserved":true}`, CreatedAt: now}).Error; err != nil {
			return err
		}
		result.RetiredAt = &now
		result.RetiredBy = actor
		result.CanRetire = false
		result.Blockers = []string{"already_retired"}
		return nil
	})
	return result, err
}
