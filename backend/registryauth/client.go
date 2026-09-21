// Package registryauth authorizes personal Harbor publishes. Pull robot
// credentials are deliberately not accepted as a fallback.
package registryauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const Origin = "https://harbor.wellspiking.ai"
const Host = "harbor.wellspiking.ai"
const registryService = "harbor-registry"
const maxResponseBytes = 2 << 20

var (
	ErrCredentials   = errors.New("Harbor credentials are invalid or expired")
	ErrForbidden     = errors.New("Harbor does not grant push access to this repository")
	ErrUnavailable   = errors.New("Harbor request failed; retry or check registry availability")
	ErrInvalidTarget = errors.New("invalid Harbor project or repository")
	ErrProjectsUnavailable = errors.New("Harbor project list is unavailable; enter the project and verify repository push access")
	componentPattern = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
)

type Credentials struct {
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

// String prevents accidental logging of credentials with formatting verbs.
func (Credentials) String() string   { return "[Harbor credentials redacted]" }
func (Credentials) GoString() string { return "[Harbor credentials redacted]" }

type Identity struct {
	Username string `json:"username"`
	UserID   int64  `json:"userId"`
}
type Project struct {
	Name      string `json:"name"`
	ProjectID int64  `json:"projectId"`
	CanPush   bool   `json:"canPush"`
}
type ProjectPage struct {
	Items    []Project `json:"items"`
	NextPage int       `json:"nextPage,omitempty"`
}
type Target struct {
	Repository string `json:"repository"`
}
type Client struct{ http *http.Client }

func NewClient() *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 15 * time.Second
	return &Client{http: &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func validCredentials(c Credentials) bool {
	if len(c.Username) == 0 || len(c.Username) > 256 || len(c.Secret) == 0 || len(c.Secret) > 8192 || strings.Contains(c.Username, ":") {
		return false
	}
	for _, value := range []string{c.Username, c.Secret} {
		for _, r := range value {
			if unicode.IsControl(r) {
				return false
			}
		}
	}
	return true
}

func ValidateTarget(project, repository string) (Target, error) {
	if len(project) > 63 || !componentPattern.MatchString(project) || len(project)+len(repository)+1 > 255 {
		return Target{}, ErrInvalidTarget
	}
	for _, component := range strings.Split(repository, "/") {
		if !componentPattern.MatchString(component) {
			return Target{}, ErrInvalidTarget
		}
	}
	return Target{Repository: project + "/" + repository}, nil
}

func (c *Client) get(ctx context.Context, credentials Credentials, path string, query url.Values, output any) (http.Header, error) {
	if !validCredentials(credentials) {
		return nil, ErrCredentials
	}
	target := Origin + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	request.SetBasicAuth(credentials.Username, credentials.Secret)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return nil, ErrCredentials
	case http.StatusForbidden:
		return nil, ErrForbidden
	case http.StatusOK:
	default:
		return nil, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return nil, ErrUnavailable
	}
	if json.Unmarshal(data, output) != nil {
		return nil, ErrUnavailable
	}
	return response.Header, nil
}

func (c *Client) Authenticate(ctx context.Context, credentials Credentials) (Identity, error) {
	var response struct {
		Token string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	// Harbor OIDC CLI Secrets authenticate registry operations, but need not
	// authenticate the management API. Ask the trusted issuer for identity only;
	// repository permissions are checked separately against the frozen target.
	_, err := c.get(ctx, credentials, "/service/token", url.Values{"service":{registryService},"account":{credentials.Username}}, &response)
	if err != nil {
		return Identity{}, err
	}
	token:=response.Token;if token==""{token=response.AccessToken}
	if !hasRegistryIdentity(token,credentials.Username,time.Now()) {
		return Identity{}, ErrCredentials
	}
	return Identity{Username: credentials.Username}, nil
}

// This accepts only a token returned directly by the fixed TLS-verified Harbor
// issuer above, never an API caller's bearer token. Registry upload requests
// still undergo independent signature and repository-scope verification.
func hasRegistryIdentity(token,username string,now time.Time)bool{
	parts:=strings.Split(token,".");if len(parts)!=3 || parts[2]==""{return false}
	payload,err:=base64.RawURLEncoding.DecodeString(parts[1]);if err!=nil{return false}
	var claims struct{
		Issuer string `json:"iss"`;Subject string `json:"sub"`;Audience string `json:"aud"`
		Expires int64 `json:"exp"`;IssuedAt int64 `json:"iat"`;NotBefore int64 `json:"nbf"`
		Access []json.RawMessage `json:"access"`
	}
	if json.Unmarshal(payload,&claims)!=nil{return false}
	return username!="" && claims.Issuer=="harbor-token-issuer" && claims.Subject==username && claims.Audience==registryService && claims.Expires>now.Unix() && claims.IssuedAt>0 && claims.NotBefore>0 && claims.IssuedAt<=now.Add(30*time.Second).Unix() && claims.NotBefore<=now.Add(30*time.Second).Unix() && claims.Expires>claims.IssuedAt && claims.Expires>claims.NotBefore && len(claims.Access)==0
}

// Projects returns candidate destinations, not authorization for a particular
// repository. CheckPush must run again against the full frozen repository.
func (c *Client) Projects(ctx context.Context, credentials Credentials, page, pageSize int) (ProjectPage, error) {
	if page < 1 || page > 10000 || pageSize < 1 || pageSize > 100 {
		return ProjectPage{}, ErrInvalidTarget
	}
	var projects []struct {
		Name string `json:"name"`
		ID   int64  `json:"project_id"`
		Role int    `json:"current_user_role_id"`
	}
	headers, err := c.get(ctx, credentials, "/api/v2.0/projects", url.Values{"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(pageSize)}, "with_detail": {"true"}}, &projects)
	if err != nil {
		if errors.Is(err,ErrCredentials) || errors.Is(err,ErrForbidden){return ProjectPage{},ErrProjectsUnavailable}
		return ProjectPage{}, err
	}
	if len(projects) > pageSize {
		return ProjectPage{}, ErrUnavailable
	}
	result := ProjectPage{Items: make([]Project, 0, len(projects))}
	for _, p := range projects {
		if _, err := ValidateTarget(p.Name, "environment"); err != nil {
			continue
		}
		result.Items = append(result.Items, Project{Name: p.Name, ProjectID: p.ID, CanPush: p.Role == 1 || p.Role == 2 || p.Role == 4})
	}
	total, err := strconv.Atoi(headers.Get("X-Total-Count"))
	if (err == nil && page*pageSize < total) || (err != nil && len(projects) == pageSize) {
		result.NextPage = page + 1
	}
	return result, nil
}

func (c *Client) CheckPush(ctx context.Context, credentials Credentials, project, repository string) (Target, error) {
	target, err := ValidateTarget(project, repository)
	if err != nil {
		return Target{}, err
	}
	_, err = c.pushToken(ctx, credentials, target.Repository)
	if err != nil {
		return Target{}, err
	}
	return target, nil
}

func (c *Client) pushToken(ctx context.Context, credentials Credentials, repository string) (string, error) {
	var response struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	_, err := c.get(ctx, credentials, "/service/token", url.Values{"service": {registryService}, "scope": {"repository:" + repository + ":pull,push"}, "account": {credentials.Username}}, &response)
	if err != nil {
		return "", err
	}
	token := response.Token
	if token == "" {
		token = response.AccessToken
	}
	if !hasPushGrant(token, repository, time.Now()) {
		return "", ErrForbidden
	}
	return token, nil
}

// The token is obtained directly from the fixed TLS-verified Harbor issuer;
// never call this on a token supplied by an API caller. Examining its claims
// checks whether Harbor granted the requested scope (HTTP 200 alone does not).
// The registry independently verifies the signature on every publish request.
func hasPushGrant(token, repository string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[2] == "" {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Expires  int64           `json:"exp"`
		Audience json.RawMessage `json:"aud"`
		Access   []struct {
			Type    string   `json:"type"`
			Name    string   `json:"name"`
			Actions []string `json:"actions"`
		} `json:"access"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Expires <= now.Unix() {
		return false
	}
	var audience string
	if json.Unmarshal(claims.Audience, &audience) != nil || audience != registryService {
		return false
	}
	for _, access := range claims.Access {
		if access.Type == "repository" && access.Name == repository {
			for _, action := range access.Actions {
				if action == "push" {
					return true
				}
			}
		}
	}
	return false
}
