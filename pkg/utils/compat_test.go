package utils

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func TestContainerCreateWithCompatibilityInternal(t *testing.T) {
	var createCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPathForTestInternal(r.URL.Path)
		switch {
		case r.Method == http.MethodPost && path == "/containers/create":
			createCalled = true
			if got := r.URL.Query().Get("name"); got != "web" {
				t.Fatalf("create name = %q, want web", got)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{"Id": "created-id", "Warnings": []string{}}); err != nil {
				t.Fatalf("encode create response: %v", err)
			}
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	dockerClient, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.41"))
	if err != nil {
		t.Fatalf("new docker client: %v", err)
	}
	t.Cleanup(func() {
		if err := dockerClient.Close(); err != nil {
			t.Errorf("close docker client: %v", err)
		}
	})

	got, err := ContainerCreateWithCompatibility(context.Background(), dockerClient, client.ContainerCreateOptions{
		Config: &container.Config{Image: "nginx:1.27"},
		Name:   "web",
	})
	if err != nil {
		t.Fatalf("ContainerCreateWithCompatibility() error = %v", err)
	}
	if got.ID != "created-id" {
		t.Fatalf("ContainerCreateWithCompatibility() ID = %q, want created-id", got.ID)
	}
	if !createCalled {
		t.Fatal("ContainerCreateWithCompatibility() did not call container create")
	}
}

func dockerAPIPathForTestInternal(path string) string {
	trimmed := strings.TrimPrefix(path, "/")
	version, rest, ok := strings.Cut(trimmed, "/")
	if ok && strings.HasPrefix(version, "v") {
		return "/" + rest
	}
	return path
}
