package api

import (
	"context"
	"errors"
	"io"
	"path"
	"strconv"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	ml "ray-train-platform-backend/modellifecycle"
	"ray-train-platform-backend/observability"
	ws "ray-train-platform-backend/warehousesync"
)

type warehouseSyncSource struct { h *Handler }

type warehouseJobSourceResolver interface {
	ResolveJobSource(context.Context, string, string) (observability.JobSource, error)
}

func (s warehouseSyncSource) Prepare(ctx context.Context, op ws.Operation) ([]ws.File, ws.SourceIdentity, error) {
	identity := ws.SourceIdentity{}
	accounts, ok := s.h.repository.(auth.OAuth2ProxyAccountResolver)
	if !ok || s.h.models == nil || s.h.modelSnapshots == nil { return nil, identity, ws.ErrForbidden }
	account, found, err := accounts.ResolveOAuth2ProxyAccount(ctx, op.OwnerName)
	if err != nil { return nil, identity, err }
	if !found || account.Disabled || account.ID != op.OwnerID || account.TenantID != op.TenantID || len(account.Roles) == 0 {
		return nil, identity, ws.ErrForbidden
	}
	job, err := s.h.repository.Get(ctx, op.TenantID, op.JobID)
	if err != nil { return nil, identity, err }
	if job == nil || job.UserID != op.OwnerID || job.TenantID != op.TenantID { return nil, identity, ws.ErrForbidden }
	switch job.ObservedState {
	case domain.StateSucceeded:
	case domain.StateFailed, domain.StateCanceled, domain.StateTimedOut:
		if op.Automatic { return nil, identity, ws.ErrSourceFailed }
	default: return nil, identity, ws.ErrPending
	}
	p := auth.Principal{Subject: account.ID, Username: account.Username, TenantID: account.TenantID, StorageTenantID: account.StorageTenantID, StorageKey: account.StorageKey, Roles: append([]string(nil), account.Roles...)}
	root, ok := s.h.logicalJobArtifactRootContext(ctx, p, job)
	if !ok { root, ok = s.h.personalStorageJobArtifactRootContext(ctx, p, job) }
	if !ok { return nil, identity, ws.ErrForbidden }
	resolver, ok := s.h.experiments.(warehouseJobSourceResolver)
	if !ok { return nil, identity, errors.New("training source is unavailable") }
	source, err := resolver.ResolveJobSource(ctx, op.TenantID, op.JobID)
	if err != nil { return nil, identity, err }
	identity = ws.SourceIdentity{JobID: source.JobID, RunID: source.RunID, ExperimentID: source.ExperimentID}
	model, err := s.h.models.CreateModel(ctx, ml.Model{Name: "训练同步 " + op.JobID, Description: "自动同步功能仓时生成的训练权重副本", OwnerID: op.OwnerID, OwnerName: account.Username, TenantID: op.TenantID, IdempotencyKey: "warehouse-sync:" + op.ID})
	if err != nil { return nil, identity, err }
	if model.Archived { return nil, identity, ws.ErrConflict }
	files := make([]ws.File, 0, len(op.Paths))
	pending := false
	for i, relative := range op.Paths {
		request := ml.VersionRequest{ModelID: model.ID, CreatorID: op.OwnerID, CreatorName: account.Username, JobID: job.ID, JobName: job.Spec.Name, RunID: source.RunID, FileName: path.Base(relative), SourceRoot: root, RelativePath: relative, IdempotencyKey: "warehouse-sync:" + op.ID + ":" + strconv.Itoa(i), RuntimeImage: job.Spec.Image}
		if isModelSourceDigest(job.Spec.Source.ArtifactSHA256, 64) { request.CodeSHA256 = job.Spec.Source.ArtifactSHA256 }
		if isModelSourceDigest(job.Spec.Source.Commit, 40) || isModelSourceDigest(job.Spec.Source.Commit, 64) { request.CodeCommit = job.Spec.Source.Commit }
		if job.DatasetProvenance.DatasetVersionID != "" {
			request.DatasetID = job.DatasetProvenance.DatasetID
			request.DatasetVersionID = job.DatasetProvenance.DatasetVersionID
			request.DatasetManifestSHA256 = job.DatasetProvenance.ManifestSHA256
			request.DatasetAssociation = "training-record"
		}
		v, err := s.h.modelSnapshots.RequestVersion(ctx, request)
		if err != nil { return nil, identity, err }
		if v.State == ml.Failed { return nil, identity, errors.New("model snapshot failed") }
		if v.State != ml.Ready { pending = true; continue }
		files = append(files, ws.File{ModelID: model.ID, VersionID: v.ID, Name: v.FileName, Size: v.SizeBytes, SHA256: v.SHA256})
	}
	if pending { return nil, identity, ws.ErrPending }
	return files, identity, nil
}

func (s warehouseSyncSource) Open(ctx context.Context, file ws.File) (io.ReadCloser, error) {
	v, err := s.h.models.GetVersion(ctx, file.ModelID, file.VersionID)
	if err != nil { return nil, err }
	if v.State != ml.Ready || v.SHA256 != file.SHA256 || v.SizeBytes != file.Size || v.FileName != file.Name { return nil, ws.ErrConflict }
	ctx, cancel := context.WithCancel(ctx)
	r, w := io.Pipe()
	go func() { defer cancel(); _ = w.CloseWithError(s.h.modelSnapshots.Download(ctx, v, w)) }()
	return &warehouseSnapshotReader{PipeReader: r, cancel: cancel}, nil
}

type warehouseSnapshotReader struct { *io.PipeReader; cancel context.CancelFunc }
func (r *warehouseSnapshotReader) Close() error { r.cancel(); return r.PipeReader.Close() }
