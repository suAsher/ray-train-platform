package repositories

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
)

const TrainingEventRateLimit = 120

var (
	ErrTrainingEventUnauthorized = errors.New("training event authentication failed")
	ErrTrainingEventInvalid      = errors.New("invalid training event")
	ErrTrainingEventRateLimited  = errors.New("training event rate limit exceeded")
)

type TrainingCheckpointRecord struct {
	JobID          string `gorm:"primaryKey;index"`
	ID             string `gorm:"primaryKey"`
	TenantID       string `gorm:"index"`
	UserID         string `gorm:"index"`
	Epoch          int64
	Step           int64
	ObjectPath     string
	MetricName     string
	MetricValue    *float64
	Complete       bool
	IsBest         bool
	ManifestSHA256 string
	CreatedAt      time.Time
}

type TrainingJobEventTokenRecord struct {
	JobID               string `gorm:"primaryKey"`
	TokenSHA256         string
	ExpiresAt           time.Time
	LastGeneration      int64
	LastEpoch           int64
	LastStep            int64
	RateWindowStartedAt time.Time
	RateCount           int
	UpdatedAt           time.Time
}

type TrainingJobEventRecord struct {
	JobID      string `gorm:"primaryKey"`
	EventID    string `gorm:"primaryKey"`
	EventType  string
	Generation int64
	Epoch      int64
	Step       int64
	ResultJSON string `gorm:"type:jsonb"`
	CreatedAt  time.Time
}

func (TrainingCheckpointRecord) TableName() string    { return "training_checkpoints" }
func (TrainingJobEventTokenRecord) TableName() string { return "training_job_event_tokens" }
func (TrainingJobEventRecord) TableName() string      { return "training_job_events" }

func trainingEventTokenDigest(token []byte) string {
	digest := sha256.Sum256(token)
	return hex.EncodeToString(digest[:])
}

func (r *GormRepository) EnsureTrainingEventToken(ctx context.Context, jobID string, token []byte, expiresAt time.Time) error {
	if strings.TrimSpace(jobID) == "" || len(token) != 32 || !expiresAt.After(time.Now().UTC()) {
		return fmt.Errorf("ensure training event token: invalid arguments")
	}
	now := time.Now().UTC()
	record := TrainingJobEventTokenRecord{
		JobID: jobID, TokenSHA256: trainingEventTokenDigest(token), ExpiresAt: expiresAt.UTC(),
		RateWindowStartedAt: now, UpdatedAt: now,
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&JobRecord{}).Where("id = ? AND training_engine = ?", jobID, domain.TrainingEngineRayTrain).Count(&count).Error; err != nil {
			return fmt.Errorf("find managed training job: %w", err)
		}
		if count != 1 {
			return fmt.Errorf("managed training job was not found")
		}
		result := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "job_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"token_sha256": record.TokenSHA256, "expires_at": record.ExpiresAt, "updated_at": now,
			}),
		}).Create(&record)
		if result.Error != nil {
			return fmt.Errorf("persist training event token digest: %w", result.Error)
		}
		return nil
	})
}

