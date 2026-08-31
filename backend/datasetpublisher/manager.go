package datasetpublisher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"

	"ray-train-platform-backend/domain"
)

var (
	ErrInvalidPublicationManager     = errors.New("invalid dataset publication manager")
	ErrInvalidPublicationRequest     = errors.New("invalid dataset publication request")
	ErrPublicationManagerUnavailable = errors.New("dataset publication manager unavailable")
)

const (
	defaultPublicationBatchLimit   = 32
	maximumPublicationBatchLimit   = 256
	defaultPublicationPollInterval = 10 * time.Second
)

type PublicationManagerRepository interface {
	CreateDatasetPublicationRequest(context.Context, string, bool, domain.DatasetVersion, domain.DatasetPublicationRun) (domain.DatasetPublicationRun, error)
	ListActiveDatasetPublications(context.Context, int) ([]domain.DatasetPublicationWork, error)
	ListDatasetVersionGCCandidates(context.Context) ([]domain.DatasetVersion, error)
}

type PublicationReconciler interface {
	Reconcile(context.Context, ReconcileRequest) (domain.DatasetPublicationRun, error)
}

type ManagerOptions struct {
	PublicRoot       string
	SourceIndexName  string
	BatchLimit       int
	PollInterval     time.Duration
	Now              func() time.Time
	NewID            func(string) (string, error)
	OnReconcileError func(error)
}

type Manager struct {
	repository       PublicationManagerRepository
	controller       PublicationReconciler
	publicRoot       string
	sourceIndexName  string
	batchLimit       int
	pollInterval     time.Duration
	now              func() time.Time
	newID            func(string) (string, error)
	onReconcileError func(error)
}

// publicationReconcileError keeps a bounded, non-secret stage marker for
// operators while preserving the intentionally generic public error string.
// Dependency errors, object paths, credentials, and response bodies are never
// retained in this value.
type publicationReconcileError struct {
	stage string
	runID string
}

func (err *publicationReconcileError) Error() string { return ErrPublicationManagerUnavailable.Error() }

func (err *publicationReconcileError) Unwrap() error { return ErrPublicationManagerUnavailable }

// ReconcileDiagnostic returns an operator-safe failure location. It is meant
// for server logs only; user-facing API errors remain generic.
func ReconcileDiagnostic(err error) string {
	var diagnostic *publicationReconcileError
	if !errors.As(err, &diagnostic) {
		return ErrPublicationManagerUnavailable.Error()
	}
	if diagnostic.runID == "" {
		return diagnostic.stage + " failed"
	}
	return diagnostic.stage + " failed for " + diagnostic.runID
}

func NewManager(repository PublicationManagerRepository, controller PublicationReconciler, options ManagerOptions) (*Manager, error) {
	if isNilPublicationDependency(repository) || isNilPublicationDependency(controller) {
		return nil, ErrInvalidPublicationManager
	}
	root := strings.TrimSuffix(strings.TrimSpace(options.PublicRoot), "/") + "/"
	normalizedRoot, err := domain.NormalizePublicDataRoot(root)
	if err != nil {
		return nil, ErrInvalidPublicationManager
	}
	sourceIndex := strings.TrimSpace(options.SourceIndexName)
	if !validPublicationRelativePath(sourceIndex) {
		return nil, ErrInvalidPublicationManager
	}
	batchLimit := options.BatchLimit
	if batchLimit == 0 {
		batchLimit = defaultPublicationBatchLimit
	}
	if batchLimit < 1 || batchLimit > maximumPublicationBatchLimit {
		return nil, ErrInvalidPublicationManager
	}
	pollInterval := options.PollInterval
	if pollInterval == 0 {
		pollInterval = defaultPublicationPollInterval
	}
	if pollInterval < time.Second || pollInterval > 5*time.Minute || pollInterval%time.Second != 0 {
		return nil, ErrInvalidPublicationManager
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	newID := options.NewID
	if newID == nil {
		newID = newPublicationManagerID
	}
	return &Manager{
		repository: repository, controller: controller,
		publicRoot: strings.TrimSuffix(normalizedRoot, "/"), sourceIndexName: sourceIndex,
		batchLimit: batchLimit, pollInterval: pollInterval, now: now, newID: newID,
		onReconcileError: options.OnReconcileError,
	}, nil
}

func (manager *Manager) RequestDatasetPublication(ctx context.Context, dataset domain.Dataset, requestedBy string) (domain.DatasetPublicationRun, error) {
	if manager == nil || isNilPublicationDependency(manager.repository) || isNilPublicationDependency(manager.controller) {
		return domain.DatasetPublicationRun{}, ErrInvalidPublicationManager
	}
	if err := ctx.Err(); err != nil {
		return domain.DatasetPublicationRun{}, err
	}
	if err := dataset.Validate(); err != nil || !validPublicationRequester(requestedBy) {
		return domain.DatasetPublicationRun{}, ErrInvalidPublicationRequest
	}
	versionID, err := manager.newID("version")
	if err != nil || !validIdentifier(versionID) {
		return domain.DatasetPublicationRun{}, ErrPublicationManagerUnavailable
	}
	runID, err := manager.newID("publication")
	if err != nil || !validIdentifier(runID) || runID == versionID {
		return domain.DatasetPublicationRun{}, ErrPublicationManagerUnavailable
	}
	version := domain.DatasetVersion{
		ID: versionID, DatasetID: dataset.ID, Version: publicationVersionName(manager.now().UTC(), versionID),
		State: domain.DatasetVersionDiscovering, SchemaVersion: dataset.SchemaVersion,
	}
	if err := version.Validate(); err != nil {
		return domain.DatasetPublicationRun{}, ErrPublicationManagerUnavailable
	}
	run := domain.DatasetPublicationRun{ID: runID, DatasetID: dataset.ID, DatasetVersionID: versionID, State: domain.DatasetVersionDiscovering}
	tenantID, superAdmin := publicationMutationScope(dataset)
	created, err := manager.repository.CreateDatasetPublicationRequest(ctx, tenantID, superAdmin, version, run)
	if err != nil || created.Validate() != nil || created.ID != run.ID || created.DatasetID != run.DatasetID || created.DatasetVersionID != run.DatasetVersionID {
		if contextError := ctx.Err(); contextError != nil {
			return domain.DatasetPublicationRun{}, contextError
		}
		return domain.DatasetPublicationRun{}, ErrPublicationManagerUnavailable
	}
	return created, nil
}

func (manager *Manager) ReconcileOnce(ctx context.Context) error {
	if manager == nil || isNilPublicationDependency(manager.repository) || isNilPublicationDependency(manager.controller) {
		return ErrInvalidPublicationManager
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	work, err := manager.repository.ListActiveDatasetPublications(ctx, manager.batchLimit)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return contextError
		}
		return &publicationReconcileError{stage: "list active publications"}
	}
	var firstFailure *publicationReconcileError
	for _, item := range work {
		request, err := manager.reconcileRequest(item)
		stage := "build reconcile request"
		if err == nil {
			stage = "controller reconcile"
			_, err = manager.controller.Reconcile(ctx, request)
			stage = safePublicationFailureStage(stage, err)
		}
		if err != nil {
			if contextError := ctx.Err(); contextError != nil {
				return contextError
			}
			if firstFailure == nil {
				firstFailure = &publicationReconcileError{stage: stage, runID: item.Run.ID}
			}
		}
	}
	if firstFailure != nil {
		return firstFailure
	}
	return nil
}

