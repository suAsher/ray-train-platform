package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/domain"
)

type JobRecord struct {
	ID       string `gorm:"primaryKey"`
	TenantID string `gorm:"index"`
	UserID   string
	// Nullable: a job whose source is git or a workspace snapshot has no
	// artifact, and an empty string would violate the foreign key.
	SourceArtifactID     *string
	SubmissionOrigin     string
	ExternalSubmissionID string
	Name                 string `gorm:"index"`
	SpecJSON             string `gorm:"type:jsonb"`
	DesiredState         string `gorm:"index"`
	ObservedState        string `gorm:"index"`
	StatusReason         string
	StatusMessage        string
	KubernetesNS         string
	RayJobName           string
	RayJobUID            string
	RayClusterName       string
	ResourceVersion      string
	RetryCount           int
	TimeoutSeconds       int64
	CleanupJSON          string `gorm:"type:jsonb"`
	CreatedAt            time.Time
	UpdatedAt            time.Time
	LastObservedAt       *time.Time
	StartedAt            *time.Time
	FinishedAt           *time.Time
	ArchivedAt           *time.Time `gorm:"index"`
}

type OutboxRecord struct {
	ID            string `gorm:"primaryKey"`
	AggregateType string
	AggregateID   string `gorm:"index"`
	EventType     string
	PayloadJSON   string `gorm:"column:payload;type:jsonb"`
	Status        string `gorm:"index"`
	Attempts      int
	NextAttemptAt time.Time `gorm:"index"`
	LockedAt      *time.Time
	LastError     string
	CreatedAt     time.Time
	CompletedAt   *time.Time
}

func (JobRecord) TableName() string    { return "training_jobs" }
func (OutboxRecord) TableName() string { return "outbox_events" }

type GormRepository struct {
	db *gorm.DB
}

type GPUQuotaExceededError struct {
	Quota     int
	Used      int
	Requested int
}

func (e *GPUQuotaExceededError) Error() string {
	return fmt.Sprintf("tenant GPU quota exceeded: quota=%d used=%d requested=%d", e.Quota, e.Used, e.Requested)
}

func NewGormRepository(database *gorm.DB) *GormRepository {
	return &GormRepository{db: database}
}

func (r *GormRepository) Create(ctx context.Context, job *domain.TrainingJob, idempotencyKey string) error {
	if job == nil {
		return fmt.Errorf("job is required")
	}
	if err := job.Spec.Validate(); err != nil {
		return fmt.Errorf("validate job spec: %w", err)
	}
	if job.DesiredState == "" {
		job.DesiredState = domain.DesiredActive
	}
	if job.ObservedState == "" {
		job.ObservedState = domain.StateSubmitted
	}
	outboxPayload, err := json.Marshal(map[string]string{"job_id": job.ID, "idempotency_key": idempotencyKey})
	if err != nil {
		return fmt.Errorf("marshal outbox payload: %w", err)
	}
	record, err := newJobRecord(job)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	job.CreatedAt = now
	job.UpdatedAt = now
	outbox := OutboxRecord{
		ID:            job.ID + "-submit",
		AggregateType: "TrainingJob",
		AggregateID:   job.ID,
		EventType:     "TRAINING_JOB_SUBMITTED",
		PayloadJSON:   string(outboxPayload),
		Status:        "PENDING",
		NextAttemptAt: time.Now().UTC(),
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if idempotencyKey != "" {
			idempotencyJSON, marshalErr := json.Marshal(map[string]string{"job_id": job.ID})
			if marshalErr != nil {
				return fmt.Errorf("marshal idempotency response: %w", marshalErr)
			}
			idempotency := IdempotencyRecord{TenantID: job.TenantID, Key: idempotencyKey, ResponseJSON: string(idempotencyJSON), ExpiresAt: time.Now().UTC().Add(24 * time.Hour), CreatedAt: time.Now().UTC()}
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&idempotency)
			if result.Error != nil {
				return fmt.Errorf("create idempotency record: %w", result.Error)
			}
			if result.RowsAffected == 0 {
				var existing IdempotencyRecord
				if err := tx.Where("tenant_id = ? AND key = ?", job.TenantID, idempotencyKey).First(&existing).Error; err != nil {
					return fmt.Errorf("get existing idempotency record: %w", err)
				}
				var response struct {
					JobID string `json:"job_id"`
				}
				if err := json.Unmarshal([]byte(existing.ResponseJSON), &response); err != nil || response.JobID == "" {
					return fmt.Errorf("existing idempotency record is invalid")
				}
				return &IdempotencyConflictError{JobID: response.JobID}
			}
		}
		if err := enforceTenantGPUQuota(tx, job); err != nil {
			return err
		}
		if err := tx.Create(&record).Error; err != nil {
			return fmt.Errorf("create job: %w", err)
		}
		if err := tx.Create(&outbox).Error; err != nil {
			return fmt.Errorf("create outbox event: %w", err)
		}
		return nil
	})
}

