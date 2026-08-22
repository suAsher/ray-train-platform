package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

// ErrGitRefNotFound distinguishes "the host answered, this branch does not
// exist" from a transport failure, so the Portal can tell the user to check the
// branch name instead of blaming the network.
var ErrGitRefNotFound = errors.New("git ref was not found")

var fullCommitSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// GitResolvedRef is the pinned result of a branch or tag lookup. A job records
// the commit, never the moving ref, so re-running it always runs the same code.
type GitResolvedRef struct {
	Ref     string `json:"ref"`
	RefType string `json:"refType"`
	Commit  string `json:"commit"`
}

type GitRefResolver interface {
	ResolveRef(ctx context.Context, repositoryURL, ref, username, token string) (GitResolvedRef, error)
}

type resolveGitRefRequest struct {
	RepositoryURL string `json:"repositoryUrl"`
	Ref           string `json:"ref"`
}

func (h *Handler) resolveGitRef(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if !principal.Allowed(domain.RoleEngineer) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "engineer role is required")
		return
	}
	if h.gitRefResolver == nil {
		h.writeError(c, http.StatusServiceUnavailable, "GIT_REF_RESOLVER_UNAVAILABLE", "Git branch resolution is not configured")
		return
	}
	var request resolveGitRefRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	ref := strings.TrimSpace(request.Ref)
	if ref == "" {
		h.writeError(c, http.StatusBadRequest, "GIT_REF_REQUIRED", "a branch, tag or commit is required")
		return
	}
	repositoryURL, err := validateAllowlistedGitRepositoryURL(request.RepositoryURL, h.gitAllowlist)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_GIT_REPOSITORY", err.Error())
		return
	}
	// An already-pinned commit is what submission stores anyway; resolving it
	// would be a pointless outbound request.
	if fullCommitSHA.MatchString(ref) {
		h.writeSuccess(c, http.StatusOK, GitResolvedRef{Ref: ref, RefType: "commit", Commit: strings.ToLower(ref)})
		return
	}
	username, token := h.gitCredentialForRepository(c.Request.Context(), principal, repositoryURL)
	resolved, err := h.gitRefResolver.ResolveRef(c.Request.Context(), repositoryURL, ref, username, token)
	if errors.Is(err, ErrGitRefNotFound) {
		h.writeError(c, http.StatusNotFound, "GIT_REF_NOT_FOUND", "该仓库中没有这个分支、标签或提交；请确认名称，私有仓库还需要在「账户与安全」保存 Git 凭据")
		return
	}
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "GIT_REF_RESOLVE_FAILED", "无法连接 Git 仓库解析分支，请稍后重试或检查凭据权限")
		return
	}
	h.writeSuccess(c, http.StatusOK, resolved)
}

// gitCredentialForRepository picks the caller's own credential first and falls
// back to the tenant's shared one. Missing credentials are not an error: public
// repositories resolve anonymously.
func (h *Handler) gitCredentialForRepository(ctx context.Context, principal auth.Principal, repositoryURL string) (string, string) {
	if h.gitCredentials == nil || h.kubernetes == nil {
		return "", ""
	}
	host := gitHostOf(repositoryURL)
	if host == "" {
		return "", ""
	}
	namespace := "tenant-" + sanitizeDNS(principal.TenantID)
	scopes := []struct {
		scope  domain.GitCredentialScope
		userID string
	}{
		{domain.GitCredentialScopePersonal, principal.Subject},
		{domain.GitCredentialScopeTeam, ""},
	}
	for _, candidate := range scopes {
		credentials, err := h.gitCredentials.ListGitCredentials(ctx, principal.TenantID, candidate.userID, candidate.scope)
		if err != nil {
			continue
		}
		for _, credential := range credentials {
			if domain.NormalizeGitHost(credential.Host) != host || credential.SecretName == "" {
				continue
			}
			username, token, readErr := h.kubernetes.ReadGitCredentialSecret(ctx, namespace, credential.SecretName)
			if readErr == nil && token != "" {
				return username, token
			}
		}
	}
	return "", ""
}

