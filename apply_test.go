package updater

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/opencontainers/go-digest"
)

func TestApplyPendingDefaultStoreNoOperations(t *testing.T) {
	service := newServiceForTest(t, Config{})

	got, err := service.ApplyPending(context.Background(), Options{})
	if err != nil {
		t.Fatalf("ApplyPending() error = %v", err)
	}
	if got.Checked != 0 || got.Updated != 0 || got.Skipped != 0 || got.Failed != 0 {
		t.Fatalf("ApplyPending() counts = checked:%d updated:%d skipped:%d failed:%d", got.Checked, got.Updated, got.Skipped, got.Failed)
	}
}

func TestApplyPendingDryRunRecordsSkippedImage(t *testing.T) {
	store := &fakePendingStore{records: []ImageUpdateRecord{{
		ID:         "sha256:old",
		Repository: "nginx",
		Tag:        "1.27",
		HasUpdate:  true,
		UpdateType: UpdateTypeDigest,
	}}}
	recorder := &fakeRunRecorder{}
	puller := &fakePuller{}
	service := newServiceForTest(t, Config{
		DockerClientProvider: &fakeDockerClientProvider{err: errors.New("not used in dry run")},
		PendingStore:         store,
		RunRecorder:          recorder,
		ImagePuller:          puller,
		UsedImageCollector: UsedImageCollectorFunc(func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"docker.io/library/nginx:1.27": {}}, nil
		}),
	})

	got, err := service.ApplyPending(context.Background(), Options{DryRun: true})
	if err != nil {
		t.Fatalf("ApplyPending() error = %v", err)
	}
	if got.Checked != 1 || got.Skipped != 1 || got.Updated != 0 || got.Failed != 0 {
		t.Fatalf("ApplyPending() counts = checked:%d skipped:%d updated:%d failed:%d", got.Checked, got.Skipped, got.Updated, got.Failed)
	}
	if len(puller.pulled) != 0 {
		t.Fatalf("dry run pulled images: %#v", puller.pulled)
	}
	if len(recorder.results) != 1 || recorder.results[0].Status != StatusSkipped {
		t.Fatalf("recorded results = %#v, want one skipped result", recorder.results)
	}
}

func TestApplyPendingSkipsUnchangedPulledImage(t *testing.T) {
	store := &fakePendingStore{records: []ImageUpdateRecord{{
		ID:         "record-1",
		Repository: "nginx",
		Tag:        "1.27",
		HasUpdate:  true,
		UpdateType: UpdateTypeDigest,
	}}}
	puller := &fakePuller{}
	dockerClient := newDockerClientForHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch dockerAPIPath(r.URL.Path) {
		case "/images/nginx:1.27/json":
			writeDockerJSON(t, w, image.InspectResponse{
				ID:       "sha256:same",
				RepoTags: []string{"nginx:1.27"},
			})
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newService(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		PendingStore:         store,
		ImagePuller:          puller,
		UsedImageCollector: UsedImageCollectorFunc(func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"docker.io/library/nginx:1.27": {}}, nil
		}),
	})

	got, err := service.ApplyPending(context.Background(), Options{})
	if err != nil {
		t.Fatalf("ApplyPending() error = %v", err)
	}
	if got.Checked != 1 || got.Updated != 0 || got.Skipped != 0 || got.Failed != 0 {
		t.Fatalf("ApplyPending() counts = checked:%d updated:%d skipped:%d failed:%d", got.Checked, got.Updated, got.Skipped, got.Failed)
	}
	if len(got.Items) != 1 || got.Items[0].Status != StatusUpToDate {
		t.Fatalf("ApplyPending() items = %#v, want one up-to-date item", got.Items)
	}
	if len(puller.pulled) != 1 || puller.pulled[0] != "nginx:1.27" {
		t.Fatalf("pulled images = %#v, want nginx:1.27", puller.pulled)
	}
	if len(store.cleared) != 1 || store.cleared[0] != "record-1" {
		t.Fatalf("cleared records = %#v, want record-1", store.cleared)
	}
}

