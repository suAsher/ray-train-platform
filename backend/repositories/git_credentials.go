package repositories

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
)

var ErrGitCredentialNotFound = errors.New("git credential not found")

type GitCredentialRecord struct {
	ID         string `gorm:"primaryKey"`
	TenantID   string `gorm:"column:tenant_id;index"`
	Name       string
	Host       string
	Username   string
	SecretName string `gorm:"column:secret_name"`
	CreatedBy  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (GitCredentialRecord) TableName() string { return "git_credentials" }

func toGitCredential(record GitCredentialRecord) domain.GitCredential {
	return domain.GitCredential{
		ID: record.ID, TenantID: record.TenantID, Name: record.Name, Host: record.Host,
		Username: record.Username, SecretName: record.SecretName, CreatedBy: record.CreatedBy,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

// UpsertGitCredential registers the credential reference. The token itself is
// never stored here: only the name of the Kubernetes Secret that holds it.
func (r *GormRepository) UpsertGitCredential(ctx context.Context, credential domain.GitCredential) error {
	if err := credential.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	record := GitCredentialRecord{
		ID: credential.ID, TenantID: credential.TenantID, Name: credential.Name,
		Host: domain.NormalizeGitHost(credential.Host), Username: credential.Username,
		SecretName: credential.SecretName, CreatedBy: credential.CreatedBy,
		CreatedAt: now, UpdatedAt: now,
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// One credential per host per tenant: a second one would make the
		// choice at materialization time ambiguous.
		if err := tx.Where("tenant_id = ? AND host = ?", record.TenantID, record.Host).
			Delete(&GitCredentialRecord{}).Error; err != nil {
			return fmt.Errorf("replace git credential: %w", err)
		}
		if err := tx.Create(&record).Error; err != nil {
			return fmt.Errorf("create git credential: %w", err)
		}
		return nil
	})
}

func (r *GormRepository) ListGitCredentials(ctx context.Context, tenantID string) ([]domain.GitCredential, error) {
	var records []GitCredentialRecord
	if err := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID).Order("host ASC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list git credentials: %w", err)
	}
	credentials := make([]domain.GitCredential, 0, len(records))
	for _, record := range records {
		credentials = append(credentials, toGitCredential(record))
	}
	return credentials, nil
}

// GitCredentialForURL finds the credential that covers a repository URL, so the
// materializer only receives a token when the repository actually needs one.
func (r *GormRepository) GitCredentialForURL(ctx context.Context, tenantID, repositoryURL string) (domain.GitCredential, error) {
	parsed, err := url.Parse(repositoryURL)
	if err != nil || parsed.Hostname() == "" {
		return domain.GitCredential{}, ErrGitCredentialNotFound
	}
	var record GitCredentialRecord
	err = r.db.WithContext(ctx).
		Where("tenant_id = ? AND host = ?", tenantID, domain.NormalizeGitHost(parsed.Hostname())).
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.GitCredential{}, ErrGitCredentialNotFound
	}
	if err != nil {
		return domain.GitCredential{}, fmt.Errorf("find git credential: %w", err)
	}
	return toGitCredential(record), nil
}

func (r *GormRepository) DeleteGitCredential(ctx context.Context, tenantID, id string) (string, error) {
	var record GitCredentialRecord
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrGitCredentialNotFound
	}
	if err != nil {
		return "", fmt.Errorf("find git credential: %w", err)
	}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Delete(&GitCredentialRecord{}).Error; err != nil {
		return "", fmt.Errorf("delete git credential: %w", err)
	}
	// The caller removes the Kubernetes Secret; returning its name keeps that
	// cleanup from needing a second lookup.
	return strings.TrimSpace(record.SecretName), nil
}

// GitCredentialSecretFor returns the Secret name covering a repository URL, or
// an empty string when the repository is public for this tenant.
func (r *GormRepository) GitCredentialSecretFor(ctx context.Context, tenantID, repositoryURL string) string {
	credential, err := r.GitCredentialForURL(ctx, tenantID, repositoryURL)
	if err != nil {
		return ""
	}
	return credential.SecretName
}
