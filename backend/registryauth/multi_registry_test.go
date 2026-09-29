package registryauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
)

func selectedTestClient(t *testing.T, host string, transport roundTripFunc) *Client {
	t.Helper()
	client, err := NewClientForHost(host)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = transport
	return client
}

func TestNormalizeHostAllowsOnlyCanonicalHarborHosts(t *testing.T) {
	for _, input := range []string{"", Host, QomoloHost} {
		want := input
		if want == "" {
			want = Host
		}
		got, err := NormalizeHost(input)
		if err != nil || got != want {
			t.Fatalf("canonical host: got=%q error=%v", got, err)
		}
	}
	for _, input := range []string{
		"https://" + Host, "http://" + QomoloHost, Host + ":443", QomoloHost + ":443",
		"user:fixture-private-secret@" + QomoloHost, QomoloHost + "/", Host + ".",
		strings.ToUpper(QomoloHost), " " + Host, QomoloHost + " ", "evil.invalid", Host + ".evil.invalid",
		QomoloHost + "?token=fixture-private-secret", QomoloHost + "#fragment", QomoloHost + "\n",
	} {
		if got, err := NormalizeHost(input); !errors.Is(err, ErrInvalidTarget) || got != "" || strings.Contains(err.Error(), "fixture-private-secret") {
			t.Fatalf("unsafe normalization: host=%q error=%v", got, err)
		}
		if client, err := NewClientForHost(input); client != nil || !errors.Is(err, ErrInvalidTarget) {
			t.Fatalf("invalid registry constructed a client: error=%v", err)
		}
	}
}

func TestSelectedRegistryBindsIdentityProjectsAndRepositoryGrant(t *testing.T) {
	for _, input := range []string{"", Host, QomoloHost} {
		t.Run("registry_"+input, func(t *testing.T) {
			host := input
			if host == "" {
				host = Host
			}
			client := selectedTestClient(t, input, func(request *http.Request) (*http.Response, error) {
				if request.URL.Host != host || request.URL.Scheme != "https" {
					t.Fatal("personal credential left selected HTTPS origin")
				}
				username, secret, ok := request.BasicAuth()
				if !ok || username != credentials.Username || secret != credentials.Secret {
					t.Fatal("personal credential changed")
				}
				if request.URL.Path == "/api/v2.0/projects" {
					return reply(200, `[{"project_id":9,"name":"team","current_user_role_id":2}]`), nil
				}
				if request.URL.Path != "/service/token" || request.URL.Query().Get("account") != credentials.Username || request.URL.Query().Get("service") != registryService {
					t.Fatal("unexpected registry identity endpoint")
				}
				if request.URL.Query().Get("scope") == "" {
					return reply(200, identityTokenBody(credentials.Username, nil)), nil
				}
				if request.URL.Query().Get("scope") != "repository:team/model:pull,push" {
					t.Fatal("grant was not bound to selected repository")
				}
				body, _ := json.Marshal(map[string]string{"token": jwt([]string{"pull", "push"}, "team/model")})
				return reply(200, string(body)), nil
			})
			if identity, err := client.Authenticate(context.Background(), credentials); err != nil || identity.Username != credentials.Username {
				t.Fatalf("identity failed: %v", err)
			}
			if projects, err := client.Projects(context.Background(), credentials, 1, 10); err != nil || len(projects.Items) != 1 || !projects.Items[0].CanPush {
				t.Fatalf("project listing failed: %v", err)
			}
			if target, err := client.CheckPush(context.Background(), credentials, "team", "model"); err != nil || target.Repository != "team/model" {
				t.Fatalf("repository check failed: %v", err)
			}
		})
	}
}

