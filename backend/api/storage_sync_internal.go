package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	ss "ray-train-platform-backend/storagesync"
)

func StorageSyncWorkerToken(key []byte, kind, id string, attempt int, generation int64) string {
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "storage-sync:%s:%s:%d:%d", kind, id, attempt, generation)
	return hex.EncodeToString(mac.Sum(nil))
}
func (h *StorageSyncHandler) RegisterInternalRoutes(group *gin.RouterGroup) {
	if h == nil || h.manager == nil || len(h.key) == 0 {
		return
	}
	g := group.Group("/storage-sync/:kind/:id/:attempt/:generation")
	g.POST("/claim", h.claim)
	g.POST("/report", h.report)
	g.POST("/metadata", h.readMetadata)
}
func (h *StorageSyncHandler) authorizeWorker(c *gin.Context) (ss.WorkSpec, bool) {
	kind, id := c.Param("kind"), c.Param("id")
	attempt, e1 := strconv.Atoi(c.Param("attempt"))
	generation, e2 := strconv.ParseInt(c.Param("generation"), 10, 64)
	provided := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	want := StorageSyncWorkerToken(h.key, kind, id, attempt, generation)
	if (kind != "run" && kind != "preview") || id == "" || e1 != nil || e2 != nil || attempt < 1 || generation < 1 || len(provided) != len(want) || !hmac.Equal([]byte(provided), []byte(want)) {
		h.response.writeError(c, 401, "STORAGE_SYNC_WORKER_UNAUTHORIZED", "worker credential rejected")
		return ss.WorkSpec{}, false
	}
	var spec ss.WorkSpec
	var err error
	if strings.HasSuffix(c.FullPath(), "/report") {
		spec, err = h.manager.GetReportSpec(c.Request.Context(), id, attempt, generation)
	} else {
		spec, err = h.manager.GetWorkSpec(c.Request.Context(), id, attempt, generation)
	}
	if err != nil {
		h.fail(c, err)
		return ss.WorkSpec{}, false
	}
	if spec.SubjectKind != kind {
		h.fail(c, ss.ErrStaleAttempt)
		return ss.WorkSpec{}, false
	}
	c.Header("Cache-Control", "no-store")
	return spec, true
}
func (h *StorageSyncHandler) claimedWorker(c *gin.Context, spec ss.WorkSpec, worker string) bool {
	var claimed string
	var err error
	if spec.SubjectKind == "run" {
		var run ss.Run
		run, err = h.manager.GetRun(c.Request.Context(), spec.RunID)
		claimed = run.WorkerID
	} else {
		var preview ss.Preview
		preview, err = h.manager.GetPreview(c.Request.Context(), spec.RunID)
		claimed = preview.WorkerID
	}
	if err != nil || worker == "" || claimed != worker {
		h.fail(c, ss.ErrStaleAttempt)
		return false
	}
	return true
}
func (h *StorageSyncHandler) claim(c *gin.Context) {
	spec, ok := h.authorizeWorker(c)
	if !ok {
		return
	}
	var req struct {
		RunID      string `json:"runId"`
		Attempt    int    `json:"attempt"`
		Generation int64  `json:"generation"`
		WorkerID   string `json:"workerId"`
	}
	if !h.decode(c, &req) {
		return
	}
	if req.RunID != spec.RunID || req.Attempt != spec.Attempt || req.Generation != spec.Generation {
		h.fail(c, ss.ErrStaleAttempt)
		return
	}
	if !h.workerAuthority(c, spec) {
		return
	}
	err := h.manager.Claim(c.Request.Context(), spec.RunID, spec.Attempt, spec.Generation, req.WorkerID)
	h.send(c, 200, gin.H{"claimed": err == nil}, err)
}
func (h *StorageSyncHandler) report(c *gin.Context) {
	spec, ok := h.authorizeWorker(c)
	if !ok {
		return
	}
	var req ss.Report
	if !h.decode(c, &req) {
		return
	}
	if req.RunID != spec.RunID || req.Attempt != spec.Attempt || req.Generation != spec.Generation || !h.claimedWorker(c, spec, req.WorkerID) {
		if !c.Writer.Written() {
			h.fail(c, ss.ErrStaleAttempt)
		}
		return
	}
	if (spec.SubjectKind == "preview" && (req.PreviewID != spec.RunID)) || (spec.SubjectKind == "run" && req.PreviewID == spec.RunID) {
		h.fail(c, ss.ErrInvalid)
		return
	}
	if err := h.manager.Report(c.Request.Context(), req); err != nil {
		h.fail(c, err)
		return
	}
	control := ""
	if spec.SubjectKind == "run" {
		var err error
		control, err = h.manager.ControlForRun(c.Request.Context(), spec.RunID)
		if err != nil {
			h.fail(c, err)
			return
		}
	}
	h.send(c, 200, gin.H{"control": control}, nil)
}