func (r *GormRepository) RecordTrainingEvent(ctx context.Context, jobID string, token []byte, event domain.TrainingEvent, now time.Time) (domain.TrainingEventResult, error) {
	if strings.TrimSpace(jobID) == "" || len(token) != 32 {
		return domain.TrainingEventResult{}, ErrTrainingEventUnauthorized
	}
	now = now.UTC()
	var result domain.TrainingEventResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var tokenRecord TrainingJobEventTokenRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id = ?", jobID).First(&tokenRecord).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTrainingEventUnauthorized
			}
			return fmt.Errorf("load training event credential: %w", err)
		}
		if !validTrainingEventToken(tokenRecord.TokenSHA256, token) || !now.Before(tokenRecord.ExpiresAt) {
			return ErrTrainingEventUnauthorized
		}

		var replay TrainingJobEventRecord
		if err := tx.Where("job_id = ? AND event_id = ?", jobID, event.ID).First(&replay).Error; err == nil {
			if unmarshalErr := json.Unmarshal([]byte(replay.ResultJSON), &result); unmarshalErr != nil {
				return fmt.Errorf("decode replayed training event result: %w", unmarshalErr)
			}
			if err := consumeTrainingEventRate(&tokenRecord, now); err != nil {
				return err
			}
			tokenRecord.UpdatedAt = now
			if err := tx.Save(&tokenRecord).Error; err != nil {
				return fmt.Errorf("persist replayed training event rate: %w", err)
			}
			result.Replayed = true
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("find replayed training event: %w", err)
		}
		if err := event.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrTrainingEventInvalid, err)
		}

		var job JobRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND training_engine = ?", jobID, domain.TrainingEngineRayTrain).First(&job).Error; err != nil {
			return fmt.Errorf("%w: managed job was not found", ErrTrainingEventInvalid)
		}
		if err := validateEventProgress(tokenRecord, event); err != nil {
			return err
		}
		if err := consumeTrainingEventRate(&tokenRecord, now); err != nil {
			return err
		}

		result = domain.TrainingEventResult{EventID: event.ID, WorkerRestartCount: job.WorkerRestartCount}
		if event.Type == domain.TrainingEventWorkerGroupStarted && event.Generation > tokenRecord.LastGeneration {
			if tokenRecord.LastGeneration > 0 {
				job.WorkerRestartCount++
				if err := tx.Model(&JobRecord{}).Where("id = ?", jobID).Update("worker_restart_count", job.WorkerRestartCount).Error; err != nil {
					return fmt.Errorf("increment worker restart count: %w", err)
				}
			}
			result.WorkerRestartCount = job.WorkerRestartCount
		}
		if event.Generation > tokenRecord.LastGeneration {
			tokenRecord.LastGeneration = event.Generation
		}
		if event.Type == domain.TrainingEventCheckpointComplete {
			checkpoint, err := checkpointForEvent(job, event)
			if err != nil {
				return err
			}
			if err := tx.Create(&checkpoint).Error; err != nil {
				return fmt.Errorf("persist complete checkpoint: %w", err)
			}
			result.CheckpointID = checkpoint.ID
		}
		if event.Type != domain.TrainingEventWorkerGroupStarted {
			tokenRecord.LastEpoch = event.Epoch
			tokenRecord.LastStep = event.Step
		}

		resultJSON, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("encode training event result: %w", err)
		}
		eventRecord := TrainingJobEventRecord{
			JobID: jobID, EventID: event.ID, EventType: string(event.Type), Generation: event.Generation,
			Epoch: event.Epoch, Step: event.Step, ResultJSON: string(resultJSON), CreatedAt: now,
		}
		if err := tx.Create(&eventRecord).Error; err != nil {
			return fmt.Errorf("persist training event: %w", err)
		}
		tokenRecord.UpdatedAt = now
		if err := tx.Save(&tokenRecord).Error; err != nil {
			return fmt.Errorf("persist training event cursor: %w", err)
		}
		return nil
	})
	return result, err
}

func validTrainingEventToken(storedHex string, token []byte) bool {
	stored, err := hex.DecodeString(storedHex)
	if err != nil || len(stored) != sha256.Size {
		stored = make([]byte, sha256.Size)
	}
	digest := sha256.Sum256(token)
	return subtle.ConstantTimeCompare(stored, digest[:]) == 1 && err == nil
}

func validateEventProgress(cursor TrainingJobEventTokenRecord, event domain.TrainingEvent) error {
	if event.Generation < cursor.LastGeneration {
		return fmt.Errorf("%w: worker generation regressed", ErrTrainingEventInvalid)
	}
	if event.Type == domain.TrainingEventWorkerGroupStarted {
		return nil
	}
	if event.Epoch < cursor.LastEpoch || (event.Epoch == cursor.LastEpoch && event.Step < cursor.LastStep) {
		return fmt.Errorf("%w: epoch or step regressed", ErrTrainingEventInvalid)
	}
	return nil
}

