package auth

import "context"

type verifiedOAuth2TokenKey struct{}

// VerifiedOAuth2AccessToken returns only the token verified by the OAuth2
// middleware for this request. It is never part of the public Principal or a
// persistent job specification. Callers must restrict its upstream destination.
func VerifiedOAuth2AccessToken(ctx context.Context) (string, bool) {
	token, ok := ctx.Value(verifiedOAuth2TokenKey{}).(string)
	return token, ok && token != ""
}
