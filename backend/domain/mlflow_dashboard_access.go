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

type MLflowDashboardSessionClaims struct {
	TenantID  string
	Subject   string
	Nonce     string
	ExpiresAt time.Time
}

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
	claims, err := VerifyMLflowDashboardSessionClaims(token, key, now)
	if err != nil {
		return err
	}
	if claims.TenantID != expectedTenantID || claims.Subject != expectedSubject {
		return fmt.Errorf("invalid MLflow dashboard session")
	}
	return nil
}

func VerifyMLflowDashboardSessionClaims(token string, key []byte, now time.Time) (MLflowDashboardSessionClaims, error) {
	if err := validatePATPepper(key); err != nil {
		return MLflowDashboardSessionClaims{}, err
	}

	payload, signatureText, found := strings.Cut(token, ".")
	if !found || payload == "" || signatureText == "" || len(signatureText) != sha256.Size*2 || strings.Contains(signatureText, ".") {
		return MLflowDashboardSessionClaims{}, fmt.Errorf("malformed MLflow dashboard session")
	}
	signature, err := hex.DecodeString(signatureText)
	if err != nil || len(signature) != sha256.Size {
		return MLflowDashboardSessionClaims{}, fmt.Errorf("malformed MLflow dashboard session")
	}
	expectedSignature := mlflowDashboardSessionSignature(payload, key)
	if !hmac.Equal(signature, expectedSignature) {
		return MLflowDashboardSessionClaims{}, fmt.Errorf("invalid MLflow dashboard session")
	}

	decodedPayload, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || base64.RawURLEncoding.EncodeToString(decodedPayload) != payload {
		return MLflowDashboardSessionClaims{}, fmt.Errorf("malformed MLflow dashboard session")
	}
	claims := strings.Split(string(decodedPayload), "\x00")
	if len(claims) != 4 || strings.TrimSpace(claims[0]) == "" || strings.TrimSpace(claims[1]) == "" || strings.TrimSpace(claims[2]) == "" || strings.TrimSpace(claims[3]) == "" {
		return MLflowDashboardSessionClaims{}, fmt.Errorf("malformed MLflow dashboard session")
	}
	expiry, err := strconv.ParseInt(claims[3], 10, 64)
	if err != nil {
		return MLflowDashboardSessionClaims{}, fmt.Errorf("malformed MLflow dashboard session")
	}
	if now.UTC().Unix() > expiry {
		return MLflowDashboardSessionClaims{}, fmt.Errorf("MLflow dashboard session has expired")
	}
	return MLflowDashboardSessionClaims{
		TenantID: claims[0], Subject: claims[1], Nonce: claims[2], ExpiresAt: time.Unix(expiry, 0).UTC(),
	}, nil
}

func mlflowDashboardSessionSignature(payload string, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("mlflow-dashboard-session\x00" + payload))
	return mac.Sum(nil)
}
