package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	"go.getarcane.app/updater/pkg/labels"
	"go.getarcane.app/updater/types"
)

type fakeDockerClientProvider struct {
	client *client.Client
	err    error
	calls  int
}

func (f *fakeDockerClientProvider) DockerClient(ctx context.Context) (*client.Client, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	f.calls++
	return f.client, f.err
}

type fakePendingStore struct {
	records []types.ImageUpdateRecord
	cleared []string
}

func (f *fakePendingStore) PendingImageUpdates(ctx context.Context) ([]types.ImageUpdateRecord, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return f.records, nil
}

func (f *fakePendingStore) ClearImageUpdateRecord(ctx context.Context, record types.ImageUpdateRecord) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	f.cleared = append(f.cleared, record.ID)
	return nil
}

type fakeRunRecorder struct {
	results []types.ResourceResult
}

func (f *fakeRunRecorder) RecordUpdateRun(ctx context.Context, result types.ResourceResult) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	f.results = append(f.results, result)
	return nil
}

type fakePuller struct {
	pulled []string
	err    error
	after  func(imageRef string)
}

func (f *fakePuller) PullImage(ctx context.Context, imageRef string, progress io.Writer) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	f.pulled = append(f.pulled, imageRef)
	if progress != nil {
		if _, err := io.WriteString(progress, imageRef); err != nil {
			return err
		}
	}
	if f.err != nil {
		return f.err
	}
	if f.after != nil {
		f.after(imageRef)
	}
	return nil
}

type fakeDigestResolver struct{}

func (fakeDigestResolver) GetImageDigest(ctx context.Context, imageRef string) (string, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(imageRef) == "" {
		return "", errors.New("image ref is required")
	}
	return digest.FromString(imageRef).String(), nil
}

type countingDigestResolver struct {
	digest string
	err    error
	calls  int
}

func (r *countingDigestResolver) GetImageDigest(ctx context.Context, imageRef string) (string, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	r.calls++
	if r.err != nil {
		return "", r.err
	}
	return r.digest, nil
}

type fakeProjectUpdater struct {
	projects    map[string]types.ComposeProject
	updateCalls []string
	err         error
	delay       time.Duration
}

func (f *fakeProjectUpdater) ProjectByComposeName(ctx context.Context, composeName string) (types.ComposeProject, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return types.ComposeProject{}, err
		}
	}
	if project, ok := f.projects[composeName]; ok {
		return project, nil
	}
	return types.ComposeProject{}, errors.New("project not found")
}

func (f *fakeProjectUpdater) UpdateServices(ctx context.Context, projectID string, services []string) error {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	f.updateCalls = append(f.updateCalls, projectID+":"+strings.Join(services, ","))
	return f.err
}

type fakeSelfUpdater struct {
	targets []types.SelfUpdateTarget
}

func (f *fakeSelfUpdater) TriggerSelfUpdate(ctx context.Context, target types.SelfUpdateTarget) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	f.targets = append(f.targets, target)
	return nil
}

type fakeEventRecorder struct {
	events []types.Event
}

func (f *fakeEventRecorder) RecordEvent(ctx context.Context, event types.Event) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	f.events = append(f.events, event)
	return nil
}

type captureLogHandlerInternal struct {
	records []slog.Record
}

func (h *captureLogHandlerInternal) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *captureLogHandlerInternal) Handle(_ context.Context, record slog.Record) error {
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *captureLogHandlerInternal) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *captureLogHandlerInternal) WithGroup(string) slog.Handler {
	return h
}

type recordingSelfUpdater struct {
	operations *[]string
	targets    []types.SelfUpdateTarget
}

func (r *recordingSelfUpdater) TriggerSelfUpdate(ctx context.Context, target types.SelfUpdateTarget) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	*r.operations = append(*r.operations, "self-update:"+target.ContainerID)
	r.targets = append(r.targets, target)
	return nil
}

func newDockerClientForHandlerInternal(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	dockerClient, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.41"))
	if err != nil {
		t.Fatalf("new docker client: %v", err)
	}
	return dockerClient
}

func dockerAPIPathInternal(path string) string {
	trimmed := strings.TrimPrefix(path, "/")
	version, rest, ok := strings.Cut(trimmed, "/")
	if ok && strings.HasPrefix(version, "v") {
		return "/" + rest
	}
	return path
}

func writeDockerJSONInternal(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write json response: %v", err)
	}
}

func closeHTTPConnectionInternal(t *testing.T, w http.ResponseWriter) {
	t.Helper()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("response writer does not support hijacking")
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		t.Fatalf("hijack connection: %v", err)
	}
	_ = conn.Close()
}

