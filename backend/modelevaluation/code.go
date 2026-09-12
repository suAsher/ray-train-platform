package modelevaluation

import (
	"context"
	"io"
	"regexp"

	"ray-train-platform-backend/domain"
)

const MaxEvaluationCodeSize int64 = 64 << 20

// CodeSnapshot identifies an independently retained, immutable code archive.
// It deliberately contains neither an object key nor a mutable source path.
type CodeSnapshot struct {
	ID        string `json:"id"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
	Format    string `json:"format"`
}

// Publish receives a READY artifact loaded with the authenticated caller's
// tenant and personal identity. The API owns that authorization check.
type EvaluationCodeStore interface {
	Publish(context.Context, string, domain.SourceArtifact) (CodeSnapshot, error)
	Open(context.Context, CodeSnapshot) (io.ReadCloser, int64, error)
}

var evaluationCodeIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func ValidCodeSnapshotID(id string) bool { return evaluationCodeIDPattern.MatchString(id) }

func ValidateCodeSnapshot(code CodeSnapshot) error {
	if !ValidCodeSnapshotID(code.ID) || !shaPattern.MatchString(code.SHA256) || code.SizeBytes < 1 || code.SizeBytes > MaxEvaluationCodeSize || code.Format != "zip" {
		return invalid("evaluation code snapshot is invalid")
	}
	return nil
}
