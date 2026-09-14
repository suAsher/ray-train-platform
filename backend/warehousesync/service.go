package warehousesync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	fw "ray-train-platform-backend/functionwarehouse"
)

const leaseDuration = 2 * time.Minute

var restrictedID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var storageSegment = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type Service struct {
	store       Store
	source      Source
	clients     map[fw.Environment]Upstream
	credentials credentialBox
}

func NewService(store Store, source Source, clients map[fw.Environment]Upstream, pepper []byte) (*Service, error) {
	if store == nil || source == nil || len(clients) == 0 {
		return nil, ErrInvalid
	}
	box, err := newCredentialBox(pepper)
	if err != nil {
		return nil, err
	}
	copyClients := make(map[fw.Environment]Upstream, len(clients))
	for env, client := range clients {
		if (env != fw.Production && env != fw.Development) || client == nil {
			return nil, ErrInvalid
		}
		copyClients[env] = client
	}
	return &Service{store: store, source: source, clients: copyClients, credentials: box}, nil
}

func validRequest(actor Actor, request Request) bool {
	if actor.ID == "" || actor.TenantID == "" || !restrictedID.MatchString(request.JobID) || !restrictedID.MatchString(request.WarehouseID) || !restrictedID.MatchString(request.ModelTypeID) || !restrictedID.MatchString(request.IdempotencyKey) {
		return false
	}
	if strings.TrimSpace(request.Version) == "" || len(request.Version) > 128 || !utf8.ValidString(request.Version) || strings.ContainsAny(request.Version, "\x00\r\n") {
		return false
	}
	if len(request.Paths) < 1 || len(request.Paths) > 8 {
		return false
	}
	names := make(map[string]bool, len(request.Paths))
	for _, value := range request.Paths {
		if value == "" || len(value) > 1024 || !utf8.ValidString(value) || strings.ContainsAny(value, "\\\x00\r\n") || path.IsAbs(value) || path.Clean(value) != value || value == ".." || strings.HasPrefix(value, "../") {
			return false
		}
		name := path.Base(value)
		if names[name] || len(name) > 255 {
			return false
		}
		names[name] = true
		switch strings.ToLower(path.Ext(name)) {
		case ".pth", ".pt", ".ckpt", ".safetensors", ".onnx":
		default:
			return false
		}
	}
	return true
}

func (s *Service) Create(ctx context.Context, actor Actor, request Request, token string) (Operation, error) {
	if !validRequest(actor, request) {
		return Operation{}, ErrInvalid
	}
	client, ok := s.clients[request.Environment]
	if !ok {
		return Operation{}, ErrInvalid
	}
	op := Operation{ID: uuid.NewString(), OwnerID: actor.ID, OwnerName: actor.Name, TenantID: actor.TenantID,
		JobID: request.JobID, Environment: request.Environment, WarehouseID: request.WarehouseID, ModelTypeID: request.ModelTypeID,
		Version: request.Version, Paths: append([]string(nil), request.Paths...), Automatic: request.Automatic,
		State: Queued, Message: stateMessage(Queued), IdempotencyKey: request.IdempotencyKey, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	encoded, _ := json.Marshal(request)
	sum := sha256.Sum256(encoded)
	op.RequestSHA256 = hex.EncodeToString(sum[:])
	credential, err := s.credentials.seal(op, token)
	if err != nil {
		return Operation{}, fw.ErrUnauthorized
	}
	if err := checkTarget(ctx, client, token, op); err != nil {
		return Operation{}, safeError(err)
	}
	op.Credential = credential
	created, err := s.store.Create(ctx, op)
	return created, safeError(err)
}

func (s *Service) Retry(ctx context.Context, id string, actor Actor, token string) (Operation, error) {
	op, err := s.owned(ctx, id, actor)
	if err != nil {
		return Operation{}, err
	}
	if (op.State != Failed && op.State != WaitingReauth) || op.TargetVersionID != "" {
		return Operation{}, ErrConflict
	}
	client := s.clients[op.Environment]
	if client == nil {
		return Operation{}, ErrInvalid
	}
	credential, err := s.credentials.seal(op, token)
	if err != nil {
		return Operation{}, fw.ErrUnauthorized
	}
	if err := checkTarget(ctx, client, token, op); err != nil {
		return Operation{}, safeError(err)
	}
	resumed, err := s.store.Resume(ctx, id, actor, credential)
	return resumed, safeError(err)
}

func (s *Service) Cancel(ctx context.Context, id string, actor Actor) (Operation, error) {
	op, err := s.owned(ctx, id, actor)
	if err != nil {
		return Operation{}, err
	}
	if op.State != Queued && op.State != WaitingSource && op.State != WaitingReauth && op.State != Uploading {
		return Operation{}, ErrConflict
	}
	canceled, err := s.store.Cancel(ctx, id, actor)
	return canceled, safeError(err)
}

func (s *Service) ListJob(ctx context.Context, actor Actor, jobID string) ([]Operation, error) {
	if actor.ID == "" || actor.TenantID == "" || !restrictedID.MatchString(jobID) {
		return nil, ErrInvalid
	}
	operations, err := s.store.ListJob(ctx, actor.ID, actor.TenantID, jobID)
	return operations, safeError(err)
}

func (s *Service) owned(ctx context.Context, id string, actor Actor) (Operation, error) {
	if !restrictedID.MatchString(id) || actor.ID == "" || actor.TenantID == "" {
		return Operation{}, ErrInvalid
	}
	op, err := s.store.Get(ctx, id)
	if err != nil {
		return Operation{}, safeError(err)
	}
	if op.OwnerID != actor.ID || op.TenantID != actor.TenantID {
		return Operation{}, ErrForbidden
	}
	return op, nil
}

func checkTarget(ctx context.Context, client Upstream, token string, op Operation) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	warehouse, err := client.GetWarehouse(ctx, token, op.WarehouseID)
	if err != nil {
		return err
	}
	group := ""
	for _, target := range fw.Environments() {
		if target.Environment == op.Environment {
			group = target.GroupID
		}
	}
	if warehouse.ID != op.WarehouseID || warehouse.GroupID != group || group == "" {
		return fw.ErrForbidden
	}
	canEdit := false
	for _, permission := range warehouse.PermissionCodes {
		if permission == "edit" || permission == "admin" {
			canEdit = true
		}
	}
	if !canEdit {
		return fw.ErrForbidden
	}
	models, err := client.ListModelTypes(ctx, token, op.WarehouseID)
	if err != nil {
		return err
	}
	for _, model := range models {
		if model.ID == op.ModelTypeID && (model.GroupID == "" || model.GroupID == group) && (model.FunctionWarehouseID == "" || model.FunctionWarehouseID == op.WarehouseID) {
			return nil
		}
	}
	return fw.ErrForbidden
}

