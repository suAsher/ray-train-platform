package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	MLflowDashboardTicketTTL  = 2 * time.Minute
	MLflowDashboardSessionTTL = 8 * time.Hour
)

func IssueMLflowDashboardSession(tenantID, subject, nonce string, key []byte, now time.Time, ttl time.Duration) (string, error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(subject) == "" || strings.TrimSpace(nonce) == "" {
		return "", fmt.Errorf("tenant, subject and nonce are required")
	}
	if strings.ContainsRune(tenantID, '\x00') || strings.ContainsRune(subject, '\x00') || strings.ContainsRune(nonce, '\x00') {
		return "", fmt.Errorf("tenant, subject and nonce must not contain NUL")
	}
	if err := validatePATPepper(key); err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = MLflowDashboardSessionTTL
	}

	expiry := strconv.FormatInt(now.UTC().Add(ttl).Unix(), 10)
	claims := tenantID + "\x00" + subject + "\x00" + nonce + "\x00" + expiry
	payload := base64.RawURLEncoding.EncodeToString([]byte(claims))
	signature := mlflowDashboardSessionSignature(payload, key)
	return payload + "." + hex.EncodeToString(signature), nil
}

func VerifyMLflowDashboardSession(token, expectedTenantID, expectedSubject string, key []byte, now time.Time) error {
	if strings.TrimSpace(expectedTenantID) == "" || strings.TrimSpace(expectedSubject) == "" {
		return fmt.Errorf("tenant and subject are required")
	}
	if err := validatePATPepper(key); err != nil {
		return err
	}

	payload, signatureText, found := strings.Cut(token, ".")
	if !found || payload == "" || signatureText == "" || len(signatureText) != sha256.Size*2 || strings.Contains(signatureText, ".") {
		return fmt.Errorf("malformed MLflow dashboard session")
	}
	signature, err := hex.DecodeString(signatureText)
	if err != nil || len(signature) != sha256.Size {
		return fmt.Errorf("malformed MLflow dashboard session")
	}
	expectedSignature := mlflowDashboardSessionSignature(payload, key)
	if !hmac.Equal(signature, expectedSignature) {
		return fmt.Errorf("invalid MLflow dashboard session")
	}

	decodedPayload, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || base64.RawURLEncoding.EncodeToString(decodedPayload) != payload {
		return fmt.Errorf("malformed MLflow dashboard session")
	}
	claims := strings.Split(string(decodedPayload), "\x00")
	if len(claims) != 4 || strings.TrimSpace(claims[0]) == "" || strings.TrimSpace(claims[1]) == "" || strings.TrimSpace(claims[2]) == "" || strings.TrimSpace(claims[3]) == "" {
		return fmt.Errorf("malformed MLflow dashboard session")
	}
	expiry, err := strconv.ParseInt(claims[3], 10, 64)
	if err != nil {
		return fmt.Errorf("malformed MLflow dashboard session")
	}
	if claims[0] != expectedTenantID || claims[1] != expectedSubject {
		return fmt.Errorf("invalid MLflow dashboard session")
	}
	if now.UTC().Unix() > expiry {
		return fmt.Errorf("MLflow dashboard session has expired")
	}
	return nil
}

func mlflowDashboardSessionSignature(payload string, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("mlflow-dashboard-session\x00" + payload))
	return mac.Sum(nil)
}