func gitHostOf(repositoryURL string) string {
	parsed, err := parseHTTPSURL(repositoryURL)
	if err != nil {
		return ""
	}
	return domain.NormalizeGitHost(parsed.Hostname())
}

type httpsGitRefResolver struct {
	client *http.Client
}

func newHTTPSGitRefResolver() GitRefResolver {
	return httpsGitRefResolver{client: &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) == 0 || request.URL.Hostname() != via[0].URL.Hostname() || request.URL.Scheme != "https" {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}}
}

// maxAdvertisementBytes bounds the ref advertisement a Git host may return. A
// repository with a very large ref namespace must not be able to exhaust the
// control plane's memory through this endpoint.
const maxAdvertisementBytes = 8 * 1024 * 1024

func (resolver httpsGitRefResolver) ResolveRef(ctx context.Context, repositoryURL, ref, username, token string) (GitResolvedRef, error) {
	endpoint, err := gitAdvertisementURL(repositoryURL)
	if err != nil {
		return GitResolvedRef{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return GitResolvedRef{}, fmt.Errorf("build Git ref advertisement request: %w", err)
	}
	request.Header.Set("Accept", "application/x-git-upload-pack-advertisement")
	if token != "" {
		if username == "" {
			username = "git"
		}
		request.SetBasicAuth(username, token)
	}
	response, err := resolver.client.Do(request)
	if err != nil {
		return GitResolvedRef{}, fmt.Errorf("contact Git host: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound {
		return GitResolvedRef{}, ErrGitRefNotFound
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return GitResolvedRef{}, fmt.Errorf("git host returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAdvertisementBytes))
	if err != nil {
		return GitResolvedRef{}, fmt.Errorf("read Git ref advertisement: %w", err)
	}
	return parseGitRefAdvertisement(body, ref)
}

// parseGitRefAdvertisement reads Git's smart HTTP pkt-line advertisement. An
// annotated tag advertises both the tag object and its dereferenced commit
// (`^{}`); the commit is what a checkout actually needs, so it wins.
func parseGitRefAdvertisement(advertisement []byte, ref string) (GitResolvedRef, error) {
	wanted := map[string]string{
		"refs/heads/" + ref:        "branch",
		"refs/tags/" + ref:         "tag",
		"refs/tags/" + ref + "^{}": "tag",
	}
	var branch, tag, dereferencedTag GitResolvedRef
	for _, line := range pktLineRecords(advertisement) {
		commit, name, found := strings.Cut(strings.TrimRight(line, "\n"), " ")
		if !found || !fullCommitSHA.MatchString(commit) {
			continue
		}
		// The first ref carries capabilities after a NUL byte.
		if index := strings.IndexByte(name, 0); index >= 0 {
			name = name[:index]
		}
		if _, ok := wanted[name]; !ok {
			continue
		}
		resolved := GitResolvedRef{Ref: ref, RefType: wanted[name], Commit: strings.ToLower(commit)}
		switch {
		case strings.HasSuffix(name, "^{}"):
			dereferencedTag = resolved
		case strings.HasPrefix(name, "refs/heads/"):
			branch = resolved
		default:
			tag = resolved
		}
	}
	for _, candidate := range []GitResolvedRef{dereferencedTag, branch, tag} {
		if candidate.Commit != "" {
			return candidate, nil
		}
	}
	return GitResolvedRef{}, ErrGitRefNotFound
}

// pktLineRecords splits a pkt-line stream into payloads, skipping flush and
// sideband framing. A malformed length header ends the scan rather than
// producing garbage refs.
func pktLineRecords(stream []byte) []string {
	records := make([]string, 0, 64)
	for offset := 0; offset+4 <= len(stream); {
		length, err := strconv.ParseUint(string(stream[offset:offset+4]), 16, 32)
		if err != nil {
			return records
		}
		if length == 0 {
			offset += 4
			continue
		}
		if length < 4 || offset+int(length) > len(stream) {
			return records
		}
		records = append(records, string(stream[offset+4:offset+int(length)]))
		offset += int(length)
	}
	return records
}
