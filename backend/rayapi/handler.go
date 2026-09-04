package rayapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/httpapi"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/observability"
	"ray-train-platform-backend/repositories"
)

type Repository interface {
	api.JobRepository
	api.DataSpaceStore
	CreateOrReuseSourceArtifactWithLimits(context.Context, *domain.SourceArtifact, repositories.SourceArtifactLimits) (*domain.SourceArtifact, error)
	ReopenSourceArtifactUploadWithLimits(context.Context, string, string, string, time.Time, repositories.SourceArtifactLimits) (*domain.SourceArtifact, error)
	GetSourceArtifact(context.Context, string, string, string) (*domain.SourceArtifact, error)
	MarkSourceArtifactReady(context.Context, string, string, string, time.Time) (*domain.SourceArtifact, error)
}

type Options struct {
	Limits              repositories.SourceArtifactLimits
	SpoolDir            string
	Defaults            SubmissionDefaults
	Logs                api.LogProvider
	UploadLimiter       UploadLimiter
	UploadMaxConcurrent int
	UploadRateLimit     int
	TailPollInterval    time.Duration
	Now                 func() time.Time
	RayVersion          string
}

type Handler struct {
	repository    Repository
	store         objectstore.Store
	submission    *api.SubmissionService
	limits        repositories.SourceArtifactLimits
	spoolDir      string
	defaults      SubmissionDefaults
	logs          api.LogProvider
	uploadLimiter UploadLimiter
	uploads       chan struct{}
	tailPoll      time.Duration
	now           func() time.Time
	rayVersion    string
}