func safePublicationFailureStage(stage string, err error) string {
	switch {
	case errors.Is(err, ErrInvalidPublicationControllerRequest):
		return stage + ": invalid request"
	case errors.Is(err, ErrPublicationJobUnavailable):
		return stage + ": publication job unavailable"
	case errors.Is(err, ErrPublicationJobFailed):
		return stage + ": publication job failed"
	case errors.Is(err, ErrPublicationControllerUnavailable):
		return stage + ": controller unavailable"
	default:
		return stage
	}
}

func (manager *Manager) Run(ctx context.Context) error {
	if manager == nil {
		return ErrInvalidPublicationManager
	}
	ticker := time.NewTicker(manager.pollInterval)
	defer ticker.Stop()
	for {
		if err := manager.ReconcileOnce(ctx); err != nil {
			if contextError := ctx.Err(); contextError != nil {
				return contextError
			}
			if manager.onReconcileError != nil {
				manager.onReconcileError(err)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (manager *Manager) DryRunDatasetVersionGC(ctx context.Context) ([]domain.DatasetVersion, error) {
	if manager == nil || isNilPublicationDependency(manager.repository) {
		return nil, ErrInvalidPublicationManager
	}
	versions, err := manager.repository.ListDatasetVersionGCCandidates(ctx)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return nil, contextError
		}
		return nil, ErrPublicationManagerUnavailable
	}
	result := append([]domain.DatasetVersion(nil), versions...)
	for _, version := range result {
		if err := version.Validate(); err != nil || version.State != domain.DatasetVersionDeprecated {
			return nil, ErrPublicationManagerUnavailable
		}
	}
	return result, nil
}

func (manager *Manager) reconcileRequest(work domain.DatasetPublicationWork) (ReconcileRequest, error) {
	if err := work.Validate(); err != nil || work.Run.State != domain.DatasetVersionDiscovering && !activePublicationState(work.Run.State) {
		return ReconcileRequest{}, ErrInvalidPublicationRequest
	}
	tenantID, superAdmin := publicationMutationScope(work.Dataset)
	root := manager.publicRoot
	if work.Dataset.Visibility == domain.DatasetVisibilityTeam {
		root = path.Join("ray-train", "tenants", work.Dataset.OwnerTenantID, "shared")
	}
	root = path.Join(root, work.Dataset.SourceRelativePath)
	request := ReconcileRequest{
		TenantID: tenantID, SuperAdmin: superAdmin,
		RunID: work.Run.ID, DatasetID: work.Dataset.ID, DatasetVersionID: work.Version.ID,
		Version: work.Version.Version, SchemaVersion: work.Version.SchemaVersion,
		SourceRoot: root, SourceIndex: manager.sourceIndexName,
	}
	if !request.valid() {
		return ReconcileRequest{}, ErrInvalidPublicationRequest
	}
	return request, nil
}

func publicationMutationScope(dataset domain.Dataset) (string, bool) {
	if dataset.Visibility == domain.DatasetVisibilityPublic {
		return "", true
	}
	return dataset.OwnerTenantID, false
}

func publicationVersionName(now time.Time, versionID string) string {
	suffix := strings.TrimPrefix(versionID, "version-")
	if len(suffix) > 12 {
		suffix = suffix[:12]
	}
	return now.UTC().Format("20060102T150405Z") + "+" + suffix
}

func newPublicationManagerID(prefix string) (string, error) {
	if prefix != "version" && prefix != "publication" {
		return "", fmt.Errorf("invalid identity prefix")
	}
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(buffer), nil
}

func validPublicationRequester(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}
