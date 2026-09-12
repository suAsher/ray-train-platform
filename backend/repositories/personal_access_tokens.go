package repositories

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

var ErrPersonalAccessTokenNotFound = errors.New("personal access token not found")

const patLastUsedUpdateInterval = 5 * time.Minute

type PersonalAccessTokenRecord struct {
	ID          string `gorm:"primaryKey"`
	PublicID    string `gorm:"column:public_id;uniqueIndex"`
	UserID      string `gorm:"column:user_id;index"`
	TenantID    string `gorm:"column:tenant_id;index"`
	TokenDigest string `gorm:"column:token_digest"`
	ScopesJSON  string `gorm:"column:scopes;type:jsonb"`
	ExpiresAt   time.Time
	LastUsedAt  *time.Time
	RevokedAt   *time.Time
	CreatedAt   time.Time
}

func (PersonalAccessTokenRecord) TableName() string { return "personal_access_tokens" }

func (r *GormRepository) CreatePersonalAccessToken(ctx context.Context, token domain.PersonalAccessToken, digest string) error {
	if _, err := hex.DecodeString(digest); err != nil || len(digest) != 64 {
		return fmt.Errorf("PAT digest must be a SHA-256 hex value")
	}
	scopes, err := domain.NormalizePATScopes(token.Scopes)
	if err != nil {
		return err
	}
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return fmt.Errorf("marshal PAT scopes: %w", err)
	}
	record := PersonalAccessTokenRecord{
		ID: token.ID, PublicID: token.PublicID, UserID: token.UserID, TenantID: token.TenantID,
		TokenDigest: digest, ScopesJSON: string(scopesJSON), ExpiresAt: token.ExpiresAt,
		LastUsedAt: token.LastUsedAt, RevokedAt: token.RevokedAt, CreatedAt: token.CreatedAt,
	}
	if err := r.withActiveIdentityTenant(ctx, token.TenantID, func(tx *gorm.DB) error { return tx.Create(&record).Error }); err != nil {
		return fmt.Errorf("create personal access token: %w", err)
	}
	return nil
}

func (r *GormRepository) ListPersonalAccessTokens(ctx context.Context, tenantID, userID string) ([]domain.PersonalAccessToken, error) {
	var records []PersonalAccessTokenRecord
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ?", tenantID, userID).Order("created_at DESC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list personal access tokens: %w", err)
	}
	items := make([]domain.PersonalAccessToken, 0, len(records))
	for _, record := range records {
		item, err := record.toDomain()
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (r *GormRepository) FindPATByPublicID(ctx context.Context, publicID string) (auth.PATRecord, error) {
	var token PersonalAccessTokenRecord
	if err := r.db.WithContext(ctx).Where("public_id = ?", publicID).First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return r.findIntegrationPAT(ctx, publicID)
		}
		return auth.PATRecord{}, fmt.Errorf("find personal access token: %w", err)
	}
	if err := requireActiveIdentityTenant(r.db.WithContext(ctx), token.TenantID, false); err != nil {
		if errors.Is(err, ErrTenantRetirementBlocked) || errors.Is(err, gorm.ErrRecordNotFound) {
			return auth.PATRecord{}, auth.ErrPATNotFound
		}
		return auth.PATRecord{}, fmt.Errorf("find personal access token tenant: %w", err)
	}
	// A backend can briefly serve against the pre-membership schema while the
	// rollout migration is still being applied. Preserve PAT authentication in
	// that window by using the legacy tenant-scoped owner record instead of
	// turning a missing table into a server error.
	if !r.db.Migrator().HasTable(&LocalUserRecord{}) {
		return r.findLegacyPATOwner(ctx, token)
	}
	var account LocalUserRecord
	if err := r.db.WithContext(ctx).Where("id = ? AND disabled = FALSE AND decommissioned_at IS NULL", token.UserID).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return r.findLegacyPATOwner(ctx, token)
		}
		return auth.PATRecord{}, fmt.Errorf("load personal access token owner: %w", err)
	}
	var scopes []string
	if err := json.Unmarshal([]byte(token.ScopesJSON), &scopes); err != nil {
		return auth.PATRecord{}, fmt.Errorf("decode personal access token scopes: %w", err)
	}
	roles, err := r.rolesForPATMembership(ctx, account, token.TenantID)
	if err != nil {
		if errors.Is(err, ErrMembershipNotFound) {
			return auth.PATRecord{}, auth.ErrPATNotFound
		}
		return auth.PATRecord{}, err
	}
	return auth.PATRecord{
		PublicID: token.PublicID, Digest: token.TokenDigest,
		Principal: auth.Principal{Subject: account.ID, Username: account.Username, Email: account.Email, TenantID: token.TenantID, StorageTenantID: account.TenantID, StorageKey: account.StorageKey, Roles: roles},
		Scopes:    scopes, ExpiresAt: token.ExpiresAt, RevokedAt: token.RevokedAt, LastUsedAt: token.LastUsedAt,
	}, nil
}