func consumeTrainingEventRate(cursor *TrainingJobEventTokenRecord, now time.Time) error {
	if now.Sub(cursor.RateWindowStartedAt) >= time.Minute || now.Before(cursor.RateWindowStartedAt) {
		cursor.RateWindowStartedAt = now
		cursor.RateCount = 0
	}
	if cursor.RateCount >= TrainingEventRateLimit {
		return ErrTrainingEventRateLimited
	}
	cursor.RateCount++
	return nil
}

func checkpointForEvent(job JobRecord, event domain.TrainingEvent) (TrainingCheckpointRecord, error) {
	var spec domain.JobSpec
	if err := json.Unmarshal([]byte(job.SpecJSON), &spec); err != nil || spec.ResolvedDataMounts.Output == nil {
		return TrainingCheckpointRecord{}, fmt.Errorf("%w: job has no resolved writable output", ErrTrainingEventInvalid)
	}
	output := spec.ResolvedDataMounts.Output
	if output.MountPath != domain.DataMountOutputPath || output.ReadOnly || output.Space != domain.DataSpaceMyRuns {
		return TrainingCheckpointRecord{}, fmt.Errorf("%w: job output contract is invalid", ErrTrainingEventInvalid)
	}
	checkpoint := *event.Checkpoint
	checkpoint.JobID = job.ID
	checkpoint.TenantID = job.TenantID
	checkpoint.UserID = job.UserID
	checkpoint.Epoch = event.Epoch
	checkpoint.Step = event.Step
	checkpoint.CreatedAt = time.Now().UTC()
	if err := checkpoint.Validate(); err != nil {
		return TrainingCheckpointRecord{}, fmt.Errorf("%w: %v", ErrTrainingEventInvalid, err)
	}
	root := path.Join(domain.DataMountOutputPath, ".platform", "ray-train", job.ID, "checkpoints")
	if !strings.HasPrefix(checkpoint.ObjectPath, root+"/") {
		return TrainingCheckpointRecord{}, fmt.Errorf("%w: checkpoint path is outside the authenticated job output", ErrTrainingEventInvalid)
	}
	return TrainingCheckpointRecord{
		ID: checkpoint.ID, JobID: job.ID, TenantID: job.TenantID, UserID: job.UserID,
		Epoch: checkpoint.Epoch, Step: checkpoint.Step, ObjectPath: checkpoint.ObjectPath,
		MetricName: checkpoint.MetricName, MetricValue: checkpoint.MetricValue, Complete: true,
		IsBest: checkpoint.IsBest, ManifestSHA256: checkpoint.ManifestSHA256, CreatedAt: checkpoint.CreatedAt,
	}, nil
}

func (r *GormRepository) ListUsableCheckpoints(ctx context.Context, tenantID, userID, jobID string) ([]domain.TrainingCheckpoint, error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(userID) == "" || strings.TrimSpace(jobID) == "" {
		return nil, fmt.Errorf("checkpoint owner and job are required")
	}
	var records []TrainingCheckpointRecord
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ? AND job_id = ? AND complete = ?", tenantID, userID, jobID, true).
		Order("epoch DESC, step DESC, created_at DESC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list usable checkpoints: %w", err)
	}
	items := make([]domain.TrainingCheckpoint, 0, len(records))
	for _, record := range records {
		items = append(items, domain.TrainingCheckpoint{
			ID: record.ID, JobID: record.JobID, TenantID: record.TenantID, UserID: record.UserID,
			Epoch: record.Epoch, Step: record.Step, ObjectPath: record.ObjectPath,
			MetricName: record.MetricName, MetricValue: record.MetricValue, Complete: record.Complete,
			IsBest: record.IsBest, ManifestSHA256: record.ManifestSHA256, CreatedAt: record.CreatedAt,
		})
	}
	return items, nil
}
