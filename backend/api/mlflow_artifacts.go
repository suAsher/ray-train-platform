package api

import (
 "context"
 "errors"
 "io"
 "mime"
 "net/http"
 "strconv"
 "time"

 "github.com/gin-gonic/gin"
 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/domain"
 "ray-train-platform-backend/mlflowtracking"
 "ray-train-platform-backend/repositories"
 "ray-train-platform-backend/trackingartifacts"
)

// Artifact storage receives only the current grant's canonical owner. The HTTP
// principal is retained for rate limits and audit, including machine identities.
type trackingArtifactService interface {
 Init(context.Context, trackingartifacts.Scope, string, trackingartifacts.InitInput) (trackingartifacts.Artifact, error)
 List(context.Context, trackingartifacts.Scope, string, int) (trackingartifacts.Page, error)
 Get(context.Context, trackingartifacts.Scope, string) (trackingartifacts.Artifact, error)
 PutPart(context.Context, trackingartifacts.Scope, string, int, string, io.Reader) (trackingartifacts.Artifact, error)
 Complete(context.Context, trackingartifacts.Scope, string) (trackingartifacts.Artifact, error)
 Cancel(context.Context, trackingartifacts.Scope, string) (trackingartifacts.Artifact, error)
 Download(context.Context, trackingartifacts.Scope, string) (trackingartifacts.Artifact, io.ReadCloser, error)
}
type trackingArtifactAuthorizer interface {
 AuthorizeArtifact(context.Context, mlflowtracking.Actor, string, bool) (mlflowtracking.Actor, mlflowtracking.Run, error)
}

func (h *Handler) registerMLflowArtifactRoutes(group *gin.RouterGroup) {
 limiter := newFixedWindowSourceArtifactLimiter(60, 120, 10000, time.Now)
 base := group.Group("/mlflow/runs/:runId/artifacts", auth.RequireScopes(domain.PATScopeExperimentsRead, domain.PATScopeArtifactsRead), h.mlflowArtifactGuard(limiter, false))
 base.GET("", h.listMLflowArtifacts)
 base.GET("/:artifactId", h.getMLflowArtifact)
 base.GET("/:artifactId/content", h.downloadMLflowArtifact)
 write := group.Group("/mlflow/runs/:runId/artifacts", auth.RequireScopes(domain.PATScopeExperimentsRead, domain.PATScopeArtifactsRead, domain.PATScopeArtifactsWrite), h.mlflowArtifactGuard(limiter, true))
 write.POST("", h.initMLflowArtifact)
 write.PUT("/:artifactId/parts/:partNumber", h.putMLflowArtifactPart)
 write.POST("/:artifactId/complete", h.completeMLflowArtifact)
 write.DELETE("/:artifactId", h.cancelMLflowArtifact)
}

func (h *Handler) mlflowArtifactGuard(limiter SourceArtifactLimiter, write bool) gin.HandlerFunc {
 return func(c *gin.Context) {
  p, ok := auth.PrincipalFromGin(c)
  if !ok { h.writeError(c, 401, "AUTH_REQUIRED", "authentication is required"); c.Abort(); return }
  if write && p.AuthType != auth.AuthTypePAT { h.writeError(c, 403, "ARTIFACT_PAT_REQUIRED", "artifact writes require a scoped access token"); c.Abort(); return }
  action := sourceArtifactActionComplete
  if write { action = sourceArtifactActionCreate }
  if allowed, _ := limiter.Allow(p.TenantID+"\x00"+p.Subject+"\x00"+p.IntegrationID, action); !allowed {
   c.Header("Retry-After", "60"); h.writeError(c, 429, "ARTIFACT_RATE_LIMITED", "artifact request rate limit exceeded"); c.Abort(); return
  }
  c.Header("Cache-Control", "no-store")
  c.Header("X-Content-Type-Options", "nosniff")
  ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Minute)
  defer cancel()
  c.Request = c.Request.WithContext(ctx)
  c.Next()
 }
}