func TestNewServiceAppliesGenericDockerDefaultsInternal(t *testing.T) {
	service := NewDefaultService()
	if _, ok := service.config.DockerClientProvider.(*defaultDockerClientProvider); !ok {
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
		DockerClientProvider:   &fakeDockerClientProvider{err: errors.New("custom")},
		ImagePuller:            puller,
		PendingStore:           store,
		RegistryDigestResolver: fakeDigestResolver{},
	})
	if _, ok := service.config.DockerClientProvider.(*fakeDockerClientProvider); !ok {
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

func TestApplyPendingDefaultStoreNoOperationsInternal(t *testing.T) {
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

func TestMemoryPendingStoreRespectsCanceledContextInternal(t *testing.T) {
	store := NewMemoryPendingStore(types.ImageUpdateRecord{ID: "a", Repository: "repo", Tag: "1"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.PendingImageUpdates(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("PendingImageUpdates() error = %v, want context canceled", err)
	}
	if err := store.ClearImageUpdateRecord(ctx, types.ImageUpdateRecord{ID: "a"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ClearImageUpdateRecord() error = %v, want context canceled", err)
	}

	records, err := store.PendingImageUpdates(context.Background())
	if err != nil {
		t.Fatalf("PendingImageUpdates() after canceled clear error = %v", err)
	}
	if len(records) != 1 || records[0].ID != "a" {
		t.Fatalf("PendingImageUpdates() after canceled clear = %#v, want record a", records)
	}
}

func TestDefaultRegistryDigestResolverFetchesDigestInternal(t *testing.T) {
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
	resolver := newRegistryDigestResolverInternal(server.Client())

	got, err := resolver.GetImageDigest(context.Background(), serverURL.Host+"/owner/app:1.0")
	if err != nil {
		t.Fatalf("GetImageDigest() error = %v", err)
	}
	if got != want {
		t.Fatalf("GetImageDigest() = %q, want %q", got, want)
	}
}

func TestDefaultDockerClientProviderCachesAndRecreatesAfterPingFailureInternal(t *testing.T) {
	var pingCalls int
	failPing := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dockerAPIPathInternal(r.URL.Path) != "/_ping" {
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
	if closer, ok := provider.(io.Closer); !ok {
		t.Fatalf("provider does not implement io.Closer")
	} else if err := closer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if pingCalls < 4 {
		t.Fatalf("pingCalls = %d, want at least 4", pingCalls)
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
		DockerClientProvider: &fakeDockerClientProvider{err: errors.New("not used in dry run")},
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

func TestApplyPendingSkipsUnchangedPulledImageInternal(t *testing.T) {
	store := &fakePendingStore{records: []types.ImageUpdateRecord{{
		ID:         "record-1",
		Repository: "nginx",
		Tag:        "1.27",
		HasUpdate:  true,
		UpdateType: types.UpdateTypeDigest,
	}}}
	puller := &fakePuller{}
	dockerClient := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		switch dockerAPIPathInternal(r.URL.Path) {
		case "/images/nginx:1.27/json":
			writeDockerJSONInternal(t, w, image.InspectResponse{
				ID:       "sha256:same",
				RepoTags: []string{"nginx:1.27"},
			})
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newServiceInternal(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		PendingStore:         store,
		ImagePuller:          puller,
		UsedImageCollector: UsedImageCollectorFunc(func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"docker.io/library/nginx:1.27": {}}, nil
		}),
	})

	got, err := service.ApplyPending(context.Background(), types.Options{})
	if err != nil {
		t.Fatalf("ApplyPending() error = %v", err)
	}
	if got.Checked != 1 || got.Updated != 0 || got.Skipped != 0 || got.Failed != 0 {
		t.Fatalf("ApplyPending() counts = checked:%d updated:%d skipped:%d failed:%d", got.Checked, got.Updated, got.Skipped, got.Failed)
	}
	if len(got.Items) != 1 || got.Items[0].Status != types.StatusUpToDate {
		t.Fatalf("ApplyPending() items = %#v, want one up-to-date item", got.Items)
	}
	if len(puller.pulled) != 1 || puller.pulled[0] != "nginx:1.27" {
		t.Fatalf("pulled images = %#v, want nginx:1.27", puller.pulled)
	}
	if len(store.cleared) != 1 || store.cleared[0] != "record-1" {
		t.Fatalf("cleared records = %#v, want record-1", store.cleared)
	}
}

func TestApplyPendingForceBypassesUnchangedPulledImageSkipInternal(t *testing.T) {
	store := &fakePendingStore{records: []types.ImageUpdateRecord{{
		ID:         "record-1",
		Repository: "nginx",
		Tag:        "1.27",
		HasUpdate:  true,
		UpdateType: types.UpdateTypeDigest,
	}}}
	puller := &fakePuller{}
	dockerClient := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		switch dockerAPIPathInternal(r.URL.Path) {
		case "/images/nginx:1.27/json":
			writeDockerJSONInternal(t, w, image.InspectResponse{
				ID:       "sha256:same",
				RepoTags: []string{"nginx:1.27"},
			})
		case "/containers/json":
			writeDockerJSONInternal(t, w, []container.Summary{})
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newServiceInternal(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		PendingStore:         store,
		ImagePuller:          puller,
		UsedImageCollector: UsedImageCollectorFunc(func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"docker.io/library/nginx:1.27": {}}, nil
		}),
	})

	got, err := service.ApplyPending(context.Background(), types.Options{Force: true})
	if err != nil {
		t.Fatalf("ApplyPending() error = %v", err)
	}
	if got.Checked != 1 || got.Updated != 1 || got.Skipped != 0 || got.Failed != 0 {
		t.Fatalf("ApplyPending() counts = checked:%d updated:%d skipped:%d failed:%d", got.Checked, got.Updated, got.Skipped, got.Failed)
	}
	if len(got.Items) == 0 || got.Items[0].Status != types.StatusUpdated {
		t.Fatalf("ApplyPending() first item = %#v, want updated", got.Items)
	}
	if len(store.cleared) != 1 || store.cleared[0] != "record-1" {
		t.Fatalf("cleared records = %#v, want record-1", store.cleared)
	}
}

func TestApplyPendingUsesRecordDigestBeforeResolverInternal(t *testing.T) {
	oldDigest := digest.FromString("old-record-digest").String()
	newDigest := digest.FromString("new-record-digest").String()

	store := &fakePendingStore{records: []types.ImageUpdateRecord{{
		ID:           "record-1",
		Repository:   "nginx",
		Tag:          "1.27",
		HasUpdate:    true,
		UpdateType:   types.UpdateTypeDigest,
		LatestDigest: &newDigest,
	}}}
	pulled := false
	puller := &fakePuller{after: func(string) {
		pulled = true
	}}
	resolver := &countingDigestResolver{digest: oldDigest}
	dockerClient := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		switch dockerAPIPathInternal(r.URL.Path) {
		case "/images/nginx:1.27/json", "/images/docker.io/library/nginx:1.27/json":
			if !pulled {
				writeDockerJSONInternal(t, w, image.InspectResponse{
					ID:          "sha256:old-image",
					RepoTags:    []string{"nginx:1.27"},
					RepoDigests: []string{"nginx@" + oldDigest},
				})
				return
			}
			writeDockerJSONInternal(t, w, image.InspectResponse{
				ID:          "sha256:new-image",
				RepoTags:    []string{"nginx:1.27"},
				RepoDigests: []string{"nginx@" + newDigest},
			})
		case "/containers/json":
			writeDockerJSONInternal(t, w, []container.Summary{})
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newServiceInternal(Config{
		DockerClientProvider:   &fakeDockerClientProvider{client: dockerClient},
		PendingStore:           store,
		ImagePuller:            puller,
		RegistryDigestResolver: resolver,
		UsedImageCollector: UsedImageCollectorFunc(func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"docker.io/library/nginx:1.27": {}}, nil
		}),
	})

	got, err := service.ApplyPending(context.Background(), types.Options{})
	if err != nil {
		t.Fatalf("ApplyPending() error = %v", err)
	}
	if got.Checked != 1 || got.Updated != 1 || got.Skipped != 0 || got.Failed != 0 {
		t.Fatalf("ApplyPending() counts = checked:%d updated:%d skipped:%d failed:%d", got.Checked, got.Updated, got.Skipped, got.Failed)
	}
	if len(got.Items) != 1 || got.Items[0].Status != types.StatusUpdated {
		t.Fatalf("ApplyPending() items = %#v, want one updated item", got.Items)
	}
	if len(puller.pulled) != 1 || puller.pulled[0] != "nginx:1.27" {
		t.Fatalf("pulled images = %#v, want nginx:1.27", puller.pulled)
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver calls = %d, want 0", resolver.calls)
	}
	if len(store.cleared) != 1 || store.cleared[0] != "record-1" {
		t.Fatalf("cleared records = %#v, want record-1", store.cleared)
	}
}

func TestApplyPendingSkipsWhenKnownDigestMatchesAnyLocalRepoDigestInternal(t *testing.T) {
	firstDigest := digest.FromString("first-local-digest").String()
	secondDigest := digest.FromString("second-local-digest").String()

	store := &fakePendingStore{records: []types.ImageUpdateRecord{{
		ID:           "record-1",
		Repository:   "nginx",
		Tag:          "1.27",
		HasUpdate:    true,
		UpdateType:   types.UpdateTypeDigest,
		LatestDigest: &secondDigest,
	}}}
	puller := &fakePuller{}
	resolver := &countingDigestResolver{digest: secondDigest}
	dockerClient := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		switch dockerAPIPathInternal(r.URL.Path) {
		case "/images/nginx:1.27/json", "/images/docker.io/library/nginx:1.27/json":
			writeDockerJSONInternal(t, w, image.InspectResponse{
				ID:          "sha256:same",
				RepoTags:    []string{"nginx:1.27"},
				RepoDigests: []string{"nginx@" + firstDigest, "nginx@" + secondDigest},
			})
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newServiceInternal(Config{
		DockerClientProvider:   &fakeDockerClientProvider{client: dockerClient},
		PendingStore:           store,
		ImagePuller:            puller,
		RegistryDigestResolver: resolver,
		UsedImageCollector: UsedImageCollectorFunc(func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"docker.io/library/nginx:1.27": {}}, nil
		}),
	})

	got, err := service.ApplyPending(context.Background(), types.Options{})
	if err != nil {
		t.Fatalf("ApplyPending() error = %v", err)
	}
	if got.Checked != 1 || got.Updated != 0 || got.Skipped != 1 || got.Failed != 0 {
		t.Fatalf("ApplyPending() counts = checked:%d updated:%d skipped:%d failed:%d", got.Checked, got.Updated, got.Skipped, got.Failed)
	}
	if len(got.Items) != 1 || got.Items[0].Status != types.StatusSkipped {
		t.Fatalf("ApplyPending() items = %#v, want one skipped item", got.Items)
	}
	if len(puller.pulled) != 0 {
		t.Fatalf("pulled images = %#v, want none", puller.pulled)
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver calls = %d, want 0", resolver.calls)
	}
	if len(store.cleared) != 0 {
		t.Fatalf("cleared records = %#v, want none", store.cleared)
	}
}

func TestApplyPendingReusesDockerClientWhileBuildingPlansInternal(t *testing.T) {
	store := &fakePendingStore{records: []types.ImageUpdateRecord{
		{ID: "sha256:old-one", Repository: "nginx", Tag: "1.27", HasUpdate: true, UpdateType: types.UpdateTypeDigest},
		{ID: "sha256:old-two", Repository: "redis", Tag: "7", HasUpdate: true, UpdateType: types.UpdateTypeDigest},
	}}
	dockerClient := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		switch dockerAPIPathInternal(r.URL.Path) {
		case "/images/nginx:1.27/json", "/images/redis:7/json":
			http.Error(w, "not found", http.StatusNotFound)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	provider := &fakeDockerClientProvider{client: dockerClient}
	service := newServiceInternal(Config{
		DockerClientProvider: provider,
		PendingStore:         store,
		ImagePuller:          &fakePuller{err: errors.New("stop before restart")},
		UsedImageCollector: UsedImageCollectorFunc(func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{
				"docker.io/library/nginx:1.27": {},
				"docker.io/library/redis:7":    {},
			}, nil
		}),
	})

	_, _ = service.ApplyPending(context.Background(), types.Options{})

	if provider.calls != 2 {
		t.Fatalf("DockerClient calls = %d, want 2 total calls independent of record count", provider.calls)
	}
}

func TestApplyPendingKeepsPulledRecordWhenRestartFailsInternal(t *testing.T) {
	store := &fakePendingStore{records: []types.ImageUpdateRecord{{
		ID:         "sha256:old-app",
		Repository: "app",
		Tag:        "1",
		HasUpdate:  true,
		UpdateType: types.UpdateTypeDigest,
	}}}
	pulled := false
	puller := &fakePuller{after: func(string) {
		pulled = true
	}}
	dockerClient := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPathInternal(r.URL.Path)
		switch {
		case r.Method == http.MethodGet && path == "/images/app:1/json":
			if pulled {
				writeDockerJSONInternal(t, w, image.InspectResponse{ID: "sha256:new-app"})
				return
			}
			writeDockerJSONInternal(t, w, image.InspectResponse{ID: "sha256:old-app"})
		case r.Method == http.MethodGet && path == "/containers/json":
			writeDockerJSONInternal(t, w, []container.Summary{
				{ID: "app-id", Names: []string{"/app"}, Image: "app:1", ImageID: "sha256:old-app", State: "running"},
			})
		case r.Method == http.MethodGet && path == "/containers/app-id/json":
			writeDockerJSONInternal(t, w, container.InspectResponse{ID: "app-id", Name: "/app", Image: "sha256:old-app", Config: &container.Config{Image: "app:1"}})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/stop"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/containers/"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && path == "/containers/create":
			writeDockerJSONInternal(t, w, map[string]any{"Id": "new-app-id", "Warnings": []string{}})
		case r.Method == http.MethodPost && path == "/containers/new-app-id/start":
			http.Error(w, "start failed", http.StatusInternalServerError)
		case r.Method == http.MethodPost && path == "/containers/new-app-id/stop":
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newServiceInternal(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		PendingStore:         store,
		ImagePuller:          puller,
		UsedImageCollector: UsedImageCollectorFunc(func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"docker.io/library/app:1": {}}, nil
		}),
	})

	got, err := service.ApplyPending(context.Background(), types.Options{})
	if err != nil {
		t.Fatalf("ApplyPending() error = %v", err)
	}
	if len(store.cleared) != 0 {
		t.Fatalf("cleared records = %#v, want none after restart failure; items=%#v", store.cleared, got.Items)
	}
	foundFailedContainer := false
	for _, item := range got.Items {
		if item.ResourceType == types.ResourceTypeContainer && item.Status == types.StatusFailed {
			foundFailedContainer = true
		}
	}
	if !foundFailedContainer {
		t.Fatalf("ApplyPending() items = %#v, want failed container result", got.Items)
	}
}