// Run deliberately has a single worker per replica. Store leases coordinate
// replicas. Ambiguous registrations are never reclaimed for another POST.
func (s *Service) Run(ctx context.Context) {
	for ctx.Err() == nil {
		now := time.Now().UTC()
		op, err := s.store.Claim(ctx, uuid.NewString(), now, now.Add(leaseDuration))
		if err == nil {
			s.process(ctx, op)
			continue
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Service) process(parent context.Context, op Operation) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go s.renewLease(ctx, cancel, op, done)
	if op.State == Registering || op.TargetVersionID != "" {
		s.finish(op, Unknown)
		return
	}
	token, err := s.credentials.open(op)
	if err != nil {
		s.finish(op, WaitingReauth)
		return
	}
	client := s.clients[op.Environment]
	if client == nil {
		s.finish(op, Failed)
		return
	}
	if err := checkTarget(ctx, client, token, op); err != nil {
		s.fail(op, err)
		return
	}
	files, identity, err := s.source.Prepare(ctx, op)
	if err != nil {
		s.fail(op, err)
		return
	}
	if !validSource(op, files, identity) {
		s.finish(op, Failed)
		return
	}
	op.Files = append([]File(nil), files...)
	op.RunID = identity.RunID
	op.ExperimentID = identity.ExperimentID
	if !s.saveStage(ctx, &op, Uploading) {
		return
	}
	paths, err := s.upload(ctx, client, token, op)
	if err != nil {
		s.fail(op, err)
		return
	}
	// Membership/source provenance may have changed during a large upload.
	currentFiles, currentIdentity, err := s.source.Prepare(ctx, op)
	if err != nil {
		s.fail(op, err)
		return
	}
	if !sameSource(files, identity, currentFiles, currentIdentity) {
		s.finish(op, Failed)
		return
	}
	if err = checkTarget(ctx, client, token, op); err != nil {
		s.fail(op, err)
		return
	}
	// A synchronous lease check is required immediately before committing the
	// registration boundary; a failed background renewal cannot race a POST.
	if ctx.Err() != nil || s.store.Renew(ctx, op.ID, op.LeaseID, time.Now().UTC().Add(leaseDuration)) != nil {
		return
	}
	if !s.saveStage(ctx, &op, Registering) {
		return
	}
	if ctx.Err() != nil {
		s.finish(op, Unknown)
		return
	}
	request := fw.CreateVersionRequest{FunctionWarehouseID: op.WarehouseID, ModelTypeID: op.ModelTypeID,
		Version: op.Version, Description: "训练平台模型同步 " + op.ID, Paths: paths, JobID: op.JobID, RunID: op.RunID, ExperimentID: op.ExperimentID}
	version, err := client.CreateVersion(ctx, token, request)
	if version.ID != "" {
		op.TargetVersionID = version.ID
	}
	if err != nil {
		if op.TargetVersionID != "" || errors.Is(err, fw.ErrUnknownOutcome) || ctx.Err() != nil {
			s.finish(op, Unknown)
		} else {
			s.fail(op, err)
		}
		return
	}
	if op.TargetVersionID == "" {
		s.finish(op, Unknown)
		return
	}
	if !s.saveStage(ctx, &op, Registering) {
		return
	}
	if _, err = client.VerifyVersion(ctx, token, op.WarehouseID, op.TargetVersionID, request); err != nil {
		s.finish(op, Unknown)
		return
	}
	s.finish(op, Succeeded)
}

func (s *Service) renewLease(ctx context.Context, cancel context.CancelFunc, op Operation, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewCtx, release := context.WithTimeout(ctx, 10*time.Second)
			err := s.store.Renew(renewCtx, op.ID, op.LeaseID, time.Now().UTC().Add(leaseDuration))
			release()
			if err != nil {
				cancel()
				return
			}
		}
	}
}

