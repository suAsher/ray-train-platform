package repositories

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

var (
	ErrLocalUserNotFound        = errors.New("local user not found")
	ErrLocalSessionNotFound     = errors.New("local session not found")
	ErrUsernameTaken            = errors.New("username is already taken")
	ErrLocalUserActiveWorkloads = errors.New("local user has active workloads")
	ErrTenantNotFound           = errors.New("tenant not found")
)

const sessionLastUsedUpdateInterval = 5 * time.Minute

type LocalUserRecord struct {
	ID               string `gorm:"primaryKey"`
	Username         string `gorm:"column:username;uniqueIndex"`
	StorageKey       string `gorm:"column:storage_key"`
	Email            string `gorm:"column:email"`
	TenantID         string `gorm:"column:tenant_id;index"`
	ActiveTenantID   string `gorm:"column:active_tenant_id;index"`
	RolesJSON        string `gorm:"column:roles;type:jsonb"`
	GlobalRolesJSON  string `gorm:"column:global_roles;type:jsonb"`
	PasswordHash     string `gorm:"column:password_hash"`
	IdentityProvider string `gorm:"column:identity_provider"`
	Disabled         bool   `gorm:"column:disabled"`
	DecommissionedAt *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (LocalUserRecord) TableName() string { return "local_users" }

type LocalSessionRecord struct {
	ID          string `gorm:"primaryKey"`
	PublicID    string `gorm:"column:public_id;uniqueIndex"`
	UserID      string `gorm:"column:user_id;index"`
	TenantID    string `gorm:"column:tenant_id"`
	TokenDigest string `gorm:"column:token_digest"`
	ExpiresAt   time.Time
	RevokedAt   *time.Time
	LastUsedAt  *time.Time
	CreatedAt   time.Time
}

func (LocalSessionRecord) TableName() string { return "local_sessions" }

func (r *GormRepository) toLocalUser(record LocalUserRecord) (domain.LocalUser, error) {
	var roles []string
	if record.RolesJSON != "" {
		if err := json.Unmarshal([]byte(record.RolesJSON), &roles); err != nil {
			return domain.LocalUser{}, fmt.Errorf("decode local user roles: %w", err)
		}
	}
	return domain.LocalUser{
		ID: record.ID, Username: record.Username, StorageKey: record.StorageKey, Email: record.Email, TenantID: record.TenantID,
		Roles: roles, Disabled: record.Disabled, PasswordHash: record.PasswordHash,
		IdentityProvider: normalizeIdentityProvider(record.IdentityProvider),
		CreatedAt:        record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}, nil
}

func (record LocalUserRecord) activeTenantID() string {
	if tenantID := strings.TrimSpace(record.ActiveTenantID); tenantID != "" {
		return tenantID
	}
	return record.TenantID
}

func (r *GormRepository) localUserWithActiveMembership(ctx context.Context, record LocalUserRecord) (domain.LocalUser, error) {
	user, err := r.toLocalUser(record)
	if err != nil {
		return domain.LocalUser{}, err
	}
	activeTenantID := record.activeTenantID()
	if !r.db.Migrator().HasTable(&TenantMembershipRecord{}) {
		return user, nil
	}
	var membership TenantMembershipRecord
	err = r.db.WithContext(ctx).Where("identity_id = ? AND tenant_id = ? AND status = ?", record.ID, activeTenantID, domain.MembershipStatusActive).First(&membership).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.LocalUser{}, ErrLocalUserNotFound
	}
	if err != nil {
		return domain.LocalUser{}, fmt.Errorf("load active tenant membership: %w", err)
	}
	roles, err := membershipRoles(membership.RolesJSON)
	if err != nil {
		return domain.LocalUser{}, err
	}
	var globalRoles []string
	if strings.TrimSpace(record.GlobalRolesJSON) != "" {
		if err := json.Unmarshal([]byte(record.GlobalRolesJSON), &globalRoles); err != nil {
			return domain.LocalUser{}, fmt.Errorf("decode global roles: %w", err)
		}
	}
	user.TenantID = activeTenantID
	user.Roles = mergeMembershipRoles(roles, globalRoles)
	return user, nil
}