func TestRestartContainersUsingOldImagesRestartsDependenciesInWatchtowerOrderInternal(t *testing.T) {
	var operations []string
	createIDs := map[string]string{"db": "new-db-id", "web": "new-web-id"}

	dockerClient := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPathInternal(r.URL.Path)
		switch {
		case r.Method == http.MethodGet && path == "/containers/json":
			writeDockerJSONInternal(t, w, []container.Summary{
				{ID: "db-id", Names: []string{"/db"}, Image: "db:1", ImageID: "sha256:old-db", State: "running"},
				{ID: "web-id", Names: []string{"/web"}, Image: "web:1", ImageID: "sha256:web", Labels: map[string]string{labels.LabelDependsOn: "db"}, State: "running"},
			})
		case r.Method == http.MethodGet && path == "/containers/db-id/json":
			writeDockerJSONInternal(t, w, container.InspectResponse{
				ID:    "db-id",
				Name:  "/db",
				Image: "sha256:old-db",
				Config: &container.Config{
					Image: "db:1",
				},
			})
		case r.Method == http.MethodGet && path == "/containers/web-id/json":
			writeDockerJSONInternal(t, w, container.InspectResponse{
				ID:    "web-id",
				Name:  "/web",
				Image: "sha256:web",
				Config: &container.Config{
					Image:  "web:1",
					Labels: map[string]string{labels.LabelDependsOn: "db"},
				},
			})
		case r.Method == http.MethodGet && path == "/images/db:2/json":
			writeDockerJSONInternal(t, w, image.InspectResponse{ID: "sha256:new-db"})
		case r.Method == http.MethodGet && path == "/images/web:1/json":
			writeDockerJSONInternal(t, w, image.InspectResponse{ID: "sha256:web"})
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/stop"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/stop")
			operations = append(operations, "stop:"+id)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/containers/"):
			id := strings.TrimPrefix(path, "/containers/")
			operations = append(operations, "remove:"+id)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && path == "/containers/create":
			name := r.URL.Query().Get("name")
			operations = append(operations, "create:"+name)
			writeDockerJSONInternal(t, w, map[string]any{"Id": createIDs[name], "Warnings": []string{}})
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/start"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/start")
			operations = append(operations, "start:"+id)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newServiceInternal(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
	})

	results, err := service.RestartContainersUsingOldImages(context.Background(), map[string]string{"sha256:old-db": "db:2"}, nil)
	if err != nil {
		t.Fatalf("RestartContainersUsingOldImages() error = %v", err)
	}
	assertOperationsInOrderInternal(t, operations, []string{
		"stop:web-id",
		"stop:db-id",
		"start:new-db-id",
		"start:new-web-id",
	})
	statusByName := map[string]string{}
	for _, result := range results {
		statusByName[result.ResourceName] = result.Status
	}
	if statusByName["db"] != types.StatusUpdated {
		t.Fatalf("db status = %q, want updated; results=%#v", statusByName["db"], results)
	}
	if statusByName["web"] != types.StatusRestarted {
		t.Fatalf("web status = %q, want restarted; results=%#v", statusByName["web"], results)
	}
}

