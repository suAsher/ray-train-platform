package registryauth

import (
	"net/http"
	"testing"
)

func TestPublishTransportRejectsCredentialRedirectsAndWrongRepository(t *testing.T) {
	for _, destination := range []string{"https://evil.invalid/v2/team/model/blobs/uploads/", "http://registry.example.com/v2/", "https://registry.example.com/v2/other/model/manifests/v1", "https://registry.example.com/service/token?service=evil", "https://registry.example.com/v2/team/model/../other", "https://registry.example.com:443/v2/", "https://registry.example.com/v2/team/model/blobs/..", "https://registry.example.com/service/token?service=harbor-registry&service=evil"} {
		guard := &publishTransport{repository: "team/model", base: roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unsafe request escaped"); return nil, nil })}
		req, _ := http.NewRequest("GET", destination, nil)
		if _, err := guard.RoundTrip(req); err == nil {
			t.Errorf("accepted %s", destination)
		}
	}
}

func TestPublishTransportRejectsHostOverride(t *testing.T) {
	guard := &publishTransport{repository: "team/model", base: roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("host override escaped"); return nil, nil })}
	request, _ := http.NewRequest("GET", Origin+"/v2/", nil)
	request.Host = "evil.invalid"
	if _, err := guard.RoundTrip(request); err == nil {
		t.Fatal("host override accepted")
	}
}

func TestChallengeAllowsQuotedScopeCommaButNotAlternateRealm(t *testing.T) {
	guard := &publishTransport{repository: "team/model"}
	if !guard.validChallenge(`Bearer realm="https://registry.example.com/service/token",service="harbor-registry",scope="repository:team/model:pull,push"`) {
		t.Fatal("legitimate scoped challenge rejected")
	}
	for _, challenge := range []string{
		`Bearer realm="http://registry.example.com/service/token",service="harbor-registry"`,
		`Bearer realm="https://registry.example.com/service/token?redirect=evil",service="harbor-registry"`,
		`Bearer realm="https://registry.example.com/service/token",service="harbor-registry",service="evil"`,
	} {
		if guard.validChallenge(challenge) {
			t.Fatalf("unsafe challenge accepted: %s", challenge)
		}
	}
}

func TestPublishTransportValidatesResponseLocationsAndRealm(t *testing.T) {
	for _, tc := range []struct{ header, value string }{
		{"Location", "https://evil.invalid/upload"}, {"Location", "/v2/other/model/blobs/uploads/123"},
		{"WWW-Authenticate", `Bearer realm="https://evil.invalid/token",service="harbor-registry"`},
		{"WWW-Authenticate", `Bearer realm="https://registry.example.com/service/token",service="evil"`},
	} {
		guard := &publishTransport{repository: "team/model", base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			res := reply(401, "")
			res.Header.Set(tc.header, tc.value)
			return res, nil
		})}
		req, _ := http.NewRequest("GET", Origin+"/v2/", nil)
		if _, err := guard.RoundTrip(req); err == nil {
			t.Errorf("accepted unsafe %s", tc.header)
		}
	}
}

func TestPublishTransportAcceptsSameRepositoryUpload(t *testing.T) {
	guard := &publishTransport{repository: "team/model", base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		res := reply(202, "")
		res.Header.Set("Location", "/v2/team/model/blobs/uploads/123?_state=opaque")
		return res, nil
	})}
	req, _ := http.NewRequest("POST", Origin+"/v2/team/model/blobs/uploads/", nil)
	if _, err := guard.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
}