func (r *GormRepository) CreateLocalUser(ctx context.Context, user domain.LocalUser) error {
	roles, err := domain.NormalizeRoles(user.Roles)
	if err != nil {
		return err
	}
	rolesJSON, err := json.Marshal(roles)
	if err != nil {
		return fmt.Errorf("marshal local user roles: %w", err)
	}
	username := domain.NormalizeUsername(user.Username)
	if err := domain.ValidateUsername(username); err != nil {
		return err
	}
	if user.PasswordHash == "" {
		return fmt.Errorf("local user requires a password hash")
	}
	now := time.Now().UTC()
	storageKey := strings.TrimSpace(user.StorageKey)
	if storageKey == "" {
		storageKey = username
	}
	if err := domain.ValidateUsername(storageKey); err != nil {
		return fmt.Errorf("local user storage key: %w", err)
	}
	identityProvider := normalizeIdentityProvider(user.IdentityProvider)
	globalRoles := make([]string, 0, 1)
	for _, role := range roles {
		if role == domain.RoleSuperAdmin {
			globalRoles = append(globalRoles, role)
		}
	}
	globalRolesJSON, err := json.Marshal(globalRoles)
	if err != nil {
		return fmt.Errorf("marshal global user roles: %w", err)
	}
	record := LocalUserRecord{
		ID: user.ID, Username: username, StorageKey: storageKey, Email: user.Email, TenantID: user.TenantID, ActiveTenantID: user.TenantID,
		RolesJSON: string(rolesJSON), GlobalRolesJSON: string(globalRolesJSON), PasswordHash: user.PasswordHash, IdentityProvider: identityProvider, Disabled: user.Disabled,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := r.withActiveIdentityTenant(ctx, user.TenantID, func(tx *gorm.DB) error {
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		if !tx.Migrator().HasTable(&TenantMembershipRecord{}) {
			return nil
		}
		return tx.Create(&TenantMembershipRecord{
			IdentityID: record.ID, TenantID: record.TenantID, RolesJSON: record.RolesJSON,
			Status: string(domain.MembershipStatusActive), CreatedAt: now, UpdatedAt: now,
		}).Error
	}); err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "UNIQUE constraint") {
			return ErrUsernameTaken
		}
		return fmt.Errorf("create local user: %w", err)
	}
	return nil
}

func normalizeIdentityProvider(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return domain.IdentityProviderLocal
	}
	return provider
}

func (r *GormRepository) FindLocalUserByUsername(ctx context.Context, username string) (domain.LocalUser, error) {
	var record LocalUserRecord
	err := r.db.WithContext(ctx).Where("username = ? AND decommissioned_at IS NULL", domain.NormalizeUsername(username)).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.LocalUser{}, ErrLocalUserNotFound
	}
	if err != nil {
		return domain.LocalUser{}, fmt.Errorf("find local user: %w", err)
	}
	if err := requireActiveIdentityTenant(r.db.WithContext(ctx), record.activeTenantID(), false); err != nil {
		if errors.Is(err, ErrTenantRetirementBlocked) || errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.LocalUser{}, ErrLocalUserNotFound
		}
		return domain.LocalUser{}, fmt.Errorf("find local user tenant: %w", err)
	}
	return r.localUserWithActiveMembership(ctx, record)
}

func (r *GormRepository) ResolveOAuth2ProxyAccount(ctx context.Context, username string) (domain.LocalUser, bool, error) {
	user, err := r.FindLocalUserByUsername(ctx, username)
	if errors.Is(err, ErrLocalUserNotFound) {
		return domain.LocalUser{}, false, nil
	}
	if err != nil {
		return domain.LocalUser{}, false, err
	}
	return user, true, nil
}