func TestUpdateStandaloneContainerRollsBackAndRemovesDanglingCreateOnStartFailureInternal(t *testing.T) {
	var operations []string
	var createdImages []string
	recorder := &fakeEventRecorder{}
	dockerClient := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPathInternal(r.URL.Path)
		switch {
		case r.Method == http.MethodPost && path == "/containers/old-id/stop":
			operations = append(operations, "stop:old-id")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && path == "/containers/old-id":
			operations = append(operations, "remove:old-id")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && path == "/containers/new-id":
			operations = append(operations, "remove:new-id")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && path == "/containers/create":
			var body container.Config
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			createdImages = append(createdImages, body.Image)
			if len(createdImages) == 1 {
				operations = append(operations, "create:new")
				writeDockerJSONInternal(t, w, map[string]any{"Id": "new-id", "Warnings": []string{}})
				return
			}
			operations = append(operations, "create:rollback")
			writeDockerJSONInternal(t, w, map[string]any{"Id": "rollback-id", "Warnings": []string{}})
		case r.Method == http.MethodPost && path == "/containers/new-id/start":
			operations = append(operations, "start:new-id")
			http.Error(w, "start failed", http.StatusInternalServerError)
		case r.Method == http.MethodPost && path == "/containers/rollback-id/start":
			operations = append(operations, "start:rollback-id")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newServiceInternal(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		EventRecorder:        recorder,
	})

	err := service.UpdateStandaloneContainer(context.Background(),
		container.Summary{ID: "old-id", Names: []string{"/app"}},
		container.InspectResponse{ID: "old-id", Name: "/app", Image: "sha256:old-image", Config: &container.Config{Image: "app:1"}},
		"app:2",
	)

	if err == nil {
		t.Fatal("UpdateStandaloneContainer() error = nil, want start failure with rollback outcome")
	}
	if !strings.Contains(err.Error(), "rollback succeeded") {
		t.Fatalf("error = %q, want rollback succeeded detail", err.Error())
	}
	if len(createdImages) != 2 || createdImages[0] != "app:2" || createdImages[1] != "sha256:old-image" {
		t.Fatalf("created images = %#v, want new ref then old image ID", createdImages)
	}
	assertOperationsInOrderInternal(t, operations, []string{
		"stop:old-id",
		"remove:old-id",
		"create:new",
		"start:new-id",
		"remove:new-id",
		"create:rollback",
		"start:rollback-id",
	})
	var sawCleanup, sawRollback bool
	for _, event := range recorder.events {
		if event.Phase == "container_cleanup" {
			sawCleanup = true
		}
		if event.Phase == "container_rollback" {
			sawRollback = true
		}
	}
	if !sawCleanup || !sawRollback {
		t.Fatalf("events = %#v, want cleanup and rollback events", recorder.events)
	}
}

