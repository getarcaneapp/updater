package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	dockerauthconfig "github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/api/types/jsonstream"
	dockerregistry "github.com/moby/moby/api/types/registry"
)

func TestDefaultImagePullerIgnoresRepositoryEnvAuthInternal(t *testing.T) {
	t.Setenv("REPO_USER", "env-user")
	t.Setenv("REPO_PASS", "env-token")
	t.Setenv("DOCKER_CONFIG", t.TempDir())

	var gotAuth string
	dcli := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		switch dockerAPIPathInternal(r.URL.Path) {
		case "/images/create":
			gotAuth = r.Header.Get(dockerregistry.AuthHeader)
			if got := r.URL.Query().Get("fromImage"); got != "registry.example.com/team/app" {
				t.Fatalf("fromImage = %q, want registry.example.com/team/app", got)
			}
			writeDockerJSONInternal(t, w, jsonstream.Message{Status: "Pulled"})
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})

	puller := defaultImagePuller{dockerClientProvider: fakeDockerClientProvider{client: dcli}}
	err := puller.PullImage(context.Background(), "registry.example.com/team/app:1.2.3", io.Discard)
	if err != nil {
		t.Fatalf("PullImage() error = %v", err)
	}

	if gotAuth != "" {
		t.Fatalf("registry auth header = %q, want no auth from REPO_USER/REPO_PASS", gotAuth)
	}
}

func TestDefaultImagePullerUsesDockerConfigRegistryAuthInternal(t *testing.T) {
	t.Setenv("REPO_USER", "")
	t.Setenv("REPO_PASS", "")
	dockerConfigDir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dockerConfigDir)
	writeDockerConfigAuthInternal(t, dockerConfigDir, "registry.example.com", "config-user", "config-token")

	var gotAuth string
	dcli := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		switch dockerAPIPathInternal(r.URL.Path) {
		case "/images/create":
			gotAuth = r.Header.Get(dockerregistry.AuthHeader)
			writeDockerJSONInternal(t, w, jsonstream.Message{Status: "Pulled"})
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})

	puller := defaultImagePuller{dockerClientProvider: fakeDockerClientProvider{client: dcli}}
	err := puller.PullImage(context.Background(), "registry.example.com/team/app:1.2.3", io.Discard)
	if err != nil {
		t.Fatalf("PullImage() error = %v", err)
	}

	authCfg := decodeDefaultRegistryAuthInternal(t, gotAuth)
	if authCfg.Username != "config-user" || authCfg.Password != "config-token" {
		t.Fatalf("auth = %#v, want Docker config credentials", authCfg)
	}
}

func TestDefaultImagePullerRetriesAnonymouslyAfterAuthRejectedInternal(t *testing.T) {
	dockerConfigDir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dockerConfigDir)
	writeDockerConfigAuthInternal(t, dockerConfigDir, "registry.example.com", "config-user", "config-token")

	var authHeaders []string
	dcli := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		switch dockerAPIPathInternal(r.URL.Path) {
		case "/images/create":
			authHeaders = append(authHeaders, r.Header.Get(dockerregistry.AuthHeader))
			if len(authHeaders) == 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			writeDockerJSONInternal(t, w, jsonstream.Message{Status: "Pulled anonymously"})
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})

	puller := defaultImagePuller{dockerClientProvider: fakeDockerClientProvider{client: dcli}}
	err := puller.PullImage(context.Background(), "registry.example.com/team/app:1.2.3", io.Discard)
	if err != nil {
		t.Fatalf("PullImage() error = %v", err)
	}

	if len(authHeaders) != 2 {
		t.Fatalf("image create calls = %d, want 2", len(authHeaders))
	}
	if authHeaders[0] == "" {
		t.Fatal("first pull did not include registry auth")
	}
	if authHeaders[1] != "" {
		t.Fatalf("retry auth header = %q, want anonymous retry", authHeaders[1])
	}
}

func TestDefaultRegistryDigestResolverUsesDockerConfigAuthAfterUnauthorizedInternal(t *testing.T) {
	t.Setenv("REPO_USER", "")
	t.Setenv("REPO_PASS", "")
	dockerConfigDir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dockerConfigDir)

	wantDigest := "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	var tokenUser string
	var tokenPassword string
	var tokenURL string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/team/app/manifests/1.2.3":
			if r.Header.Get("Authorization") != "Bearer credential-token" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+tokenURL+`",service="registry.example.com"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Docker-Content-Digest", wantDigest)
			w.WriteHeader(http.StatusOK)
		case "/token":
			tokenUser, tokenPassword, _ = r.BasicAuth()
			if err := json.NewEncoder(w).Encode(map[string]string{"token": "credential-token"}); err != nil {
				t.Fatalf("encode token response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	tokenURL = server.URL + "/token"

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	writeDockerConfigAuthInternal(t, dockerConfigDir, serverURL.Host, "config-user", "config-token")

	resolver := defaultRegistryDigestResolver{httpClient: server.Client()}
	got, err := resolver.GetImageDigest(context.Background(), serverURL.Host+"/team/app:1.2.3")
	if err != nil {
		t.Fatalf("GetImageDigest() error = %v", err)
	}
	if got != wantDigest {
		t.Fatalf("digest = %q, want %q", got, wantDigest)
	}
	if tokenUser != "config-user" || tokenPassword != "config-token" {
		t.Fatalf("token auth = %q/%q, want Docker config credentials", tokenUser, tokenPassword)
	}
}

func decodeDefaultRegistryAuthInternal(t *testing.T, encoded string) dockerregistry.AuthConfig {
	t.Helper()

	if strings.TrimSpace(encoded) == "" {
		t.Fatal("registry auth header is empty")
	}
	authCfg, err := dockerauthconfig.Decode(encoded)
	if err != nil {
		t.Fatalf("decode registry auth: %v", err)
	}
	return *authCfg
}

func writeDockerConfigAuthInternal(t *testing.T, dir, serverAddress, username, token string) {
	t.Helper()

	authValue := base64.StdEncoding.EncodeToString([]byte(username + ":" + token))
	config := map[string]any{
		"auths": map[string]any{
			serverAddress: map[string]string{
				"auth": authValue,
			},
		},
	}
	payload, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal Docker config: %v", err)
	}
	if err := os.WriteFile(dir+"/config.json", payload, 0o600); err != nil {
		t.Fatalf("write Docker config: %v", err)
	}
}
