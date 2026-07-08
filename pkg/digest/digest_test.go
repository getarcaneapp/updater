package digest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	ocidigest "github.com/opencontainers/go-digest"
)

type fakeRemoteResolverInternal struct {
	digest string
}

func (f fakeRemoteResolverInternal) GetImageDigest(ctx context.Context, imageRef string) (string, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	return f.digest, nil
}

func newDockerClientForImageInspectInternal(t *testing.T, imageRef string, repoDigests []string, imageID string) *client.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trimmed := strings.TrimPrefix(r.URL.Path, "/")
		version, rest, ok := strings.Cut(trimmed, "/")
		if ok && strings.HasPrefix(version, "v") {
			trimmed = rest
		}
		if r.Method != http.MethodGet || trimmed != "images/"+imageRef+"/json" {
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(image.InspectResponse{ID: imageID, RepoDigests: repoDigests}); err != nil {
			t.Fatalf("encode image inspect response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	dockerClient, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.41"))
	if err != nil {
		t.Fatalf("new docker client: %v", err)
	}
	return dockerClient
}

func TestFromReferenceSuffixInternal(t *testing.T) {
	want := ocidigest.FromString("arcane-reference").String()

	got, ok := FromReferenceSuffix("docker.io/library/nginx@" + want)
	if !ok {
		t.Fatal("FromReferenceSuffix() ok = false, want true")
	}
	if got != want {
		t.Fatalf("digest = %q, want %q", got, want)
	}

	if _, ok := FromReferenceSuffix("docker.io/library/nginx@sha256:bad"); ok {
		t.Fatal("FromReferenceSuffix() ok = true for invalid digest")
	}
}

func TestCheckerCheckImageNeedsUpdateSkipsDigestPinnedReferenceInternal(t *testing.T) {
	pinnedDigest := ocidigest.FromString("pinned-newt").String()
	imageRef := "ghcr.io/fosrl/newt@" + pinnedDigest

	got := NewChecker(nil, nil).CheckImageNeedsUpdate(context.Background(), imageRef)

	if got.Error != nil {
		t.Fatalf("CheckImageNeedsUpdate() error = %v, want nil", got.Error)
	}
	if got.NeedsUpdate {
		t.Fatal("CheckImageNeedsUpdate() NeedsUpdate = true, want false")
	}
	if got.CheckedViaAPI {
		t.Fatal("CheckImageNeedsUpdate() CheckedViaAPI = true, want false")
	}
	if got.LocalDigest != pinnedDigest {
		t.Fatalf("CheckImageNeedsUpdate() LocalDigest = %q, want %q", got.LocalDigest, pinnedDigest)
	}
	if got.RemoteDigest != pinnedDigest {
		t.Fatalf("CheckImageNeedsUpdate() RemoteDigest = %q, want %q", got.RemoteDigest, pinnedDigest)
	}
}

func TestCheckerCheckImageNeedsUpdateMatchesManifestListRepoDigestInternal(t *testing.T) {
	listDigest := ocidigest.FromString("manifest-list").String()
	resolver := fakeRemoteResolverInternal{digest: listDigest}
	dockerClient := newDockerClientForImageInspectInternal(t, "docker.io/library/app:1", []string{"docker.io/library/app@" + listDigest}, "sha256:platform-image")

	got := NewChecker(dockerClient, resolver).CheckImageNeedsUpdate(context.Background(), "docker.io/library/app:1")

	if got.Error != nil {
		t.Fatalf("CheckImageNeedsUpdate() error = %v", got.Error)
	}
	if got.NeedsUpdate {
		t.Fatalf("CheckImageNeedsUpdate() NeedsUpdate = true, want false; result=%#v", got)
	}
	if !got.CheckedViaAPI {
		t.Fatal("CheckImageNeedsUpdate() CheckedViaAPI = false, want true")
	}
}

func TestCheckerCheckImageNeedsUpdateTreatsPlatformDigestMismatchAsUpdateInternal(t *testing.T) {
	listDigest := ocidigest.FromString("manifest-list").String()
	platformDigest := ocidigest.FromString("platform-manifest").String()
	resolver := fakeRemoteResolverInternal{digest: listDigest}
	dockerClient := newDockerClientForImageInspectInternal(t, "docker.io/library/app:1", []string{"docker.io/library/app@" + platformDigest}, "sha256:platform-image")

	got := NewChecker(dockerClient, resolver).CheckImageNeedsUpdate(context.Background(), "docker.io/library/app:1")

	if got.Error != nil {
		t.Fatalf("CheckImageNeedsUpdate() error = %v", got.Error)
	}
	if !got.NeedsUpdate {
		t.Fatalf("CheckImageNeedsUpdate() NeedsUpdate = false, want documented mismatch behavior; result=%#v", got)
	}
}