type storageSyncMetadataRequest struct {
	Operation         string `json:"operation"`
	WorkerID          string `json:"workerId"`
	Bucket            string `json:"bucket"`
	Prefix            string `json:"prefix"`
	Key               string `json:"key"`
	ContinuationToken string `json:"continuationToken"`
	Limit             int    `json:"limit"`
	ETag              string `json:"etag"`
	VersionID         string `json:"versionId"`
}

func storageSyncMetadataAllowed(spec ss.WorkSpec, req storageSyncMetadataRequest) bool {
	value := req.Key
	switch req.Operation {
	case "head", "read":
	case "list", "browse":
		value = strings.TrimSuffix(req.Prefix, "/")
	default:
		return false
	}
	if value == "" || ss.ValidateRelativePath(value) != nil {
		return false
	}
	for _, mapping := range spec.Mappings {
		for _, location := range []ss.ResolvedLocation{mapping.Source, mapping.Destination} {
			root := strings.TrimSuffix(location.Prefix, "/")
			if location.Kind == "TOS" && location.Bucket == req.Bucket && root != "" && (value == root || strings.HasPrefix(value, root+"/")) {
				return true
			}
		}
	}
	return false
}
func (h *StorageSyncHandler) readMetadata(c *gin.Context) {
	spec, ok := h.authorizeWorker(c)
	if !ok {
		return
	}
	var req storageSyncMetadataRequest
	if !h.decode(c, &req) {
		return
	}
	if !h.claimedWorker(c, spec, req.WorkerID) {
		return
	}
	if !storageSyncMetadataAllowed(spec, req) {
		h.fail(c, ss.ErrForbidden)
		return
	}
	if !h.workerAuthority(c, spec) {
		return
	}
	if h.metadata == nil {
		h.fail(c, errStorageSyncUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	switch req.Operation {
	case "head":
		result, err := h.metadata.StorageSyncHead(ctx, req.Bucket, req.Key)
		h.send(c, 200, result, err)
	case "read":
		result, err := h.metadata.StorageSyncReadURL(ctx, req.Bucket, req.Key, req.ETag, req.VersionID)
		h.send(c, 200, result, err)
	case "list", "browse":
		limit := req.Limit
		if limit == 0 {
			limit = 1000
		}
		if limit < 1 || limit > 1000 || len(req.ContinuationToken) > 4096 {
			h.fail(c, ss.ErrInvalid)
			return
		}
		prefix := strings.TrimSuffix(req.Prefix, "/") + "/"
		delimiter := ""
		if req.Operation == "browse" {
			delimiter = "/"
		}
		page, err := h.metadata.StorageSyncList(ctx, req.Bucket, prefix, req.ContinuationToken, limit, delimiter)
		if err != nil {
			h.fail(c, err)
			return
		}
		if req.Operation == "list" {
			h.send(c, 200, page, nil)
			return
		}
		location := spec.Mappings[0].Source
		entries := make([]ss.BrowseEntry, 0, len(page.Entries)+len(page.Directories))
		for _, directory := range page.Directories {
			name := strings.TrimSuffix(strings.TrimPrefix(directory, prefix), "/")
			if name == "" || strings.Contains(name, "/") {
				continue
			}
			entries = append(entries, ss.BrowseEntry{Name: name, RelativePath: path.Join(location.RelativePath, name), Kind: "DIRECTORY"})
		}
		for _, object := range page.Entries {
			name := strings.TrimPrefix(object.Key, prefix)
			if name == "" || strings.Contains(name, "/") {
				continue
			}
			entries = append(entries, ss.BrowseEntry{Name: name, RelativePath: path.Join(location.RelativePath, name), Kind: "FILE", SizeBytes: object.Size, ModifiedAt: object.LastModified})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		h.send(c, 200, gin.H{"entries": entries, "nextToken": page.NextToken}, nil)
	}
}
func (h *StorageSyncHandler) files(c *gin.Context) {
	if !h.checkRun(c, c.Param("id")) {
		return
	}
	limit := 100
	var err error
	if raw := c.Query("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			h.fail(c, ss.ErrInvalid)
			return
		}
	}
	result, err := h.manager.ListRunFiles(c.Request.Context(), c.Param("id"), c.Query("cursor"), limit)
	h.send(c, 200, result, err)
}

func (h *Handler) ConfigureStorageSync(handler *StorageSyncHandler) { h.storageSync = handler }
func (h *Handler) RegisterStorageSyncManagementRoutes(group *gin.RouterGroup) {
	if h.storageSync != nil {
		h.storageSync.RegisterManagementRoutes(group)
	}
}
func (h *Handler) RegisterStorageSyncInternalRoutes(group *gin.RouterGroup) {
	if h.storageSync != nil {
		h.storageSync.RegisterInternalRoutes(group)
	}
}