func NewHandler(repository Repository, store objectstore.Store, submission *api.SubmissionService, options Options) (*Handler, error) {
	if repository == nil || store == nil || submission == nil {
		return nil, fmt.Errorf("Ray API dependencies are required")
	}
	limits := options.Limits
	if limits.MaxPending == 0 && limits.QuotaBytes == 0 {
		limits = repositories.DefaultSourceArtifactLimits()
	}
	if limits.MaxPending < 1 || limits.QuotaBytes < 1 {
		return nil, fmt.Errorf("invalid source artifact limits")
	}
	spoolDir := strings.TrimSpace(options.SpoolDir)
	if err := validateSpoolDir(spoolDir); err != nil {
		return nil, fmt.Errorf("invalid Ray API spool directory")
	}
	maxConcurrent := options.UploadMaxConcurrent
	if maxConcurrent == 0 {
		maxConcurrent = 1
	}
	if maxConcurrent < 1 {
		return nil, fmt.Errorf("invalid Ray API upload concurrency")
	}
	rateLimit := options.UploadRateLimit
	if rateLimit == 0 {
		rateLimit = defaultUploadRateLimit
	}
	if rateLimit < 1 {
		return nil, fmt.Errorf("invalid Ray API upload rate limit")
	}
	tailPoll := options.TailPollInterval
	if tailPoll == 0 {
		tailPoll = 250 * time.Millisecond
	}
	if tailPoll < time.Millisecond {
		return nil, fmt.Errorf("invalid Ray API tail poll interval")
	}
	limiter := options.UploadLimiter
	if limiter == nil {
		limiter = newFixedWindowUploadLimiter(rateLimit, defaultUploadMaxEntries, time.Now)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	rayVersion := strings.TrimSpace(options.RayVersion)
	if rayVersion == "" {
		rayVersion = domain.RayVersionLegacy
	}
	return &Handler{repository: repository, store: store, submission: submission, limits: limits, spoolDir: spoolDir, defaults: options.Defaults, logs: options.Logs, uploadLimiter: limiter, uploads: make(chan struct{}, maxConcurrent), tailPoll: tailPoll, now: now, rayVersion: rayVersion}, nil
}

func validateSpoolDir(spoolDir string) error {
	info, err := os.Stat(spoolDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("spool directory is unavailable")
	}
	probe, err := os.CreateTemp(spoolDir, ".rayapi-spool-check-*")
	if err != nil {
		return fmt.Errorf("spool directory is not writable")
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

// RegisterRoutes registers the Ray public paths beneath a caller-provided
// prefix. Supplying a /ray group produces the documented /ray/api surface.
func (handler *Handler) RegisterRoutes(group *gin.RouterGroup) {
	group.GET("/api/version", handler.version)

	packages := group.Group("")
	packages.Use(auth.RequireScopes(domain.PATScopeSourcesWrite))
	packages.GET("/api/packages/:protocol/:package", handler.packageExists)
	packages.HEAD("/api/packages/:protocol/:package", handler.packageExists)
	packages.PUT("/api/packages/:protocol/:package", handler.putPackage)

	read := group.Group("")
	read.Use(auth.RequireScopes(domain.PATScopeJobsRead))
	read.GET("/api/jobs", handler.listJobs)
	read.GET("/api/jobs/", handler.listJobs)
	read.GET("/api/jobs/:id", handler.getJob)
	read.GET("/api/jobs/:id/logs", handler.getLogs)
	read.GET("/api/jobs/:id/logs/tail", handler.tailLogs)

	write := group.Group("")
	write.Use(auth.RequireScopes(domain.PATScopeJobsWrite))
	write.POST("/api/jobs", handler.submitJob)
	write.POST("/api/jobs/", handler.submitJob)
	write.POST("/api/jobs/:id/stop", handler.stopJob)
	write.DELETE("/api/jobs/:id", handler.deleteJob)
}

func (handler *Handler) version(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"version":      "4",
		"ray_version":  handler.rayVersion,
		"ray_commit":   "",
		"session_name": "ray-training-platform",
	})
}

func (handler *Handler) packageExists(c *gin.Context) {
	principal, ok := handler.engineer(c)
	if !ok {
		return
	}
	packageName, err := ParsePackageName(c.Param("protocol"), c.Param("package"))
	if err != nil {
		handler.writeError(c, http.StatusBadRequest)
		return
	}
	artifactID := rayPackageArtifactID(principal.TenantID, principal.Subject, packageName.Name)
	artifact, err := handler.repository.GetSourceArtifact(c.Request.Context(), principal.TenantID, principal.Subject, artifactID)
	if err != nil || artifact == nil || artifact.State != domain.SourceArtifactReady {
		handler.writeError(c, http.StatusNotFound)
		return
	}
	_, status := handler.recoverReadyArtifact(c.Request.Context(), principal, artifact)
	if status == 0 {
		status = http.StatusNotFound
	}
	if status != http.StatusOK {
		handler.writeArtifactError(c, status)
		return
	}
	c.Header(httpapi.SourceArtifactIDHeader, artifactID)
	c.Status(http.StatusOK)
}

func (handler *Handler) putPackage(c *gin.Context) {
	principal, ok := handler.engineer(c)
	if !ok {
		return
	}
	packageName, err := ParsePackageName(c.Param("protocol"), c.Param("package"))
	if err != nil {
		handler.writeError(c, http.StatusBadRequest)
		return
	}
	if c.Request.ContentLength < 1 {
		handler.writeError(c, http.StatusLengthRequired)
		return
	}
	if c.Request.ContentLength > domain.MaxSourceArtifactSize {
		handler.writeError(c, http.StatusRequestEntityTooLarge)
		return
	}
	if allowed, retryAfter := handler.uploadLimiter.Allow(principal.TenantID + "\x00" + principal.Subject); !allowed {
		handler.writeUploadBusy(c, retryAfter)
		return
	}
	select {
	case handler.uploads <- struct{}{}:
		defer func() { <-handler.uploads }()
	default:
		handler.writeUploadBusy(c, 5*time.Second)
		return
	}

	temporary, digest, sizeBytes, err := spoolUpload(handler.spoolDir, c.Request.Body, c.Request.ContentLength)
	if err != nil {
		handler.writeError(c, http.StatusBadRequest)
		return
	}
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
	}()
	if err := handler.repository.EnsureIdentity(c.Request.Context(), principal); err != nil {
		handler.writeError(c, http.StatusInternalServerError)
		return
	}
	artifactID := rayPackageArtifactID(principal.TenantID, principal.Subject, packageName.Name)
	now := handler.now().UTC()
	storageRoot, err := handler.personalSourceArtifactRoot(c.Request.Context(), principal)
	if err != nil {
		handler.writeError(c, http.StatusServiceUnavailable)
		return
	}
	artifact, err := domain.NewSourceArtifact(domain.SourceArtifactInput{
		ID: artifactID, TenantID: principal.TenantID, UserID: principal.Subject, StorageRoot: storageRoot, SHA256: digest, SizeBytes: sizeBytes,
	}, now.Add(api.SourceArtifactUploadTTL), now)
	if err != nil {
		handler.writeError(c, http.StatusBadRequest)
		return
	}
	stored, err := handler.repository.CreateOrReuseSourceArtifactWithLimits(c.Request.Context(), &artifact, handler.limits)
	if errors.Is(err, repositories.ErrSourceArtifactQuotaExceeded) {
		handler.writeError(c, http.StatusTooManyRequests)
		return
	}
	if err != nil || stored == nil {
		handler.writeError(c, http.StatusConflict)
		return
	}
	if stored.ID != artifactID {
		handler.writeError(c, http.StatusConflict)
		return
	}
	if stored.State == domain.SourceArtifactReady {
		var status int
		stored, status = handler.recoverReadyArtifact(c.Request.Context(), principal, stored)
		switch status {
		case http.StatusOK:
			c.Header(httpapi.SourceArtifactIDHeader, artifactID)
			c.Status(http.StatusOK)
			return
		case 0:
			// Object was missing and the owner-scoped artifact is pending again.
		default:
			handler.writeArtifactError(c, status)
			return
		}
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		handler.writeError(c, http.StatusInternalServerError)
		return
	}
	err = handler.store.Put(c.Request.Context(), stored.ObjectKey, stored.SHA256, stored.SizeBytes, temporary)
	if err != nil {
		if !errors.Is(err, objectstore.ErrAlreadyExists) {
			handler.writeArtifactError(c, http.StatusServiceUnavailable)
			return
		}
		status := handler.storedObjectStatus(c.Request.Context(), stored)
		if status != http.StatusOK {
			if status == http.StatusNotFound {
				status = http.StatusServiceUnavailable
			}
			handler.writeArtifactError(c, status)
			return
		}
	}
	if _, err := handler.repository.MarkSourceArtifactReady(c.Request.Context(), principal.TenantID, principal.Subject, stored.ID, handler.now().UTC()); err != nil {
		handler.writeError(c, http.StatusServiceUnavailable)
		return
	}
	c.Header(httpapi.SourceArtifactIDHeader, artifactID)
	c.Status(http.StatusOK)
}

