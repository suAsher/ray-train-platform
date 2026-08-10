package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

type Validator struct {
	verifier    *oidc.IDTokenVerifier
	audience    string
	groupPrefix string
}

func NewValidator(ctx context.Context, issuer, clientID, audience, groupPrefix string) (*Validator, error) {
	if strings.TrimSpace(issuer) == "" {
		return nil, fmt.Errorf("OIDC issuer is required")
	}
	if strings.TrimSpace(clientID) == "" {
		return nil, fmt.Errorf("OIDC client ID is required")
	}
	if strings.TrimSpace(audience) == "" {
		return nil, fmt.Errorf("OIDC audience is required")
	}
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: clientID, SkipClientIDCheck: true})
	return &Validator{verifier: verifier, audience: audience, groupPrefix: groupPrefix}, nil
}

func (v *Validator) Verify(ctx context.Context, rawToken string) (Principal, error) {
	if v == nil || v.verifier == nil {
		return Principal{}, fmt.Errorf("OIDC validator is not initialized")
	}
	idToken, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return Principal{}, fmt.Errorf("verify OIDC token: %w", err)
	}
	if !contains(idToken.Audience, v.audience) {
		return Principal{}, fmt.Errorf("OIDC token audience is not allowed")
	}
	var claims TokenClaims
	if err := idToken.Claims(&claims); err != nil {
		return Principal{}, fmt.Errorf("decode OIDC claims: %w", err)
	}
	return claims.Principal(v.groupPrefix)
}

func contains(items []string, expected string) bool {
	for _, item := range items {
		if item == expected {
			return true
		}
	}
	return false
}