func (h *Handler) mlflowArtifactScope(c *gin.Context, write, running bool) (trackingartifacts.Scope, bool) {
 id := c.Param("runId")
 if !trackingartifacts.ValidID(id) { h.mlflowArtifactError(c, 0, trackingartifacts.ErrInvalid); return trackingartifacts.Scope{}, false }
 authorizer, ok := h.mlflowTracking.(trackingArtifactAuthorizer)
 if !ok || h.trackingArtifacts == nil { h.mlflowArtifactError(c, 0, trackingartifacts.ErrUnavailable); return trackingartifacts.Scope{}, false }
 owner, run, err := authorizer.AuthorizeArtifact(c.Request.Context(), sdkActor(c), id, write)
 if err != nil { h.mlflowTrackingError(c, 0, err); return trackingartifacts.Scope{}, false }
 if running && run.State != "RUNNING" { h.mlflowArtifactError(c, 0, trackingartifacts.ErrConflict); return trackingartifacts.Scope{}, false }
 // A malformed or mismatched canonical result must never select storage scope.
 if run.ID != id || owner.TenantID == "" || owner.UserID == "" || owner.TenantID != actorPrincipal(c).TenantID {
  h.mlflowArtifactError(c, 0, trackingartifacts.ErrNotFound); return trackingartifacts.Scope{}, false
 }
 return trackingartifacts.Scope{TenantID: owner.TenantID, OwnerID: owner.UserID, RunID: id}, true
}

func (h *Handler) mlflowArtifactID(c *gin.Context) (string, bool) {
 id := c.Param("artifactId")
 if !trackingartifacts.ValidID(id) { h.mlflowArtifactError(c, 0, trackingartifacts.ErrInvalid); return "", false }
 return id, true
}

func (h *Handler) listMLflowArtifacts(c *gin.Context) {
 scope, ok := h.mlflowArtifactScope(c, false, false); if !ok { return }
 cursor := c.Query("cursor")
 limit := 50
 if raw := c.Query("limit"); raw != "" { parsed, err := strconv.Atoi(raw); if err != nil || parsed < 1 { h.mlflowArtifactError(c, 0, trackingartifacts.ErrInvalid); return }; limit = parsed }
 if limit > 100 { limit = 100 }
 if cursor != "" && !trackingartifacts.ValidID(cursor) { h.mlflowArtifactError(c, 0, trackingartifacts.ErrInvalid); return }
 page, err := h.trackingArtifacts.List(c.Request.Context(), scope, cursor, limit)
 if err != nil { h.mlflowArtifactError(c, 0, err); return }
 h.writeSuccess(c, 200, page)
}

func (h *Handler) getMLflowArtifact(c *gin.Context) {
 scope, ok := h.mlflowArtifactScope(c, false, false); if !ok { return }
 id, ok := h.mlflowArtifactID(c); if !ok { return }
 artifact, err := h.trackingArtifacts.Get(c.Request.Context(), scope, id)
 if err != nil { h.mlflowArtifactError(c, 0, err); return }
 h.writeSuccess(c, 200, artifact)
}

func (h *Handler) initMLflowArtifact(c *gin.Context) {
 scope, ok := h.mlflowArtifactScope(c, true, true); if !ok { return }
 key, ok := h.mlflowTrackingIdempotencyKey(c); if !ok { return }
 if len(key) < 8 { h.writeError(c, 400, "ARTIFACT_INVALID_IDEMPOTENCY_KEY", "Idempotency-Key must contain 8 to 128 ASCII characters"); return }
 key, err := integrationCreateKey(sdkActor(c), key)
 if err != nil { h.mlflowTrackingError(c, 0, err); return }
 var input trackingartifacts.InitInput
 if !h.decodeMLflowTrackingJSON(c, &input) { return }
 h.mutateMLflowArtifact(c, 201, func(ctx context.Context) (trackingartifacts.Artifact, error) { return h.trackingArtifacts.Init(ctx, scope, key, input) })
}

func (h *Handler) completeMLflowArtifact(c *gin.Context) {
 scope, ok := h.mlflowArtifactScope(c, true, true); if !ok { return }
 id, ok := h.mlflowArtifactID(c); if !ok { return }
 h.mutateMLflowArtifact(c, 200, func(ctx context.Context) (trackingartifacts.Artifact, error) { return h.trackingArtifacts.Complete(ctx, scope, id) })
}

