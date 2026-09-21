package registryauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func reply(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func testClient(f roundTripFunc) *Client { c := NewClient(); c.http.Transport = f; return c }

var credentials = Credentials{Username: "test-user", Secret: "opaque:test-secret"}

func jwt(actions []string, repository string) string {
	body, _ := json.Marshal(map[string]any{"access": []any{map[string]any{"type": "repository", "name": repository, "actions": actions}}, "exp": time.Now().Add(time.Hour).Unix(), "aud": "harbor-registry"})
	return "e30." + base64.RawURLEncoding.EncodeToString(body) + ".signature"
}

func TestAuthenticateOnlyTrustedOriginAndIdentity(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != Origin+"/api/v2.0/users/current" {
			t.Fatalf("unexpected destination %s", r.URL)
		}
		u, p, ok := r.BasicAuth()
		if !ok || u != credentials.Username || p != credentials.Secret {
			t.Fatal("missing credentials")
		}
		return reply(200, `{"user_id":7,"username":"test-user"}`), nil
	})
	got, err := c.Authenticate(context.Background(), credentials)
	if err != nil || got.UserID != 7 {
		t.Fatalf("identity=%+v error=%v", got, err)
	}
}

func TestAuthenticateSanitizesErrorsAndRejectsRedirect(t *testing.T) {
	for _, status := range []int{401, 403, 302, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c := testClient(func(r *http.Request) (*http.Response, error) {
				res := reply(status, credentials.Secret)
				res.Header.Set("Location", "https://evil.invalid")
				return res, nil
			})
			_, err := c.Authenticate(context.Background(), credentials)
			if err == nil || strings.Contains(err.Error(), credentials.Secret) {
				t.Fatalf("unsafe error %v", err)
			}
		})
	}
}

func TestCheckPushRequiresExactRepositoryPushGrant(t *testing.T) {
	for _, tc := range []struct {
		name, repo string
		actions    []string
		allowed    bool
	}{
		{"push", "team/model", []string{"pull", "push"}, true}, {"pull only", "team/model", []string{"pull"}, false},
		{"wrong repository", "other/model", []string{"push"}, false}, {"empty scope", "team/model", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/service/token" || r.URL.Query().Get("scope") != "repository:team/model:pull,push" || r.URL.Query().Get("service") != "harbor-registry" {
					t.Fatal("wrong scoped grant request")
				}
				body, _ := json.Marshal(map[string]string{"token": jwt(tc.actions, tc.repo)})
				return reply(200, string(body)), nil
			})
			got, err := c.CheckPush(context.Background(), credentials, "team", "model")
			if tc.allowed {
				if err != nil || got.Repository != "team/model" {
					t.Fatalf("%+v %v", got, err)
				}
			} else if !errors.Is(err, ErrForbidden) {
				t.Fatalf("expected forbidden, got %v", err)
			}
		})
	}
}

func TestCheckPushRejectsInvalidTargetBeforeRequest(t *testing.T) {
	c := testClient(func(*http.Request) (*http.Response, error) { t.Fatal("invalid target sent request"); return nil, nil })
	for _, repo := range []string{"../other", "MODEL", "model:tag", "model@sha256:abc", "https://evil.invalid", "model?scope=all", "model//sub"} {
		if _, err := c.CheckPush(context.Background(), credentials, "team", repo); !errors.Is(err, ErrInvalidTarget) {
			t.Errorf("accepted %q: %v", repo, err)
		}
	}
}

func TestProjectsPageDoesNotFollowUntrustedLink(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v2.0/projects" || r.URL.Query().Get("page") != "2" {
			t.Fatal("wrong page")
		}
		res := reply(200, `[{"project_id":9,"name":"team","current_user_role_id":2}]`)
		res.Header.Set("X-Total-Count", "3")
		res.Header.Set("Link", `<https://evil.invalid>; rel="next"`)
		return res, nil
	})
	got, err := c.Projects(context.Background(), credentials, 2, 1)
	if err != nil || got.NextPage != 3 || len(got.Items) != 1 || !got.Items[0].CanPush {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestOpaqueSecretIsNotRewritten(t *testing.T) {
	secret := Credentials{Username: "name", Secret: " spaces:and!symbols "}
	c := testClient(func(r *http.Request) (*http.Response, error) {
		_, got, _ := r.BasicAuth()
		if got != secret.Secret {
			t.Fatal("opaque secret changed")
		}
		return reply(200, `{"user_id":1,"username":"name"}`), nil
	})
	if _, err := c.Authenticate(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
}

func TestTokenWithoutVerifiableGrantFailsClosed(t *testing.T) {
	for _, token := range []string{"opaque-token", "e30.e30.signature", jwt([]string{"push"}, "other/model")} {
		c := testClient(func(*http.Request) (*http.Response, error) {
			body, _ := json.Marshal(map[string]string{"token": token})
			return reply(200, string(body)), nil
		})
		if _, err := c.CheckPush(context.Background(), credentials, "team", "model"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("missing grant accepted: %v", err)
		}
	}
}

func TestPushGrantRequiresCurrentHarborAudience(t *testing.T) {
	for _, tc := range []struct {
		audience string
		expires  int64
	}{
		{"other-registry", time.Now().Add(time.Hour).Unix()},
		{"harbor-registry", time.Now().Add(-time.Hour).Unix()},
	} {
		body, _ := json.Marshal(map[string]any{"access": []any{map[string]any{"type": "repository", "name": "team/model", "actions": []string{"push"}}}, "aud": tc.audience, "exp": tc.expires})
		token := "e30." + base64.RawURLEncoding.EncodeToString(body) + ".signature"
		if hasPushGrant(token, "team/model", time.Now()) {
			t.Fatal("wrong audience or expired grant accepted")
		}
	}
}