func (handler *Handler) personalSourceArtifactRoot(ctx context.Context, principal auth.Principal) (string, error) {
	bindings, err := handler.repository.ListDataBindings(ctx, principal.TenantID, principal.Subject)
	if err != nil {
		return "", err
	}
	for _, binding := range bindings {
		if binding.Scope != domain.DataMountScopePersonal || binding.SpaceID != domain.DataSpaceWorkspace || binding.UserID != principal.Subject || binding.TenantID != principal.TenantID || binding.RootPrefix == "" {
			continue
		}
		if _, err := domain.PersonalDataSpacesForRoot(principal.TenantID, binding.RootPrefix); err != nil {
			return "", err
		}
		return binding.RootPrefix, nil
	}
	return domain.PersonalDataRootFor(principal.TenantID, api.StorageKeyForPrincipal(principal))
}

func (handler *Handler) writeUploadBusy(c *gin.Context, retryAfter time.Duration) {
	seconds := int(retryAfter.Round(time.Second).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	c.Header("Retry-After", strconv.Itoa(seconds))
	handler.writeError(c, http.StatusTooManyRequests)
}

func spoolUpload(spoolDir string, body io.Reader, contentLength int64) (*os.File, string, int64, error) {
	temporary, err := os.CreateTemp(spoolDir, "ray-package-*")
	if err != nil {
		return nil, "", 0, err
	}
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return nil, "", 0, err
	}
	hash := domainSHA256()
	written, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(body, contentLength+1))
	if err != nil || written != contentLength {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return nil, "", 0, fmt.Errorf("invalid upload stream")
	}
	return temporary, hex.EncodeToString(hash.Sum(nil)), written, nil
}

