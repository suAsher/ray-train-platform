package registryauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func identityTokenBody(username string, overrides map[string]any) string {
	now := time.Now().Unix()
	claims := map[string]any{"iss": "harbor-token-issuer", "sub": username, "aud": registryService, "iat": now, "nbf": now, "exp": now + 3600, "access": nil}
	for key, value := range overrides {
		claims[key] = value
	}
	payload, _ := json.Marshal(claims)
	body, _ := json.Marshal(map[string]string{"token": "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".fixture-signature"})
	return string(body)
}

func TestAuthenticateCLISecretDoesNotRequireManagementAPIAccess(t *testing.T) {
	client := testClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/service/token" {
			return reply(401, "management API requires a different session"), nil
		}
		if request.URL.Query().Has("scope") || request.URL.Query().Get("service") != registryService || request.URL.Query().Get("account") != credentials.Username {
			t.Fatal("identity lookup requested repository privileges")
		}
		return reply(200, identityTokenBody(credentials.Username, nil)), nil
	})
	identity, err := client.Authenticate(context.Background(), credentials)
	if err != nil || identity.Username != credentials.Username {
		t.Fatalf("valid registry CLI Secret rejected: %+v %v", identity, err)
	}
}

func TestAuthenticateRejectsAnonymousExpiredForeignOrPrematureIdentityToken(t *testing.T) {
	for _, overrides := range []map[string]any{
		{"sub": ""}, {"sub": "another-user"}, {"iss": "foreign-issuer"}, {"aud": "foreign-registry"},
		{"exp": time.Now().Add(-time.Minute).Unix()}, {"nbf": time.Now().Add(time.Hour).Unix()},
		{"iat": time.Now().Add(time.Hour).Unix()}, {"iat": 0}, {"access": []any{map[string]any{"name": "unexpected-scope"}}},
	} {
		client := testClient(func(*http.Request) (*http.Response, error) {
			return reply(200, identityTokenBody(credentials.Username, overrides)), nil
		})
		if _, err := client.Authenticate(context.Background(), credentials); !errors.Is(err, ErrCredentials) {
			t.Fatalf("unsafe identity token accepted: %v", err)
		}
	}
}

func TestProjectManagementDenialDoesNotInvalidateRegistryIdentity(t *testing.T) {
	for _, status := range []int{401, 403} {
		client := testClient(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path == "/api/v2.0/projects" {
				return reply(status, "management API denied"), nil
			}
			return reply(200, identityTokenBody(credentials.Username, nil)), nil
		})
		if _, err := client.Authenticate(context.Background(), credentials); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Projects(context.Background(), credentials, 1, 50); !errors.Is(err, ErrProjectsUnavailable) || errors.Is(err, ErrCredentials) {
			t.Fatalf("project list denial misclassified: %v", err)
		}
	}
}
