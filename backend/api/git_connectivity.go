package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ray-train-platform-backend/domain"
)

// GitCredentialTestResult is intentionally small and token-free. It tells the
// Portal whether the approved host and the exact repository accepted the
// stored credential without returning a secret, URL query, or response body.
type GitCredentialTestResult struct {
	Reachable     bool   `json:"reachable"`
	Authenticated bool   `json:"authenticated"`
	Message       string `json:"message"`
}

type GitCredentialTester interface {
	Probe(context.Context, string, string, string) (GitCredentialTestResult, error)
}

type httpsGitCredentialTester struct {
	client *http.Client
}

func newHTTPSGitCredentialTester() GitCredentialTester {
	return httpsGitCredentialTester{client: &http.Client{
		Timeout: 12 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) == 0 || request.URL.Hostname() != via[0].URL.Hostname() || request.URL.Scheme != "https" {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}}
}

func (tester httpsGitCredentialTester) Probe(ctx context.Context, repositoryURL, username, token string) (GitCredentialTestResult, error) {
	endpoint, err := gitAdvertisementURL(repositoryURL)
	if err != nil {
		return GitCredentialTestResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return GitCredentialTestResult{}, fmt.Errorf("build Git repository probe: %w", err)
	}
	request.Header.Set("Accept", "application/x-git-upload-pack-advertisement")
	request.SetBasicAuth(username, token)
	response, err := tester.client.Do(request)
	if err != nil {
		return GitCredentialTestResult{Message: "无法连接 Git 主机，请检查 DNS、TLS 或网络策略"}, nil
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices:
		return GitCredentialTestResult{Reachable: true, Authenticated: true, Message: "仓库连接与只读访问已验证"}, nil
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound:
		return GitCredentialTestResult{Reachable: true, Authenticated: false, Message: "Git 主机可达，但凭据没有该仓库的只读权限"}, nil
	default:
		return GitCredentialTestResult{Reachable: true, Authenticated: false, Message: "Git 主机返回异常状态，请稍后重试或联系管理员"}, nil
	}
}

// validateApprovedGitRepositoryURL makes the backend an outbound allowlist
// boundary: the repository must be HTTPS, use the exact credential host, have
// a repository path, and be approved by deployment configuration. It rejects
// redirects to another host in the actual tester as a second safeguard.
func validateApprovedGitRepositoryURL(rawURL, credentialHost string, allowlist []string) (string, error) {
	parsed, err := parseHTTPSURL(rawURL)
	if err != nil {
		return "", err
	}
	if domain.NormalizeGitHost(parsed.Hostname()) != domain.NormalizeGitHost(credentialHost) {
		return "", fmt.Errorf("repository host must match the saved credential host")
	}
	return allowlistedRepositoryURL(parsed, allowlist)
}

// validateAllowlistedGitRepositoryURL applies the same outbound boundary
// without binding the request to one saved credential. Branch resolution needs
// it because a public repository has no credential at all.
func validateAllowlistedGitRepositoryURL(rawURL string, allowlist []string) (string, error) {
	parsed, err := parseHTTPSURL(rawURL)
	if err != nil {
		return "", err
	}
	return allowlistedRepositoryURL(parsed, allowlist)
}

func parseHTTPSURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Hostname() == "" || parsed.Port() != "" && parsed.Port() != "443" {
		return nil, fmt.Errorf("repository URL must be a HTTPS URL without embedded credentials")
	}
	if strings.Trim(parsed.Path, "/") == "" {
		return nil, fmt.Errorf("repository URL must include a repository path")
	}
	return parsed, nil
}

func allowlistedRepositoryURL(parsed *url.URL, allowlist []string) (string, error) {
	sanitized := *parsed
	sanitized.RawQuery = ""
	sanitized.Fragment = ""
	if !matchesGitAllowlist(sanitized.String(), allowlist) {
		return "", fmt.Errorf("the requested Git host is not in the platform allowlist")
	}
	return sanitized.String(), nil
}

func gitAdvertisementURL(repositoryURL string) (string, error) {
	parsed, err := url.Parse(repositoryURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return "", fmt.Errorf("repository URL is invalid")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/info/refs"
	parsed.RawQuery = "service=git-upload-pack"
	parsed.Fragment = ""
	return parsed.String(), nil
}