func (handler *Handler) recoverReadyArtifact(ctx context.Context, principal auth.Principal, artifact *domain.SourceArtifact) (*domain.SourceArtifact, int) {
	info, err := handler.store.Head(ctx, artifact.ObjectKey)
	switch {
	case errors.Is(err, objectstore.ErrNotFound):
		reopened, reopenErr := handler.repository.ReopenSourceArtifactUploadWithLimits(ctx, principal.TenantID, principal.Subject, artifact.ID, handler.now().UTC().Add(api.SourceArtifactUploadTTL), handler.limits)
		if errors.Is(reopenErr, repositories.ErrSourceArtifactQuotaExceeded) {
			return nil, http.StatusTooManyRequests
		}
		if reopenErr != nil || reopened == nil {
			return nil, http.StatusServiceUnavailable
		}
		return reopened, 0
	case err != nil:
		return nil, http.StatusServiceUnavailable
	case info.SizeBytes != artifact.SizeBytes || info.Metadata["sha256"] != artifact.SHA256:
		return nil, http.StatusConflict
	default:
		return artifact, http.StatusOK
	}
}

func (handler *Handler) storedObjectStatus(ctx context.Context, artifact *domain.SourceArtifact) int {
	info, err := handler.store.Head(ctx, artifact.ObjectKey)
	if errors.Is(err, objectstore.ErrNotFound) {
		return http.StatusNotFound
	}
	if err != nil {
		return http.StatusServiceUnavailable
	}
	if info.SizeBytes != artifact.SizeBytes || info.Metadata["sha256"] != artifact.SHA256 {
		return http.StatusConflict
	}
	return http.StatusOK
}

func (handler *Handler) writeArtifactError(c *gin.Context, status int) {
	if status == http.StatusServiceUnavailable {
		c.Header("Retry-After", "5")
	}
	handler.writeError(c, status)
}

func (handler *Handler) submitJob(c *gin.Context) {
	principal, ok := handler.engineer(c)
	if !ok {
		return
	}
	var request JobSubmitRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		handler.writeError(c, http.StatusBadRequest)
		return
	}
	if request.SubmissionID == "" && request.JobID == "" {
		generated, err := newSubmissionID()
		if err != nil {
			handler.writeError(c, http.StatusInternalServerError)
			return
		}
		request.SubmissionID = generated
	}
	translated, err := TranslateSubmitRequestWithDefaults(request, handler.defaults)
	if err != nil {
		logRaySubmissionFailure("translate", request.SubmissionID, err)
		handler.writeError(c, http.StatusBadRequest)
		return
	}
	artifactID := rayPackageArtifactID(principal.TenantID, principal.Subject, translated.Package.Name)
	artifact, err := handler.repository.GetSourceArtifact(c.Request.Context(), principal.TenantID, principal.Subject, artifactID)
	if err != nil || artifact == nil || artifact.State != domain.SourceArtifactReady {
		handler.writeError(c, http.StatusNotFound)
		return
	}
	artifact, status := handler.recoverReadyArtifact(c.Request.Context(), principal, artifact)
	if status == 0 {
		status = http.StatusNotFound
	}
	if status != http.StatusOK {
		handler.writeArtifactError(c, status)
		return
	}
	// Ray SDK packs its working directory into a zip. Keep that archive below
	// the caller's personal workspace root and materialize it from the
	// governed PVC, never by injecting TOS credentials into the Ray workload.
	translated.Spec.Source = domain.CodeSource{
		Type: "workspace-archive", ArtifactID: artifact.ID,
		ArtifactObjectKey: artifact.ObjectKey, ArtifactSHA256: artifact.SHA256,
	}
	job, err := handler.submission.Submit(c.Request.Context(), api.SubmissionInput{
		Principal: principal, Spec: translated.Spec, Origin: domain.SubmissionOriginRayCLI, ExternalSubmissionID: translated.ExternalSubmissionID,
	})
	if err != nil || job == nil {
		if err == nil {
			err = errors.New("submission returned no job")
		}
		logRaySubmissionFailure("submit", translated.ExternalSubmissionID, err)
		handler.writeError(c, http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, jobSubmitResponse{SubmissionID: translated.ExternalSubmissionID, JobID: job.ID})
}