func TestApplyPendingForceBypassesUnchangedPulledImageSkip(t *testing.T) {
	store := &fakePendingStore{records: []ImageUpdateRecord{{
		ID:         "record-1",
		Repository: "nginx",
		Tag:        "1.27",
		HasUpdate:  true,
		UpdateType: UpdateTypeDigest,
	}}}
	puller := &fakePuller{}
	dockerClient := newDockerClientForHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch dockerAPIPath(r.URL.Path) {
		case "/images/nginx:1.27/json":
			writeDockerJSON(t, w, image.InspectResponse{
				ID:       "sha256:same",
				RepoTags: []string{"nginx:1.27"},
			})
		case "/containers/json":
			writeDockerJSON(t, w, []container.Summary{})
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newService(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		PendingStore:         store,
		ImagePuller:          puller,
		UsedImageCollector: UsedImageCollectorFunc(func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"docker.io/library/nginx:1.27": {}}, nil
		}),
	})

	got, err := service.ApplyPending(context.Background(), Options{Force: true})
	if err != nil {
		t.Fatalf("ApplyPending() error = %v", err)
	}
	if got.Checked != 1 || got.Updated != 1 || got.Skipped != 0 || got.Failed != 0 {
		t.Fatalf("ApplyPending() counts = checked:%d updated:%d skipped:%d failed:%d", got.Checked, got.Updated, got.Skipped, got.Failed)
	}
	if len(got.Items) == 0 || got.Items[0].Status != StatusUpdated {
		t.Fatalf("ApplyPending() first item = %#v, want updated", got.Items)
	}
	if len(store.cleared) != 1 || store.cleared[0] != "record-1" {
		t.Fatalf("cleared records = %#v, want record-1", store.cleared)
	}
}

