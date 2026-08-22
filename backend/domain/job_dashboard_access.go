package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// JobDashboardAccessTokenTTL keeps the credential carried in the first URL
	// short-lived. The proxy exchanges it for a path-scoped HttpOnly session
	// and redirects to a clean URL before loading Ray assets.
	JobDashboardAccessTokenTTL = 2 * time.Minute
	JobDashboardSessionTTL     = 8 * time.Hour
)

func IssueJobDashboardAccessToken(tenantID, jobID, userID string, pepper []byte, now time.Time, ttl time.Duration) (string, error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(jobID) == "" || strings.TrimSpace(userID) == "" {
		return "", fmt.Errorf("tenant, job and user are required")
	}
	if err := validatePATPepper(pepper); err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = JobDashboardAccessTokenTTL
	}
	expiry := strconv.FormatInt(now.UTC().Add(ttl).Unix(), 10)
	return expiry + "." + jobDashboardSignature(tenantID, jobID, userID, expiry, pepper), nil
}

func VerifyJobDashboardAccessToken(token, tenantID, jobID, userID string, pepper []byte, now time.Time) error {
	expiryText, signature, found := strings.Cut(token, ".")
	if !found || expiryText == "" || signature == "" {
		return fmt.Errorf("malformed job dashboard access token")
	}
	expiry, err := strconv.ParseInt(expiryText, 10, 64)
	if err != nil {
		return fmt.Errorf("malformed job dashboard access token")
	}
	expected := jobDashboardSignature(tenantID, jobID, userID, expiryText, pepper)
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return fmt.Errorf("invalid job dashboard access token")
	}
	if now.UTC().Unix() > expiry {
		return fmt.Errorf("job dashboard access token has expired")
	}
	return nil
}

func jobDashboardSignature(tenantID, jobID, userID, expiry string, pepper []byte) string {
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte("job-dashboard-access\x00" + tenantID + "\x00" + jobID + "\x00" + userID + "\x00" + expiry))
	return hex.EncodeToString(mac.Sum(nil))
}