// logRaySubmissionFailure keeps the Ray-compatible HTTP error deliberately
// generic while preserving an operator-visible reason. Never pass request
// bodies, metadata, entrypoints, or authorization material to this helper.
func logRaySubmissionFailure(stage, submissionID string, err error) {
	log.Printf("Ray API job submission rejected: stage=%s submission_id=%q error=%v", stage, submissionID, err)
}

func (handler *Handler) listJobs(c *gin.Context) {
	principal, ok := handler.engineer(c)
	if !ok {
		return
	}
	jobs := make([]jobDetailsResponse, 0)
	for offset := 0; ; offset += 200 {
		page, err := handler.repository.List(c.Request.Context(), domain.JobFilter{TenantID: principal.TenantID, Limit: 200, Offset: offset})
		if err != nil {
			handler.writeError(c, http.StatusServiceUnavailable)
			return
		}
		for _, job := range page.Items {
			jobs = append(jobs, jobDetails(job))
		}
		if int64(offset+len(page.Items)) >= page.Total || len(page.Items) == 0 {
			break
		}
	}
	c.JSON(http.StatusOK, jobs)
}

func (handler *Handler) getJob(c *gin.Context) {
	principal, ok := handler.engineer(c)
	if !ok {
		return
	}
	job, err := handler.resolveJob(c.Request.Context(), principal, c.Param("id"))
	if err != nil {
		handler.writeError(c, http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, jobDetails(*job))
}

func (handler *Handler) getLogs(c *gin.Context) {
	principal, ok := handler.engineer(c)
	if !ok {
		return
	}
	job, err := handler.resolveJob(c.Request.Context(), principal, c.Param("id"))
	if err != nil {
		handler.writeError(c, http.StatusNotFound)
		return
	}
	if handler.logs == nil {
		handler.writeError(c, http.StatusServiceUnavailable)
		return
	}
	lines, err := api.QueryJobLogsForLifecycle(c.Request.Context(), handler.logs, *job, 1000)
	if err != nil {
		handler.writeError(c, http.StatusServiceUnavailable)
		return
	}
	c.JSON(http.StatusOK, jobLogsResponse{Logs: formatLogLines(lines)})
}

func (handler *Handler) tailLogs(c *gin.Context) {
	principal, ok := handler.engineer(c)
	if !ok {
		return
	}
	job, err := handler.resolveJob(c.Request.Context(), principal, c.Param("id"))
	if err != nil {
		handler.writeError(c, http.StatusNotFound)
		return
	}
	if !isWebSocketUpgrade(c.Request) {
		handler.writeError(c, http.StatusBadRequest)
		return
	}
	if handler.logs == nil {
		handler.writeError(c, http.StatusServiceUnavailable)
		return
	}
	lines, err := api.QueryJobLogsForLifecycle(c.Request.Context(), handler.logs, *job, 1000)
	if err != nil {
		handler.writeError(c, http.StatusServiceUnavailable)
		return
	}
	connection, writer, err := upgradeWebSocket(c)
	if err != nil {
		handler.writeError(c, http.StatusServiceUnavailable)
		return
	}
	defer connection.Close()
	defer func() { _ = writeWebSocketClose(writer) }()
	clientClosed := make(chan struct{})
	go func() {
		watchWebSocketClient(connection)
		close(clientClosed)
	}()

	previous := ""
	for {
		current := formatLogLines(lines)
		if delta := tailLogDelta(previous, current); delta != "" {
			if err := writeWebSocketText(writer, delta); err != nil {
				return
			}
		}
		previous = current
		if tailJobTerminal(job.ObservedState) {
			return
		}

		timer := time.NewTimer(handler.tailPoll)
		select {
		case <-c.Request.Context().Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-clientClosed:
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		job, err = handler.resolveJob(c.Request.Context(), principal, c.Param("id"))
		if err != nil {
			return
		}
		lines, err = api.QueryJobLogsForLifecycle(c.Request.Context(), handler.logs, *job, 1000)
		if err != nil {
			return
		}
	}
}

func tailLogDelta(previous, current string) string {
	if strings.HasPrefix(current, previous) {
		return strings.TrimPrefix(current, previous)
	}
	return current
}

func tailJobTerminal(state domain.State) bool {
	switch state {
	case domain.StateSucceeded, domain.StateFailed, domain.StateCanceled, domain.StateTimedOut:
		return true
	default:
		return false
	}
}

func formatLogLines(lines []observability.LogLine) string {
	var logs strings.Builder
	for _, line := range lines {
		if line.Line == "" {
			continue
		}
		logs.WriteString(line.Line)
		logs.WriteString("\n")
	}
	return logs.String()
}

func isWebSocketUpgrade(request *http.Request) bool {
	return strings.EqualFold(request.Header.Get("Upgrade"), "websocket") && strings.EqualFold(request.Header.Get("Sec-WebSocket-Version"), "13") && headerHasToken(request.Header.Get("Connection"), "upgrade")
}

func headerHasToken(value, want string) bool {
	for _, token := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(token), want) {
			return true
		}
	}
	return false
}

