package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/getarcaneapp/updater/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	ocidigest "github.com/opencontainers/go-digest"
)

type fakeDockerClientProvider struct {
	client *client.Client
	err    error
}

func (f fakeDockerClientProvider) DockerClient(ctx context.Context) (*client.Client, error) {
	return f.client, f.err
}

type fakePendingStore struct {
	records []types.ImageUpdateRecord
	cleared []string
}

func (f *fakePendingStore) PendingImageUpdates(ctx context.Context) ([]types.ImageUpdateRecord, error) {
	return f.records, nil
}

func (f *fakePendingStore) ClearImageUpdateRecord(ctx context.Context, record types.ImageUpdateRecord) error {
	f.cleared = append(f.cleared, record.ID)
	return nil
}

type fakeRunRecorder struct {
	results []types.ResourceResult
}

func (f *fakeRunRecorder) RecordUpdateRun(ctx context.Context, result types.ResourceResult) error {
	f.results = append(f.results, result)
	return nil
}

type fakePuller struct {
	pulled []string
	err    error
}

func (f *fakePuller) PullImage(ctx context.Context, imageRef string, progress io.Writer) error {
	f.pulled = append(f.pulled, imageRef)
	return f.err
}

type fakeDigestResolver struct{}

func (fakeDigestResolver) GetImageDigest(ctx context.Context, imageRef string) (string, error) {
	return "", nil
}

func TestNewServiceAppliesGenericDockerDefaultsInternal(t *testing.T) {
	service := NewService(Config{})
	if _, ok := service.config.DockerClientProvider.(defaultDockerClientProvider); !ok {
		t.Fatalf("DockerClientProvider = %T, want built-in provider", service.config.DockerClientProvider)
	}
	if _, ok := service.config.ImagePuller.(defaultImagePuller); !ok {
		t.Fatalf("ImagePuller = %T, want built-in puller", service.config.ImagePuller)
	}
	if _, ok := service.config.PendingStore.(*memoryPendingStore); !ok {
		t.Fatalf("PendingStore = %T, want built-in memory store", service.config.PendingStore)
	}
	if _, ok := service.config.RegistryDigestResolver.(defaultRegistryDigestResolver); !ok {
		t.Fatalf("RegistryDigestResolver = %T, want built-in resolver", service.config.RegistryDigestResolver)
	}
	if _, ok := service.config.ProjectUpdater.(dockerComposeProjectUpdater); !ok {
		t.Fatalf("ProjectUpdater = %T, want built-in compose updater", service.config.ProjectUpdater)
	}
}

func TestNewServiceKeepsCustomDockerAdaptersInternal(t *testing.T) {
	puller := &fakePuller{}
	store := &fakePendingStore{}
	service := NewService(Config{
		DockerClientProvider:   fakeDockerClientProvider{err: errors.New("custom")},
		ImagePuller:            puller,
		PendingStore:           store,
		RegistryDigestResolver: fakeDigestResolver{},
	})
	if _, ok := service.config.DockerClientProvider.(fakeDockerClientProvider); !ok {
		t.Fatalf("DockerClientProvider = %T, want custom provider", service.config.DockerClientProvider)
	}
	if service.config.ImagePuller != puller {
		t.Fatalf("ImagePuller was replaced")
	}
	if service.config.PendingStore != store {
		t.Fatalf("PendingStore was replaced")
	}
	if _, ok := service.config.RegistryDigestResolver.(fakeDigestResolver); !ok {
		t.Fatalf("RegistryDigestResolver = %T, want custom resolver", service.config.RegistryDigestResolver)
	}
}

func TestApplyPendingDefaultStoreNoopsInternal(t *testing.T) {
	service := NewService(Config{})

	got, err := service.ApplyPending(context.Background(), types.Options{})
	if err != nil {
		t.Fatalf("ApplyPending() error = %v", err)
	}
	if got.Checked != 0 || got.Updated != 0 || got.Skipped != 0 || got.Failed != 0 {
		t.Fatalf("ApplyPending() counts = checked:%d updated:%d skipped:%d failed:%d", got.Checked, got.Updated, got.Skipped, got.Failed)
	}
}