func TestApplyPendingUsesRecordDigestBeforeResolver(t *testing.T) {
	oldDigest := digest.FromString("old-record-digest").String()
	newDigest := digest.FromString("new-record-digest").String()

	store := &fakePendingStore{records: []ImageUpdateRecord{{
		ID:           "record-1",
		Repository:   "nginx",
		Tag:          "1.27",
		HasUpdate:    true,
		UpdateType:   UpdateTypeDigest,
		LatestDigest: &newDigest,
	}}}
	pulled := false
	puller := &fakePuller{after: func(string) {
		pulled = true
	}}
	resolver := &countingDigestResolver{digest: oldDigest}
	dockerClient := newDockerClientForHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch dockerAPIPath(r.URL.Path) {
		case "/images/nginx:1.27/json", "/images/docker.io/library/nginx:1.27/json":
			if !pulled {
				writeDockerJSON(t, w, image.InspectResponse{
					ID:          "sha256:old-image",
					RepoTags:    []string{"nginx:1.27"},
					RepoDigests: []string{"nginx@" + oldDigest},
				})
				return
			}
			writeDockerJSON(t, w, image.InspectResponse{
				ID:          "sha256:new-image",
				RepoTags:    []string{"nginx:1.27"},
				RepoDigests: []string{"nginx@" + newDigest},
			})
		case "/containers/json":
			writeDockerJSON(t, w, []container.Summary{})
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newService(Config{
		DockerClientProvider:   &fakeDockerClientProvider{client: dockerClient},
		PendingStore:           store,
		ImagePuller:            puller,
		RegistryDigestResolver: resolver,
		UsedImageCollector: UsedImageCollectorFunc(func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"docker.io/library/nginx:1.27": {}}, nil
		}),
	})

	got, err := service.ApplyPending(context.Background(), Options{})
	if err != nil {
		t.Fatalf("ApplyPending() error = %v", err)
	}
	if got.Checked != 1 || got.Updated != 1 || got.Skipped != 0 || got.Failed != 0 {
		t.Fatalf("ApplyPending() counts = checked:%d updated:%d skipped:%d failed:%d", got.Checked, got.Updated, got.Skipped, got.Failed)
	}
	if len(got.Items) != 1 || got.Items[0].Status != StatusUpdated {
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

func TestApplyPendingSkipsWhenKnownDigestMatchesAnyLocalRepoDigest(t *testing.T) {
	firstDigest := digest.FromString("first-local-digest").String()
	secondDigest := digest.FromString("second-local-digest").String()

	store := &fakePendingStore{records: []ImageUpdateRecord{{
		ID:           "record-1",
		Repository:   "nginx",
		Tag:          "1.27",
		HasUpdate:    true,
		UpdateType:   UpdateTypeDigest,
		LatestDigest: &secondDigest,
	}}}
	puller := &fakePuller{}
	resolver := &countingDigestResolver{digest: secondDigest}
	dockerClient := newDockerClientForHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch dockerAPIPath(r.URL.Path) {
		case "/images/nginx:1.27/json", "/images/docker.io/library/nginx:1.27/json":
			writeDockerJSON(t, w, image.InspectResponse{
				ID:          "sha256:same",
				RepoTags:    []string{"nginx:1.27"},
				RepoDigests: []string{"nginx@" + firstDigest, "nginx@" + secondDigest},
			})
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newService(Config{
		DockerClientProvider:   &fakeDockerClientProvider{client: dockerClient},
		PendingStore:           store,
		ImagePuller:            puller,
		RegistryDigestResolver: resolver,
		UsedImageCollector: UsedImageCollectorFunc(func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"docker.io/library/nginx:1.27": {}}, nil
		}),
	})

	got, err := service.ApplyPending(context.Background(), Options{})
	if err != nil {
		t.Fatalf("ApplyPending() error = %v", err)
	}
	if got.Checked != 1 || got.Updated != 0 || got.Skipped != 1 || got.Failed != 0 {
		t.Fatalf("ApplyPending() counts = checked:%d updated:%d skipped:%d failed:%d", got.Checked, got.Updated, got.Skipped, got.Failed)
	}
	if len(got.Items) != 1 || got.Items[0].Status != StatusSkipped {
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

func TestApplyPendingReusesDockerClientWhileBuildingPlans(t *testing.T) {
	store := &fakePendingStore{records: []ImageUpdateRecord{
		{ID: "sha256:old-one", Repository: "nginx", Tag: "1.27", HasUpdate: true, UpdateType: UpdateTypeDigest},
		{ID: "sha256:old-two", Repository: "redis", Tag: "7", HasUpdate: true, UpdateType: UpdateTypeDigest},
	}}
	dockerClient := newDockerClientForHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch dockerAPIPath(r.URL.Path) {
		case "/images/nginx:1.27/json", "/images/redis:7/json":
			http.Error(w, "not found", http.StatusNotFound)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	provider := &fakeDockerClientProvider{client: dockerClient}
	service := newService(Config{
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

	_, _ = service.ApplyPending(context.Background(), Options{})

	if provider.calls != 2 {
		t.Fatalf("DockerClient calls = %d, want 2 total calls independent of record count", provider.calls)
	}
}

func TestApplyPendingKeepsPulledRecordWhenRestartFails(t *testing.T) {
	store := &fakePendingStore{records: []ImageUpdateRecord{{
		ID:         "sha256:old-app",
		Repository: "app",
		Tag:        "1",
		HasUpdate:  true,
		UpdateType: UpdateTypeDigest,
	}}}
	pulled := false
	puller := &fakePuller{after: func(string) {
		pulled = true
	}}
	dockerClient := newDockerClientForHandler(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPath(r.URL.Path)
		switch {
		case r.Method == http.MethodGet && path == "/images/app:1/json":
			if pulled {
				writeDockerJSON(t, w, image.InspectResponse{ID: "sha256:new-app"})
				return
			}
			writeDockerJSON(t, w, image.InspectResponse{ID: "sha256:old-app"})
		case r.Method == http.MethodGet && path == "/containers/json":
			writeDockerJSON(t, w, []container.Summary{
				{ID: "app-id", Names: []string{"/app"}, Image: "app:1", ImageID: "sha256:old-app", State: "running"},
			})
		case r.Method == http.MethodGet && path == "/containers/app-id/json":
			writeDockerJSON(t, w, container.InspectResponse{ID: "app-id", Name: "/app", Image: "sha256:old-app", Config: &container.Config{Image: "app:1"}})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/stop"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/containers/"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && path == "/containers/create":
			writeDockerJSON(t, w, map[string]any{"Id": "new-app-id", "Warnings": []string{}})
		case r.Method == http.MethodPost && path == "/containers/new-app-id/start":
			http.Error(w, "start failed", http.StatusInternalServerError)
		case r.Method == http.MethodPost && path == "/containers/new-app-id/stop":
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newService(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		PendingStore:         store,
		ImagePuller:          puller,
		UsedImageCollector: UsedImageCollectorFunc(func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"docker.io/library/app:1": {}}, nil
		}),
	})

	got, err := service.ApplyPending(context.Background(), Options{})
	if err != nil {
		t.Fatalf("ApplyPending() error = %v", err)
	}
	if len(store.cleared) != 0 {
		t.Fatalf("cleared records = %#v, want none after restart failure; items=%#v", store.cleared, got.Items)
	}
	foundFailedContainer := false
	for _, item := range got.Items {
		if item.ResourceType == ResourceTypeContainer && item.Status == StatusFailed {
			foundFailedContainer = true
		}
	}
	if !foundFailedContainer {
		t.Fatalf("ApplyPending() items = %#v, want failed container result", got.Items)
	}
}
