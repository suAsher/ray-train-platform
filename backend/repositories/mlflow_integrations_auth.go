package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/integrations"
	"time"
)

func (r *GormRepository) findIntegrationPAT(ctx context.Context, publicID string) (auth.PATRecord, error) {
	tx := r.db.WithContext(ctx)
	// During schema-46 rolling upgrades a miss remains a normal PAT miss.
	if !tx.Migrator().HasTable(&MLflowIntegrationTokenRecord{}) {
		return auth.PATRecord{}, auth.ErrPATNotFound
	}
	var token MLflowIntegrationTokenRecord
	if err := tx.Where("public_id = ? AND revoked_at IS NULL AND expires_at > ?", publicID, time.Now().UTC()).First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return auth.PATRecord{}, auth.ErrPATNotFound
		}
		return auth.PATRecord{}, err
	}
	var identity MLflowIntegrationRecord
	if err := tx.Where("id = ? AND revoked_at IS NULL", token.IntegrationID).First(&identity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return auth.PATRecord{}, auth.ErrPATNotFound
		}
		return auth.PATRecord{}, err
	}
	if err := integrationOwner(tx, identity.TenantID, identity.OwnerUserID, false); err != nil {
		if errors.Is(err, integrations.ErrNotFound) {
			return auth.PATRecord{}, auth.ErrPATNotFound
		}
		return auth.PATRecord{}, err
	}
	var scopes []string
	if err := json.Unmarshal([]byte(token.ScopesJSON), &scopes); err != nil {
		return auth.PATRecord{}, err
	}
	scopes, err := integrations.NormalizeScopes(scopes)
	if err != nil {
		return auth.PATRecord{}, auth.ErrPATNotFound
	}
	return auth.PATRecord{PublicID: token.PublicID, Digest: token.TokenDigest, Principal: auth.Principal{Subject: "integration:" + identity.ID, Username: identity.Name, TenantID: identity.TenantID, IntegrationID: identity.ID}, Scopes: scopes, ExpiresAt: token.ExpiresAt, RevokedAt: token.RevokedAt, LastUsedAt: token.LastUsedAt}, nil
}
func (r *GormRepository) touchIntegrationPAT(ctx context.Context, publicID string, usedAt time.Time) error {
	tx := r.db.WithContext(ctx)
	if !tx.Migrator().HasTable(&MLflowIntegrationTokenRecord{}) {
		return nil
	}
	return tx.Model(&MLflowIntegrationTokenRecord{}).Where("public_id = ? AND revoked_at IS NULL AND (last_used_at IS NULL OR last_used_at <= ?)", publicID, usedAt.Add(-patLastUsedUpdateInterval)).Update("last_used_at", usedAt).Error
}
