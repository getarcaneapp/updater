package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
)

func TestDefaultRegistryDigestResolverFetchesDigest(t *testing.T) {
	want := digest.FromString("manifest").String()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/owner/app/manifests/1.0" {
			http.Error(w, "unexpected manifest path", http.StatusNotFound)
			return
		}
		w.Header().Set("Docker-Content-Digest", want)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resolver := newRegistryDigestResolver(server.Client())

	got, err := resolver.ImageDigest(context.Background(), serverURL.Host+"/owner/app:1.0")
	if err != nil {
		t.Fatalf("ImageDigest() error = %v", err)
	}
	if got != want {
		t.Fatalf("ImageDigest() = %q, want %q", got, want)
	}
}

func TestDefaultDockerClientProviderCachesAndRecreatesAfterPingFailure(t *testing.T) {
	var pingCalls int
	failPing := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dockerAPIPath(r.URL.Path) != "/_ping" {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		pingCalls++
		if failPing {
			http.Error(w, "daemon unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("OK"))
	}))
	defer server.Close()

	provider := NewDockerClientProvider(client.WithHost(server.URL), client.WithAPIVersion("1.41"))
	first, err := provider.DockerClient(context.Background())
	if err != nil {
		t.Fatalf("first DockerClient() error = %v", err)
	}
	second, err := provider.DockerClient(context.Background())
	if err != nil {
		t.Fatalf("second DockerClient() error = %v", err)
	}
	if first != second {
		t.Fatal("DockerClient() returned different clients before ping failure")
	}

	failPing = true
	if _, err := provider.DockerClient(context.Background()); err == nil {
		t.Fatal("DockerClient() after ping failure returned nil error")
	}
	failPing = false
	third, err := provider.DockerClient(context.Background())
	if err != nil {
		t.Fatalf("third DockerClient() error = %v", err)
	}
	if third == first {
		t.Fatal("DockerClient() reused client evicted after ping failure")
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if pingCalls < 4 {
		t.Fatalf("pingCalls = %d, want at least 4", pingCalls)
	}
}
