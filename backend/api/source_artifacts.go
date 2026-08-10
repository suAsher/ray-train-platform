package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/httpapi"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/repositories"
)

const SourceArtifactUploadTTL = 15 * time.Minute

type SourceArtifactRepository interface {
	EnsureIdentity(context.Context, auth.Principal) error
	CreateOrReuseSourceArtifact(context.Context, *domain.SourceArtifact) (*domain.SourceArtifact, error)
	CreateOrReuseSourceArtifactWithLimits(context.Context, *domain.SourceArtifact, repositories.SourceArtifactLimits) (*domain.SourceArtifact, error)
	GetSourceArtifact(context.Context, string, string, string) (*domain.SourceArtifact, error)
	ReopenSourceArtifactUploadWithLimits(context.Context, string, string, string, time.Time, repositories.SourceArtifactLimits) (*domain.SourceArtifact, error)
	MarkSourceArtifactReady(context.Context, string, string, string, time.Time) (*domain.SourceArtifact, error)
}

type SourceArtifactOptions struct {
	AllowDemo           bool
	Limiter             SourceArtifactLimiter
	MaxPendingArtifacts int
	QuotaBytes          int64
	Now                 func() time.Time
	NewID               func() (string, error)
}

type SourceArtifactHandler struct {
	repository SourceArtifactRepository
	store      objectstore.Store
	allowDemo  bool
	limiter    SourceArtifactLimiter
	limits     repositories.SourceArtifactLimits
	now        func() time.Time
	newID      func() (string, error)
}

type createSourceArtifactRequest struct {
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
}

type sourceArtifactResponse struct {
	ArtifactID      string                     `json:"artifactId"`
	State           domain.SourceArtifactState `json:"state"`
	SHA256          string                     `json:"sha256"`
	SizeBytes       int64                      `json:"sizeBytes"`
	UploadURL       string                     `json:"uploadUrl,omitempty"`
	RequiredHeaders map[string]string          `json:"requiredHeaders,omitempty"`
	ContentLength   int64                      `json:"contentLength,omitempty"`
	ExpiresAt       *time.Time                 `json:"expiresAt,omitempty"`
	UploadRequired  bool                       `json:"uploadRequired"`
	CompletedAt     *time.Time                 `json:"completedAt,omitempty"`
}

