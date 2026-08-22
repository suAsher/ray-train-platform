package objectstore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"ray-train-platform-backend/domain"
)

const (
	GiB int64 = 1024 * 1024 * 1024
	TiB       = 1024 * GiB

	personalObjectSetPathLevel = 5
)

var (
	// ErrObjectSetNotReady means the bucket has not been explicitly prepared
	// for the governed five-level personal-root layout. It is intentionally
	// distinct from ErrUnavailable so administrators get an actionable signal
	// without treating a configuration mismatch as a transient failure.
	ErrObjectSetNotReady = errors.New("TOS ObjectSet is not prepared for personal storage")

	// ErrInvalidPersonalStorageQuota covers a zero/unlimited quota, values
	// below one GiB, and values above the platform-wide hard ceiling. Personal
	// object spaces must always be finite when quota governance is enabled.
	ErrInvalidPersonalStorageQuota = errors.New("invalid personal storage quota")
)

// PersonalStorageQuota is safe to return to the Portal: it carries only the
// effective hard quota, never bucket, prefix, access key, or ObjectSet name.
type PersonalStorageQuota struct {
	Bytes    int64 `json:"bytes"`
	Enforced bool  `json:"enforced"`
}

// PersonalStorageQuotaManager owns one native TOS ObjectSet per trusted
// platform identity. It is backend-only: workload Pods receive neither a TOS
// credential nor arbitrary storage controls.
type PersonalStorageQuotaManager interface {
	PrepareBucket(context.Context) error
	EnsurePersonalQuota(context.Context, string, string, int64) (PersonalStorageQuota, error)
	SetPersonalQuota(context.Context, string, string, int64) (PersonalStorageQuota, error)
	GetPersonalQuota(context.Context, string, string) (PersonalStorageQuota, error)
}

type tosObjectSetQuotaClient interface {
	GetBucketObjectSetConfiguration(context.Context, *tos.GetBucketObjectSetConfigurationInput) (*tos.GetBucketObjectSetConfigurationOutput, error)
	PutBucketObjectSetConfiguration(context.Context, *tos.PutBucketObjectSetConfigurationInput) (*tos.PutBucketObjectSetConfigurationOutput, error)
	GetObjectSet(context.Context, *tos.GetObjectSetInput) (*tos.GetObjectSetOutput, error)
	PutObjectSet(context.Context, *tos.PutObjectSetInput) (*tos.PutObjectSetOutput, error)
	PutObjectSetQuota(context.Context, *tos.PutObjectSetQuotaInput) (*tos.PutObjectSetQuotaOutput, error)
	GetObjectSetQuota(context.Context, *tos.GetObjectSetQuotaInput) (*tos.GetObjectSetQuotaOutput, error)
}

type personalObjectSetQuotaManager struct {
	bucket       string
	client       tosObjectSetQuotaClient
	defaultBytes int64
	maxBytes     int64
}

func newPersonalObjectSetQuotaManager(bucket string, client tosObjectSetQuotaClient, defaultBytes, maxBytes int64) *personalObjectSetQuotaManager {
	return &personalObjectSetQuotaManager{
		bucket: strings.TrimSpace(bucket), client: client, defaultBytes: defaultBytes, maxBytes: maxBytes,
	}
}

// NewPersonalStorageQuotaManager exposes quota management only when the
// configured TOS store is backed by a complete SDK client. Tests and other
// narrow object-store capabilities cannot accidentally gain bucket-governance
// privileges through this conversion.
func NewPersonalStorageQuotaManager(store *TOSStore, defaultBytes, maxBytes int64) (PersonalStorageQuotaManager, error) {
	if store == nil {
		return nil, fmt.Errorf("TOS store is required")
	}
	client, ok := store.client.(tosObjectSetQuotaClient)
	if !ok {
		return nil, fmt.Errorf("TOS ObjectSet management is not available")
	}
	manager := newPersonalObjectSetQuotaManager(store.bucket, client, defaultBytes, maxBytes)
	if err := manager.validateQuota(defaultBytes); err != nil {
		return nil, fmt.Errorf("default personal storage quota: %w", err)
	}
	if maxBytes < defaultBytes {
		return nil, fmt.Errorf("maximum personal storage quota must be at least the default")
	}
	return manager, nil
}