func TestSelectedRegistryRejectsCrossHarborQueryRedirectAndSanitizesErrors(t *testing.T) {
	for _, host := range []string{Host, QomoloHost} {
		other := QomoloHost
		if host == QomoloHost {
			other = Host
		}
		for _, operation := range []func(*Client) error{
			func(c *Client) error { _, err := c.Authenticate(context.Background(), credentials); return err },
			func(c *Client) error { _, err := c.Projects(context.Background(), credentials, 1, 10); return err },
			func(c *Client) error {
				_, err := c.CheckPush(context.Background(), credentials, "team", "model")
				return err
			},
		} {
			for _, networkFailure := range []bool{false, true} {
				calls := 0
				client := selectedTestClient(t, host, func(request *http.Request) (*http.Response, error) {
					calls++
					if request.URL.Host != host {
						t.Fatal("query followed redirect to another Harbor")
					}
					if networkFailure {
						return nil, errors.New("fixture-private-secret")
					}
					response := reply(http.StatusTemporaryRedirect, "fixture-private-secret")
					response.Header.Set("Location", "https://"+other+"/service/token?token=fixture-private-secret")
					return response, nil
				})
				if err := operation(client); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "fixture-private-secret") || calls != 1 {
					t.Fatalf("query did not fail closed: error=%v calls=%d", err, calls)
				}
			}
		}
	}
}

func TestSelectedRegistryRejectsPullOnlyOrDifferentRepositoryGrant(t *testing.T) {
	for _, host := range []string{Host, QomoloHost} {
		for _, token := range []string{jwt([]string{"pull"}, "team/model"), jwt([]string{"push"}, "other/model")} {
			client := selectedTestClient(t, host, func(request *http.Request) (*http.Response, error) {
				if request.URL.Host != host {
					t.Fatal("grant requested from wrong registry")
				}
				body, _ := json.Marshal(map[string]string{"token": token})
				return reply(200, string(body)), nil
			})
			if _, err := client.CheckPush(context.Background(), credentials, "team", "model"); !errors.Is(err, ErrForbidden) {
				t.Fatalf("nonmatching grant accepted: %v", err)
			}
		}
	}
}

func TestSelectedRegistryPublishesAndVerifiesOnlySelectedTarget(t *testing.T) {
	for _, host := range []string{Host, QomoloHost} {
		t.Run(host, func(t *testing.T) {
			directory, digest := testLayout(t)
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			var manifestWrites, manifestReads atomic.Int64
			client := selectedTestClient(t, host, func(request *http.Request) (*http.Response, error) {
				if request.URL.Scheme != "https" || request.URL.Host != host {
					t.Fatal("publication left selected registry")
				}
				if request.URL.Path == "/service/token" {
					body, _ := json.Marshal(map[string]string{"token": jwt([]string{"pull", "push"}, "team/model")})
					return reply(200, string(body)), nil
				}
				if _, _, ok := request.BasicAuth(); ok {
					t.Fatal("personal password reached upload endpoint")
				}
				if strings.Contains(request.URL.Path, "/manifests/") && request.URL.Path != "/v2/team/model/manifests/frozen-v1" {
					t.Fatal("publication used a different repository or tag")
				}
				if request.URL.Path == "/v2/team/model/manifests/frozen-v1" {
					if request.Method == http.MethodPut {
						manifestWrites.Add(1)
					}
					if request.Method == http.MethodHead {
						manifestReads.Add(1)
					}
				}
				return serveRegistryRequest(handler, request), nil
			})
			result, err := client.Publish(context.Background(), credentials, PublishRequest{LayoutPath: directory, Digest: digest, Project: "team", Repository: "model", Tag: "frozen-v1"})
			if err != nil || result.ImageDigest != digest || manifestWrites.Load() != 1 || manifestReads.Load() < 1 {
				t.Fatalf("selected publication failed: result=%+v error=%v", result, err)
			}
		})
	}
}

func TestQomoloClientCannotAssembleWithWellspikingPullCredentials(t *testing.T) {
	client := selectedTestClient(t, QomoloHost, func(*http.Request) (*http.Response, error) {
		t.Fatal("Qomolo publish client used platform pull credentials")
		return nil, nil
	})
	request := preparedAssemblyRequest(t)
	request.Base = Host + "/team/base@sha256:" + strings.Repeat("a", 64)
	if _, err := client.Assemble(context.Background(), request); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("publish registry client assembled source image: %v", err)
	}
	request.Base = QomoloHost + "/team/base@sha256:" + strings.Repeat("a", 64)
	if _, err := NewClient().Assemble(context.Background(), request); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("default assembler accepted Qomolo source: %v", err)
	}
}
