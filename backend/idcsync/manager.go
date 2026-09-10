package idcsync

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"ray-train-platform-backend/domain"
)

var ErrUnavailable = errors.New("IDC sync is not configured")

type Repository interface {
	CreateIDCDataSyncConnector(context.Context, domain.IDCDataSyncConnector) error
	ListIDCDataSyncConnectors(context.Context) ([]domain.IDCDataSyncConnector, error)
	ListIDCDataSyncRuns(context.Context, string) ([]domain.IDCDataSyncRun, error)
	CreateIDCDataSyncRun(context.Context, domain.IDCDataSyncRun) error
	ClaimIDCDataSyncRun(context.Context, string, time.Time) (domain.IDCDataSyncRun, bool, error)
	LatestSuccessfulIDCDataSyncRun(context.Context, string) (domain.IDCDataSyncRun, bool, error)
	FailIDCDataSyncRun(context.Context, string, string, time.Time) (domain.IDCDataSyncRun, error)
	ListActiveIDCDataSyncRuns(context.Context) ([]domain.IDCDataSyncRun, error)
}

type JobClient interface {
	EnsureIDCSyncJob(context.Context, JobSpec) error
	ObserveIDCSyncJob(context.Context, string, string) (JobObservation, error)
}

type JobObservation struct {
	Exists, Active, Succeeded, Failed bool
	Reason                            string
}

// JobSpec contains only platform-resolved values. In particular there is no
// public endpoint, arbitrary NFS location, shell command or user credential.
type JobSpec struct {
	Namespace            string
	RunID                string
	SourceRelativePath   string
	MirrorPrefix         string
	InternalPrefix       string
	Bucket               string
	Image                string
	TosutilConfigSecret  string
	SourceNFSServer      string
	SourceNFSPath        string
	SourceMountOptions   []string
	CallbackURL          string
	CallbackToken        string
	ServiceAccountName   string
	PreviousInventoryKey string
	WorkClaimName        string
}

type Manager struct {
	repository        Repository
	jobs              JobClient
	namespace         string
	image             string
	bucket            string
	internal          string
	secret            string
	sourceHost        string
	sourcePath        string
	sourceOpts        []string
	callback          string
	service           string
	workClaim         string
	key               []byte
	now               func() time.Time
	random            func([]byte) (int, error)
	reconcileInterval time.Duration
	runTimeout        time.Duration
	completionGrace   time.Duration
}

type Options struct {
	Namespace, Image, Bucket, InternalPrefix, TosutilConfigSecret string
	SourceNFSServer, SourceNFSPath                                string
	SourceMountOptions                                            []string
	CallbackURL, ServiceAccountName                               string
	WorkClaimName                                                 string
	CallbackKey                                                   []byte
	Now                                                           func() time.Time
	Random                                                        func([]byte) (int, error)
	ReconcileInterval, RunTimeout, CompletionGrace                time.Duration
}

func NewManager(repository Repository, jobs JobClient, options Options) (*Manager, error) {
	if repository == nil || jobs == nil {
		return nil, ErrUnavailable
	}
	values := []string{options.Namespace, options.Image, options.Bucket, options.InternalPrefix, options.TosutilConfigSecret, options.SourceNFSServer, options.SourceNFSPath, options.CallbackURL, options.ServiceAccountName, options.WorkClaimName}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, ErrUnavailable
		}
	}
	if len(options.CallbackKey) < 16 {
		return nil, ErrUnavailable
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	random := options.Random
	if random == nil {
		random = rand.Read
	}
	reconcileInterval := options.ReconcileInterval
	if reconcileInterval <= 0 {
		reconcileInterval = 30 * time.Second
	}
	runTimeout := options.RunTimeout
	if runTimeout <= 0 {
		runTimeout = 7 * 24 * time.Hour
	}
	completionGrace := options.CompletionGrace
	if completionGrace <= 0 {
		completionGrace = 2 * time.Minute
	}
	return &Manager{repository: repository, jobs: jobs, namespace: strings.TrimSpace(options.Namespace), image: strings.TrimSpace(options.Image), bucket: strings.TrimSpace(options.Bucket), internal: strings.Trim(strings.TrimSpace(options.InternalPrefix), "/"), secret: strings.TrimSpace(options.TosutilConfigSecret), sourceHost: strings.TrimSpace(options.SourceNFSServer), sourcePath: strings.TrimSpace(options.SourceNFSPath), sourceOpts: append([]string(nil), options.SourceMountOptions...), callback: strings.TrimRight(strings.TrimSpace(options.CallbackURL), "/"), service: strings.TrimSpace(options.ServiceAccountName), workClaim: strings.TrimSpace(options.WorkClaimName), key: append([]byte(nil), options.CallbackKey...), now: now, random: random, reconcileInterval: reconcileInterval, runTimeout: runTimeout, completionGrace: completionGrace}, nil
}

func (m *Manager) CreateConnector(ctx context.Context, connector domain.IDCDataSyncConnector) error {
	if m == nil {
		return ErrUnavailable
	}
	return m.repository.CreateIDCDataSyncConnector(ctx, connector)
}

