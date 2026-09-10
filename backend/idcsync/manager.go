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
}

type JobClient interface {
	EnsureIDCSyncJob(context.Context, JobSpec) error
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
}

type Manager struct {
	repository Repository
	jobs       JobClient
	namespace  string
	image      string
	bucket     string
	internal   string
	secret     string
	sourceHost string
	sourcePath string
	sourceOpts []string
	callback   string
	service    string
	key        []byte
	now        func() time.Time
	random     func([]byte) (int, error)
}

type Options struct {
	Namespace, Image, Bucket, InternalPrefix, TosutilConfigSecret string
	SourceNFSServer, SourceNFSPath                                string
	SourceMountOptions                                            []string
	CallbackURL, ServiceAccountName                               string
	CallbackKey                                                   []byte
	Now                                                           func() time.Time
	Random                                                        func([]byte) (int, error)
}

func NewManager(repository Repository, jobs JobClient, options Options) (*Manager, error) {
	if repository == nil || jobs == nil {
		return nil, ErrUnavailable
	}
	values := []string{options.Namespace, options.Image, options.Bucket, options.InternalPrefix, options.TosutilConfigSecret, options.SourceNFSServer, options.SourceNFSPath, options.CallbackURL, options.ServiceAccountName}
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
	return &Manager{repository: repository, jobs: jobs, namespace: strings.TrimSpace(options.Namespace), image: strings.TrimSpace(options.Image), bucket: strings.TrimSpace(options.Bucket), internal: strings.Trim(strings.TrimSpace(options.InternalPrefix), "/"), secret: strings.TrimSpace(options.TosutilConfigSecret), sourceHost: strings.TrimSpace(options.SourceNFSServer), sourcePath: strings.TrimSpace(options.SourceNFSPath), sourceOpts: append([]string(nil), options.SourceMountOptions...), callback: strings.TrimRight(strings.TrimSpace(options.CallbackURL), "/"), service: strings.TrimSpace(options.ServiceAccountName), key: append([]byte(nil), options.CallbackKey...), now: now, random: random}, nil
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
	spec := JobSpec{Namespace: m.namespace, RunID: claimed.ID, SourceRelativePath: connector.SourceRelativePath, MirrorPrefix: connector.MirrorPrefix, InternalPrefix: m.internal, Bucket: m.bucket, Image: m.image, TosutilConfigSecret: m.secret, SourceNFSServer: m.sourceHost, SourceNFSPath: m.sourcePath, SourceMountOptions: append([]string(nil), m.sourceOpts...), CallbackURL: m.callback + "/api/v1/internal/idc-sync/runs/" + claimed.ID, CallbackToken: m.callbackToken(claimed.ID), ServiceAccountName: m.service, PreviousInventoryKey: previousKey}
	if err := m.jobs.EnsureIDCSyncJob(ctx, spec); err != nil {
		_, _ = m.repository.FailIDCDataSyncRun(ctx, claimed.ID, "Kubernetes sync Job could not be created", m.now().UTC())
		return domain.IDCDataSyncRun{}, fmt.Errorf("create IDC sync workload: %w", err)
	}
	return claimed, nil
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
