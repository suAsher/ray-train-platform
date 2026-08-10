package repositories

import (
	"fmt"
	"time"
)

type IdempotencyConflictError struct {
	JobID string
}

func (e *IdempotencyConflictError) Error() string {
	return fmt.Sprintf("idempotency key already used for job %s", e.JobID)
}

type IdempotencyRecord struct {
	TenantID     string `gorm:"primaryKey"`
	Key          string `gorm:"primaryKey"`
	ResponseJSON string `gorm:"type:jsonb;not null"`
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

func (IdempotencyRecord) TableName() string { return "idempotency_keys" }
