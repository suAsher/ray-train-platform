package repositories

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	ss "ray-train-platform-backend/storagesync"
)

type storageSyncPathLockRecord struct {
	RunID      string `gorm:"primaryKey"`
	Attempt    int
	StorageKey string `gorm:"primaryKey"`
	Prefix     string `gorm:"primaryKey"`
	Mode       string `gorm:"primaryKey"`
}

func (storageSyncPathLockRecord) TableName() string { return "storage_sync_path_locks" }

func storageSyncLockRecord(runID string, attempt int, lock ss.PathLock) (storageSyncPathLockRecord, error) {
	if lock.Mode != "READ" && lock.Mode != "WRITE" {
		return storageSyncPathLockRecord{}, ss.ErrInvalid
	}
	prefix := strings.Trim(lock.Prefix, "/")
	if strings.ContainsAny(prefix, "\\\x00") {
		return storageSyncPathLockRecord{}, ss.ErrInvalid
	}
	for _, segment := range strings.Split(prefix, "/") {
		if segment == "." || segment == ".." || (segment == "" && prefix != "") {
			return storageSyncPathLockRecord{}, ss.ErrInvalid
		}
	}
	var root string
	if lock.Bucket != "" {
		// Two logical space aliases for the same object root must still conflict.
		root = "tos\x00" + lock.Region + "\x00" + lock.Bucket
	} else {
		if lock.StorageID == "" {
			return storageSyncPathLockRecord{}, ss.ErrInvalid
		}
		root = "storage\x00" + lock.StorageID
	}
	digest := sha256.Sum256([]byte(root))
	return storageSyncPathLockRecord{RunID: runID, Attempt: attempt, StorageKey: hex.EncodeToString(digest[:]), Prefix: prefix, Mode: lock.Mode}, nil
}

func storageSyncPrefixesOverlap(first, second string) bool {
	return first == "" || second == "" || first == second || strings.HasPrefix(first, second+"/") || strings.HasPrefix(second, first+"/")
}

func (tx *storageSyncTx) AcquireLocks(runID string, attempt int, locks []ss.PathLock) error {
	run, err := tx.GetRun(runID)
	if err != nil && !errors.Is(err, ss.ErrNotFound) {
		return err
	}
	if runID == "" || attempt < 1 || (err == nil && (attempt < run.Attempt || attempt > run.Attempt+1 || (attempt == run.Attempt && run.FinishedAt != nil))) {
		return ss.ErrConflict
	}
	var previous int64
	if err := tx.db.Model(&storageSyncPathLockRecord{}).Where("run_id = ? AND attempt <> ?", runID, attempt).Count(&previous).Error; err != nil {
		return err
	}
	if previous != 0 {
		return ss.ErrLocked
	}
	wanted := make([]storageSyncPathLockRecord, 0, len(locks))
	for _, lock := range locks {
		row, err := storageSyncLockRecord(runID, attempt, lock)
		if err != nil {
			return err
		}
		wanted = append(wanted, row)
	}
	sort.Slice(wanted, func(i, j int) bool {
		a, b := wanted[i], wanted[j]
		if a.StorageKey != b.StorageKey {
			return a.StorageKey < b.StorageKey
		}
		if a.Prefix != b.Prefix {
			return a.Prefix < b.Prefix
		}
		return a.Mode < b.Mode
	})
	pending := make([]storageSyncPathLockRecord, 0, len(wanted))
	seen := make(map[string]bool, len(wanted))
	for _, row := range wanted {
		exists, err := tx.storageSyncCheckLock(row)
		if err != nil {
			return err
		}
		key := row.StorageKey + "\x00" + row.Prefix + "\x00" + row.Mode
		if !exists && !seen[key] {
			pending = append(pending, row)
			seen[key] = true
		}
	}
	// The domain reserves locks before inserting a run or its next attempt.
	// Delay only the inserts until callback success; conflict checks are already
	// protected by the transaction-wide advisory lock.
	tx.pendingLocks = append(tx.pendingLocks, pending...)
	return nil
}

func (tx *storageSyncTx) storageSyncCheckLock(wanted storageSyncPathLockRecord) (bool, error) {
	var held []storageSyncPathLockRecord
	if err := tx.db.Where("storage_key = ?", wanted.StorageKey).Find(&held).Error; err != nil {
		return false, err
	}
	for _, pending := range tx.pendingLocks {
		if pending.StorageKey == wanted.StorageKey {
			held = append(held, pending)
		}
	}
	for _, lock := range held {
		if lock.RunID == wanted.RunID && lock.Attempt == wanted.Attempt {
			if lock.Prefix == wanted.Prefix && lock.Mode == wanted.Mode {
				return true, nil
			}
			continue
		}
		if storageSyncPrefixesOverlap(lock.Prefix, wanted.Prefix) && (lock.Mode == "WRITE" || wanted.Mode == "WRITE") {
			return false, ss.ErrLocked
		}
		if lock.RunID == wanted.RunID && lock.Attempt != wanted.Attempt {
			return false, ss.ErrLocked
		}
	}
	return false, nil
}

func (tx *storageSyncTx) ReleaseLocks(runID string) error {
	pending := make([]storageSyncPathLockRecord, 0, len(tx.pendingLocks))
	for _, lock := range tx.pendingLocks {
		if lock.RunID != runID {
			pending = append(pending, lock)
		}
	}
	tx.pendingLocks = pending
	return tx.db.Where("run_id = ?", runID).Delete(&storageSyncPathLockRecord{}).Error
}

func (tx *storageSyncTx) storageSyncFlushLocks() error {
	for _, lock := range tx.pendingLocks {
		run, err := tx.GetRun(lock.RunID)
		if err != nil {
			return err
		}
		if run.Attempt != lock.Attempt || run.FinishedAt != nil {
			return ss.ErrConflict
		}
		if err := tx.db.Create(&lock).Error; err != nil {
			return storageSyncError(err)
		}
	}
	return nil
}
