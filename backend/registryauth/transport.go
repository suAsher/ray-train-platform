package registryauth

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"syscall"
)

// publishTransport constrains the registry library's auth exchanges, upload
// locations and redirects to the frozen Harbor repository. In particular no
// cross-registry layer mounting or externally advertised token realm is allowed.
type publishTransport struct {
	base       http.RoundTripper
	repository string
}

func (t *publishTransport) allowed(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.Host != Host || u.User != nil || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	if path.Clean(u.Path) != strings.TrimSuffix(u.Path, "/") {
		return false
	}
	if u.Path == "/service/token" {
		q := u.Query()
		if len(q["service"]) != 1 || q.Get("service") != registryService {
			return false
		}
		for _, scope := range q["scope"] {
			if scope != "repository:"+t.repository+":pull,push" && scope != "repository:"+t.repository+":push,pull" && scope != "repository:"+t.repository+":pull" {
				return false
			}
		}
		return true
	}
	if u.Path == "/v2/" {
		return true
	}
	prefix := "/v2/" + t.repository + "/"
	if !strings.HasPrefix(u.Path, prefix) {
		return false
	}
	suffix := strings.TrimPrefix(u.Path, prefix)
	if !strings.HasPrefix(suffix, "blobs/") && !strings.HasPrefix(suffix, "manifests/") {
		return false
	}
	return u.Query().Get("from") == "" && u.Query().Get("mount") == ""
}

func validChallenge(value string) bool {
	if value == "" {
		return true
	}
	scheme, parameters, ok := strings.Cut(value, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	values := map[string]string{}
	for parameters != "" {
		key, rest, ok := strings.Cut(strings.TrimSpace(parameters), "=")
		if !ok {
			return false
		}
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, `"`) {
			return false
		}
		end := strings.Index(rest[1:], `"`)
		if end < 0 {
			return false
		}
		value := rest[1 : end+1]
		if strings.Contains(value, `\`) {
			return false
		}
		key = strings.TrimSpace(key)
		if _, exists := values[key]; exists {
			return false
		}
		values[key] = value
		parameters = strings.TrimSpace(rest[end+2:])
		if parameters != "" {
			if parameters[0] != ',' {
				return false
			}
			parameters = strings.TrimSpace(parameters[1:])
			if parameters == "" {
				return false
			}
		}
	}
	return values["realm"] == Origin+"/service/token" && values["service"] == registryService
}

// Preserve retry semantics without retaining an upstream error which may carry
// a credential, signed upload query or private address. Only ErrUnavailable is
// unwrapped; Error never formats the original error.
type publishNetworkError struct{ timeout, temporary bool }

func (e *publishNetworkError) Error() string   { return "Harbor transport request failed" }
func (e *publishNetworkError) Unwrap() error   { return ErrUnavailable }
func (e *publishNetworkError) Timeout() bool   { return e.timeout }
func (e *publishNetworkError) Temporary() bool { return e.temporary }

func safeNetworkError(err error) *publishNetworkError {
	safe := &publishNetworkError{}
	var network net.Error
	if errors.As(err, &network) {
		safe.timeout = network.Timeout()
		safe.temporary = network.Temporary() || safe.timeout
	}
	for _, retryable := range []error{io.EOF, io.ErrUnexpectedEOF, syscall.EPIPE, syscall.ECONNRESET, net.ErrClosed} {
		safe.temporary = safe.temporary || errors.Is(err, retryable)
	}
	return safe
}

func diagnosticMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	default:
		return "OTHER"
	}
}

func (t *publishTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !t.allowed(request.URL) || (request.Host != "" && request.Host != Host) {
		publishDiagnostic(request.Context(), PublishDiagnostic{Code: "TRANSPORT_TARGET_REJECTED", Method: diagnosticMethod(request.Method)})
		return nil, ErrUnavailable
	}
	publishDiagnostic(request.Context(), PublishDiagnostic{Code: "REQUEST_STARTED", Method: diagnosticMethod(request.Method)})
	response, err := t.base.RoundTrip(request)
	if err != nil {
		safe := safeNetworkError(err)
		code := "TRANSPORT_NETWORK_FAILED"
		if safe.temporary {
			code = "TRANSPORT_NETWORK_RETRYABLE"
		}
		if safe.timeout {
			code = "TRANSPORT_NETWORK_TIMEOUT"
		}
		publishDiagnostic(request.Context(), PublishDiagnostic{Code: code, Method: diagnosticMethod(request.Method), Timeout: safe.timeout})
		return nil, safe
	}
	status := response.StatusCode
	if status < 100 || status > 599 {
		status = 0
	}
	publishDiagnostic(request.Context(), PublishDiagnostic{Code: "RESPONSE_RECEIVED", Method: diagnosticMethod(request.Method), Status: status})
	valid := validChallenge(response.Header.Get("WWW-Authenticate"))
	rejection := "TRANSPORT_CHALLENGE_REJECTED"
	if location := response.Header.Get("Location"); location != "" {
		relative, err := url.Parse(location)
		locationAllowed := err == nil && t.allowed(request.URL.ResolveReference(relative))
		if valid && !locationAllowed {
			rejection = "TRANSPORT_LOCATION_REJECTED"
		}
		valid = valid && locationAllowed
	}
	if !valid {
		publishDiagnostic(request.Context(), PublishDiagnostic{Code: rejection, Method: diagnosticMethod(request.Method), Status: status})
		response.Body.Close()
		return nil, ErrUnavailable
	}
	return response, nil
}