func TestUpdateStandaloneContainerRemovesCreatedContainerWhenExtraNetworkConnectTimesOutInternal(t *testing.T) {
	var operations []string
	removedNew := false
	dockerClient := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPathInternal(r.URL.Path)
		switch {
		case r.Method == http.MethodPost && path == "/containers/old-id/stop":
			operations = append(operations, "stop:old-id")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && path == "/containers/old-id":
			operations = append(operations, "remove:old-id")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && path == "/containers/create":
			var body container.Config
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			if body.Image == "app:2" {
				operations = append(operations, "create:new")
				writeDockerJSONInternal(t, w, map[string]any{"Id": "new-id", "Warnings": []string{}})
				return
			}
			if !removedNew {
				http.Error(w, "name already in use", http.StatusConflict)
				return
			}
			operations = append(operations, "create:rollback")
			writeDockerJSONInternal(t, w, map[string]any{"Id": "rollback-id", "Warnings": []string{}})
		case r.Method == http.MethodPost && path == "/networks/secondary/connect":
			var body struct {
				Container string
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode network connect body: %v", err)
			}
			operations = append(operations, "connect:secondary:"+body.Container)
			if body.Container != "new-id" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			time.Sleep(50 * time.Millisecond)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && path == "/containers/new-id":
			removedNew = true
			operations = append(operations, "remove:new-id")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && path == "/containers/rollback-id/start":
			operations = append(operations, "start:rollback-id")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newServiceInternal(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		OperationTimeout:     10 * time.Millisecond,
	})

	err := service.UpdateStandaloneContainer(context.Background(),
		container.Summary{ID: "old-id", Names: []string{"/app"}},
		container.InspectResponse{
			ID:     "old-id",
			Name:   "/app",
			Image:  "sha256:old-image",
			Config: &container.Config{Image: "app:1"},
			NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{
				"primary":   {},
				"secondary": {},
			}},
		},
		"app:2",
	)

	if err == nil {
		t.Fatal("UpdateStandaloneContainer() error = nil, want network timeout with rollback outcome")
	}
	if !strings.Contains(err.Error(), "rollback succeeded") {
		t.Fatalf("error = %q, want rollback succeeded detail", err.Error())
	}
	assertOperationsInOrderInternal(t, operations, []string{
		"create:new",
		"connect:secondary:new-id",
		"remove:new-id",
		"create:rollback",
		"start:rollback-id",
	})
}

func TestUpdateStandaloneContainerTreatsAmbiguousStartErrorAsSuccessWhenInspectRunningInternal(t *testing.T) {
	var operations []string
	dockerClient := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPathInternal(r.URL.Path)
		switch {
		case r.Method == http.MethodPost && path == "/containers/old-id/stop":
			operations = append(operations, "stop:old-id")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && path == "/containers/old-id":
			operations = append(operations, "remove:old-id")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && path == "/containers/create":
			operations = append(operations, "create:"+r.URL.Query().Get("name"))
			writeDockerJSONInternal(t, w, map[string]any{"Id": "new-id", "Warnings": []string{}})
		case r.Method == http.MethodPost && path == "/containers/new-id/start":
			operations = append(operations, "start:new-id")
			closeHTTPConnectionInternal(t, w)
		case r.Method == http.MethodGet && path == "/containers/new-id/json":
			operations = append(operations, "inspect:new-id")
			writeDockerJSONInternal(t, w, container.InspectResponse{
				ID:    "new-id",
				Name:  "/app",
				Image: "sha256:new-image",
				State: &container.State{Running: true, Status: container.StateRunning},
			})
		case r.Method == http.MethodDelete && path == "/containers/new-id":
			operations = append(operations, "remove:new-id")
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newServiceInternal(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
	})

	err := service.UpdateStandaloneContainer(context.Background(),
		container.Summary{ID: "old-id", Names: []string{"/app"}},
		container.InspectResponse{ID: "old-id", Name: "/app", Image: "sha256:old-image", Config: &container.Config{Image: "app:1"}},
		"app:2",
	)

	if err != nil {
		t.Fatalf("UpdateStandaloneContainer() error = %v, want nil after inspect confirms running", err)
	}
	assertOperationsInOrderInternal(t, operations, []string{
		"start:new-id",
		"inspect:new-id",
	})
	for _, operation := range operations {
		if operation == "remove:new-id" {
			t.Fatalf("operations = %#v, did not expect removal after inspect confirms running", operations)
		}
	}
}