// ProvisionOAuth2ProxyAccount creates the least-privileged platform membership
// for a verified external identity. Existing active memberships are returned
// unchanged; in particular, roles are never taken from OAuth/Keycloak claims.
func (r *GormRepository) ProvisionOAuth2ProxyAccount(ctx context.Context, username, email, tenantID string) (domain.LocalUser, error) {
	normalizedUsername := domain.NormalizeUsername(username)
	if err := domain.ValidateUsername(normalizedUsername); err != nil {
		return domain.LocalUser{}, err
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return domain.LocalUser{}, ErrTenantNotFound
	}
	if existing, err := r.FindLocalUserByUsername(ctx, normalizedUsername); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrLocalUserNotFound) {
		return domain.LocalUser{}, err
	}
	exists, err := r.TenantExists(ctx, tenantID)
	if err != nil {
		return domain.LocalUser{}, err
	}
	if !exists {
		return domain.LocalUser{}, ErrTenantNotFound
	}
	digest := sha256.Sum256([]byte("oauth2-proxy\x00" + normalizedUsername))
	user := domain.LocalUser{
		ID: "oauth2-" + fmt.Sprintf("%x", digest[:12]), Username: normalizedUsername,
		StorageKey: normalizedUsername, Email: strings.TrimSpace(email), TenantID: tenantID,
		Roles: []string{domain.RoleEngineer}, PasswordHash: "!external-only",
		IdentityProvider: domain.IdentityProviderOAuth2Proxy,
	}
	if err := r.CreateLocalUser(ctx, user); err != nil {
		if errors.Is(err, ErrUsernameTaken) {
			if existing, lookupErr := r.FindLocalUserByUsername(ctx, normalizedUsername); lookupErr == nil {
				return existing, nil
			}
		}
		return domain.LocalUser{}, err
	}
	created, err := r.FindLocalUserByUsername(ctx, normalizedUsername)
	if err != nil {
		return domain.LocalUser{}, err
	}
	return created, nil
}

func (r *GormRepository) FindLocalUserByID(ctx context.Context, userID string) (domain.LocalUser, error) {
	var record LocalUserRecord
	err := r.db.WithContext(ctx).Where("id = ? AND decommissioned_at IS NULL", userID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.LocalUser{}, ErrLocalUserNotFound
	}
	if err != nil {
		return domain.LocalUser{}, fmt.Errorf("find local user by id: %w", err)
	}
	if err := requireActiveIdentityTenant(r.db.WithContext(ctx), record.activeTenantID(), false); err != nil {
		if errors.Is(err, ErrTenantRetirementBlocked) || errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.LocalUser{}, ErrLocalUserNotFound
		}
		return domain.LocalUser{}, fmt.Errorf("find local user tenant: %w", err)
	}
	return r.localUserWithActiveMembership(ctx, record)
}

func (r *GormRepository) ListLocalUsers(ctx context.Context) ([]domain.LocalUser, error) {
	var records []LocalUserRecord
	if err := r.db.WithContext(ctx).Where("decommissioned_at IS NULL").Order("username ASC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list local users: %w", err)
	}
	users := make([]domain.LocalUser, 0, len(records))
	for _, record := range records {
		user, err := r.localUserWithActiveMembership(ctx, record)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, nil
}

func (r *GormRepository) CountLocalUsers(ctx context.Context) (int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).Model(&LocalUserRecord{}).Where("decommissioned_at IS NULL").Count(&total).Error; err != nil {
		return 0, fmt.Errorf("count local users: %w", err)
	}
	return total, nil
}

// TenantExists is deliberately separate from identity upsert: account
// administration must not create an implicit tenant merely because a request
// named it.
func (r *GormRepository) TenantExists(ctx context.Context, tenantID string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&TenantRecord{}).Where("id = ? AND retired_at IS NULL", strings.TrimSpace(tenantID)).Count(&count).Error; err != nil {
		return false, fmt.Errorf("check tenant: %w", err)
	}
	return count > 0, nil
}

