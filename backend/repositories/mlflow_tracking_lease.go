package repositories

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	tracking "ray-train-platform-backend/mlflowtracking"
)

func trackingTerminal(state string) bool {
	return state == "FINISHED" || state == "FAILED" || state == "KILLED"
}

func (s *MLflowTrackingStore) ClaimRunLease(ctx context.Context, actor tracking.Actor, id, leaseID, finish string, now, until time.Time, endTimeMS int64) (tracking.Run, error) {
	if leaseID == "" || !until.After(now) || (finish != "" && (!trackingTerminal(finish) || endTimeMS < 0 || endTimeMS > 253402300799999)) {
		return tracking.Run{}, tracking.ErrInvalid
	}
	var result tracking.Run
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record MLflowTrackingRunRecord
		if err := scopedTracking(tx.Clauses(clause.Locking{Strength: "UPDATE"}), actor).Where("id = ?", id).First(&record).Error; err != nil {
			return trackingReadError(err)
		}
		if finish != "" && endTimeMS < record.StartTimeMS {
			return tracking.ErrInvalid
		}
		if trackingTerminal(record.State) {
			if record.State == finish {
				result = trackingRunValue(record)
				return nil
			}
			return tracking.ErrConflict
		}
		if record.State == "PENDING" {
			return tracking.ErrPending
		}
		if record.State == "FINISHING" && (finish == "" || finish != record.FinishStatus) {
			return tracking.ErrConflict
		}
		if record.LeaseID != "" && record.LeaseExpiresAt != nil && record.LeaseExpiresAt.After(now) {
			return tracking.ErrBusy
		}
		updates := map[string]any{"lease_id": leaseID, "lease_expires_at": until, "updated_at": now}
		if finish != "" {
			updates["state"] = "FINISHING"
			updates["finish_status"] = finish
			if record.State != "FINISHING" {
				updates["end_time_ms"] = endTimeMS
			}
		}
		changed := scopedTracking(tx.Model(&MLflowTrackingRunRecord{}), actor).Where("id = ? AND state = ? AND lease_id = ?", id, record.State, record.LeaseID).Updates(updates)
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return tracking.ErrBusy
		}
		if err := scopedTracking(tx, actor).Where("id = ?", id).First(&record).Error; err != nil {
			return err
		}
		result = trackingRunValue(record)
		return nil
	})
	return result, err
}

func (s *MLflowTrackingStore) ReleaseRunLease(ctx context.Context, actor tracking.Actor, id, leaseID, finish string, now time.Time) (tracking.Run, error) {
	if leaseID == "" || (finish != "" && !trackingTerminal(finish)) {
		return tracking.Run{}, tracking.ErrInvalid
	}
	var result tracking.Run
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record MLflowTrackingRunRecord
		if err := scopedTracking(tx.Clauses(clause.Locking{Strength: "UPDATE"}), actor).Where("id = ?", id).First(&record).Error; err != nil {
			return trackingReadError(err)
		}
		if record.LeaseID != leaseID {
			return tracking.ErrBusy
		}
		if finish != "" && record.State != "FINISHING" {
			return tracking.ErrConflict
		}
		updates := map[string]any{"lease_id": "", "lease_expires_at": nil, "updated_at": now}
		// Only the lease owner can complete or reconcile a verified upstream
		// terminal state. Preserve the original end-time intent across retries.
		if finish != "" {
			updates["state"] = finish
			updates["finish_status"] = finish
			updates["finished_at"] = time.UnixMilli(record.EndTimeMS).UTC()
		}
		changed := scopedTracking(tx.Model(&MLflowTrackingRunRecord{}), actor).Where("id = ? AND lease_id = ?", id, leaseID).Updates(updates)
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return tracking.ErrBusy
		}
		if err := scopedTracking(tx, actor).Where("id = ?", id).First(&record).Error; err != nil {
			return err
		}
		result = trackingRunValue(record)
		return nil
	})
	return result, err
}