func TestMemoryPendingStoreReadsAndClearsRecordsInternal(t *testing.T) {
	store := NewMemoryPendingStore(
		types.ImageUpdateRecord{ID: "b", Repository: "redis", Tag: "7", HasUpdate: true},
		types.ImageUpdateRecord{ID: "a", Repository: "nginx", Tag: "1.27", HasUpdate: true},
	)

	records, err := store.PendingImageUpdates(context.Background())
	if err != nil {
		t.Fatalf("PendingImageUpdates() error = %v", err)
	}
	if len(records) != 2 || records[0].ID != "a" || records[1].ID != "b" {
		t.Fatalf("PendingImageUpdates() = %#v, want records sorted by key", records)
	}

	if err := store.ClearImageUpdateRecord(context.Background(), records[0]); err != nil {
		t.Fatalf("ClearImageUpdateRecord() error = %v", err)
	}
	records, err = store.PendingImageUpdates(context.Background())
	if err != nil {
		t.Fatalf("PendingImageUpdates() after clear error = %v", err)
	}
	if len(records) != 1 || records[0].ID != "b" {
		t.Fatalf("PendingImageUpdates() after clear = %#v, want only b", records)
	}
}

func TestDefaultRegistryDigestResolverFetchesDigestInternal(t *testing.T) {
	want := ocidigest.FromString("manifest").String()
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
	resolver := newRegistryDigestResolverInternal(server.Client())

	got, err := resolver.GetImageDigest(context.Background(), serverURL.Host+"/owner/app:1.0")
	if err != nil {
		t.Fatalf("GetImageDigest() error = %v", err)
	}
	if got != want {
		t.Fatalf("GetImageDigest() = %q, want %q", got, want)
	}
}

func TestApplyPendingDryRunRecordsSkippedImageInternal(t *testing.T) {
	store := &fakePendingStore{records: []types.ImageUpdateRecord{{
		ID:         "sha256:old",
		Repository: "nginx",
		Tag:        "1.27",
		HasUpdate:  true,
		UpdateType: types.UpdateTypeDigest,
	}}}
	recorder := &fakeRunRecorder{}
	puller := &fakePuller{}
	service := NewService(Config{
		DockerClientProvider: fakeDockerClientProvider{err: errors.New("not used in dry run")},
		PendingStore:         store,
		RunRecorder:          recorder,
		ImagePuller:          puller,
		UsedImageCollector: UsedImageCollectorFunc(func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"docker.io/library/nginx:1.27": {}}, nil
		}),
	})

	got, err := service.ApplyPending(context.Background(), types.Options{DryRun: true})
	if err != nil {
		t.Fatalf("ApplyPending() error = %v", err)
	}
	if got.Checked != 1 || got.Skipped != 1 || got.Updated != 0 || got.Failed != 0 {
		t.Fatalf("ApplyPending() counts = checked:%d skipped:%d updated:%d failed:%d", got.Checked, got.Skipped, got.Updated, got.Failed)
	}
	if len(puller.pulled) != 0 {
		t.Fatalf("dry run pulled images: %#v", puller.pulled)
	}
	if len(recorder.results) != 1 || recorder.results[0].Status != types.StatusSkipped {
		t.Fatalf("recorded results = %#v, want one skipped result", recorder.results)
	}
}

func TestStatusTracksContainersInternal(t *testing.T) {
	service := NewService(Config{})
	done := service.BeginContainerUpdate("abc")
	status := service.Status()
	if status.UpdatingContainers != 1 || len(status.ContainerIDs) != 1 || status.ContainerIDs[0] != "abc" {
		t.Fatalf("Status() while updating = %#v", status)
	}
	done()
	status = service.Status()
	if status.UpdatingContainers != 0 || len(status.ContainerIDs) != 0 {
		t.Fatalf("Status() after done = %#v", status)
	}
}

func TestResolvePullableImageRefInternal(t *testing.T) {
	ref, source := ResolvePullableImageRef("sha256:abc", "nginx:1.27", nil)
	if ref != "nginx:1.27" || source != "container_inspect_config" {
		t.Fatalf("ResolvePullableImageRef() = %q/%q, want config image", ref, source)
	}

	ref, source = ResolvePullableImageRef("sha256:abc", "", []string{"redis:7"})
	if ref != "redis:7" || source != "image_repo_tag" {
		t.Fatalf("ResolvePullableImageRef() fallback = %q/%q, want repo tag", ref, source)
	}
}

func TestServiceRejectsComposeFallbackByDefaultInternal(t *testing.T) {
	service := NewService(Config{})
	err := service.UpdateStandaloneContainer(context.Background(), container.Summary{
		ID: "container-1",
	}, container.InspectResponse{
		Config: &container.Config{Labels: map[string]string{
			"com.docker.compose.project": "app",
			"com.docker.compose.service": "web",
		}},
	}, "nginx:latest")
	if err == nil {
		t.Fatal("UpdateStandaloneContainer() error = nil for compose container without fallback")
	}
}