// optionalID converts an empty identifier into SQL NULL so nullable foreign
// keys are not handed an empty string.
func optionalID(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// newJobRecord maps a domain job onto its database row. Every jsonb column has
// to receive syntactically valid JSON: PostgreSQL rejects an empty string with
// SQLSTATE 22P02 and fails the whole insert.
func newJobRecord(job *domain.TrainingJob) (JobRecord, error) {
	specJSON, err := json.Marshal(job.Spec)
	if err != nil {
		return JobRecord{}, fmt.Errorf("marshal job spec: %w", err)
	}
	cleanupJSON, err := json.Marshal(job.Spec.CleanupPolicy)
	if err != nil {
		return JobRecord{}, fmt.Errorf("marshal cleanup policy: %w", err)
	}
	return JobRecord{
		ID:                   job.ID,
		TenantID:             job.TenantID,
		UserID:               job.UserID,
		SourceArtifactID:     optionalID(job.SourceArtifactID),
		SubmissionOrigin:     string(job.SubmissionOrigin),
		ExternalSubmissionID: job.ExternalSubmissionID,
		Name:                 job.Spec.Name,
		SpecJSON:             string(specJSON),
		DesiredState:         string(job.DesiredState),
		ObservedState:        string(job.ObservedState),
		KubernetesNS:         job.KubernetesNS,
		TimeoutSeconds:       job.Spec.TimeoutSeconds,
		CleanupJSON:          string(cleanupJSON),
	}, nil
}

func effectiveGPUQuota(limit int) int {
	if limit < 0 {
		return defaultTenantGPUQuota()
	}
	return limit
}

// reservedTenantGPUs sums the GPUs held by the tenant's non-terminal jobs. The
// quota check and the quota readout in the Portal both use it, so a user is
// never shown capacity that submission would then refuse.
func reservedTenantGPUs(tx *gorm.DB, tenantID string) (int, error) {
	var records []JobRecord
	if err := tx.Where("tenant_id = ? AND desired_state = ?", tenantID, string(domain.DesiredActive)).Find(&records).Error; err != nil {
		return 0, fmt.Errorf("load active tenant jobs: %w", err)
	}
	used := 0
	for _, record := range records {
		switch domain.State(record.ObservedState) {
		case domain.StateSucceeded, domain.StateFailed, domain.StateCanceled, domain.StateTimedOut:
			continue
		}
		var spec domain.JobSpec
		if err := json.Unmarshal([]byte(record.SpecJSON), &spec); err == nil {
			used += spec.Resources.WorkerReplicas * spec.Resources.GPUsPerWorker
		}
	}
	workspaceGPUs, err := reservedWorkspaceGPUs(tx, tenantID)
	if err != nil {
		return 0, err
	}
	return used + workspaceGPUs, nil
}

func reservedWorkspaceGPUs(tx *gorm.DB, tenantID string) (int, error) {
	var records []WorkspaceRecord
	if err := tx.Where("tenant_id = ?", tenantID).Find(&records).Error; err != nil {
		return 0, fmt.Errorf("load tenant workspaces: %w", err)
	}
	used := 0
	for _, record := range records {
		switch domain.WorkspaceState(record.ObservedState) {
		case domain.WorkspaceStopped, domain.WorkspaceFailed:
			continue
		}
		used += record.GPUCount
	}
	return used, nil
}

// TenantGPUQuota reports the tenant's budget for display in the Portal.
func (r *GormRepository) TenantGPUQuota(ctx context.Context, tenantID string) (domain.TenantQuota, error) {
	database := r.db.WithContext(ctx)
	var tenant TenantRecord
	if err := database.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
		return domain.TenantQuota{}, fmt.Errorf("load tenant quota: %w", err)
	}
	limit := effectiveGPUQuota(tenant.GPUQuotaLimit)
	used, err := reservedTenantGPUs(database, tenantID)
	if err != nil {
		return domain.TenantQuota{}, err
	}
	available := limit - used
	if available < 0 {
		available = 0
	}
	return domain.TenantQuota{TenantID: tenantID, GPULimit: limit, GPUUsed: used, GPUAvailable: available}, nil
}

