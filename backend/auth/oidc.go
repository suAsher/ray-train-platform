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
	claims, err := v.verifyClaims(ctx, rawToken)
	if err != nil {
		return Principal{}, err
	}
	return claims.Principal(v.groupPrefix)
}

func (v *Validator) VerifyIdentity(ctx context.Context, rawToken string) (OIDCIdentity, error) {
	claims, err := v.verifyClaims(ctx, rawToken)
	if err != nil {
		return OIDCIdentity{}, err
	}
	if strings.TrimSpace(claims.Subject) == "" {
		return OIDCIdentity{}, fmt.Errorf("OIDC token subject is required")
	}
	username := strings.TrimSpace(claims.PreferredUsername)
	if username == "" {
		return OIDCIdentity{}, fmt.Errorf("OIDC token preferred username is required")
	}
	return OIDCIdentity{Subject: claims.Subject, Username: username, Email: strings.TrimSpace(claims.Email)}, nil
}

func (v *Validator) verifyClaims(ctx context.Context, rawToken string) (TokenClaims, error) {
	if v == nil || v.verifier == nil {
		return TokenClaims{}, fmt.Errorf("OIDC validator is not initialized")
	}
	idToken, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return TokenClaims{}, fmt.Errorf("verify OIDC token: %w", err)
	}
	if !contains(idToken.Audience, v.audience) {
		return TokenClaims{}, fmt.Errorf("OIDC token audience is not allowed")
	}
	var claims TokenClaims
	if err := idToken.Claims(&claims); err != nil {
		return TokenClaims{}, fmt.Errorf("decode OIDC claims: %w", err)
	}
	return claims, nil
}

func contains(items []string, expected string) bool {
	for _, item := range items {
		if item == expected {
			return true
		}
	}
	return false
}
