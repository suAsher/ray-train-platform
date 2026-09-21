package environmentbuild

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"ray-train-platform-backend/registryauth"
)

func randomID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}
func authorizationAAD(a Authorization) []byte {
	// JSON prevents ambiguous concatenation; every authorization belongs to one
	// owner and, after reservation, exactly one build and repository.
	b, _ := json.Marshal([]string{"raytrain/environment-publish/v1", RegistryHost, a.ID, a.TenantID, a.OwnerID, a.Username, a.BuildID, a.Target, a.ExpiresAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")})
	return b
}
func (s *Service) encrypt(a Authorization, credentials Credentials) ([]byte, error) {
	block, err := aes.NewCipher(s.config.EncryptionKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	plain, err := json.Marshal(credentials)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	return gcm.Seal(nonce, nonce, plain, authorizationAAD(a)), nil
}
func (s *Service) credentials(ctx context.Context, a Authorization) (Credentials, error) {
	if !s.now().Before(a.ExpiresAt) {
		return Credentials{}, ErrAuthorization
	}
	sealed, err := s.vault.Get(ctx, a.SecretRef)
	if err != nil {
		return Credentials{}, ErrAuthorization
	}
	return s.decryptCredential(a, sealed)
}
func (s *Service) decryptCredential(a Authorization, sealed []byte) (Credentials, error) {
	block, err := aes.NewCipher(s.config.EncryptionKey)
	if err != nil {
		return Credentials{}, ErrAuthorization
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(sealed) < gcm.NonceSize() {
		return Credentials{}, ErrAuthorization
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], authorizationAAD(a))
	if err != nil {
		return Credentials{}, ErrAuthorization
	}
	defer clear(plain)
	var c Credentials
	if json.Unmarshal(plain, &c) != nil || c.Username != a.Username || c.Secret == "" {
		return Credentials{}, ErrAuthorization
	}
	return c, nil
}
func validCredential(c Credentials) bool {
	if strings.TrimSpace(c.Username) != c.Username || c.Username == "" || len(c.Username) > 256 || c.Secret == "" || len(c.Secret) > 8192 {
		return false
	}
	for _, value := range []string{c.Username, c.Secret} {
		for _, r := range value {
			if unicode.IsControl(r) {
				return false
			}
		}
	}
	return true
}
func (s *Service) CreateAuthorization(ctx context.Context, owner Owner, credentials Credentials) (Authorization, error) {
	if !s.Enabled() {
		return Authorization{}, ErrUnavailable
	}
	if owner.UserID == "" || owner.TenantID == "" || !validCredential(credentials) {
		return Authorization{}, ErrInvalid
	}
	if err := s.registry.Authenticate(ctx, credentials); err != nil {
		return Authorization{}, registryAuthorizationError(err)
	}
	id, err := randomID("env-auth-")
	if err != nil {
		return Authorization{}, err
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	a := Authorization{ID: id, TenantID: owner.TenantID, OwnerID: owner.UserID, Username: credentials.Username, SecretRef: id, ExpiresAt: now.Add(s.config.AuthorizationTTL).Truncate(time.Microsecond), CreatedAt: now}
	sealed, err := s.encrypt(a, credentials)
	if err != nil {
		return Authorization{}, err
	}
	if err = s.putCredentialMaterial(ctx, a, sealed); err != nil {
		if errors.Is(err, ErrCredentialCapacity) {
			return Authorization{}, err
		}
		return Authorization{}, ErrUnavailable
	}
	if err = s.store.SaveEnvironmentAuthorization(ctx, a); err != nil {
		_ = s.deleteCredentialMaterial(ctx, a.SecretRef)
		return Authorization{}, err
	}
	return a, nil
}
func (s *Service) Projects(ctx context.Context, owner Owner, id string, page int) ([]Project, error) {
	a, err := s.store.EnvironmentAuthorization(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	c, err := s.credentials(ctx, a)
	if err != nil {
		return nil, err
	}
	if page < 1 || page > 1000 {
		return nil, ErrInvalid
	}
	return s.registry.Projects(ctx, c, page)
}
func (s *Service) CheckTarget(ctx context.Context, owner Owner, id, project, repository string) error {
	if !validTarget(project, repository) {
		return ErrInvalid
	}
	a, err := s.store.EnvironmentAuthorization(ctx, owner, id)
	if err != nil {
		return err
	}
	if a.Target != "" && a.Target != project+"/"+repository {
		return ErrConflict
	}
	c, err := s.credentials(ctx, a)
	if err != nil {
		return err
	}
	if err = s.registry.CheckPush(ctx, c, project+"/"+repository); err != nil {
		return registryAuthorizationError(err)
	}
	return nil
}

func registryAuthorizationError(err error)error{
	if errors.Is(err,ErrAuthorization) || errors.Is(err,registryauth.ErrCredentials) || errors.Is(err,registryauth.ErrForbidden){return ErrAuthorization}
	return ErrUnavailable
}
func (s *Service) RevokeAuthorization(ctx context.Context, owner Owner, id string) error {
	a, err := s.store.EnvironmentAuthorization(ctx, owner, id)
	if err != nil {
		return err
	}
	// Deleting metadata only after its material is gone permits cleanup retry.
	if err = s.deleteAuthorizationMaterials(ctx, a.ID); err != nil {
		return ErrUnavailable
	}
	return s.store.DeleteEnvironmentAuthorization(ctx, id)
}
func (s *Service) bindAuthorization(ctx context.Context, owner Owner, id string, b Build) (Authorization, error) {
	a, err := s.store.EnvironmentAuthorization(ctx, owner, id)
	if err != nil {
		return Authorization{}, err
	}
	if a.BuildID != "" && a.BuildID != b.ID {
		return Authorization{}, ErrConflict
	}
	if a.ExpiresAt.Sub(s.now()) < time.Minute {
		return Authorization{}, ErrAuthorization
	}
	c, err := s.credentials(ctx, a)
	if err != nil {
		return Authorization{}, err
	}
	if err = s.registry.CheckPush(ctx, c, b.Project+"/"+b.Repository); err != nil {
		return Authorization{}, ErrAuthorization
	}
	// Use a separate material reference so a DB failure never invalidates the
	// existing unbound authorization. Store CAS prevents cross-build reuse.
	bound := a
	bound.BuildID = b.ID
	bound.Target = b.Project + "/" + b.Repository
	bound.SecretRef = a.ID + "-" + b.ID
	sealed, err := s.encrypt(bound, c)
	if err != nil {
		return Authorization{}, err
	}
	if err = s.putCredentialMaterial(ctx, bound, sealed); err != nil {
		if errors.Is(err, ErrCredentialCapacity) {
			return Authorization{}, err
		}
		return Authorization{}, ErrUnavailable
	}
	if err = s.store.SaveEnvironmentAuthorization(ctx, bound); err != nil {
		return Authorization{}, err
	}
	if a.SecretRef != bound.SecretRef {
		_ = s.deleteCredentialMaterial(ctx, a.SecretRef)
	}
	return bound, nil
}
func (s *Service) cleanupAuthorization(ctx context.Context, b Build) error {
	if b.AuthID == "" {
		return nil
	}
	err := s.RevokeAuthorization(ctx, Owner{TenantID: b.TenantID, UserID: b.OwnerID}, b.AuthID)
	if err == ErrNotFound {
		return s.deleteAuthorizationMaterials(ctx, b.AuthID)
	}
	return err
}
func (s *Service) putCredentialMaterial(ctx context.Context, a Authorization, sealed []byte) error {
	material := CredentialMaterial{Ref: a.SecretRef, AuthorizationID: a.ID, TenantID: a.TenantID, OwnerID: a.OwnerID, ExpiresAt: a.ExpiresAt, CreatedAt: s.now()}
	if err := s.store.ReserveEnvironmentCredentialMaterial(ctx, material); err != nil {
		return err
	}
	if err := s.vault.Put(ctx, a.SecretRef, sealed, a.ExpiresAt); err != nil {
		// A previous process may have written this immutable material before its
		// metadata transaction completed. Reuse only an authenticated identical
		// payload, not byte equality (AES-GCM deliberately uses a fresh nonce).
		wanted, decodeErr := s.decryptCredential(a, sealed)
		existing, readErr := s.credentials(ctx, a)
		if decodeErr == nil && readErr == nil && wanted.Username == existing.Username && wanted.Secret == existing.Secret {
			return nil
		}
		return ErrUnavailable
	}
	return nil
}
func (s *Service) deleteCredentialMaterial(ctx context.Context, ref string) error {
	if err := s.vault.Delete(ctx, ref); err != nil {
		return ErrUnavailable
	}
	return s.store.DeleteEnvironmentCredentialMaterial(ctx, ref)
}
func (s *Service) deleteAuthorizationMaterials(ctx context.Context, id string) error {
	materials, err := s.store.EnvironmentCredentialMaterials(ctx, id)
	if err != nil {
		return err
	}
	for _, m := range materials {
		if err = s.deleteCredentialMaterial(ctx, m.Ref); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) purgeExpiredCredentials(ctx context.Context) error {
	materials, err := s.store.ExpiredEnvironmentCredentialMaterials(ctx, s.now())
	if err != nil {
		return err
	}
	for _, m := range materials {
		if err = s.deleteCredentialMaterial(ctx, m.Ref); err != nil {
			return err
		}
	}
	items, err := s.store.ExpiredEnvironmentAuthorizations(ctx, s.now())
	if err != nil {
		return err
	}
	for _, a := range items {
		if err = s.deleteAuthorizationMaterials(ctx, a.ID); err != nil {
			return fmt.Errorf("credential cleanup unavailable")
		}
		if err = s.store.DeleteEnvironmentAuthorization(ctx, a.ID); err != nil {
			return err
		}
	}
	return nil
}