func enforceTenantGPUQuota(tx *gorm.DB, job *domain.TrainingJob) error {
	requested := job.Spec.Resources.WorkerReplicas * job.Spec.Resources.GPUsPerWorker
	return enforceTenantGPUQuotaRequest(tx, job.TenantID, requested)
}

func enforceTenantGPUQuotaRequest(tx *gorm.DB, tenantID string, requested int) error {
	var tenant TenantRecord
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", tenantID).First(&tenant)
	if result.Error != nil {
		return fmt.Errorf("load tenant quota: %w", result.Error)
	}
	quota := effectiveGPUQuota(tenant.GPUQuotaLimit)
	used, err := reservedTenantGPUs(tx, tenantID)
	if err != nil {
		return err
	}
	if used+requested > quota {
		return &GPUQuotaExceededError{Quota: quota, Used: used, Requested: requested}
	}
	return nil
}

func (r *GormRepository) Get(ctx context.Context, tenantID, jobID string) (*domain.TrainingJob, error) {
	var record JobRecord
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, jobID).First(&record).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("job not found")
		}
		return nil, fmt.Errorf("get job: %w", err)
	}
	return record.toDomain()
}

func (r *GormRepository) GetByID(ctx context.Context, jobID string) (*domain.TrainingJob, error) {
	var record JobRecord
	err := r.db.WithContext(ctx).Where("id = ?", jobID).First(&record).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("job not found")
		}
		return nil, fmt.Errorf("get job: %w", err)
	}
	return record.toDomain()
}

func (r *GormRepository) List(ctx context.Context, filter domain.JobFilter) (domain.Page[domain.TrainingJob], error) {
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	query := r.db.WithContext(ctx).Model(&JobRecord{}).Where("archived_at IS NULL")
	if !filter.AllTenants {
		query = query.Where("tenant_id = ?", filter.TenantID)
	}
	if filter.Status != "" {
		query = query.Where("observed_state = ?", filter.Status)
	}
	if keyword := strings.TrimSpace(filter.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("(name LIKE ? OR id LIKE ?)", like, like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return domain.Page[domain.TrainingJob]{}, fmt.Errorf("count jobs: %w", err)
	}
	var records []JobRecord
	if err := query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&records).Error; err != nil {
		return domain.Page[domain.TrainingJob]{}, fmt.Errorf("list jobs: %w", err)
	}
	items := make([]domain.TrainingJob, 0, len(records))
	for _, record := range records {
		job, err := record.toDomain()
		if err != nil {
			return domain.Page[domain.TrainingJob]{}, err
		}
		items = append(items, *job)
	}
	return domain.Page[domain.TrainingJob]{Items: items, Limit: limit, Offset: offset, Total: total}, nil
}

func (r *GormRepository) SetDesiredState(ctx context.Context, tenantID, jobID string, state domain.DesiredState) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record JobRecord
		if err := tx.Where("tenant_id = ? AND id = ?", tenantID, jobID).First(&record).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("job not found")
			}
			return fmt.Errorf("get job for desired state: %w", err)
		}
		if err := tx.Model(&JobRecord{}).Where("tenant_id = ? AND id = ?", tenantID, jobID).Updates(map[string]any{
			"desired_state": state,
			"updated_at":    time.Now().UTC(),
		}).Error; err != nil {
			return fmt.Errorf("set desired state: %w", err)
		}
		if state != domain.DesiredCanceled {
			return nil
		}
		payload, err := json.Marshal(map[string]string{"job_id": jobID, "tenant_id": tenantID})
		if err != nil {
			return fmt.Errorf("marshal cancel outbox payload: %w", err)
		}
		outbox := OutboxRecord{
			ID:            jobID + "-cancel",
			AggregateType: "TrainingJob",
			AggregateID:   jobID,
			EventType:     "TRAINING_JOB_CANCEL_REQUESTED",
			PayloadJSON:   string(payload),
			Status:        "PENDING",
			NextAttemptAt: time.Now().UTC(),
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&outbox).Error; err != nil {
			return fmt.Errorf("create cancel outbox event: %w", err)
		}
		return nil
	})
}

func (r *GormRepository) ListReconcileCandidates(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var records []JobRecord
	activeTerminalStates := terminalStates()
	cancelTerminalStates := []domain.State{domain.StateCanceled, domain.StateSucceeded, domain.StateFailed, domain.StateTimedOut}
	query := r.db.WithContext(ctx).Where("(desired_state = ? AND observed_state NOT IN ?) OR (desired_state = ? AND observed_state NOT IN ?)", domain.DesiredActive, activeTerminalStates, domain.DesiredCanceled, cancelTerminalStates)
	if err := query.Order("updated_at ASC").Limit(limit).Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list reconcile candidates: %w", err)
	}
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids, nil
}

