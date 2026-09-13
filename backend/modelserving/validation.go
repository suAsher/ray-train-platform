package modelserving

import (
	"encoding/json"
	"path"
	me "ray-train-platform-backend/modelevaluation"
	ml "ray-train-platform-backend/modellifecycle"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var modulePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
var jobIDPattern = regexp.MustCompile(`^job-[0-9a-f]{24}$`)

func ValidateContract(c Contract) error {
	if (c.ID != "" && !identifierPattern.MatchString(c.ID)) || utf8.RuneCountInString(c.OutputDescription) > 4000 || len(c.InputExample) > MaxInputExampleBytes || !json.Valid(c.InputExample) || c.Code == nil {
		return ErrInvalid
	}
	e := me.Evaluator{Name: c.Name, Description: c.Description, OwnerID: c.OwnerID, TenantID: c.TenantID, ImageReference: c.ImageReference, ImageDigest: c.ImageDigest, Code: c.Code, EntryPoint: c.EntryPoint, SchemaVersion: "serving", Protocol: me.Protocol}
	if me.ValidateEvaluator(e) != nil {
		return ErrInvalid
	}
	if len(c.EntryPoint) < 2 || c.EntryPoint[0] != "python" {
		return ErrInvalid
	}
	entry := c.EntryPoint[1]
	if entry == "-m" {
		if len(c.EntryPoint) < 3 || !modulePattern.MatchString(c.EntryPoint[2]) {
			return ErrInvalid
		}
	} else if strings.HasPrefix(entry, "-") || path.IsAbs(entry) || path.Clean(entry) != entry || strings.HasPrefix(entry, "../") || !strings.HasSuffix(entry, ".py") || strings.Contains(entry, "\\") {
		return ErrInvalid
	}
	return nil
}
func ValidateDeployment(d Deployment) error {
	for _, id := range []string{d.ID, d.ReleaseID, d.ModelID, d.VersionID, d.OwnerID, d.TenantID, d.Contract.ID} {
		if !identifierPattern.MatchString(id) {
			return ErrInvalid
		}
	}
	if !jobIDPattern.MatchString(d.JobID) {
		return ErrInvalid
	}
	if strings.TrimSpace(d.Name) == "" || utf8.RuneCountInString(d.Name) > 200 || strings.IndexFunc(d.Name, unicode.IsControl) >= 0 || !digestPattern.MatchString(d.ModelSHA256) || !digestPattern.MatchString(d.RequestSHA256) || d.ModelSizeBytes < 1 || d.ModelSizeBytes > ml.MaxFileSize {
		return ErrInvalid
	}
	if d.FileName == "" || d.FileName == "." || d.FileName == ".." || path.Base(d.FileName) != d.FileName || len(d.FileName) > 255 || strings.ContainsAny(d.FileName, "\\\x00\r\n") {
		return ErrInvalid
	}
	if len(d.IdempotencyKey) < 1 || len(d.IdempotencyKey) > 128 {
		return ErrInvalid
	}
	for _, ch := range d.IdempotencyKey {
		if ch < 33 || ch > 126 {
			return ErrInvalid
		}
	}
	ttl := d.ExpiresAt.Sub(d.CreatedAt)
	if d.CreatedAt.IsZero() || ttl < time.Hour || ttl > 7*24*time.Hour {
		return ErrInvalid
	}
	if ValidateContract(d.Contract) != nil || me.ValidateResources(d.Resources) != nil || d.Resources.GPUsPerWorker != 1 {
		return ErrInvalid
	}
	return nil
}
