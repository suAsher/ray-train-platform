package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
)

type DataSpaceUploadRepository interface {
	FindActiveDataSpaceUpload(context.Context, string, string, domain.DataSpaceID, string) (domain.DataSpaceUploadSession, []domain.DataSpaceUploadPart, error)
	CreateOrResumeDataSpaceUpload(context.Context, domain.DataSpaceUploadSession) (domain.DataSpaceUploadSession, []domain.DataSpaceUploadPart, bool, error)
	GetDataSpaceUpload(context.Context, string, string, string) (domain.DataSpaceUploadSession, []domain.DataSpaceUploadPart, error)
	RecordDataSpaceUploadPart(context.Context, domain.DataSpaceUploadSession, domain.DataSpaceUploadPart, time.Time) error
	StartDataSpaceUploadCompletion(context.Context, string, string, string) (domain.DataSpaceUploadSession, []domain.DataSpaceUploadPart, error)
	FinishDataSpaceUploadCompletion(context.Context, string, bool) error
	StartDataSpaceUploadAbort(context.Context, string, string, string) (domain.DataSpaceUploadSession, error)
	FinishDataSpaceUploadAbort(context.Context, string, bool) error
	ClaimExpiredDataSpaceUploads(context.Context, time.Time, int) ([]domain.DataSpaceUploadSession, error)
}

type completedDataSpacePart struct {
	PartNumber int    `json:"partNumber"`
	SizeBytes  int64  `json:"sizeBytes"`
	SHA256     string `json:"sha256"`
}

type dataSpaceMultipartTicket struct {
	Mode           domain.DataSpaceUploadMode `json:"mode"`
	SessionID      string                     `json:"sessionId"`
	ContentType    string                     `json:"contentType"`
	SizeBytes      int64                      `json:"sizeBytes"`
	PartSizeBytes  int64                      `json:"partSizeBytes"`
	TotalParts     int                        `json:"totalParts"`
	CompletedParts []completedDataSpacePart   `json:"completedParts"`
	ExpiresAt      time.Time                  `json:"expiresAt"`
}

func multipartTicket(session domain.DataSpaceUploadSession, parts []domain.DataSpaceUploadPart) dataSpaceMultipartTicket {
	completed := make([]completedDataSpacePart, 0, len(parts))
	for _, part := range parts {
		completed = append(completed, completedDataSpacePart{PartNumber: part.PartNumber, SizeBytes: part.SizeBytes, SHA256: part.SHA256})
	}
	return dataSpaceMultipartTicket{Mode: domain.DataSpaceUploadMultipart, SessionID: session.ID, ContentType: session.ContentType, SizeBytes: session.SizeBytes, PartSizeBytes: session.PartSizeBytes, TotalParts: session.TotalParts, CompletedParts: completed, ExpiresAt: session.ExpiresAt}
}

func (h *Handler) createDataSpaceMultipart(c *gin.Context, space domain.DataSpace, relativePath, contentType string, plan domain.DataSpaceUploadPlan) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.dataSpaceUploads == nil || h.dataMultipartStore == nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_MULTIPART_UNAVAILABLE", "resumable uploads are not configured")
		return
	}
	existing, parts, err := h.dataSpaceUploads.FindActiveDataSpaceUpload(c.Request.Context(), principal.TenantID, principal.Subject, space.ID, relativePath)
	if err == nil {
		if existing.SizeBytes != plan.SizeBytes || existing.PartSizeBytes != plan.PartSizeBytes || existing.TotalParts != plan.TotalParts || existing.ContentType != contentType {
			h.writeError(c, http.StatusConflict, "DATA_SPACE_UPLOAD_CONFLICT", "abort the existing upload for this path before uploading a different file")
			return
		}
		h.writeSuccess(c, http.StatusOK, multipartTicket(existing, parts))
		return
	}
	if !errors.Is(err, domain.ErrDataSpaceUploadNotFound) {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_UPLOAD_STATE_UNAVAILABLE", "could not load resumable upload state")
		return
	}
	providerID, err := h.dataMultipartStore.CreateDataMultipart(c.Request.Context(), space.RootPrefix, relativePath, contentType)
	if err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_STORE_UNAVAILABLE", "could not initialize resumable storage")
		return
	}
	id, err := h.newID()
	if err != nil {
		_ = h.dataMultipartStore.AbortDataMultipart(context.Background(), space.RootPrefix, relativePath, providerID)
		h.writeError(c, http.StatusInternalServerError, "DATA_SPACE_UPLOAD_ID_FAILED", "could not initialize upload")
		return
	}
	now := time.Now().UTC()
	candidate := domain.DataSpaceUploadSession{ID: "upload-" + strings.TrimPrefix(id, "job-"), TenantID: principal.TenantID, UserID: principal.Subject, SpaceID: space.ID, RootPrefix: space.RootPrefix, RelativePath: relativePath, ContentType: contentType, SizeBytes: plan.SizeBytes, PartSizeBytes: plan.PartSizeBytes, TotalParts: plan.TotalParts, ProviderID: providerID, State: domain.DataSpaceUploadActive, ExpiresAt: now.Add(domain.DataSpaceUploadSessionTTL), CreatedAt: now, UpdatedAt: now}
	session, parts, created, err := h.dataSpaceUploads.CreateOrResumeDataSpaceUpload(c.Request.Context(), candidate)
	if err != nil {
		_ = h.dataMultipartStore.AbortDataMultipart(context.Background(), space.RootPrefix, relativePath, providerID)
		status, code, message := http.StatusServiceUnavailable, "DATA_SPACE_UPLOAD_STATE_UNAVAILABLE", "could not persist resumable upload state"
		if errors.Is(err, domain.ErrDataSpaceUploadConflict) {
			status, code, message = http.StatusConflict, "DATA_SPACE_UPLOAD_CONFLICT", "another upload is already using this path"
		}
		h.writeError(c, status, code, message)
		return
	}
	if !created {
		_ = h.dataMultipartStore.AbortDataMultipart(context.Background(), space.RootPrefix, relativePath, providerID)
	}
	h.writeSuccess(c, http.StatusCreated, multipartTicket(session, parts))
}