func (r *GormRepository) SetLocalUserPassword(ctx context.Context, userID, passwordHash string) error {
	if passwordHash == "" {
		return fmt.Errorf("password hash is required")
	}
	result := r.db.WithContext(ctx).Model(&LocalUserRecord{}).Where("id = ?", userID).
		Updates(map[string]any{"password_hash": passwordHash, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("update local user password: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrLocalUserNotFound
	}
	return nil
}

// SetLocalUserRoles changes an existing account's roles. Session verification
// re-reads this row on every request, so the new role is in force immediately.
func (r *GormRepository) SetLocalUserRoles(ctx context.Context, userID string, roles []string) error {
	normalized, err := domain.NormalizeRoles(roles)
	if err != nil {
		return fmt.Errorf("normalize roles: %w", err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("encode roles: %w", err)
	}
	globalRoles := make([]string, 0, 1)
	for _, role := range normalized {
		if role == domain.RoleSuperAdmin {
			globalRoles = append(globalRoles, role)
		}
	}
	globalEncoded, err := json.Marshal(globalRoles)
	if err != nil {
		return fmt.Errorf("encode global roles: %w", err)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account LocalUserRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", userID).First(&account).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLocalUserNotFound
			}
			return fmt.Errorf("load local user roles: %w", err)
		}
		result := tx.Model(&LocalUserRecord{}).Where("id = ?", userID).Updates(map[string]any{
			"roles": string(encoded), "global_roles": string(globalEncoded), "updated_at": time.Now().UTC(),
		})
		if result.Error != nil {
			return fmt.Errorf("update local user roles: %w", result.Error)
		}
		if tx.Migrator().HasTable(&TenantMembershipRecord{}) {
			result = tx.Model(&TenantMembershipRecord{}).
				Where("identity_id = ? AND tenant_id = ?", userID, account.activeTenantID()).
				Updates(map[string]any{"roles": string(encoded), "updated_at": time.Now().UTC()})
			if result.Error != nil {
				return fmt.Errorf("update active membership roles: %w", result.Error)
			}
			if result.RowsAffected == 0 {
				return ErrMembershipNotFound
			}
		}
		return nil
	})
}