func TestUpdateStandaloneContainerRollsBackAmbiguousStartErrorWhenInspectNotRunningInternal(t *testing.T) {
	var operations []string
	dockerClient := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPathInternal(r.URL.Path)
		switch {
		case r.Method == http.MethodPost && path == "/containers/old-id/stop":
			operations = append(operations, "stop:old-id")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && path == "/containers/old-id":
			operations = append(operations, "remove:old-id")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && path == "/containers/new-id":
			operations = append(operations, "remove:new-id")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && path == "/containers/create":
			var body container.Config
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			if body.Image == "app:2" {
				operations = append(operations, "create:new")
				writeDockerJSONInternal(t, w, map[string]any{"Id": "new-id", "Warnings": []string{}})
				return
			}
			operations = append(operations, "create:rollback")
			writeDockerJSONInternal(t, w, map[string]any{"Id": "rollback-id", "Warnings": []string{}})
		case r.Method == http.MethodPost && path == "/containers/new-id/start":
			operations = append(operations, "start:new-id")
			closeHTTPConnectionInternal(t, w)
		case r.Method == http.MethodGet && path == "/containers/new-id/json":
			operations = append(operations, "inspect:new-id")
			writeDockerJSONInternal(t, w, container.InspectResponse{
				ID:    "new-id",
				Name:  "/app",
				Image: "sha256:new-image",
				State: &container.State{Running: false, Status: container.StateExited},
			})
		case r.Method == http.MethodPost && path == "/containers/rollback-id/start":
			operations = append(operations, "start:rollback-id")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newServiceInternal(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
	})

	err := service.UpdateStandaloneContainer(context.Background(),
		container.Summary{ID: "old-id", Names: []string{"/app"}},
		container.InspectResponse{ID: "old-id", Name: "/app", Image: "sha256:old-image", Config: &container.Config{Image: "app:1"}},
		"app:2",
	)

	if err == nil {
		t.Fatal("UpdateStandaloneContainer() error = nil, want ambiguous start failure with rollback outcome")
	}
	if !strings.Contains(err.Error(), "rollback succeeded") {
		t.Fatalf("error = %q, want rollback succeeded detail", err.Error())
	}
	assertOperationsInOrderInternal(t, operations, []string{
		"start:new-id",
		"inspect:new-id",
		"remove:new-id",
		"create:rollback",
		"start:rollback-id",
	})
}

func TestRestartContainersUsingOldImagesLogsCycleFallbackInternal(t *testing.T) {
	logs := &captureLogHandlerInternal{}
	dockerClient := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPathInternal(r.URL.Path)
		switch {
		case r.Method == http.MethodGet && path == "/containers/json":
			writeDockerJSONInternal(t, w, []container.Summary{
				{ID: "a-id", Names: []string{"/a"}, Image: "a:1", ImageID: "sha256:old-a", Labels: map[string]string{labels.LabelDependsOn: "b"}, State: "running"},
				{ID: "b-id", Names: []string{"/b"}, Image: "b:1", ImageID: "sha256:old-b", Labels: map[string]string{labels.LabelDependsOn: "a"}, State: "running"},
			})
		case r.Method == http.MethodGet && path == "/containers/a-id/json":
			writeDockerJSONInternal(t, w, container.InspectResponse{ID: "a-id", Name: "/a", Image: "sha256:old-a", Config: &container.Config{Image: "a:1", Labels: map[string]string{labels.LabelDependsOn: "b"}}})
		case r.Method == http.MethodGet && path == "/containers/b-id/json":
			writeDockerJSONInternal(t, w, container.InspectResponse{ID: "b-id", Name: "/b", Image: "sha256:old-b", Config: &container.Config{Image: "b:1", Labels: map[string]string{labels.LabelDependsOn: "a"}}})
		case r.Method == http.MethodGet && path == "/images/a:2/json":
			writeDockerJSONInternal(t, w, image.InspectResponse{ID: "sha256:new-a"})
		case r.Method == http.MethodGet && path == "/images/b:2/json":
			writeDockerJSONInternal(t, w, image.InspectResponse{ID: "sha256:new-b"})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/stop"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/containers/"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && path == "/containers/create":
			writeDockerJSONInternal(t, w, map[string]any{"Id": "new-" + r.URL.Query().Get("name"), "Warnings": []string{}})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newServiceInternal(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		Logger:               slog.New(logs),
	})

	results, err := service.RestartContainersUsingOldImages(context.Background(), map[string]string{
		"sha256:old-a": "a:2",
		"sha256:old-b": "b:2",
	}, nil)
	if err != nil {
		t.Fatalf("RestartContainersUsingOldImages() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v, want both containers processed", results)
	}
	for _, record := range logs.records {
		if record.Message == "container dependency sort failed; restarting in discovery order" {
			return
		}
	}
	t.Fatalf("log records = %#v, want cycle fallback warning", logs.records)
}