func (h *Handler) ownedMultipart(c *gin.Context) (domain.DataSpaceUploadSession, []domain.DataSpaceUploadPart, bool) {
	principal, space, ok := h.authorizeWritableDataSpace(c)
	if !ok {
		return domain.DataSpaceUploadSession{}, nil, false
	}
	if h.dataSpaceUploads == nil || h.dataMultipartStore == nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_MULTIPART_UNAVAILABLE", "resumable uploads are not configured")
		return domain.DataSpaceUploadSession{}, nil, false
	}
	session, parts, err := h.dataSpaceUploads.GetDataSpaceUpload(c.Request.Context(), c.Param("session"), principal.TenantID, principal.Subject)
	if err != nil || session.SpaceID != space.ID || session.RootPrefix != space.RootPrefix {
		h.writeError(c, http.StatusNotFound, "DATA_SPACE_UPLOAD_NOT_FOUND", "upload session was not found")
		return domain.DataSpaceUploadSession{}, nil, false
	}
	return session, parts, true
}

type countingReader struct {
	reader io.Reader
	read   int64
}

func (reader *countingReader) Read(buffer []byte) (int, error) {
	n, err := reader.reader.Read(buffer)
	reader.read += int64(n)
	return n, err
}

func (h *Handler) uploadDataSpacePart(c *gin.Context) {
	session, completed, ok := h.ownedMultipart(c)
	if !ok {
		return
	}
	if session.State != domain.DataSpaceUploadActive {
		h.writeError(c, http.StatusConflict, "DATA_SPACE_UPLOAD_NOT_ACTIVE", "upload session is not active")
		return
	}
	partNumber, err := strconv.Atoi(c.Param("partNumber"))
	expected, sizeErr := session.Plan().ExpectedPartSize(partNumber)
	digest := strings.ToLower(strings.TrimSpace(c.GetHeader("X-Part-SHA256")))
	if err != nil || sizeErr != nil || len(digest) != 64 {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATA_SPACE_UPLOAD_PART", "part number or SHA-256 is invalid")
		return
	}
	if _, err := hex.DecodeString(digest); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_DATA_SPACE_UPLOAD_PART", "part SHA-256 is invalid")
		return
	}
	if c.Request.ContentLength != expected {
		h.writeError(c, http.StatusBadRequest, "DATA_SPACE_UPLOAD_PART_SIZE_MISMATCH", fmt.Sprintf("part %d must contain exactly %d bytes", partNumber, expected))
		return
	}
	for _, part := range completed {
		if part.PartNumber == partNumber && part.SizeBytes == expected && part.SHA256 == digest {
			h.writeSuccess(c, http.StatusOK, completedDataSpacePart{PartNumber: partNumber, SizeBytes: expected, SHA256: digest})
			return
		}
	}
	hasher := sha256.New()
	limited := http.MaxBytesReader(c.Writer, c.Request.Body, expected)
	defer limited.Close()
	counter := &countingReader{reader: io.TeeReader(limited, hasher)}
	etag, err := h.dataMultipartStore.UploadDataPart(c.Request.Context(), session.RootPrefix, session.RelativePath, session.ProviderID, partNumber, expected, counter)
	if err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_UPLOAD_PART_FAILED", "could not store this part; retry it")
		return
	}
	actualDigest := hex.EncodeToString(hasher.Sum(nil))
	if counter.read != expected || actualDigest != digest {
		h.writeError(c, http.StatusUnprocessableEntity, "DATA_SPACE_UPLOAD_PART_DIGEST_MISMATCH", "part integrity check failed; retry it")
		return
	}
	now := time.Now().UTC()
	part := domain.DataSpaceUploadPart{SessionID: session.ID, PartNumber: partNumber, SizeBytes: expected, SHA256: digest, ETag: etag, CreatedAt: now, UpdatedAt: now}
	if err := h.dataSpaceUploads.RecordDataSpaceUploadPart(c.Request.Context(), session, part, now.Add(domain.DataSpaceUploadSessionTTL)); err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_UPLOAD_STATE_UNAVAILABLE", "part was stored but its receipt could not be recorded; retry it")
		return
	}
	h.writeSuccess(c, http.StatusOK, completedDataSpacePart{PartNumber: partNumber, SizeBytes: expected, SHA256: digest})
}

