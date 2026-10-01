package storagesync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var safeErrorCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,95}$`)

func terminalReceipt(state string) bool {
	return state == "SUCCEEDED" || state == "FAILED" || state == "PAUSED" || state == "CANCELLED"
}
func reportDigest(report Report) string {
	b, _ := json.Marshal(report)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func safeFailure(value string) string {
	if value == "" {
		return ""
	}
	if safeErrorCode.MatchString(value) {
		return value
	}
	return "WORKER_FAILED"
}
func validateFileReference(id string, ref WorkerFileReference) error {
	if ref.Count < 0 || len(ref.Path) > 4096 || len(ref.Digest) > 128 {
		return ErrInvalid
	}
	if ref.Path == "" {
		return nil
	}
	allowed := "/work/" + id + "/"
	preview := "/work/previews/" + id + "/"
	if !strings.HasPrefix(ref.Path, allowed) && !strings.HasPrefix(ref.Path, preview) {
		return ErrInvalid
	}
	if err := ValidateRelativePath(strings.TrimPrefix(ref.Path, "/")); err != nil {
		return err
	}
	return nil
}
func validateReport(report Report) error {
	if report.RunID == "" || report.Attempt < 1 || report.Generation < 1 || report.Sequence < 1 || len(report.ManifestDigest) > 128 || len(report.SourceFingerprint) > 256 || len(report.TargetFingerprint) > 256 || len(report.NextCursor) > 2048 {
		return ErrInvalid
	}
	if report.State != "RUNNING" && !terminalReceipt(report.State) {
		return ErrInvalid
	}
	if report.RequestsDrained && !terminalReceipt(report.State) {
		return ErrInvalid
	}
	if err := report.Progress.Validate(); err != nil {
		return err
	}
	if err := validateFileReference(report.RunID, report.Files); err != nil {
		return err
	}
	if len(report.FileResults) > 1000 {
		return ErrInvalid
	}
	for _, item := range report.FileResults {
		if item.MappingIndex < 0 || item.SizeBytes < 0 || item.RelativePath == "" || ValidateRelativePath(item.RelativePath) != nil || (item.State != "VERIFIED" && item.State != "REUSED" && item.State != "FAILED") || (item.ErrorCode != "" && !safeErrorCode.MatchString(item.ErrorCode)) {
			return ErrInvalid
		}
	}
	if len(report.MappingProgress) > 32 {
		return ErrInvalid
	}
	seen := map[int]bool{}
	for _, item := range report.MappingProgress {
		if item.MappingIndex < 0 || seen[item.MappingIndex] || item.Progress.Validate() != nil {
			return ErrInvalid
		}
		seen[item.MappingIndex] = true
	}
	return nil
}
func monotonicProgress(old, next Progress) bool {
	return next.DiscoveredFiles >= old.DiscoveredFiles && next.CompletedFiles >= old.CompletedFiles && next.CompletedBytes >= old.CompletedBytes && next.VerifiedFiles >= old.VerifiedFiles && next.VerifiedBytes >= old.VerifiedBytes && next.NetworkBytes >= old.NetworkBytes && (!old.ScanComplete || next.ScanComplete)
}
func (m *Manager) Report(ctx context.Context, report Report) error {
	if err := validateReport(report); err != nil {
		return err
	}
	return m.repo.Transact(ctx, func(tx Tx) error {
		if report.PreviewID != "" && report.PreviewID == report.RunID {
			return m.reportPreview(tx, report)
		}
		run, err := tx.GetRun(report.RunID)
		if err != nil {
			return err
		}
		if run.Attempt != report.Attempt || run.Generation != report.Generation {
			return ErrStaleAttempt
		}
		if report.Sequence < run.Sequence {
			return nil
		}
		digest := reportDigest(report)
		if report.Sequence == run.Sequence {
			if run.LastReportDigest != digest {
				return ErrConflict
			}
			return nil
		}
		if !run.Active() || run.State == "PAUSED" {
			return ErrStaleAttempt
		}
		if run.WorkerID == "" || report.WorkerID != run.WorkerID {
			return ErrStaleAttempt
		}
		if run.ReceiptState != "" {
			return ErrConflict
		}
		if run.Sequence > 0 && !monotonicProgress(run.Progress, report.Progress) {
			return fmt.Errorf("%w: cumulative counters regressed", ErrInvalid)
		}
		if run.Phase == "PREVIEW" && report.Phase != "PREVIEW" && report.Phase != "SCANNING" && report.Phase != "PLANNING" {
			return ErrInvalid
		}
		if run.Phase == "TRANSFER" && report.Phase != "TRANSFER" && report.Phase != "TRANSFERRING" && report.Phase != "VERIFYING" && report.Phase != "PLANNING" {
			return ErrInvalid
		}
		for _, item := range report.FileResults {
			if item.MappingIndex >= len(run.Config.Mappings) {
				return ErrInvalid
			}
		}
		for _, item := range report.MappingProgress {
			if item.MappingIndex >= len(run.Config.Mappings) {
				return ErrInvalid
			}
		}
		updated := run
		now := m.now()
		updated.Sequence = report.Sequence
		updated.LastReportDigest = digest
		updated.Progress = report.Progress
		updated.HeartbeatAt = &now
		updated.UpdatedAt = now
		updated.RecoverableUntil = now.Add(m.options.CheckpointRetention)
		updated.MappingProgress = append([]MappingProgress(nil), report.MappingProgress...)
		updated.Stage = report.Phase
		updated.Files = FileReference{Path: report.Files.Path, Digest: report.Files.Digest, Count: report.Files.Count}
		updated.FailureReason = safeFailure(report.FailureReason)
		if terminalReceipt(report.State) {
			updated.ReceiptState = report.State
			updated.RequestsDrained = report.RequestsDrained
			if report.State == "SUCCEEDED" {
				if run.Phase == "PREVIEW" {
					if report.ManifestDigest == "" || report.SourceFingerprint == "" || report.TargetFingerprint == "" || !report.Progress.ScanComplete {
						return ErrInvalid
					}
					if (run.ManifestDigest != "" && run.ManifestDigest != report.ManifestDigest) || (run.SourceFingerprint != "" && run.SourceFingerprint != report.SourceFingerprint) || (run.TargetFingerprint != "" && run.TargetFingerprint != report.TargetFingerprint) {
						updated.ReceiptState = "FAILED"
						updated.FailureReason = "PREVIEW_CHANGED"
					} else {
						updated.ManifestDigest = report.ManifestDigest
						updated.SourceFingerprint = report.SourceFingerprint
						updated.TargetFingerprint = report.TargetFingerprint
					}
				} else if !report.Progress.Complete() || report.ManifestDigest != run.ManifestDigest {
					return fmt.Errorf("%w: success requires complete verified manifest", ErrInvalid)
				}
			}
		}
		if err = tx.PutRun(updated); err != nil {
			return err
		}
		return tx.PutFileResults(run.ID, run.Attempt, run.Generation, report.FileResults)
	})
}
func (m *Manager) reportPreview(tx Tx, report Report) error {
	p, err := tx.GetPreview(report.RunID)
	if err != nil {
		return err
	}
	if p.Attempt != report.Attempt || p.Generation != report.Generation {
		return ErrStaleAttempt
	}
	if report.Sequence < p.Sequence {
		return nil
	}
	digest := reportDigest(report)
	if report.Sequence == p.Sequence {
		if digest != p.LastReportDigest {
			return ErrConflict
		}
		return nil
	}
	cleanup := terminalReceipt(report.State) && report.State != "SUCCEEDED"
	if p.ReceiptState != "" || p.WorkerID == "" || report.WorkerID != p.WorkerID {
		return ErrStaleAttempt
	}
	if p.State != "RUNNING" && !(p.State == "FAILED" && cleanup) {
		return ErrStaleAttempt
	}
	if (!p.ExpiresAt.After(m.now()) || !previewScanDeadline(p).After(m.now())) && !cleanup {
		return ErrStaleAttempt
	}
	if report.Phase != p.Kind && (p.Kind != "PREVIEW" || (report.Phase != "SCANNING" && report.Phase != "PLANNING")) {
		return ErrInvalid
	}
	if len(report.BrowseEntries) > p.Limit && p.Kind == "BROWSE" {
		return ErrInvalid
	}
	for _, item := range report.MappingProgress {
		if item.MappingIndex >= len(p.Config.Mappings) {
			return ErrInvalid
		}
	}
	for _, entry := range report.BrowseEntries {
		kind := strings.ToUpper(entry.Kind)
		if ValidateRelativePath(entry.RelativePath) != nil || entry.Name == "" || strings.ContainsAny(entry.Name, "/\\\x00") || (kind != "DIRECTORY" && kind != "FILE") || entry.SizeBytes < 0 {
			return ErrInvalid
		}
	}
	updated := p
	updated.Sequence = report.Sequence
	updated.LastReportDigest = digest
	updated.Progress = report.Progress
	updated.UpdatedAt = m.now()
	updated.FailureReason = safeFailure(report.FailureReason)
	if p.State == "FAILED" {
		updated.FailureReason = p.FailureReason
	} else if report.State == "SUCCEEDED" {
		updated.ExpiresAt = m.now().Add(m.options.PreviewTTL)
	} else if report.State == "RUNNING" {
		updated.ExpiresAt = m.previewLease(p)
	}
	updated.MappingProgress = append([]MappingProgress(nil), report.MappingProgress...)
	updated.Stage = report.Phase
	if terminalReceipt(report.State) {
		if report.State == "SUCCEEDED" && p.Kind == "PREVIEW" && (report.ManifestDigest == "" || report.SourceFingerprint == "" || report.TargetFingerprint == "" || !report.Progress.ScanComplete) {
			return ErrInvalid
		}
		updated.ReceiptState = report.State
		updated.RequestsDrained = report.RequestsDrained
		updated.ManifestDigest = report.ManifestDigest
		updated.SourceFingerprint = report.SourceFingerprint
		updated.TargetFingerprint = report.TargetFingerprint
		updated.Files = FileReference{Path: report.Files.Path, Digest: report.Files.Digest, Count: report.Files.Count}
		updated.BrowseEntries = append([]BrowseEntry(nil), report.BrowseEntries...)
		updated.NextCursor = report.NextCursor
	}
	for i, entry := range updated.BrowseEntries {
		normalized := entry
		normalized.Kind = strings.ToUpper(entry.Kind)
		updated.BrowseEntries[i] = normalized
	}
	return tx.PutPreview(updated)
}
