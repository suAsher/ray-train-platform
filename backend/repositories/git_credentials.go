package repositories

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
)

var ErrGitCredentialNotFound = errors.New("git credential not found")

type GitCredentialRecord struct {
	ID          string `gorm:"primaryKey"`
	TenantID    string `gorm:"column:tenant_id;index"`
	Scope       string `gorm:"column:scope;index"`
	OwnerUserID string `gorm:"column:owner_user_id;index"`
	Name        string
	Host        string
	Username    string
	SecretName  string `gorm:"column:secret_name"`
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (GitCredentialRecord) TableName() string { return "git_credentials" }

func toGitCredential(record GitCredentialRecord) domain.GitCredential {
	return domain.GitCredential{
		ID: record.ID, TenantID: record.TenantID, Scope: domain.GitCredentialScope(record.Scope), OwnerUserID: record.OwnerUserID, Name: record.Name, Host: record.Host,
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
		ID: credential.ID, TenantID: credential.TenantID, Scope: string(credential.Scope), OwnerUserID: credential.OwnerUserID, Name: credential.Name,
		Host: domain.NormalizeGitHost(credential.Host), Username: credential.Username,
		SecretName: credential.SecretName, CreatedBy: credential.CreatedBy,
		CreatedAt: now, UpdatedAt: now,
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// One credential per host and scope. A personal credential has an owner;
		// the team fallback uses the empty owner field.
		if err := tx.Where("tenant_id = ? AND scope = ? AND owner_user_id = ? AND host = ?", record.TenantID, record.Scope, record.OwnerUserID, record.Host).
			Delete(&GitCredentialRecord{}).Error; err != nil {
			return fmt.Errorf("replace git credential: %w", err)
		}
		if err := tx.Create(&record).Error; err != nil {
			return fmt.Errorf("create git credential: %w", err)
		}
		return nil
	})
}

func (r *GormRepository) ListGitCredentials(ctx context.Context, tenantID, userID string, scope domain.GitCredentialScope) ([]domain.GitCredential, error) {
	var records []GitCredentialRecord
	query := r.db.WithContext(ctx).Where("tenant_id = ? AND scope = ?", tenantID, scope)
	if scope == domain.GitCredentialScopePersonal {
		query = query.Where("owner_user_id = ?", userID)
	}
	if err := query.Order("host ASC").Find(&records).Error; err != nil {
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
func (r *GormRepository) GitCredentialForURL(ctx context.Context, tenantID, userID, repositoryURL string) (domain.GitCredential, error) {
	parsed, err := url.Parse(repositoryURL)
	if err != nil || parsed.Hostname() == "" {
		return domain.GitCredential{}, ErrGitCredentialNotFound
	}
	var record GitCredentialRecord
	err = r.db.WithContext(ctx).
		Where("tenant_id = ? AND host = ? AND ((scope = ? AND owner_user_id = ?) OR (scope = ? AND owner_user_id = ''))", tenantID, domain.NormalizeGitHost(parsed.Hostname()), domain.GitCredentialScopePersonal, userID, domain.GitCredentialScopeTeam).
		Order("CASE WHEN scope = 'personal' THEN 0 ELSE 1 END").
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.GitCredential{}, ErrGitCredentialNotFound
	}
	if err != nil {
		return domain.GitCredential{}, fmt.Errorf("find git credential: %w", err)
	}
	return toGitCredential(record), nil
}

func (r *GormRepository) GetGitCredential(ctx context.Context, tenantID, id string) (domain.GitCredential, error) {
	var record GitCredentialRecord
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.GitCredential{}, ErrGitCredentialNotFound
	}
	if err != nil {
		return domain.GitCredential{}, fmt.Errorf("find git credential: %w", err)
	}
	return toGitCredential(record), nil
}

func (r *GormRepository) DeleteGitCredential(ctx context.Context, tenantID, id string) (domain.GitCredential, error) {
	record, err := r.GetGitCredential(ctx, tenantID, id)
	if err != nil {
		return domain.GitCredential{}, err
	}
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).Delete(&GitCredentialRecord{}).Error; err != nil {
		return domain.GitCredential{}, fmt.Errorf("delete git credential: %w", err)
	}
	// The caller removes the Kubernetes Secret; returning its name keeps that
	// cleanup from needing a second lookup.
	return record, nil
}

// GitCredentialSecretFor returns the Secret name covering a repository URL, or
// an empty string when the repository is public for this tenant.
func (r *GormRepository) GitCredentialSecretFor(ctx context.Context, tenantID, userID, repositoryURL string) string {
	credential, err := r.GitCredentialForURL(ctx, tenantID, userID, repositoryURL)
	if err != nil {
		return ""
	}
	return credential.SecretName
}
