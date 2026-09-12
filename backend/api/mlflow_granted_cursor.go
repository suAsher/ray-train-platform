package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"ray-train-platform-backend/mlflowtracking"
	"strings"
	"time"
)

type integrationCursor struct {
	Tenant      string `json:"t"`
	Subject     string `json:"s"`
	Integration string `json:"i"`
	Kind        string `json:"k"`
	Experiment  string `json:"e"`
	Inner       string `json:"v"`
	Expires     int64  `json:"x"`
}

func (s *GrantedMLflowTracking) wrapCursor(a mlflowtracking.Actor, kind, experiment, inner string) string {
	raw, _ := json.Marshal(integrationCursor{a.TenantID, a.UserID, a.IntegrationID, kind, experiment, inner, time.Now().Add(30 * time.Minute).Unix()})
	mac := hmac.New(sha256.New, s.cursorKey)
	mac.Write(raw)
	return "ig1." + base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *GrantedMLflowTracking) unwrapCursor(a mlflowtracking.Actor, kind, experiment, token string) (string, error) {
	if len(s.cursorKey) < 32 {
		return "", mlflowtracking.ErrUnavailable
	}
	if token == "" {
		return "", nil
	}
	parts := strings.Split(token, ".")
	if len(token) > 4096 || len(parts) != 3 || parts[0] != "ig1" {
		return "", mlflowtracking.ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", mlflowtracking.ErrInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", mlflowtracking.ErrInvalid
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", mlflowtracking.ErrInvalid
	}
	var c integrationCursor
	if json.Unmarshal(raw, &c) != nil || c.Tenant != a.TenantID || c.Subject != a.UserID || c.Integration != a.IntegrationID || c.Kind != kind || c.Experiment != experiment || c.Expires <= time.Now().Unix() {
		return "", mlflowtracking.ErrInvalid
	}
	return c.Inner, nil
}
