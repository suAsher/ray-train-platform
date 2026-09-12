package mlflowtracking

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

type cursorClaims struct {
	Tenant     string `json:"t"`
	Owner      string `json:"o"`
	Kind       string `json:"k"`
	Experiment string `json:"e"`
	After      string `json:"a"`
	Expires    int64  `json:"x"`
}

func (s *Service) cursor(actor Actor, kind, experiment, after string) string {
	claims := cursorClaims{actor.TenantID, actor.UserID, kind, experiment, after, s.now().Add(30 * time.Minute).Unix()}
	raw, _ := json.Marshal(claims)
	mac := hmac.New(sha256.New, s.key)
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) after(actor Actor, kind, experiment, token string) (string, error) {
	if token == "" {
		return "", nil
	}
	if len(token) > 2048 {
		return "", ErrInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", ErrInvalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrInvalid
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(raw)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return "", ErrInvalid
	}
	var claims cursorClaims
	if json.Unmarshal(raw, &claims) != nil || claims.Tenant != actor.TenantID || claims.Owner != actor.UserID || claims.Kind != kind || claims.Experiment != experiment || claims.Expires <= s.now().Unix() || !validID(claims.After) {
		return "", ErrInvalid
	}
	return claims.After, nil
}

func (s *Service) ListExperiments(ctx context.Context, actor Actor, limit int, token string) (ExperimentPage, error) {
	if err := s.ready(actor); err != nil {
		return ExperimentPage{}, err
	}
	if limit < 1 || limit > 100 {
		return ExperimentPage{}, ErrInvalid
	}
	after, err := s.after(actor, "experiments", "", token)
	if err != nil {
		return ExperimentPage{}, err
	}
	items, err := s.store.ListExperiments(ctx, actor, after, limit+1)
	if err != nil {
		return ExperimentPage{}, err
	}
	page := ExperimentPage{Items: items}
	if page.Items == nil {
		page.Items = []Experiment{}
	}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = s.cursor(actor, "experiments", "", items[limit-1].ID)
	}
	return page, nil
}

func (s *Service) ListRuns(ctx context.Context, actor Actor, experimentID string, limit int, token string) (RunPage, error) {
	if err := s.ready(actor); err != nil {
		return RunPage{}, err
	}
	if !validID(experimentID) || limit < 1 || limit > 100 {
		return RunPage{}, ErrInvalid
	}
	after, err := s.after(actor, "runs", experimentID, token)
	if err != nil {
		return RunPage{}, err
	}
	if _, err := s.store.GetExperiment(ctx, actor, experimentID); err != nil {
		return RunPage{}, err
	}
	items, err := s.store.ListRuns(ctx, actor, experimentID, after, limit+1)
	if err != nil {
		return RunPage{}, err
	}
	page := RunPage{Items: items}
	if page.Items == nil {
		page.Items = []Run{}
	}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = s.cursor(actor, "runs", experimentID, items[limit-1].ID)
	}
	return page, nil
}