func (m *Manager) ListConnectors(ctx context.Context) ([]domain.IDCDataSyncConnector, error) {
	if m == nil {
		return nil, ErrUnavailable
	}
	return m.repository.ListIDCDataSyncConnectors(ctx)
}

func (m *Manager) ListRuns(ctx context.Context, connectorID string) ([]domain.IDCDataSyncRun, error) {
	if m == nil {
		return nil, ErrUnavailable
	}
	return m.repository.ListIDCDataSyncRuns(ctx, connectorID)
}

func (m *Manager) Request(ctx context.Context, connector domain.IDCDataSyncConnector, requestedBy string) (domain.IDCDataSyncRun, error) {
	if m == nil {
		return domain.IDCDataSyncRun{}, ErrUnavailable
	}
	id, err := m.newID()
	if err != nil {
		return domain.IDCDataSyncRun{}, err
	}
	run := domain.IDCDataSyncRun{ID: id, ConnectorID: connector.ID, IdempotencyKey: id, Mode: domain.IDCDataSyncRunModeSync, State: domain.IDCDataSyncRunPending, RequestedBy: requestedBy}
	if err := m.repository.CreateIDCDataSyncRun(ctx, run); err != nil {
		return domain.IDCDataSyncRun{}, err
	}
	claimed, claimedNow, err := m.repository.ClaimIDCDataSyncRun(ctx, run.ID, m.now().UTC())
	if err != nil {
		return domain.IDCDataSyncRun{}, err
	}
	if !claimedNow {
		return claimed, nil
	}
	previous, found, err := m.repository.LatestSuccessfulIDCDataSyncRun(ctx, connector.ID)
	if err != nil {
		_, _ = m.repository.FailIDCDataSyncRun(ctx, claimed.ID, "previous inventory lookup failed", m.now().UTC())
		return domain.IDCDataSyncRun{}, err
	}
	previousKey := ""
	if found {
		previousKey = previous.InventoryObjectKey
	}
	spec := JobSpec{Namespace: m.namespace, RunID: claimed.ID, SourceRelativePath: connector.SourceRelativePath, MirrorPrefix: connector.MirrorPrefix, InternalPrefix: m.internal, Bucket: m.bucket, Image: m.image, TosutilConfigSecret: m.secret, SourceNFSServer: m.sourceHost, SourceNFSPath: m.sourcePath, SourceMountOptions: append([]string(nil), m.sourceOpts...), CallbackURL: m.callback + "/api/v1/internal/idc-sync/runs/" + claimed.ID, CallbackToken: m.callbackToken(claimed.ID), ServiceAccountName: m.service, PreviousInventoryKey: previousKey, WorkClaimName: m.workClaim}
	if err := m.jobs.EnsureIDCSyncJob(ctx, spec); err != nil {
		_, _ = m.repository.FailIDCDataSyncRun(ctx, claimed.ID, "Kubernetes sync Job could not be created", m.now().UTC())
		return domain.IDCDataSyncRun{}, fmt.Errorf("create IDC sync workload: %w", err)
	}
	return claimed, nil
}

func (m *Manager) Run(ctx context.Context) error {
	_ = m.reconcile(ctx)
	ticker := time.NewTicker(m.reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_ = m.reconcile(ctx)
		}
	}
}

func (m *Manager) reconcile(ctx context.Context) error {
	runs, err := m.repository.ListActiveIDCDataSyncRuns(ctx)
	if err != nil {
		return fmt.Errorf("list active IDC sync runs: %w", err)
	}
	now := m.now().UTC()
	for _, run := range runs {
		observation, err := m.jobs.ObserveIDCSyncJob(ctx, m.namespace, run.ID)
		if err != nil {
			return fmt.Errorf("observe IDC sync Job: %w", err)
		}
		age := now.Sub(run.CreatedAt)
		if run.StartedAt != nil {
			age = now.Sub(*run.StartedAt)
		}
		reason := ""
		switch {
		case run.StartedAt != nil && age > m.runTimeout:
			reason = "IDC sync exceeded its execution deadline"
		case observation.Failed:
			reason = "Kubernetes sync Job failed"
			if strings.TrimSpace(observation.Reason) != "" {
				reason += ": " + strings.TrimSpace(observation.Reason)
			}
		case observation.Succeeded && age > m.completionGrace:
			reason = "Kubernetes sync Job completed without a valid receipt"
		case !observation.Exists && age > m.completionGrace:
			reason = "Kubernetes sync Job is missing"
		}
		if len(reason) > 512 {
			reason = reason[:512]
		}
		if reason != "" {
			if _, err := m.repository.FailIDCDataSyncRun(ctx, run.ID, reason, now); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) callbackToken(runID string) string {
	mac := hmac.New(sha256.New, m.key)
	_, _ = mac.Write([]byte("idc-sync:" + runID))
	return hex.EncodeToString(mac.Sum(nil))
}

func (m *Manager) newID() (string, error) {
	bytes := make([]byte, 12)
	if _, err := m.random(bytes); err != nil {
		return "", fmt.Errorf("generate IDC sync ID: %w", err)
	}
	return "idc-sync-" + hex.EncodeToString(bytes), nil
}