func (h *Handler) cancelMLflowArtifact(c *gin.Context) {
 scope, ok := h.mlflowArtifactScope(c, true, false); if !ok { return }
 id, ok := h.mlflowArtifactID(c); if !ok { return }
 h.mutateMLflowArtifact(c, 200, func(ctx context.Context) (trackingartifacts.Artifact, error) { return h.trackingArtifacts.Cancel(ctx, scope, id) })
}

func (h *Handler) mutateMLflowArtifact(c *gin.Context, success int, fn func(context.Context) (trackingartifacts.Artifact, error)) {
 var artifact trackingartifacts.Artifact
 status, err := h.auditMLflowTrackingWrite(c.Request.Context(), actorPrincipal(c), c.Request.Method, c.Request.URL.Path, c.GetHeader("X-Request-ID"), repositories.MLflowAuditTrackingArtifactWrite, func(ctx context.Context) (int, error) {
  var err error
  artifact, err = fn(ctx)
  if err != nil { code, _, _ := mlflowArtifactErrorDetails(err); return code, err }
  return success, nil
 })
 if err != nil { h.mlflowArtifactError(c, status, err); return }
 h.writeSuccess(c, status, artifact)
}

// Declared length is checked while streaming, before PutPart can commit. The
// artifact service separately requires exactly the immutable expected part size.
type artifactRequestReader struct { reader io.Reader; declared, count int64; err error }
func (r *artifactRequestReader) Read(p []byte) (int, error) {
 if r.err != nil { return 0, r.err }
 n, err := r.reader.Read(p)
 r.count += int64(n)
 if r.declared >= 0 && (r.count > r.declared || (err == io.EOF && r.count != r.declared)) { r.err = trackingartifacts.ErrInvalid; return 0, r.err }
 if err != nil && err != io.EOF { r.err = err }
 return n, err
}

func (h *Handler) putMLflowArtifactPart(c *gin.Context) {
 scope, ok := h.mlflowArtifactScope(c, true, true); if !ok { return }
 id, ok := h.mlflowArtifactID(c); if !ok { return }
 index, err := strconv.Atoi(c.Param("partNumber"))
 sha := c.GetHeader("X-Content-SHA256")
 if err != nil || index < 1 || index > trackingartifacts.MaxParts || strconv.Itoa(index) != c.Param("partNumber") || !artifactSHAValid(sha) {
  h.mlflowArtifactError(c, 0, trackingartifacts.ErrInvalid); return
 }
 if c.Request.ContentLength > trackingartifacts.PartSizeBytes { h.writeError(c, 413, "ARTIFACT_BODY_TOO_LARGE", "artifact part exceeds 8 MiB"); return }
 // Read deadline is per upload, not a global server change affecting training.
 deadline := time.Now().Add(2*time.Minute)
 controller := http.NewResponseController(c.Writer)
 if err := controller.SetReadDeadline(deadline); err == nil { defer controller.SetReadDeadline(time.Time{}) }
 ctx, cancel := context.WithDeadline(c.Request.Context(), deadline)
 defer cancel()
 body := http.MaxBytesReader(c.Writer, c.Request.Body, trackingartifacts.PartSizeBytes)
 defer body.Close()
 stop := context.AfterFunc(ctx, func() { _ = body.Close() })
 defer stop()
 reader := &artifactRequestReader{reader: body, declared: c.Request.ContentLength}
 c.Request = c.Request.WithContext(ctx)
 h.mutateMLflowArtifact(c, 200, func(ctx context.Context) (trackingartifacts.Artifact, error) {
  artifact, err := h.trackingArtifacts.PutPart(ctx, scope, id, index, sha, reader)
  if reader.err != nil { return trackingartifacts.Artifact{}, reader.err }
  if ctx.Err() != nil { return trackingartifacts.Artifact{}, trackingartifacts.ErrUnavailable }
  return artifact, err
 })
}

