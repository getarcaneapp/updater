package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"go.getarcane.app/updater/pkg/labels"
	"go.getarcane.app/updater/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
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

type fakeProjectUpdater struct {
	projects    map[string]types.ComposeProject
	updateCalls []string
}

func (f *fakeProjectUpdater) ProjectByComposeName(ctx context.Context, composeName string) (types.ComposeProject, error) {
	if project, ok := f.projects[composeName]; ok {
		return project, nil
	}
	return types.ComposeProject{}, errors.New("project not found")
}

func (f *fakeProjectUpdater) UpdateServices(ctx context.Context, projectID string, services []string) error {
	f.updateCalls = append(f.updateCalls, projectID+":"+strings.Join(services, ","))
	return nil
}

type fakeSelfUpdater struct {
	targets []types.SelfUpdateTarget
}

func (f *fakeSelfUpdater) TriggerSelfUpdate(ctx context.Context, target types.SelfUpdateTarget) error {
	f.targets = append(f.targets, target)
	return nil
}

func newDockerClientForHandlerInternal(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	dcli, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.41"))
	if err != nil {
		t.Fatalf("new docker client: %v", err)
	}
	return dcli
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

func TestApplyPendingSkipsUnchangedPulledImageInternal(t *testing.T) {
	store := &fakePendingStore{records: []types.ImageUpdateRecord{{
		ID:         "record-1",
		Repository: "nginx",
		Tag:        "1.27",
		HasUpdate:  true,
		UpdateType: types.UpdateTypeDigest,
	}}}
	puller := &fakePuller{}
	dcli := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
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
		DockerClientProvider: fakeDockerClientProvider{client: dcli},
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
	dcli := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
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
		DockerClientProvider: fakeDockerClientProvider{client: dcli},
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

func TestRestartContainersUsingOldImagesRestartsDependenciesInWatchtowerOrderInternal(t *testing.T) {
	operations := []string{}
	createIDs := map[string]string{"db": "new-db-id", "web": "new-web-id"}

	dcli := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
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
		DockerClientProvider: fakeDockerClientProvider{client: dcli},
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

func TestRestartContainersUsingOldImagesRoutesLegacyArcaneServerThroughSelfUpdaterInternal(t *testing.T) {
	projectUpdater := &fakeProjectUpdater{
		projects: map[string]types.ComposeProject{
			"arcane": {ID: "project-arcane", Name: "arcane"},
		},
	}
	selfUpdater := &fakeSelfUpdater{}
	operations := []string{}

	dcli := newDockerClientForHandlerInternal(t, func(w http.ResponseWriter, r *http.Request) {
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
		DockerClientProvider: fakeDockerClientProvider{client: dcli},
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

	pinnedRef := "nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ref, source = ResolvePullableImageRef("sha256:abc", pinnedRef, []string{pinnedRef})
	if ref != "" || source != "" {
		t.Fatalf("ResolvePullableImageRef() digest-pinned fallback = %q/%q, want empty", ref, source)
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