func (r *GormRepository) rolesForPATMembership(ctx context.Context, account LocalUserRecord, tenantID string) ([]string, error) {
	if !r.db.Migrator().HasTable(&TenantMembershipRecord{}) {
		if account.TenantID != tenantID {
			return nil, ErrMembershipNotFound
		}
		return membershipRoles(account.RolesJSON)
	}
	var membership TenantMembershipRecord
	if err := r.db.WithContext(ctx).Where("identity_id = ? AND tenant_id = ? AND status = ?", account.ID, tenantID, domain.MembershipStatusActive).First(&membership).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrMembershipNotFound
		}
		return nil, fmt.Errorf("load personal access token membership: %w", err)
	}
	roles, err := membershipRoles(membership.RolesJSON)
	if err != nil {
		return nil, err
	}
	var globalRoles []string
	if strings.TrimSpace(account.GlobalRolesJSON) != "" {
		if err := json.Unmarshal([]byte(account.GlobalRolesJSON), &globalRoles); err != nil {
			return nil, fmt.Errorf("decode personal access token global roles: %w", err)
		}
	}
	return mergeMembershipRoles(roles, globalRoles), nil
}

func (r *GormRepository) findLegacyPATOwner(ctx context.Context, token PersonalAccessTokenRecord) (auth.PATRecord, error) {
	var user UserRecord
	if err := r.db.WithContext(ctx).Where("id = ? AND tenant_id = ?", token.UserID, token.TenantID).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return auth.PATRecord{}, auth.ErrPATNotFound
		}
		return auth.PATRecord{}, fmt.Errorf("load legacy personal access token owner: %w", err)
	}
	var scopes []string
	if err := json.Unmarshal([]byte(token.ScopesJSON), &scopes); err != nil {
		return auth.PATRecord{}, fmt.Errorf("decode personal access token scopes: %w", err)
	}
	var roles []string
	if err := json.Unmarshal([]byte(user.RolesJSON), &roles); err != nil {
		return auth.PATRecord{}, fmt.Errorf("decode personal access token owner roles: %w", err)
	}
	return auth.PATRecord{
		PublicID: token.PublicID, Digest: token.TokenDigest,
		Principal: auth.Principal{Subject: user.ID, Username: user.Username, Email: user.Email, TenantID: token.TenantID, Roles: roles},
		Scopes:    scopes, ExpiresAt: token.ExpiresAt, RevokedAt: token.RevokedAt, LastUsedAt: token.LastUsedAt,
	}, nil
}

func (r *GormRepository) RevokePersonalAccessToken(ctx context.Context, tenantID, userID, tokenID string, revokedAt time.Time) error {
	result := r.db.WithContext(ctx).Model(&PersonalAccessTokenRecord{}).
		Where("id = ? AND tenant_id = ? AND user_id = ?", tokenID, tenantID, userID).
		Update("revoked_at", revokedAt.UTC())
	if result.Error != nil {
		return fmt.Errorf("revoke personal access token: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrPersonalAccessTokenNotFound
	}
	return nil
}

func (r *GormRepository) TouchPATLastUsed(ctx context.Context, publicID string, usedAt time.Time) error {
	usedAt = usedAt.UTC()
	result := r.db.WithContext(ctx).Model(&PersonalAccessTokenRecord{}).
		Where("public_id = ? AND (last_used_at IS NULL OR last_used_at <= ?)", publicID, usedAt.Add(-patLastUsedUpdateInterval)).
		Update("last_used_at", usedAt)
	if result.Error != nil {
		return fmt.Errorf("touch personal access token last use: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := r.db.WithContext(ctx).Model(&PersonalAccessTokenRecord{}).Where("public_id = ?", publicID).Count(&count).Error; err != nil { return err }
		if count == 0 { return r.touchIntegrationPAT(ctx, publicID, usedAt) }
	}
	return nil
}

func (r PersonalAccessTokenRecord) toDomain() (domain.PersonalAccessToken, error) {
	var scopes []string
	if err := json.Unmarshal([]byte(r.ScopesJSON), &scopes); err != nil {
		return domain.PersonalAccessToken{}, fmt.Errorf("decode personal access token scopes: %w", err)
	}
	return domain.PersonalAccessToken{
		ID: r.ID, PublicID: r.PublicID, UserID: r.UserID, TenantID: r.TenantID, Scopes: scopes,
		ExpiresAt: r.ExpiresAt, LastUsedAt: r.LastUsedAt, RevokedAt: r.RevokedAt, CreatedAt: r.CreatedAt,
	}, nil
}
