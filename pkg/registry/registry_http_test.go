package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type roundTripFuncInternal func(*http.Request) (*http.Response, error)

func (f roundTripFuncInternal) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func crossDomainRegistryTestClientInternal(t *testing.T, server *httptest.Server, authHost string) *http.Client {
	t.Helper()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}

	client := server.Client()
	baseTransport := client.Transport
	client.Transport = roundTripFuncInternal(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == authHost {
			rewritten := req.Clone(req.Context())
			rewritten.URL.Host = serverURL.Host
			return baseTransport.RoundTrip(rewritten)
		}
		return baseTransport.RoundTrip(req)
	})
	return client
}

func TestFetchDigestAllowsHTTPSCrossDomainAuthRealmInternal(t *testing.T) {
	wantDigest := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	var manifestAuthHeaders []string
	var tokenURL string
	authHost := "auth.example.test"

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/team/app/manifests/1.2.3":
			manifestAuthHeaders = append(manifestAuthHeaders, r.Header.Get("Authorization"))
			switch len(manifestAuthHeaders) {
			case 1:
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+tokenURL+`",service="registry.example.com"`)
				w.WriteHeader(http.StatusUnauthorized)
			case 2:
				if got := r.Header.Get("Authorization"); got != "Bearer anonymous-token" {
					t.Fatalf("authorization header = %q, want Bearer anonymous-token", got)
				}
				w.Header().Set("Docker-Content-Digest", wantDigest)
				w.WriteHeader(http.StatusOK)
			default:
				t.Fatalf("unexpected manifest call %d", len(manifestAuthHeaders))
			}
		case "/token":
			if got := r.URL.Query().Get("service"); got != "registry.example.com" {
				t.Fatalf("service query = %q, want registry.example.com", got)
			}
			if got := r.URL.Query().Get("scope"); got != "repository:team/app:pull" {
				t.Fatalf("scope query = %q, want repository:team/app:pull", got)
			}
			if err := json.NewEncoder(w).Encode(map[string]string{
				"token": "anonymous-token",
			}); err != nil {
				t.Fatalf("encode token response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	tokenURL = "https://" + authHost + "/token"

	gotDigest, err := FetchDigest(context.Background(), serverURL.Host, "team/app", "1.2.3", nil, crossDomainRegistryTestClientInternal(t, server, authHost))

	if err != nil {
		t.Fatalf("FetchDigest returned error: %v", err)
	}
	if gotDigest != wantDigest {
		t.Fatalf("digest = %q, want %q", gotDigest, wantDigest)
	}
	if len(manifestAuthHeaders) != 2 {
		t.Fatalf("manifest calls = %d, want 2", len(manifestAuthHeaders))
	}
	if manifestAuthHeaders[0] != "" {
		t.Fatalf("first authorization header = %q, want empty", manifestAuthHeaders[0])
	}
	if manifestAuthHeaders[1] != "Bearer anonymous-token" {
		t.Fatalf("second authorization header = %q, want Bearer anonymous-token", manifestAuthHeaders[1])
	}
}

func TestFetchDigestUsesCredentialsForHTTPSAuthRealmInternal(t *testing.T) {
	wantDigest := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	var tokenUser string
	var tokenPassword string
	var tokenURL string
	authHost := "auth.example.test"

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/team/app/manifests/1.2.3":
			if r.Header.Get("Authorization") != "Bearer credential-token" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+tokenURL+`",service="registry.example.com"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer credential-token" {
				t.Fatalf("authorization header = %q, want Bearer credential-token", got)
			}
			w.Header().Set("Docker-Content-Digest", wantDigest)
			w.WriteHeader(http.StatusOK)
		case "/token":
			tokenUser, tokenPassword, _ = r.BasicAuth()
			if err := json.NewEncoder(w).Encode(map[string]string{
				"token": "credential-token",
			}); err != nil {
				t.Fatalf("encode token response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	tokenURL = "https://" + authHost + "/token"

	gotDigest, err := FetchDigest(context.Background(), serverURL.Host, "team/app", "1.2.3", &Credentials{
		Username: "stored-user",
		Token:    "stored-token",
	}, crossDomainRegistryTestClientInternal(t, server, authHost))

	if err != nil {
		t.Fatalf("FetchDigest returned error: %v", err)
	}
	if gotDigest != wantDigest {
		t.Fatalf("digest = %q, want %q", gotDigest, wantDigest)
	}
	if tokenUser != "stored-user" {
		t.Fatalf("token user = %q, want stored-user", tokenUser)
	}
	if tokenPassword != "stored-token" {
		t.Fatalf("token password = %q, want stored-token", tokenPassword)
	}
}

func TestFetchDigestRejectsNonHTTPSAuthRealmInternal(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/team/app/manifests/1.2.3":
			w.Header().Set("WWW-Authenticate", `Bearer realm="http://auth.example.test/token",service="registry.example.com"`)
			w.WriteHeader(http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}

	_, err = FetchDigest(context.Background(), serverURL.Host, "team/app", "1.2.3", nil, server.Client())

	if err == nil {
		t.Fatal("FetchDigest returned nil error")
	}
	if !strings.Contains(err.Error(), "registry auth realm must use https") {
		t.Fatalf("error = %q, want registry auth realm must use https", err.Error())
	}
}