func (r *GormRepository) ApplyObservedState(ctx context.Context, observed domain.ObservedJobState) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"observed_state":   observed.State,
		"status_reason":    observed.Reason,
		"status_message":   observed.Message,
		"kubernetes_ns":    observed.KubernetesNS,
		"ray_job_name":     observed.RayJobName,
		"ray_job_uid":      observed.RayJobUID,
		"ray_cluster_name": observed.RayClusterName,
		"resource_version": observed.ResourceVersion,
		"last_observed_at": now,
		"updated_at":       now,
	}
	// Prefer the workload's own execution window. Falling back to the control
	// plane clock keeps a terminal job from having no finish time at all, but it
	// is only a fallback: it is later than reality by up to one poll interval.
	if observed.StartedAt != nil {
		updates["started_at"] = observed.StartedAt.UTC()
	}
	if observed.FinishedAt != nil {
		updates["finished_at"] = observed.FinishedAt.UTC()
	} else if isTerminalState(observed.State) {
		updates["finished_at"] = now
	}
	result := r.db.WithContext(ctx).Model(&JobRecord{}).Where("id = ?", observed.ID).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("apply observed state: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("job not found")
	}
	return nil
}

func isTerminalState(state domain.State) bool {
	for _, terminal := range terminalStates() {
		if state == terminal {
			return true
		}
	}
	return false
}

func terminalStates() []domain.State {
	return []domain.State{domain.StateSucceeded, domain.StateFailed, domain.StateCanceled, domain.StateTimedOut}
}

func (r *GormRepository) ClaimOutbox(ctx context.Context, limit int) ([]domain.OutboxEvent, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var records []OutboxRecord
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Where("status = ? AND next_attempt_at <= ?", "PENDING", now).Order("created_at ASC").Limit(limit)
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		}
		if err := query.Find(&records).Error; err != nil {
			return err
		}
		lockedAt := time.Now().UTC()
		for _, record := range records {
			if err := tx.Model(&OutboxRecord{}).Where("id = ?", record.ID).Updates(map[string]any{"status": "PROCESSING", "locked_at": lockedAt, "attempts": gorm.Expr("attempts + 1")}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("claim outbox: %w", err)
	}
	events := make([]domain.OutboxEvent, 0, len(records))
	for _, record := range records {
		events = append(events, domain.OutboxEvent{ID: record.ID, AggregateID: record.AggregateID, EventType: record.EventType, Payload: []byte(record.PayloadJSON), Attempts: record.Attempts + 1, NextAttempt: record.NextAttemptAt})
	}
	return events, nil
}

func (r *GormRepository) MarkOutboxDone(ctx context.Context, id string) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Model(&OutboxRecord{}).Where("id = ?", id).Updates(map[string]any{"status": "DONE", "completed_at": now}).Error
}

func (r *GormRepository) MarkOutboxRetry(ctx context.Context, id string, next time.Time, reason string) error {
	return r.db.WithContext(ctx).Model(&OutboxRecord{}).Where("id = ?", id).Updates(map[string]any{"status": "PENDING", "next_attempt_at": next, "last_error": reason}).Error
}

func (r JobRecord) toDomain() (*domain.TrainingJob, error) {
	var spec domain.JobSpec
	if err := json.Unmarshal([]byte(r.SpecJSON), &spec); err != nil {
		return nil, fmt.Errorf("decode job spec: %w", err)
	}
	return &domain.TrainingJob{
		ID:                   r.ID,
		TenantID:             r.TenantID,
		UserID:               r.UserID,
		SourceArtifactID:     valueOrEmpty(r.SourceArtifactID),
		SubmissionOrigin:     domain.SubmissionOrigin(r.SubmissionOrigin),
		ExternalSubmissionID: r.ExternalSubmissionID,
		Spec:                 spec,
		DesiredState:         domain.DesiredState(r.DesiredState),
		ObservedState:        domain.State(r.ObservedState),
		StatusReason:         r.StatusReason,
		StatusMessage:        r.StatusMessage,
		KubernetesNS:         r.KubernetesNS,
		RayJobName:           r.RayJobName,
		RayJobUID:            r.RayJobUID,
		RayClusterName:       r.RayClusterName,
		ResourceVersion:      r.ResourceVersion,
		CreatedAt:            r.CreatedAt,
		UpdatedAt:            r.UpdatedAt,
		LastObservedAt:       r.LastObservedAt,
		StartedAt:            r.StartedAt,
		FinishedAt:           r.FinishedAt,
	}, nil
}
