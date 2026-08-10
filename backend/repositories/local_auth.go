package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

var (
	ErrLocalUserNotFound    = errors.New("local user not found")
	ErrLocalSessionNotFound = errors.New("local session not found")
	ErrUsernameTaken        = errors.New("username is already taken")
)

const sessionLastUsedUpdateInterval = 5 * time.Minute

type LocalUserRecord struct {
	ID           string `gorm:"primaryKey"`
	Username     string `gorm:"column:username;uniqueIndex"`
	Email        string `gorm:"column:email"`
	TenantID     string `gorm:"column:tenant_id;index"`
	RolesJSON    string `gorm:"column:roles;type:jsonb"`
	PasswordHash string `gorm:"column:password_hash"`
	Disabled     bool   `gorm:"column:disabled"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
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
		ID: record.ID, Username: record.Username, Email: record.Email, TenantID: record.TenantID,
		Roles: roles, Disabled: record.Disabled, PasswordHash: record.PasswordHash,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}, nil
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
	record := LocalUserRecord{
		ID: user.ID, Username: username, Email: user.Email, TenantID: user.TenantID,
		RolesJSON: string(rolesJSON), PasswordHash: user.PasswordHash, Disabled: user.Disabled,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := r.db.WithContext(ctx).Create(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "UNIQUE constraint") {
			return ErrUsernameTaken
		}
		return fmt.Errorf("create local user: %w", err)
	}
	return nil
}

func (r *GormRepository) FindLocalUserByUsername(ctx context.Context, username string) (domain.LocalUser, error) {
	var record LocalUserRecord
	err := r.db.WithContext(ctx).Where("username = ?", domain.NormalizeUsername(username)).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.LocalUser{}, ErrLocalUserNotFound
	}
	if err != nil {
		return domain.LocalUser{}, fmt.Errorf("find local user: %w", err)
	}
	return r.toLocalUser(record)
}

func (r *GormRepository) ListLocalUsers(ctx context.Context) ([]domain.LocalUser, error) {
	var records []LocalUserRecord
	if err := r.db.WithContext(ctx).Order("username ASC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list local users: %w", err)
	}
	users := make([]domain.LocalUser, 0, len(records))
	for _, record := range records {
		user, err := r.toLocalUser(record)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, nil
}

func (r *GormRepository) CountLocalUsers(ctx context.Context) (int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).Model(&LocalUserRecord{}).Count(&total).Error; err != nil {
		return 0, fmt.Errorf("count local users: %w", err)
	}
	return total, nil
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

func (r *GormRepository) CreateLocalSession(ctx context.Context, session domain.LocalSession, digest string) error {
	record := LocalSessionRecord{
		ID: session.ID, PublicID: session.PublicID, UserID: session.UserID, TenantID: session.TenantID,
		TokenDigest: digest, ExpiresAt: session.ExpiresAt, CreatedAt: session.CreatedAt,
	}
	if err := r.db.WithContext(ctx).Create(&record).Error; err != nil {
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
	if err := r.db.WithContext(ctx).Where("id = ?", record.UserID).First(&userRecord).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return auth.LocalSessionRecord{}, auth.ErrLocalSessionNotFound
		}
		return auth.LocalSessionRecord{}, fmt.Errorf("find local session user: %w", err)
	}
	user, err := r.toLocalUser(userRecord)
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
