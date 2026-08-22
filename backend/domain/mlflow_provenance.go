package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// MLflowProvenanceTag binds a run to one platform job without exposing the
// signing key. A workload receives only its own tag and cannot derive the tag
// for another job.
func MLflowProvenanceTag(key []byte, jobID string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("raytrain-mlflow-job:" + jobID))
	return hex.EncodeToString(mac.Sum(nil))
}