func NewSourceArtifactHandler(repository SourceArtifactRepository, store objectstore.Store, options SourceArtifactOptions) (*SourceArtifactHandler, error) {
	if repository == nil || store == nil {
		return nil, fmt.Errorf("source artifact repository and object store are required")
	}
	if options.Limiter == nil {
		options.Limiter = newDefaultSourceArtifactLimiter()
	}
	if options.MaxPendingArtifacts <= 0 {
		options.MaxPendingArtifacts = repositories.DefaultSourceArtifactMaxPending
	}
	if options.QuotaBytes <= 0 {
		options.QuotaBytes = repositories.DefaultSourceArtifactQuotaBytes
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewID == nil {
		options.NewID = newSourceArtifactID
	}
	return &SourceArtifactHandler{
		repository: repository, store: store, allowDemo: options.AllowDemo,
		limiter: options.Limiter, limits: repositories.SourceArtifactLimits{MaxPending: options.MaxPendingArtifacts, QuotaBytes: options.QuotaBytes},
		now: options.Now, newID: options.NewID,
	}, nil
}

func (handler *SourceArtifactHandler) RegisterRoutes(group *gin.RouterGroup) {
	write := group.Group("")
	write.Use(auth.RequireScopes(domain.PATScopeSourcesWrite))
	write.POST("/source-artifacts", handler.create)
	write.POST("/source-artifacts/:id/complete", handler.complete)
}

func (handler *SourceArtifactHandler) create(c *gin.Context) {
	principal, ok := handler.engineerPrincipal(c)
	if !ok {
		return
	}
	if !handler.allowSourceArtifactAction(c, principal, sourceArtifactActionCreate) {
		return
	}
	var request createSourceArtifactRequest
	if !handler.bindCreateSourceArtifactRequest(c, &request) {
		return
	}
	if err := handler.repository.EnsureIdentity(c.Request.Context(), principal); err != nil {
		handler.writeError(c, http.StatusInternalServerError, "IDENTITY_PERSIST_FAILED", "could not persist authenticated identity")
		return
	}
	id, err := handler.newID()
	if err != nil {
		handler.writeError(c, http.StatusInternalServerError, "ID_GENERATION_FAILED", "could not allocate source artifact id")
		return
	}
	now := handler.now().UTC()
	artifact, err := domain.NewSourceArtifact(domain.SourceArtifactInput{
		ID: id, TenantID: principal.TenantID, UserID: principal.Subject,
		SHA256: request.SHA256, SizeBytes: request.SizeBytes,
	}, now.Add(SourceArtifactUploadTTL), now)
	if err != nil {
		handler.writeError(c, http.StatusBadRequest, "INVALID_SOURCE_ARTIFACT", "sha256, sizeBytes, or authenticated identity is invalid")
		return
	}
	stored, err := handler.repository.CreateOrReuseSourceArtifactWithLimits(c.Request.Context(), &artifact, handler.limits)
	if errors.Is(err, repositories.ErrSourceArtifactQuotaExceeded) {
		handler.writeError(c, http.StatusTooManyRequests, "SOURCE_ARTIFACT_QUOTA_EXCEEDED", "source artifact owner quota exceeded")
		return
	}
	if errors.Is(err, repositories.ErrSourceArtifactConflict) {
		handler.writeError(c, http.StatusConflict, "SOURCE_ARTIFACT_CONFLICT", "the declared artifact conflicts with an existing artifact")
		return
	}
	if err != nil {
		handler.writeError(c, http.StatusInternalServerError, "SOURCE_ARTIFACT_CREATE_FAILED", "could not create source artifact")
		return
	}
	if stored.State == domain.SourceArtifactReady {
		info, headErr := handler.store.Head(c.Request.Context(), stored.ObjectKey)
		switch {
		case errors.Is(headErr, objectstore.ErrNotFound):
			stored, err = handler.repository.ReopenSourceArtifactUploadWithLimits(c.Request.Context(), principal.TenantID, principal.Subject, stored.ID, now.Add(SourceArtifactUploadTTL), handler.limits)
			if errors.Is(err, repositories.ErrSourceArtifactNotFound) {
				handler.writeError(c, http.StatusNotFound, "SOURCE_ARTIFACT_NOT_FOUND", "source artifact was not found")
				return
			}
			if errors.Is(err, repositories.ErrSourceArtifactQuotaExceeded) {
				handler.writeError(c, http.StatusTooManyRequests, "SOURCE_ARTIFACT_QUOTA_EXCEEDED", "source artifact owner quota exceeded")
				return
			}
			if err != nil {
				handler.writeError(c, http.StatusInternalServerError, "SOURCE_ARTIFACT_REOPEN_FAILED", "could not reopen source artifact upload")
				return
			}
		case headErr != nil:
			c.Header("Retry-After", "5")
			handler.writeError(c, http.StatusServiceUnavailable, "SOURCE_STORE_UNAVAILABLE", "source storage is temporarily unavailable")
			return
		case info.SizeBytes != stored.SizeBytes || info.Metadata["sha256"] != stored.SHA256:
			handler.writeError(c, http.StatusConflict, "SOURCE_OBJECT_MISMATCH", "uploaded source object does not match its declaration")
			return
		default:
			handler.writeSuccess(c, http.StatusOK, responseForReadyArtifact(stored))
			return
		}
	}
	presigned, err := handler.store.PresignPut(c.Request.Context(), stored.ObjectKey, stored.SHA256, stored.SizeBytes, SourceArtifactUploadTTL)
	if err != nil {
		c.Header("Retry-After", "5")
		handler.writeError(c, http.StatusServiceUnavailable, "SOURCE_UPLOAD_UNAVAILABLE", "source upload service is temporarily unavailable")
		return
	}
	expiresAt := presigned.ExpiresAt.UTC()
	handler.writeSuccess(c, http.StatusCreated, sourceArtifactResponse{
		ArtifactID: stored.ID, State: stored.State, SHA256: stored.SHA256, SizeBytes: stored.SizeBytes,
		UploadURL: presigned.URL, RequiredHeaders: presigned.RequiredHeaders, ContentLength: presigned.ContentLength,
		ExpiresAt: &expiresAt, UploadRequired: true,
	})
}

func (handler *SourceArtifactHandler) complete(c *gin.Context) {
	principal, ok := handler.engineerPrincipal(c)
	if !ok {
		return
	}
	if !handler.allowSourceArtifactAction(c, principal, sourceArtifactActionComplete) {
		return
	}
	if !handler.consumeCompleteSourceArtifactBody(c) {
		return
	}
	artifact, err := handler.repository.GetSourceArtifact(c.Request.Context(), principal.TenantID, principal.Subject, c.Param("id"))
	if errors.Is(err, repositories.ErrSourceArtifactNotFound) {
		handler.writeError(c, http.StatusNotFound, "SOURCE_ARTIFACT_NOT_FOUND", "source artifact was not found")
		return
	}
	if err != nil {
		handler.writeError(c, http.StatusInternalServerError, "SOURCE_ARTIFACT_LOOKUP_FAILED", "could not load source artifact")
		return
	}
	info, err := handler.store.Head(c.Request.Context(), artifact.ObjectKey)
	if errors.Is(err, objectstore.ErrNotFound) {
		_, reopenErr := handler.repository.ReopenSourceArtifactUploadWithLimits(c.Request.Context(), principal.TenantID, principal.Subject, artifact.ID, handler.now().UTC().Add(SourceArtifactUploadTTL), handler.limits)
		if errors.Is(reopenErr, repositories.ErrSourceArtifactQuotaExceeded) {
			handler.writeError(c, http.StatusTooManyRequests, "SOURCE_ARTIFACT_QUOTA_EXCEEDED", "source artifact owner quota exceeded")
			return
		}
		if reopenErr != nil && !errors.Is(reopenErr, repositories.ErrSourceArtifactNotFound) {
			c.Header("Retry-After", "5")
			handler.writeError(c, http.StatusServiceUnavailable, "SOURCE_ARTIFACT_REOPEN_FAILED", "source artifact recovery is temporarily unavailable")
			return
		}
		handler.writeError(c, http.StatusNotFound, "SOURCE_OBJECT_NOT_FOUND", "uploaded source object was not found")
		return
	}
	if err != nil {
		c.Header("Retry-After", "5")
		handler.writeError(c, http.StatusServiceUnavailable, "SOURCE_STORE_UNAVAILABLE", "source storage is temporarily unavailable")
		return
	}
	if info.SizeBytes != artifact.SizeBytes || info.Metadata["sha256"] != artifact.SHA256 {
		handler.writeError(c, http.StatusConflict, "SOURCE_OBJECT_MISMATCH", "uploaded source object does not match its declaration")
		return
	}
	ready, err := handler.repository.MarkSourceArtifactReady(c.Request.Context(), principal.TenantID, principal.Subject, artifact.ID, handler.now().UTC())
	if errors.Is(err, repositories.ErrSourceArtifactNotFound) {
		handler.writeError(c, http.StatusNotFound, "SOURCE_ARTIFACT_NOT_FOUND", "source artifact was not found")
		return
	}
	if errors.Is(err, repositories.ErrSourceArtifactConflict) {
		handler.writeError(c, http.StatusConflict, "SOURCE_ARTIFACT_CONFLICT", "source artifact completion conflicted with another update")
		return
	}
	if err != nil {
		handler.writeError(c, http.StatusInternalServerError, "SOURCE_ARTIFACT_COMPLETE_FAILED", "could not complete source artifact")
		return
	}
	handler.writeSuccess(c, http.StatusOK, responseForReadyArtifact(ready))
}

func (handler *SourceArtifactHandler) engineerPrincipal(c *gin.Context) (auth.Principal, bool) {
	principal, ok := auth.PrincipalFromGin(c)
	if !ok && handler.allowDemo {
		principal, ok = auth.DemoPrincipal(), true
	}
	if !ok {
		handler.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return auth.Principal{}, false
	}
	if !principal.Allowed("Engineer") {
		handler.writeError(c, http.StatusForbidden, "FORBIDDEN", "engineer role is required")
		return auth.Principal{}, false
	}
	return principal, true
}

func responseForReadyArtifact(artifact *domain.SourceArtifact) sourceArtifactResponse {
	return sourceArtifactResponse{
		ArtifactID: artifact.ID, State: artifact.State, SHA256: artifact.SHA256,
		SizeBytes: artifact.SizeBytes, UploadRequired: false, CompletedAt: artifact.CompletedAt,
	}
}

func (handler *SourceArtifactHandler) writeSuccess(c *gin.Context, status int, data sourceArtifactResponse) {
	handler.disableCaching(c)
	c.JSON(status, httpapi.Success(httpapi.RequestID(c.GetHeader("X-Request-ID")), data))
}

func (handler *SourceArtifactHandler) writeError(c *gin.Context, status int, code, message string) {
	handler.disableCaching(c)
	c.JSON(status, httpapi.Failure[any](httpapi.RequestID(c.GetHeader("X-Request-ID")), code, message))
}

func (handler *SourceArtifactHandler) disableCaching(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
}

func newSourceArtifactID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("read random source artifact id: %w", err)
	}
	return "artifact-" + hex.EncodeToString(value), nil
}