func artifactSHAValid(value string) bool {
 if len(value) != 64 { return false }
 for _, ch := range value { if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') { return false } }
 return true
}

func (h *Handler) downloadMLflowArtifact(c *gin.Context) {
 scope, ok := h.mlflowArtifactScope(c, false, false); if !ok { return }
 id, ok := h.mlflowArtifactID(c); if !ok { return }
 if c.GetHeader("Range") != "" { h.writeError(c, 416, "ARTIFACT_RANGE_UNSUPPORTED", "artifact downloads do not support byte ranges"); return }
 if h.mlflowDashboardStore == nil { h.mlflowArtifactError(c, 0, errMLflowTrackingAuditUnavailable); return }
 started := time.Now()
 event := repositories.MLflowAuditEvent{Action: repositories.MLflowAuditTrackingArtifactDownload, Principal: actorPrincipal(c), Method: c.Request.Method, Path: c.Request.URL.Path, Status: 102, RequestID: c.GetHeader("X-Request-ID")}
 if err := h.mlflowDashboardStore.CreateMLflowAuditLog(c.Request.Context(), event); err != nil { h.mlflowArtifactError(c, 0, errMLflowTrackingAuditUnavailable); return }
 status := 503
 defer func() {
  event.Status = status; event.Duration = time.Since(started)
  ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Second); defer cancel()
  if err := h.mlflowDashboardStore.CreateMLflowAuditLog(ctx, event); err != nil { _ = c.Error(errMLflowTrackingAuditIncomplete) }
 }()
 artifact, reader, err := h.trackingArtifacts.Download(c.Request.Context(), scope, id)
 if err != nil { status, _, _ = mlflowArtifactErrorDetails(err); h.mlflowArtifactError(c, status, err); return }
 if reader == nil { h.mlflowArtifactError(c, 0, trackingartifacts.ErrUnavailable); return }
 defer reader.Close()
 disposition := mime.FormatMediaType("attachment", map[string]string{"filename": artifact.Name})
 if disposition == "" || artifact.SizeBytes < 1 || artifact.SizeBytes > trackingartifacts.MaxFileBytes { h.mlflowArtifactError(c, 0, trackingartifacts.ErrUnavailable); return }
 c.Header("Content-Type", "application/octet-stream")
 c.Header("Content-Disposition", disposition)
 c.Header("Content-Length", strconv.FormatInt(artifact.SizeBytes, 10))
 c.Header("Accept-Ranges", "none")
 c.Header("X-Content-SHA256", artifact.SHA256)
 c.Status(200)
 // Limit memory independently of file size. Do not send JSON after a partial
 // attachment; Content-Length and the checksum let clients detect truncation.
 n, copyErr := io.CopyBuffer(c.Writer, io.LimitReader(reader, artifact.SizeBytes+1), make([]byte, 64<<10))
 if copyErr != nil || n != artifact.SizeBytes { _ = c.Error(trackingartifacts.ErrUnavailable); return }
 status = 200
}

func (h *Handler) mlflowArtifactError(c *gin.Context, status int, err error) {
 actual, code, message := mlflowArtifactErrorDetails(err)
 if status == 0 { status = actual }
 h.writeError(c, status, code, message)
}
func mlflowArtifactErrorDetails(err error) (int, string, string) {
 var oversized *http.MaxBytesError
 switch {
 case errors.As(err, &oversized): return 413, "ARTIFACT_BODY_TOO_LARGE", "artifact part exceeds 8 MiB"
 case errors.Is(err, errMLflowTrackingAuditUnavailable): return 503, "ARTIFACT_AUDIT_UNAVAILABLE", "artifact audit is unavailable"
 case errors.Is(err, errMLflowTrackingAuditIncomplete): return 503, "ARTIFACT_AUDIT_INCOMPLETE", "artifact operation may have completed; read its state before retrying"
 case errors.Is(err, trackingartifacts.ErrInvalid): return 400, "ARTIFACT_INVALID_REQUEST", "artifact request is invalid"
 case errors.Is(err, trackingartifacts.ErrNotFound): return 404, "ARTIFACT_NOT_FOUND", "artifact was not found"
 case errors.Is(err, trackingartifacts.ErrConflict): return 409, "ARTIFACT_CONFLICT", "artifact or run state conflicts with this request"
 case errors.Is(err, trackingartifacts.ErrQuota): return 409, "ARTIFACT_QUOTA_EXCEEDED", "artifact storage or pending upload quota exceeded"
 default: return 503, "ARTIFACT_UNAVAILABLE", "artifact storage is unavailable"
 }
}

var _ trackingArtifactService = (*trackingartifacts.Service)(nil)
