package registry

import (
	"context"
	"encoding/json"
	"errors"
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

func TestIsFallbackEligibleDaemonErrorInternal(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "unauthorized", err: errors.New("unauthorized: authentication required"), want: false},
		{name: "certificate", err: errors.New("x509: certificate signed by unknown authority"), want: false},
		{name: "not found", err: errors.New("manifest unknown: status 404"), want: true},
		{name: "forbidden", err: errors.New("status: 403 forbidden by administrative rules"), want: true},
		{name: "proxy", err: errors.New("proxy" + "connect tcp: connection refused"), want: true},
		{name: "unsupported", err: errors.New("distribution api not implemented"), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsFallbackEligibleDaemonError(tt.err); got != tt.want {
				t.Fatalf("IsFallbackEligibleDaemonError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
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

func TestFetchDigestAcceptsManifestListsAndOCIIndexesInternal(t *testing.T) {
	wantDigest := "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	var accept string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept = r.Header.Get("Accept")
		w.Header().Set("Docker-Content-Digest", wantDigest)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}

	if _, err := FetchDigest(context.Background(), serverURL.Host, "team/app", "1.2.3", nil, server.Client()); err != nil {
		t.Fatalf("FetchDigest() error = %v", err)
	}
	for _, mediaType := range []string{
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.index.v1+json",
	} {
		if !strings.Contains(accept, mediaType) {
			t.Fatalf("Accept = %q, want %s", accept, mediaType)
		}
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

func TestFetchRegistryRateLimitUsesHeadForTokenAuthInternal(t *testing.T) {
	var tokenCalls int
	var manifestCalls int
	var tokenURL string
	authHost := "auth.example.test"

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/team/app/manifests/1.2.3":
			manifestCalls++
			if r.Method != http.MethodHead {
				t.Fatalf("manifest method = %s, want HEAD", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer anonymous-token" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+tokenURL+`",service="registry.example.com"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("RateLimit-Limit", "100;w=21600")
			w.Header().Set("RateLimit-Remaining", "90;w=21600")
			w.WriteHeader(http.StatusOK)
		case "/token":
			tokenCalls++
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

	gotLimit, err := FetchRegistryRateLimit(context.Background(), serverURL.Host, "team/app", "1.2.3", nil, crossDomainRegistryTestClientInternal(t, server, authHost))

	if err != nil {
		t.Fatalf("FetchRegistryRateLimit returned error: %v", err)
	}
	if gotLimit == nil || gotLimit.Limit == nil || *gotLimit.Limit != 100 {
		t.Fatalf("limit = %#v, want 100", gotLimit)
	}
	if gotLimit.Remaining == nil || *gotLimit.Remaining != 90 {
		t.Fatalf("remaining = %#v, want 90", gotLimit.Remaining)
	}
	if tokenCalls != 1 {
		t.Fatalf("token calls = %d, want 1", tokenCalls)
	}
	if manifestCalls != 2 {
		t.Fatalf("manifest calls = %d, want 2", manifestCalls)
	}
}

func TestFetchRegistryRateLimitUsesHeadWithCredentialsInternal(t *testing.T) {
	var tokenUser string
	var tokenPassword string
	var tokenURL string
	authHost := "auth.example.test"

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/team/app/manifests/1.2.3":
			if r.Method != http.MethodHead {
				t.Fatalf("manifest method = %s, want HEAD", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer credential-token" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+tokenURL+`",service="registry.example.com"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("RateLimit-Limit", "200;w=21600")
			w.Header().Set("RateLimit-Remaining", "199;w=21600")
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

	gotLimit, err := FetchRegistryRateLimit(context.Background(), serverURL.Host, "team/app", "1.2.3", &Credentials{
		Username: "stored-user",
		Token:    "stored-token",
	}, crossDomainRegistryTestClientInternal(t, server, authHost))

	if err != nil {
		t.Fatalf("FetchRegistryRateLimit returned error: %v", err)
	}
	if gotLimit == nil || gotLimit.Limit == nil || *gotLimit.Limit != 200 {
		t.Fatalf("limit = %#v, want 200", gotLimit)
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
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+"http"+`://auth.example.test/token",service="registry.example.com"`)
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