func upgradeWebSocket(c *gin.Context) (net.Conn, *bufio.ReadWriter, error) {
	key := c.GetHeader("Sec-WebSocket-Key")
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(decoded) != 16 {
		return nil, nil, fmt.Errorf("invalid WebSocket key")
	}
	hijacker, ok := c.Writer.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("WebSocket hijacking unavailable")
	}
	connection, writer, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	if _, err := writer.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n"); err != nil {
		_ = connection.Close()
		return nil, nil, err
	}
	if err := writer.Flush(); err != nil {
		_ = connection.Close()
		return nil, nil, err
	}
	return connection, writer, nil
}

func writeWebSocketText(writer *bufio.ReadWriter, value string) error {
	payload := []byte(value)
	if err := writer.WriteByte(0x81); err != nil {
		return err
	}
	switch length := len(payload); {
	case length <= 125:
		if err := writer.WriteByte(byte(length)); err != nil {
			return err
		}
	case length <= 65535:
		if err := writer.WriteByte(126); err != nil {
			return err
		}
		if err := binary.Write(writer, binary.BigEndian, uint16(length)); err != nil {
			return err
		}
	default:
		if err := writer.WriteByte(127); err != nil {
			return err
		}
		if err := binary.Write(writer, binary.BigEndian, uint64(length)); err != nil {
			return err
		}
	}
	if _, err := writer.Write(payload); err != nil {
		return err
	}
	return writer.Flush()
}

func writeWebSocketClose(writer *bufio.ReadWriter) error {
	if err := writer.WriteByte(0x88); err != nil {
		return err
	}
	if err := writer.WriteByte(0); err != nil {
		return err
	}
	return writer.Flush()
}

func watchWebSocketClient(reader io.Reader) {
	for {
		closed, err := readWebSocketClientFrame(reader)
		if err != nil || closed {
			return
		}
	}
}

func readWebSocketClientFrame(reader io.Reader) (bool, error) {
	first, err := readWebSocketByte(reader)
	if err != nil {
		return false, err
	}
	second, err := readWebSocketByte(reader)
	if err != nil {
		return false, err
	}
	if second&0x80 == 0 {
		return false, fmt.Errorf("unmasked WebSocket client frame")
	}
	length := uint64(second & 0x7f)
	switch length {
	case 126:
		var value uint16
		if err := binary.Read(reader, binary.BigEndian, &value); err != nil {
			return false, err
		}
		length = uint64(value)
	case 127:
		if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
			return false, err
		}
	}
	var mask [4]byte
	if _, err := io.ReadFull(reader, mask[:]); err != nil {
		return false, err
	}
	if first&0x0f == 0x8 {
		return true, nil
	}
	if length > 1024*1024 {
		return false, fmt.Errorf("WebSocket client frame too large")
	}
	_, err = io.CopyN(io.Discard, reader, int64(length))
	return false, err
}

func readWebSocketByte(reader io.Reader) (byte, error) {
	var value [1]byte
	_, err := io.ReadFull(reader, value[:])
	return value[0], err
}