func (r *GormRepository) SetLocalUserDisabled(ctx context.Context, userID string, disabled bool) error {
	result := r.db.WithContext(ctx).Model(&LocalUserRecord{}).Where("id = ?", userID).
		Updates(map[string]any{"disabled": disabled, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("update local user disabled state: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrLocalUserNotFound
	}
	return nil
}

// DecommissionLocalUser keeps user data and audit records intact while
// disabling the account. It rejects an active workspace or training job so an
// administrator cannot orphan a running GPU workload by removing its owner.
func (r *GormRepository) DecommissionLocalUser(ctx context.Context, userID string, now time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var activeJobs int64
		if err := tx.Model(&JobRecord{}).Where("user_id = ? AND observed_state NOT IN ?", userID,
			[]string{string(domain.StateSucceeded), string(domain.StateFailed), string(domain.StateCanceled), string(domain.StateTimedOut)}).Count(&activeJobs).Error; err != nil {
			return fmt.Errorf("count active training jobs: %w", err)
		}
		if activeJobs > 0 {
			return ErrLocalUserActiveWorkloads
		}
		var activeWorkspaces int64
		if err := tx.Model(&WorkspaceRecord{}).Where("user_id = ? AND observed_state NOT IN ?", userID,
			[]string{string(domain.WorkspaceStopped), string(domain.WorkspaceFailed)}).Count(&activeWorkspaces).Error; err != nil {
			return fmt.Errorf("count active workspaces: %w", err)
		}
		if activeWorkspaces > 0 {
			return ErrLocalUserActiveWorkloads
		}
		result := tx.Model(&LocalUserRecord{}).Where("id = ?", userID).Updates(map[string]any{
			"disabled": true, "decommissioned_at": now.UTC(), "updated_at": now.UTC(),
		})
		if result.Error != nil {
			return fmt.Errorf("disable local user for decommission: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return ErrLocalUserNotFound
		}
		return nil
	})
}

func (r *GormRepository) CreateLocalSession(ctx context.Context, session domain.LocalSession, digest string) error {
	record := LocalSessionRecord{
		ID: session.ID, PublicID: session.PublicID, UserID: session.UserID, TenantID: session.TenantID,
		TokenDigest: digest, ExpiresAt: session.ExpiresAt, CreatedAt: session.CreatedAt,
	}
	if err := r.withActiveIdentityTenant(ctx, session.TenantID, func(tx *gorm.DB) error { return tx.Create(&record).Error }); err != nil {
		return fmt.Errorf("create local session: %w", err)
	}
	return nil
}

// FindLocalSessionByPublicID returns the stored session together with the
// account it belongs to, so the authenticator can reject sessions whose user
// was disabled after the token was issued.
func (r *GormRepository) FindLocalSessionByPublicID(ctx context.Context, publicID string) (auth.LocalSessionRecord, error) {
	var record LocalSessionRecord
	err := r.db.WithContext(ctx).Where("public_id = ?", publicID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return auth.LocalSessionRecord{}, auth.ErrLocalSessionNotFound
	}
	if err != nil {
		return auth.LocalSessionRecord{}, fmt.Errorf("find local session: %w", err)
	}
	var userRecord LocalUserRecord
	if err := r.db.WithContext(ctx).Where("id = ? AND decommissioned_at IS NULL", record.UserID).First(&userRecord).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return auth.LocalSessionRecord{}, auth.ErrLocalSessionNotFound
		}
		return auth.LocalSessionRecord{}, fmt.Errorf("find local session user: %w", err)
	}
	if err := requireActiveIdentityTenant(r.db.WithContext(ctx), userRecord.activeTenantID(), false); err != nil {
		if errors.Is(err, ErrTenantRetirementBlocked) || errors.Is(err, gorm.ErrRecordNotFound) {
			return auth.LocalSessionRecord{}, auth.ErrLocalSessionNotFound
		}
		return auth.LocalSessionRecord{}, fmt.Errorf("find local session tenant: %w", err)
	}
	user, err := r.localUserWithActiveMembership(ctx, userRecord)
	if err != nil {
		return auth.LocalSessionRecord{}, err
	}
	return auth.LocalSessionRecord{
		PublicID: record.PublicID,
		Digest:   record.TokenDigest,
		Principal: auth.Principal{
			Subject: user.ID, Username: user.Username, Email: user.Email,
			TenantID: user.TenantID, Roles: user.Roles,
		},
		ExpiresAt:    record.ExpiresAt,
		RevokedAt:    record.RevokedAt,
		LastUsedAt:   record.LastUsedAt,
		UserDisabled: user.Disabled,
	}, nil
}

func (r *GormRepository) TouchLocalSessionLastUsed(ctx context.Context, publicID string, usedAt time.Time) error {
	result := r.db.WithContext(ctx).Model(&LocalSessionRecord{}).
		Where("public_id = ? AND (last_used_at IS NULL OR last_used_at < ?)", publicID, usedAt.Add(-sessionLastUsedUpdateInterval)).
		Update("last_used_at", usedAt)
	if result.Error != nil {
		return fmt.Errorf("update local session last-used timestamp: %w", result.Error)
	}
	return nil
}

func (r *GormRepository) RevokeLocalSession(ctx context.Context, publicID string, revokedAt time.Time) error {
	result := r.db.WithContext(ctx).Model(&LocalSessionRecord{}).
		Where("public_id = ? AND revoked_at IS NULL", publicID).Update("revoked_at", revokedAt)
	if result.Error != nil {
		return fmt.Errorf("revoke local session: %w", result.Error)
	}
	return nil
}

// RevokeAllLocalSessions invalidates every active session for a user. It is
// called after password changes and administrative account changes so a stolen
// token cannot remain valid until its natural expiry.
func (r *GormRepository) RevokeAllLocalSessions(ctx context.Context, userID string, revokedAt time.Time) error {
	result := r.db.WithContext(ctx).Model(&LocalSessionRecord{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", revokedAt)
	if result.Error != nil {
		return fmt.Errorf("revoke local user sessions: %w", result.Error)
	}
	return nil
}
