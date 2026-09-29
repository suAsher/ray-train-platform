package registryauth

import (
	"errors"
	"net/http"
	"testing"
)

func TestClientHostPreservesLegacyDefault(t *testing.T) {
	if NewClient().Host() != Host || (&Client{}).Host() != Host {
		t.Fatal("legacy client no longer selects Wellspiking")
	}
	for _, host := range []string{Host, QomoloHost} {
		client, err := NewClientForHost(host)
		if err != nil || client.Host() != host {
			t.Fatalf("selected registry not retained: %v", err)
		}
	}
}

func TestSelectedTransportRejectsOtherRegistryAndHostOverrides(t *testing.T) {
	for _, host := range []string{Host, QomoloHost} {
		other := QomoloHost
		if host == QomoloHost {
			other = Host
		}
		for _, target := range []string{
			"https://" + other + "/v2/", "http://" + host + "/v2/", "https://" + host + ":443/v2/",
			"https://fixture-user:fixture-secret@" + host + "/v2/", "https://" + host + "/v2/other/model/manifests/v1",
			"https://" + host + "/service/token?service=harbor-registry&scope=repository:other/model:pull,push",
		} {
			guard := &publishTransport{host: host, repository: "team/model", base: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("credential-bearing request left selected registry/repository")
				return nil, nil
			})}
			request, _ := http.NewRequest(http.MethodGet, target, nil)
			if _, err := guard.RoundTrip(request); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("unsafe target accepted: %v", err)
			}
			request, _ = http.NewRequest(http.MethodGet, "https://"+host+"/v2/", nil)
			request.Host = other
			if _, err := guard.RoundTrip(request); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("other Harbor host override accepted: %v", err)
			}
		}
	}
}

func TestSelectedTransportBindsChallengesAndLocationsToRegistryAndRepository(t *testing.T) {
	for _, host := range []string{Host, QomoloHost} {
		other := QomoloHost
		if host == QomoloHost {
			other = Host
		}
		for _, tc := range []struct {
			name, header, value string
			allowed             bool
		}{
			{"selected challenge", "WWW-Authenticate", `Bearer realm="https://` + host + `/service/token",service="harbor-registry",scope="repository:team/model:pull,push"`, true},
			{"other registry challenge", "WWW-Authenticate", `Bearer realm="https://` + other + `/service/token",service="harbor-registry"`, false},
			{"other repository challenge", "WWW-Authenticate", `Bearer realm="https://` + host + `/service/token",service="harbor-registry",scope="repository:other/model:pull,push"`, false},
			{"selected upload location", "Location", "https://" + host + "/v2/team/model/blobs/uploads/123?_state=fixture", true},
			{"relative upload location", "Location", "/v2/team/model/blobs/uploads/123?_state=fixture", true},
			{"other registry location", "Location", "https://" + other + "/v2/team/model/blobs/uploads/123?_state=fixture", false},
			{"other repository location", "Location", "/v2/other/model/blobs/uploads/123?_state=fixture", false},
		} {
			t.Run(host+"/"+tc.name, func(t *testing.T) {
				guard := &publishTransport{host: host, repository: "team/model", base: roundTripFunc(func(*http.Request) (*http.Response, error) {
					response := reply(http.StatusUnauthorized, "")
					response.Header.Set(tc.header, tc.value)
					return response, nil
				})}
				request, _ := http.NewRequest(http.MethodGet, "https://"+host+"/v2/", nil)
				response, err := guard.RoundTrip(request)
				if response != nil {
					response.Body.Close()
				}
				if tc.allowed && err != nil || !tc.allowed && !errors.Is(err, ErrUnavailable) {
					t.Fatalf("unexpected selected transport result: %v", err)
				}
			})
		}
	}
}