func (h *Handler) completeDataSpaceMultipart(c *gin.Context) {
	session, _, ok := h.ownedMultipart(c)
	if !ok {
		return
	}
	if session.State == domain.DataSpaceUploadCompleted {
		h.writeSuccess(c, http.StatusOK, map[string]string{"path": session.RelativePath})
		return
	}
	started, parts, err := h.dataSpaceUploads.StartDataSpaceUploadCompletion(c.Request.Context(), session.ID, session.TenantID, session.UserID)
	if err != nil {
		if errors.Is(err, domain.ErrDataSpaceUploadIncomplete) {
			h.writeError(c, http.StatusConflict, "DATA_SPACE_UPLOAD_INCOMPLETE", "not all parts have been uploaded")
		} else {
			h.writeError(c, http.StatusConflict, "DATA_SPACE_UPLOAD_NOT_ACTIVE", "upload cannot be completed in its current state")
		}
		return
	}
	providerParts := make([]objectstore.MultipartPart, 0, len(parts))
	for _, part := range parts {
		providerParts = append(providerParts, objectstore.MultipartPart{PartNumber: part.PartNumber, SizeBytes: part.SizeBytes, ETag: part.ETag})
	}
	if err := h.dataMultipartStore.CompleteDataMultipart(c.Request.Context(), started.RootPrefix, started.RelativePath, started.ProviderID, providerParts); err != nil {
		_ = h.dataSpaceUploads.FinishDataSpaceUploadCompletion(context.Background(), started.ID, false)
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_UPLOAD_COMPLETE_FAILED", "could not finalize this upload; retry completion")
		return
	}
	if err := h.dataSpaceUploads.FinishDataSpaceUploadCompletion(c.Request.Context(), started.ID, true); err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_UPLOAD_STATE_UNAVAILABLE", "the object was finalized but upload state could not be updated")
		return
	}
	h.writeSuccess(c, http.StatusOK, map[string]string{"path": started.RelativePath})
}

func (h *Handler) abortDataSpaceMultipart(c *gin.Context) {
	session, _, ok := h.ownedMultipart(c)
	if !ok {
		return
	}
	started, err := h.dataSpaceUploads.StartDataSpaceUploadAbort(c.Request.Context(), session.ID, session.TenantID, session.UserID)
	if err != nil {
		h.writeError(c, http.StatusConflict, "DATA_SPACE_UPLOAD_NOT_ACTIVE", "upload cannot be aborted in its current state")
		return
	}
	if started.State == domain.DataSpaceUploadCompleted || started.State == domain.DataSpaceUploadAborted {
		c.Status(http.StatusNoContent)
		return
	}
	if err := h.dataMultipartStore.AbortDataMultipart(c.Request.Context(), started.RootPrefix, started.RelativePath, started.ProviderID); err != nil {
		_ = h.dataSpaceUploads.FinishDataSpaceUploadAbort(context.Background(), started.ID, false)
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_UPLOAD_ABORT_FAILED", "could not abort this upload; retry")
		return
	}
	if err := h.dataSpaceUploads.FinishDataSpaceUploadAbort(c.Request.Context(), started.ID, true); err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_UPLOAD_STATE_UNAVAILABLE", "upload was aborted but its state could not be updated")
		return
	}
	c.Status(http.StatusNoContent)
}
