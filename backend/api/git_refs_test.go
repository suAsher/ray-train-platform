package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	k8sfake "k8s.io/client-go/kubernetes/fake"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
)

type fakeGitRefResolver struct {
	repositoryURL string
	ref           string
	username      string
	token         string
	result        GitResolvedRef
	err           error
}

func (resolver *fakeGitRefResolver) ResolveRef(_ context.Context, repositoryURL, ref, username, token string) (GitResolvedRef, error) {
	resolver.repositoryURL, resolver.ref, resolver.username, resolver.token = repositoryURL, ref, username, token
	return resolver.result, resolver.err
}

func decodeResolvedRef(t *testing.T, body []byte) GitResolvedRef {
	t.Helper()
	var envelope struct {
		Success bool           `json:"success"`
		Data    GitResolvedRef `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !envelope.Success {
		t.Fatalf("expected successful envelope, got %s", string(body))
	}
	return envelope.Data
}

// Submissions must pin a commit so the same job always runs the same code.
// Making the user copy a SHA out of GitLab is the friction this removes; the
// platform resolves the branch on the user's behalf and still stores a commit.
func TestResolveGitRefTurnsABranchIntoAPinnedCommit(t *testing.T) {
	resolver := &fakeGitRefResolver{result: GitResolvedRef{Ref: "bev_3dod", RefType: "branch", Commit: "0c1dc9d1f2e3a4b5c6d7e8f90a1b2c3d4e5f6071"}}
	handler := NewHandler(&fakeJobRepository{}, Options{
		GitAllowlist: []string{"gitlab.qomolo.com"}, GitRefResolver: resolver,
		GitCredentials: &fakeGitCredentialStore{}, Kubernetes: k8s.NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset()),
	})
	router := gitCredentialRouter(handler, auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}})

	request := httptest.NewRequest(http.MethodPost, "/api/v1/git/resolve-ref", strings.NewReader(`{"repositoryUrl":"https://gitlab.qomolo.com/dl/bevfusion.git","ref":"bev_3dod"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	resolved := decodeResolvedRef(t, response.Body.Bytes())
	if resolved.Commit != "0c1dc9d1f2e3a4b5c6d7e8f90a1b2c3d4e5f6071" || resolved.RefType != "branch" {
		t.Fatalf("expected a pinned branch commit, got %+v", resolved)
	}
	if resolver.repositoryURL != "https://gitlab.qomolo.com/dl/bevfusion.git" {
		t.Fatalf("expected the requested repository, got %q", resolver.repositoryURL)
	}
}

// The resolver is an outbound HTTP client running inside the control plane. It
// must stay behind the same allowlist as job submission, or it becomes an SSRF
// primitive that reaches any host the backend can route to.
func TestResolveGitRefRejectsAHostOutsideTheAllowlist(t *testing.T) {
	resolver := &fakeGitRefResolver{}
	handler := NewHandler(&fakeJobRepository{}, Options{
		GitAllowlist: []string{"gitlab.qomolo.com"}, GitRefResolver: resolver,
		GitCredentials: &fakeGitCredentialStore{}, Kubernetes: k8s.NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset()),
	})
	router := gitCredentialRouter(handler, auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}})

	request := httptest.NewRequest(http.MethodPost, "/api/v1/git/resolve-ref", strings.NewReader(`{"repositoryUrl":"https://internal.metadata.local/repo.git","ref":"main"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
	}
	if resolver.repositoryURL != "" {
		t.Fatalf("resolver must not be reached for a rejected host, got %q", resolver.repositoryURL)
	}
}

// A private repository resolves with the caller's own saved credential. The
// response never echoes it back.
func TestResolveGitRefUsesTheCallersPersonalCredential(t *testing.T) {
	client := k8s.NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset())
	if err := client.EnsureGitCredentialSecret(context.Background(), "tenant-team-a", "git-cred-secret", "alice", "personal-token"); err != nil {
		t.Fatalf("seed credential secret: %v", err)
	}
	store := &fakeGitCredentialStore{credentials: []domain.GitCredential{{
		ID: "credential-1", TenantID: "team-a", Scope: domain.GitCredentialScopePersonal, OwnerUserID: "user-a",
		Name: "内网 GitLab", Host: "gitlab.qomolo.com", Username: "alice", SecretName: "git-cred-secret", CreatedBy: "user-a",
	}}}
	resolver := &fakeGitRefResolver{result: GitResolvedRef{Ref: "bev_3dod_s1h", RefType: "branch", Commit: "7931ceeabcdef0123456789abcdef0123456789a"}}
	handler := NewHandler(&fakeJobRepository{}, Options{
		GitAllowlist: []string{"gitlab.qomolo.com"}, GitRefResolver: resolver, GitCredentials: store, Kubernetes: client,
	})
	router := gitCredentialRouter(handler, auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}})

	request := httptest.NewRequest(http.MethodPost, "/api/v1/git/resolve-ref", strings.NewReader(`{"repositoryUrl":"https://gitlab.qomolo.com/dl/bevfusion.git","ref":"bev_3dod_s1h"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if resolver.username != "alice" || resolver.token != "personal-token" {
		t.Fatalf("expected the saved credential to be used, got %q/%q", resolver.username, resolver.token)
	}
	if strings.Contains(response.Body.String(), "personal-token") {
		t.Fatalf("the token must never be echoed back: %s", response.Body.String())
	}
}

func TestResolveGitRefReportsAnUnknownBranchAsNotFound(t *testing.T) {
	resolver := &fakeGitRefResolver{err: ErrGitRefNotFound}
	handler := NewHandler(&fakeJobRepository{}, Options{
		GitAllowlist: []string{"gitlab.qomolo.com"}, GitRefResolver: resolver,
		GitCredentials: &fakeGitCredentialStore{}, Kubernetes: k8s.NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset()),
	})
	router := gitCredentialRouter(handler, auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}})

	request := httptest.NewRequest(http.MethodPost, "/api/v1/git/resolve-ref", strings.NewReader(`{"repositoryUrl":"https://gitlab.qomolo.com/dl/bevfusion.git","ref":"no-such-branch"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", response.Code, response.Body.String())
	}
}

// A full 40-character SHA needs no round trip to the Git host at all.
func TestResolveGitRefAcceptsAnAlreadyPinnedCommitWithoutContactingTheHost(t *testing.T) {
	resolver := &fakeGitRefResolver{}
	handler := NewHandler(&fakeJobRepository{}, Options{
		GitAllowlist: []string{"gitlab.qomolo.com"}, GitRefResolver: resolver,
		GitCredentials: &fakeGitCredentialStore{}, Kubernetes: k8s.NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset()),
	})
	router := gitCredentialRouter(handler, auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}})

	request := httptest.NewRequest(http.MethodPost, "/api/v1/git/resolve-ref", strings.NewReader(`{"repositoryUrl":"https://gitlab.qomolo.com/dl/bevfusion.git","ref":"0c1dc9d1f2e3a4b5c6d7e8f90a1b2c3d4e5f6071"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	resolved := decodeResolvedRef(t, response.Body.Bytes())
	if resolved.RefType != "commit" || resolved.Commit != "0c1dc9d1f2e3a4b5c6d7e8f90a1b2c3d4e5f6071" {
		t.Fatalf("expected the commit to pass through, got %+v", resolved)
	}
	if resolver.repositoryURL != "" {
		t.Fatalf("a pinned commit must not contact the Git host, got %q", resolver.repositoryURL)
	}
}

func TestParseGitRefAdvertisementPrefersTheAnnotatedTagTarget(t *testing.T) {
	advertisement := packetLines(
		"# service=git-upload-pack\n",
		"",
		"0c1dc9d1f2e3a4b5c6d7e8f90a1b2c3d4e5f6071 HEAD\x00multi_ack symref=HEAD:refs/heads/main\n",
		"1111111111111111111111111111111111111111 refs/heads/main\n",
		"2222222222222222222222222222222222222222 refs/tags/v1.0\n",
		"3333333333333333333333333333333333333333 refs/tags/v1.0^{}\n",
	)
	resolved, err := parseGitRefAdvertisement(advertisement, "v1.0")
	if err != nil {
		t.Fatalf("parse advertisement: %v", err)
	}
	if resolved.Commit != "3333333333333333333333333333333333333333" || resolved.RefType != "tag" {
		t.Fatalf("expected the dereferenced tag commit, got %+v", resolved)
	}
}

func TestParseGitRefAdvertisementResolvesABranch(t *testing.T) {
	advertisement := packetLines(
		"# service=git-upload-pack\n",
		"",
		"1111111111111111111111111111111111111111 refs/heads/bev_3dod\n",
		"2222222222222222222222222222222222222222 refs/heads/bev_3dod_s1h\n",
	)
	resolved, err := parseGitRefAdvertisement(advertisement, "bev_3dod_s1h")
	if err != nil {
		t.Fatalf("parse advertisement: %v", err)
	}
	if resolved.Commit != "2222222222222222222222222222222222222222" || resolved.RefType != "branch" {
		t.Fatalf("expected the exact branch, got %+v", resolved)
	}
}

func TestParseGitRefAdvertisementReportsMissingRef(t *testing.T) {
	advertisement := packetLines("# service=git-upload-pack\n", "", "1111111111111111111111111111111111111111 refs/heads/main\n")
	if _, err := parseGitRefAdvertisement(advertisement, "absent"); err != ErrGitRefNotFound {
		t.Fatalf("expected ErrGitRefNotFound, got %v", err)
	}
}

// packetLines renders Git's pkt-line framing; an empty entry is a flush packet.
func packetLines(lines ...string) []byte {
	var builder strings.Builder
	for _, line := range lines {
		if line == "" {
			builder.WriteString("0000")
			continue
		}
		builder.WriteString(pktLength(len(line) + 4))
		builder.WriteString(line)
	}
	return []byte(builder.String())
}

func pktLength(length int) string {
	const hexDigits = "0123456789abcdef"
	return string([]byte{
		hexDigits[(length>>12)&0xf], hexDigits[(length>>8)&0xf],
		hexDigits[(length>>4)&0xf], hexDigits[length&0xf],
	})
}