func TestRestartContainersUsingOldImagesRoutesLegacyArcaneServerThroughSelfUpdaterInternal(t *testing.T) {
	projectUpdater := &fakeProjectUpdater{
		projects: map[string]types.ComposeProject{
			"arcane": {ID: "project-arcane", Name: "arcane"},
		},
	}
	selfUpdater := &fakeSelfUpdater{}
	var operations []string

	dockerClient := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPathInternal(r.URL.Path)
		switch {
		case r.Method == http.MethodGet && path == "/containers/json":
			writeDockerJSONInternal(t, w, []container.Summary{
				{
					ID:      "arcane-id",
					Names:   []string{"/arcane"},
					Image:   "ghcr.io/getarcaneapp/arcane:1",
					ImageID: "sha256:old-arcane",
					Labels: map[string]string{
						"com.docker.compose.project":   "arcane",
						"com.docker.compose.service":   "server",
						labels.LabelArcaneLegacyServer: "true",
					},
					State: "running",
				},
			})
		case r.Method == http.MethodGet && path == "/containers/arcane-id/json":
			writeDockerJSONInternal(t, w, container.InspectResponse{
				ID:    "arcane-id",
				Name:  "/arcane",
				Image: "sha256:old-arcane",
				Config: &container.Config{
					Image: "ghcr.io/getarcaneapp/arcane:1",
					Labels: map[string]string{
						"com.docker.compose.project":   "arcane",
						"com.docker.compose.service":   "server",
						labels.LabelArcaneLegacyServer: "true",
					},
				},
			})
		case r.Method == http.MethodGet && path == "/images/ghcr.io/getarcaneapp/arcane:2/json":
			writeDockerJSONInternal(t, w, image.InspectResponse{ID: "sha256:new-arcane"})
		case r.Method == http.MethodPost && strings.Contains(path, "/containers/"):
			operations = append(operations, r.Method+":"+path)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.Contains(path, "/containers/"):
			operations = append(operations, r.Method+":"+path)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newServiceInternal(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		ProjectUpdater:       projectUpdater,
		SelfUpdater:          selfUpdater,
		LabelPolicy:          labels.DefaultLabelPolicy(),
	})

	results, err := service.RestartContainersUsingOldImages(context.Background(), map[string]string{"sha256:old-arcane": "ghcr.io/getarcaneapp/arcane:2"}, nil)
	if err != nil {
		t.Fatalf("RestartContainersUsingOldImages() error = %v", err)
	}
	if len(selfUpdater.targets) != 1 {
		t.Fatalf("self-update targets = %#v, want exactly one", selfUpdater.targets)
	}
	if selfUpdater.targets[0].ContainerID != "arcane-id" || selfUpdater.targets[0].InstanceType != "server" {
		t.Fatalf("self-update target = %#v, want arcane server", selfUpdater.targets[0])
	}
	if selfUpdater.targets[0].NewImageRef != "ghcr.io/getarcaneapp/arcane:2" {
		t.Fatalf("self-update target NewImageRef = %q, want resolved new image ref", selfUpdater.targets[0].NewImageRef)
	}
	if len(projectUpdater.updateCalls) != 0 {
		t.Fatalf("project updater calls = %#v, want none", projectUpdater.updateCalls)
	}
	if len(operations) != 0 {
		t.Fatalf("standalone container operations = %#v, want none", operations)
	}
	if len(results) != 1 || results[0].Status != types.StatusUpdated {
		t.Fatalf("results = %#v, want one updated result", results)
	}
}

func TestRestartContainersUsingOldImagesSelfContainerIDFiresAfterStandaloneInternal(t *testing.T) {
	var operations []string
	selfUpdater := &recordingSelfUpdater{operations: &operations}

	dockerClient := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPathInternal(r.URL.Path)
		switch {
		case r.Method == http.MethodGet && path == "/containers/json":
			writeDockerJSONInternal(t, w, []container.Summary{
				{ID: "app-id", Names: []string{"/app"}, Image: "app:1", ImageID: "sha256:old-app", State: "running"},
				{ID: "self-id", Names: []string{"/arcane"}, Image: "ghcr.io/getarcaneapp/arcane:1", ImageID: "sha256:old-arcane", State: "running"},
			})
		case r.Method == http.MethodGet && path == "/containers/app-id/json":
			writeDockerJSONInternal(t, w, container.InspectResponse{
				ID:     "app-id",
				Name:   "/app",
				Image:  "sha256:old-app",
				Config: &container.Config{Image: "app:1"},
			})
		case r.Method == http.MethodGet && path == "/containers/self-id/json":
			writeDockerJSONInternal(t, w, container.InspectResponse{
				ID:     "self-id",
				Name:   "/arcane",
				Image:  "sha256:old-arcane",
				Config: &container.Config{Image: "ghcr.io/getarcaneapp/arcane:1"},
			})
		case r.Method == http.MethodGet && path == "/images/app:2/json":
			writeDockerJSONInternal(t, w, image.InspectResponse{ID: "sha256:new-app"})
		case r.Method == http.MethodGet && path == "/images/ghcr.io/getarcaneapp/arcane:2/json":
			writeDockerJSONInternal(t, w, image.InspectResponse{ID: "sha256:new-arcane"})
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/stop"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/stop")
			operations = append(operations, "stop:"+id)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/containers/"):
			id := strings.TrimPrefix(path, "/containers/")
			operations = append(operations, "remove:"+id)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && path == "/containers/create":
			operations = append(operations, "create:"+r.URL.Query().Get("name"))
			writeDockerJSONInternal(t, w, map[string]any{"Id": "new-app-id", "Warnings": []string{}})
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/start"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/start")
			operations = append(operations, "start:"+id)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newServiceInternal(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		SelfUpdater:          selfUpdater,
		SelfContainerID:      "self-id",
		LabelPolicy:          labels.DefaultLabelPolicy(),
	})

	results, err := service.RestartContainersUsingOldImages(context.Background(), map[string]string{
		"sha256:old-app":    "app:2",
		"sha256:old-arcane": "ghcr.io/getarcaneapp/arcane:2",
	}, nil)
	if err != nil {
		t.Fatalf("RestartContainersUsingOldImages() error = %v", err)
	}

	// The unlabeled self container must route through the SelfUpdater (by
	// container ID) and only after every standalone recreate has finished.
	assertOperationsInOrderInternal(t, operations, []string{
		"stop:app-id",
		"start:new-app-id",
		"self-update:self-id",
	})
	if len(selfUpdater.targets) != 1 || selfUpdater.targets[0].NewImageRef != "ghcr.io/getarcaneapp/arcane:2" {
		t.Fatalf("self-update targets = %#v, want one with the new image ref", selfUpdater.targets)
	}
	statusByName := map[string]string{}
	for _, result := range results {
		statusByName[result.ResourceName] = result.Status
	}
	if statusByName["app"] != types.StatusUpdated || statusByName["arcane"] != types.StatusUpdated {
		t.Fatalf("results = %#v, want app and arcane updated", results)
	}
}