// PrepareBucket is the one explicit, administrator-triggered bucket-wide
// action. It never changes an existing ObjectSet layout: a pre-existing
// non-five-level configuration is rejected rather than overwritten.
func (manager *personalObjectSetQuotaManager) PrepareBucket(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if manager == nil || manager.client == nil || manager.bucket == "" {
		return ErrUnavailable
	}
	output, err := manager.client.GetBucketObjectSetConfiguration(ctx, &tos.GetBucketObjectSetConfigurationInput{Bucket: manager.bucket})
	if err == nil {
		if output == nil || output.PathLevel != personalObjectSetPathLevel || (output.CustomDelimiter != "" && output.CustomDelimiter != "/") {
			return ErrObjectSetNotReady
		}
		return nil
	}
	if !isObjectSetNotFound(err) {
		return fmt.Errorf("%w: inspect bucket ObjectSet configuration", ErrUnavailable)
	}
	if _, err := manager.client.PutBucketObjectSetConfiguration(ctx, &tos.PutBucketObjectSetConfigurationInput{
		Bucket: manager.bucket, PathLevel: personalObjectSetPathLevel, CustomDelimiter: "/", EnableDefaultObjectSet: false,
	}); err != nil {
		return fmt.Errorf("%w: initialize bucket ObjectSet configuration", ErrUnavailable)
	}
	return nil
}

// EnsurePersonalQuota creates the exact ObjectSet that corresponds to a
// user's platform-owned root when absent, then enforces its requested quota.
// A zero request means "use the platform default" only during provisioning;
// it is never passed to TOS as an unlimited quota.
func (manager *personalObjectSetQuotaManager) EnsurePersonalQuota(ctx context.Context, tenantID, userID string, requestedBytes int64) (PersonalStorageQuota, error) {
	if requestedBytes == 0 {
		requestedBytes = manager.defaultBytes
	}
	return manager.setPersonalQuota(ctx, tenantID, userID, requestedBytes, true)
}

// SetPersonalQuota updates the native TOS hard limit. Unlike provisioning,
// zero is rejected so an administrator cannot accidentally turn an account
// into an unlimited writer.
func (manager *personalObjectSetQuotaManager) SetPersonalQuota(ctx context.Context, tenantID, userID string, requestedBytes int64) (PersonalStorageQuota, error) {
	// Existing accounts may have been created before ObjectSet governance was
	// enabled. An administrator's first explicit quota assignment is therefore
	// also the safe, idempotent backfill point for that user's fixed root.
	return manager.setPersonalQuota(ctx, tenantID, userID, requestedBytes, true)
}

func (manager *personalObjectSetQuotaManager) setPersonalQuota(ctx context.Context, tenantID, userID string, requestedBytes int64, create bool) (PersonalStorageQuota, error) {
	if err := ctx.Err(); err != nil {
		return PersonalStorageQuota{}, err
	}
	if err := manager.validateQuota(requestedBytes); err != nil {
		return PersonalStorageQuota{}, err
	}
	objectSetName, err := personalObjectSetName(tenantID, userID)
	if err != nil {
		return PersonalStorageQuota{}, err
	}
	if err := manager.ensureBucketReady(ctx); err != nil {
		return PersonalStorageQuota{}, err
	}
	if err := manager.ensureObjectSet(ctx, objectSetName, create); err != nil {
		return PersonalStorageQuota{}, err
	}
	if _, err := manager.client.PutObjectSetQuota(ctx, &tos.PutObjectSetQuotaInput{
		Bucket: manager.bucket, ObjectSetName: objectSetName, StorageQuota: strconv.FormatInt(requestedBytes, 10),
	}); err != nil {
		return PersonalStorageQuota{}, fmt.Errorf("%w: set personal storage quota", ErrUnavailable)
	}
	return PersonalStorageQuota{Bytes: requestedBytes, Enforced: true}, nil
}