func (handler *Handler) stopJob(c *gin.Context) {
	principal, ok := handler.engineer(c)
	if !ok {
		return
	}
	job, err := handler.resolveJob(c.Request.Context(), principal, c.Param("id"))
	if err != nil || !canMutateJob(principal, job) || handler.repository.SetDesiredState(c.Request.Context(), principal.TenantID, job.ID, domain.DesiredCanceled) != nil {
		handler.writeError(c, http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, jobStopResponse{Stopped: true})
}

func (handler *Handler) deleteJob(c *gin.Context) {
	principal, ok := handler.engineer(c)
	if !ok {
		return
	}
	job, err := handler.resolveJob(c.Request.Context(), principal, c.Param("id"))
	if err != nil || !canMutateJob(principal, job) || handler.repository.SetDesiredState(c.Request.Context(), principal.TenantID, job.ID, domain.DesiredCanceled) != nil {
		handler.writeError(c, http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, jobDeleteResponse{Deleted: true})
}

func canMutateJob(principal auth.Principal, job *domain.TrainingJob) bool {
	return job != nil && (job.UserID == principal.Subject || principal.Allowed(domain.RoleTenantAdmin))
}

func (handler *Handler) resolveJob(ctx context.Context, principal auth.Principal, value string) (*domain.TrainingJob, error) {
	if job, err := handler.repository.Get(ctx, principal.TenantID, value); err == nil && job != nil {
		return job, nil
	}
	for offset := 0; ; offset += 200 {
		page, err := handler.repository.List(ctx, domain.JobFilter{TenantID: principal.TenantID, Limit: 200, Offset: offset})
		if err != nil {
			return nil, err
		}
		for _, job := range page.Items {
			if job.ExternalSubmissionID == value {
				copy := job
				return &copy, nil
			}
		}
		if int64(offset+len(page.Items)) >= page.Total || len(page.Items) == 0 {
			return nil, repositories.ErrSourceArtifactNotFound
		}
	}
}

func (handler *Handler) engineer(c *gin.Context) (auth.Principal, bool) {
	principal, ok := auth.PrincipalFromGin(c)
	if !ok {
		handler.writeError(c, http.StatusUnauthorized)
		return auth.Principal{}, false
	}
	if !principal.Allowed("Engineer") {
		handler.writeError(c, http.StatusForbidden)
		return auth.Principal{}, false
	}
	return principal, true
}

func (handler *Handler) writeError(c *gin.Context, status int) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.JSON(status, gin.H{"error": "request rejected"})
}

func newSubmissionID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "raysubmit_" + hex.EncodeToString(value), nil
}

func jobDetails(job domain.TrainingJob) jobDetailsResponse {
	var endTime *int64
	if job.FinishedAt != nil {
		value := job.FinishedAt.UnixMilli()
		endTime = &value
	}
	startTime := int64(0)
	if !job.CreatedAt.IsZero() {
		startTime = job.CreatedAt.UnixMilli()
	}
	entrypoint := strings.Join(append(append([]string(nil), job.Spec.Entrypoint.Command...), job.Spec.Entrypoint.Args...), " ")
	if len(job.Spec.Entrypoint.Command) == 3 && job.Spec.Entrypoint.Command[0] == "/bin/sh" && job.Spec.Entrypoint.Command[1] == "-lc" {
		entrypoint = job.Spec.Entrypoint.Command[2]
	}
	return jobDetailsResponse{Type: "SUBMISSION", SubmissionID: job.ExternalSubmissionID, Status: rayStatus(job.ObservedState), Entrypoint: entrypoint, Message: rayMessage(job.ObservedState), StartTime: startTime, EndTime: endTime, Metadata: map[string]string{}, RuntimeEnv: map[string]any{}}
}

func rayStatus(state domain.State) string {
	switch state {
	case domain.StateRunning, domain.StateRecovering:
		return "RUNNING"
	case domain.StateSucceeded:
		return "SUCCEEDED"
	case domain.StateFailed, domain.StateTimedOut:
		return "FAILED"
	case domain.StateCanceled, domain.StateCanceling, domain.StateDeleting:
		return "STOPPED"
	default:
		return "PENDING"
	}
}

func rayMessage(state domain.State) string {
	if rayStatus(state) == "RUNNING" {
		return "Job is running."
	}
	if rayStatus(state) == "SUCCEEDED" {
		return "Job finished successfully."
	}
	if rayStatus(state) == "FAILED" {
		return "Job failed."
	}
	if rayStatus(state) == "STOPPED" {
		return "Job was stopped."
	}
	return "Job is pending."
}

func domainSHA256() hash.Hash { return sha256.New() }
