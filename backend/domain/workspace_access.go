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

// WorkspaceAccessTokenTTL is deliberately short. The token travels in a URL to
// open JupyterLab in a browser tab, which cannot carry an Authorization
// header, so it is exchanged for a path-scoped cookie on the first request.
const WorkspaceAccessTokenTTL = 2 * time.Minute

// IssueWorkspaceAccessToken mints a stateless token bound to one workspace and
// one user. It is signed rather than stored: it lives for a couple of minutes
// and only authorises the proxy path for a workspace the caller already owns.
func IssueWorkspaceAccessToken(workspaceID, userID string, pepper []byte, now time.Time, ttl time.Duration) (string, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(userID) == "" {
		return "", fmt.Errorf("workspace and user are required")
	}
	if err := validatePATPepper(pepper); err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = WorkspaceAccessTokenTTL
	}
	expiry := now.UTC().Add(ttl).Unix()
	payload := strconv.FormatInt(expiry, 10)
	return payload + "." + workspaceAccessSignature(workspaceID, userID, payload, pepper), nil
}

func VerifyWorkspaceAccessToken(token, workspaceID, userID string, pepper []byte, now time.Time) error {
	payload, signature, found := strings.Cut(token, ".")
	if !found || payload == "" || signature == "" {
		return fmt.Errorf("malformed workspace access token")
	}
	expiry, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return fmt.Errorf("malformed workspace access token")
	}
	expected := workspaceAccessSignature(workspaceID, userID, payload, pepper)
	// Constant-time comparison so a wrong signature cannot be probed byte by
	// byte through timing.
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return fmt.Errorf("invalid workspace access token")
	}
	if now.UTC().Unix() > expiry {
		return fmt.Errorf("workspace access token has expired")
	}
	return nil
}

func workspaceAccessSignature(workspaceID, userID, payload string, pepper []byte) string {
	mac := hmac.New(sha256.New, pepper)
	// The separator keeps the fields unambiguous, so a workspace/user pair
	// cannot be rearranged into the same signed message.
	_, _ = mac.Write([]byte("workspace-access\x00" + workspaceID + "\x00" + userID + "\x00" + payload))
	return hex.EncodeToString(mac.Sum(nil))
}