func TestRestartContainersUsingOldImagesVerifiesComposeServiceAfterProjectErrorInternal(t *testing.T) {
	projectUpdater := &fakeProjectUpdater{
		projects: map[string]types.ComposeProject{"app": {ID: "project-app", Name: "app"}},
		err:      errors.New("compose exited after partial update"),
	}
	dockerClient := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPathInternal(r.URL.Path)
		switch {
		case r.Method == http.MethodGet && path == "/containers/json":
			if strings.Contains(r.URL.RawQuery, "label") {
				writeDockerJSONInternal(t, w, []container.Summary{
					{ID: "web-new", Names: []string{"/web"}, Image: "app:2", ImageID: "sha256:new-app", State: "running"},
				})
				return
			}
			writeDockerJSONInternal(t, w, []container.Summary{
				{
					ID:      "web-old",
					Names:   []string{"/web"},
					Image:   "app:1",
					ImageID: "sha256:old-app",
					Labels: map[string]string{
						"com.docker.compose.project": "app",
						"com.docker.compose.service": "web",
					},
					State: "running",
				},
			})
		case r.Method == http.MethodGet && path == "/containers/web-old/json":
			writeDockerJSONInternal(t, w, container.InspectResponse{
				ID:    "web-old",
				Name:  "/web",
				Image: "sha256:old-app",
				Config: &container.Config{
					Image: "app:1",
					Labels: map[string]string{
						"com.docker.compose.project": "app",
						"com.docker.compose.service": "web",
					},
				},
			})
		case r.Method == http.MethodGet && path == "/images/app:2/json":
			writeDockerJSONInternal(t, w, image.InspectResponse{ID: "sha256:new-app"})
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newServiceInternal(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		ProjectUpdater:       projectUpdater,
	})

	results, err := service.RestartContainersUsingOldImages(context.Background(), map[string]string{"sha256:old-app": "app:2"}, nil)
	if err != nil {
		t.Fatalf("RestartContainersUsingOldImages() error = %v", err)
	}
	if len(results) != 1 || results[0].Status != types.StatusUpdated {
		t.Fatalf("results = %#v, want compose service marked updated after verification", results)
	}
	if len(projectUpdater.updateCalls) != 1 {
		t.Fatalf("project updater calls = %#v, want one call", projectUpdater.updateCalls)
	}
}

func TestRestartContainersUsingOldImagesOperationTimeoutCancelsSlowStopInternal(t *testing.T) {
	stopStarted := make(chan struct{})
	stopReleased := make(chan struct{})
	dockerClient := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPathInternal(r.URL.Path)
		switch {
		case r.Method == http.MethodGet && path == "/containers/json":
			writeDockerJSONInternal(t, w, []container.Summary{
				{ID: "app-id", Names: []string{"/app"}, Image: "app:1", ImageID: "sha256:old-app", State: "running"},
			})
		case r.Method == http.MethodGet && path == "/containers/app-id/json":
			writeDockerJSONInternal(t, w, container.InspectResponse{ID: "app-id", Name: "/app", Image: "sha256:old-app", Config: &container.Config{Image: "app:1"}})
		case r.Method == http.MethodGet && path == "/images/app:2/json":
			writeDockerJSONInternal(t, w, image.InspectResponse{ID: "sha256:new-app"})
		case r.Method == http.MethodPost && path == "/containers/app-id/stop":
			close(stopStarted)
			<-r.Context().Done()
			close(stopReleased)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newServiceInternal(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		OperationTimeout:     10 * time.Millisecond,
	})

	results, err := service.RestartContainersUsingOldImages(context.Background(), map[string]string{"sha256:old-app": "app:2"}, nil)
	if err != nil {
		t.Fatalf("RestartContainersUsingOldImages() error = %v", err)
	}
	<-stopStarted
	<-stopReleased
	if len(results) != 1 || results[0].Status != types.StatusFailed || !strings.Contains(results[0].Error, "context deadline exceeded") {
		t.Fatalf("results = %#v, want failed stop due to timeout", results)
	}
}

func assertOperationsInOrderInternal(t *testing.T, operations, want []string) {
	t.Helper()

	start := 0
	for _, target := range want {
		found := false
		for i := start; i < len(operations); i++ {
			if operations[i] == target {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("operations = %#v, want %q after index %d", operations, target, start)
		}
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

	pinnedRef := "nginx@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	ref, source = ResolvePullableImageRef("sha256:abc", pinnedRef, []string{pinnedRef})
	if ref != "" || source != "" {
		t.Fatalf("ResolvePullableImageRef() digest-pinned fallback = %q/%q, want empty", ref, source)
	}
}

func TestServiceFallsBackToStandaloneWhenComposeProjectUnresolvedInternal(t *testing.T) {
	projectUpdater := &fakeProjectUpdater{projects: map[string]types.ComposeProject{}}
	service := NewService(Config{
		DockerClientProvider: &fakeDockerClientProvider{err: errors.New("no docker in test")},
		ProjectUpdater:       projectUpdater,
	})
	err := service.updateComposeOrStandaloneInternal(context.Background(), container.Summary{
		ID: "container-1",
	}, container.InspectResponse{
		Config: &container.Config{Labels: map[string]string{
			"com.docker.compose.project": "app",
			"com.docker.compose.service": "web",
		}},
	}, "nginx:latest")
	// The unresolved project must route to the standalone path, which is the
	// first caller of the (failing) docker client in this setup.
	if err == nil || !strings.Contains(err.Error(), "docker connect") {
		t.Fatalf("updateComposeOrStandaloneInternal() error = %v, want standalone-path docker connect error", err)
	}
	if len(projectUpdater.updateCalls) != 0 {
		t.Fatalf("UpdateServices called %v times for unresolved project, want 0", len(projectUpdater.updateCalls))
	}
}