func (s *Service) upload(ctx context.Context, client Upstream, token string, op Operation) ([]fw.UploadedFile, error) {
	result := make([]fw.UploadedFile, 0, len(op.Files))
	for _, file := range op.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := checkTarget(ctx, client, token, op); err != nil {
			return nil, err
		}
		uploaded, err := client.Upload(ctx, token, file.Name, "raytrain/"+op.ID+"/"+file.VersionID, file.Size, file.SHA256, func(openCtx context.Context) (io.ReadCloser, error) { return s.source.Open(openCtx, file) })
		if err != nil {
			return nil, err
		}
		if uploaded.Filename != file.Name || uploaded.FileSize != file.Size || !strings.EqualFold(uploaded.FileSHA256, file.SHA256) {
			return nil, ErrInvalid
		}
		result = append(result, uploaded)
	}
	return result, nil
}

func validSource(op Operation, files []File, identity SourceIdentity) bool {
	if identity.JobID != op.JobID || !restrictedID.MatchString(identity.RunID) || !restrictedID.MatchString(identity.ExperimentID) || len(files) != len(op.Paths) {
		return false
	}
	names := make(map[string]bool, len(op.Paths))
	for _, value := range op.Paths {
		names[path.Base(value)] = true
	}
	for _, file := range files {
		if !names[file.Name] || !restrictedID.MatchString(file.ModelID) || !storageSegment.MatchString(file.VersionID) || file.Size <= 0 || file.Size > fw.MaxUploadSize || len(file.SHA256) != 64 || strings.ToLower(file.SHA256) != file.SHA256 {
			return false
		}
		if _, err := hex.DecodeString(file.SHA256); err != nil {
			return false
		}
		delete(names, file.Name)
	}
	return len(names) == 0
}

func sameSource(files []File, identity SourceIdentity, otherFiles []File, otherIdentity SourceIdentity) bool {
	if identity != otherIdentity || len(files) != len(otherFiles) {
		return false
	}
	counts := make(map[File]int, len(files))
	for _, file := range files {
		counts[file]++
	}
	for _, file := range otherFiles {
		if counts[file] == 0 {
			return false
		}
		counts[file]--
	}
	return true
}

func (s *Service) saveStage(ctx context.Context, op *Operation, state string) bool {
	next := *op
	next.State = state
	next.Message = stateMessage(state)
	next.UpdatedAt = time.Now().UTC()
	if ctx.Err() != nil || s.store.Save(ctx, next, op.LeaseID) != nil {
		return false
	}
	*op = next
	return true
}

func (s *Service) finish(op Operation, state string) {
	lease := op.LeaseID
	op.State = state
	op.Message = stateMessage(state)
	op.UpdatedAt = time.Now().UTC()
	op.LeaseID = ""
	op.LeaseExpiresAt = nil
	if state == WaitingSource {
		op.NextAttemptAt = time.Now().UTC().Add(30 * time.Second)
	} else {
		op.Credential = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Store CAS rejects canceled, expired or superseded leases. On failure the
	// recovery policy fences REGISTERING as UNKNOWN, never another create.
	_ = s.store.Save(ctx, op, lease)
}

func (s *Service) fail(op Operation, err error) {
	switch {
	case errors.Is(err, ErrPending):
		s.finish(op, WaitingSource)
	case errors.Is(err, fw.ErrUnauthorized):
		s.finish(op, WaitingReauth)
	default:
		s.finish(op, Failed)
	}
}

func stateMessage(state string) string {
	switch state {
	case Queued:
		return "等待同步"
	case WaitingSource:
		return "等待训练完成或权重副本就绪"
	case Uploading:
		return "正在上传模型文件"
	case Registering:
		return "正在创建功能仓版本并核对训练来源"
	case Succeeded:
		return "模型文件及训练来源已同步并核验"
	case WaitingReauth:
		return "登录授权已失效，请重新登录后重试同步"
	case Unknown:
		return "创建结果待确认，请先检查功能仓，避免重复创建"
	case Canceled:
		return "同步已取消"
	default:
		return "同步未完成，请检查文件、训练来源或目标权限后重试"
	}
}

func safeError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrInvalid, ErrForbidden, ErrNotFound, ErrConflict, ErrQuota, fw.ErrUnauthorized, fw.ErrForbidden, fw.ErrInvalid, fw.ErrRateLimited} {
		if errors.Is(err, known) {
			return known
		}
	}
	return fw.ErrUnavailable
}