func (manager *personalObjectSetQuotaManager) GetPersonalQuota(ctx context.Context, tenantID, userID string) (PersonalStorageQuota, error) {
	if err := ctx.Err(); err != nil {
		return PersonalStorageQuota{}, err
	}
	objectSetName, err := personalObjectSetName(tenantID, userID)
	if err != nil {
		return PersonalStorageQuota{}, err
	}
	if err := manager.ensureBucketReady(ctx); err != nil {
		return PersonalStorageQuota{}, err
	}
	if err := manager.ensureObjectSet(ctx, objectSetName, false); err != nil {
		return PersonalStorageQuota{}, err
	}
	output, err := manager.client.GetObjectSetQuota(ctx, &tos.GetObjectSetQuotaInput{Bucket: manager.bucket, ObjectSetName: objectSetName})
	if err != nil || output == nil {
		return PersonalStorageQuota{}, fmt.Errorf("%w: read personal storage quota", ErrUnavailable)
	}
	bytes, parseErr := strconv.ParseInt(strings.TrimSpace(output.StorageQuota), 10, 64)
	if parseErr != nil || manager.validateQuota(bytes) != nil {
		return PersonalStorageQuota{}, fmt.Errorf("%w: invalid personal storage quota response", ErrUnavailable)
	}
	return PersonalStorageQuota{Bytes: bytes, Enforced: true}, nil
}

func (manager *personalObjectSetQuotaManager) ensureBucketReady(ctx context.Context) error {
	if manager == nil || manager.client == nil || manager.bucket == "" {
		return ErrUnavailable
	}
	output, err := manager.client.GetBucketObjectSetConfiguration(ctx, &tos.GetBucketObjectSetConfigurationInput{Bucket: manager.bucket})
	if err != nil || output == nil || output.PathLevel != personalObjectSetPathLevel || (output.CustomDelimiter != "" && output.CustomDelimiter != "/") {
		return ErrObjectSetNotReady
	}
	return nil
}

func (manager *personalObjectSetQuotaManager) ensureObjectSet(ctx context.Context, objectSetName string, create bool) error {
	_, err := manager.client.GetObjectSet(ctx, &tos.GetObjectSetInput{Bucket: manager.bucket, ObjectSetName: objectSetName})
	if err == nil {
		return nil
	}
	if !isObjectSetNotFound(err) {
		return fmt.Errorf("%w: inspect personal ObjectSet", ErrUnavailable)
	}
	if !create {
		return ErrObjectSetNotReady
	}
	if _, err := manager.client.PutObjectSet(ctx, &tos.PutObjectSetInput{Bucket: manager.bucket, ObjectSetName: objectSetName}); err != nil {
		return fmt.Errorf("%w: create personal ObjectSet", ErrUnavailable)
	}
	return nil
}

func (manager *personalObjectSetQuotaManager) validateQuota(bytes int64) error {
	if manager == nil || bytes < GiB || manager.defaultBytes < GiB || manager.maxBytes < manager.defaultBytes || bytes > manager.maxBytes {
		return ErrInvalidPersonalStorageQuota
	}
	return nil
}

func personalObjectSetName(tenantID, userID string) (string, error) {
	root, err := domain.PersonalDataRootFor(tenantID, userID)
	if err != nil {
		return "", fmt.Errorf("derive personal ObjectSet: %w", err)
	}
	name := strings.Trim(root, "/")
	if len(strings.Split(name, "/")) != personalObjectSetPathLevel {
		return "", fmt.Errorf("%w: unexpected personal root layout", ErrObjectSetNotReady)
	}
	return name, nil
}

func isObjectSetNotFound(err error) bool {
	if tos.StatusCode(err) == http.StatusNotFound {
		return true
	}
	var statusCoder interface{ StatusCode() int }
	return errors.As(err, &statusCoder) && statusCoder.StatusCode() == http.StatusNotFound
}
