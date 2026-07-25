package updater

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"go.getarcane.app/updater/labels"
)

func TestRestartContainersUsingOldImagesRestartsDependenciesInWatchtowerOrder(t *testing.T) {
	var operations []string
	createIDs := map[string]string{"db": "new-db-id", "web": "new-web-id"}

	dockerClient := newDockerClientForHandler(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPath(r.URL.Path)
		switch {
		case r.Method == http.MethodGet && path == "/containers/json":
			writeDockerJSON(t, w, []container.Summary{
				{ID: "db-id", Names: []string{"/db"}, Image: "db:1", ImageID: "sha256:old-db", State: "running"},
				{ID: "web-id", Names: []string{"/web"}, Image: "web:1", ImageID: "sha256:web", Labels: map[string]string{labels.LabelDependsOn: "db"}, State: "running"},
			})
		case r.Method == http.MethodGet && path == "/containers/db-id/json":
			writeDockerJSON(t, w, container.InspectResponse{
				ID:    "db-id",
				Name:  "/db",
				Image: "sha256:old-db",
				Config: &container.Config{
					Image: "db:1",
				},
			})
		case r.Method == http.MethodGet && path == "/containers/web-id/json":
			writeDockerJSON(t, w, container.InspectResponse{
				ID:    "web-id",
				Name:  "/web",
				Image: "sha256:web",
				Config: &container.Config{
					Image:  "web:1",
					Labels: map[string]string{labels.LabelDependsOn: "db"},
				},
			})
		case r.Method == http.MethodGet && path == "/images/db:2/json":
			writeDockerJSON(t, w, image.InspectResponse{ID: "sha256:new-db"})
		case r.Method == http.MethodGet && path == "/images/web:1/json":
			writeDockerJSON(t, w, image.InspectResponse{ID: "sha256:web"})
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
			writeDockerJSON(t, w, map[string]any{"Id": createIDs[name], "Warnings": []string{}})
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/start"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/start")
			operations = append(operations, "start:"+id)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newService(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
	})

	results, err := service.RestartContainersUsingOldImages(context.Background(), map[string]string{"sha256:old-db": "db:2"}, nil)
	if err != nil {
		t.Fatalf("RestartContainersUsingOldImages() error = %v", err)
	}
	assertOperationsInOrder(t, operations, []string{
		"stop:web-id",
		"stop:db-id",
		"start:new-db-id",
		"start:new-web-id",
	})
	statusByName := map[string]ResourceStatus{}
	for _, result := range results {
		statusByName[result.ResourceName] = result.Status
	}
	if statusByName["db"] != StatusUpdated {
		t.Fatalf("db status = %q, want updated; results=%#v", statusByName["db"], results)
	}
	if statusByName["web"] != StatusRestarted {
		t.Fatalf("web status = %q, want restarted; results=%#v", statusByName["web"], results)
	}
}

func TestRestartContainersUsingOldImagesLogsCycleFallback(t *testing.T) {
	logs := &captureLogHandler{}
	dockerClient := newDockerClientForHandler(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPath(r.URL.Path)
		switch {
		case r.Method == http.MethodGet && path == "/containers/json":
			writeDockerJSON(t, w, []container.Summary{
				{ID: "a-id", Names: []string{"/a"}, Image: "a:1", ImageID: "sha256:old-a", Labels: map[string]string{labels.LabelDependsOn: "b"}, State: "running"},
				{ID: "b-id", Names: []string{"/b"}, Image: "b:1", ImageID: "sha256:old-b", Labels: map[string]string{labels.LabelDependsOn: "a"}, State: "running"},
			})
		case r.Method == http.MethodGet && path == "/containers/a-id/json":
			writeDockerJSON(t, w, container.InspectResponse{ID: "a-id", Name: "/a", Image: "sha256:old-a", Config: &container.Config{Image: "a:1", Labels: map[string]string{labels.LabelDependsOn: "b"}}})
		case r.Method == http.MethodGet && path == "/containers/b-id/json":
			writeDockerJSON(t, w, container.InspectResponse{ID: "b-id", Name: "/b", Image: "sha256:old-b", Config: &container.Config{Image: "b:1", Labels: map[string]string{labels.LabelDependsOn: "a"}}})
		case r.Method == http.MethodGet && path == "/images/a:2/json":
			writeDockerJSON(t, w, image.InspectResponse{ID: "sha256:new-a"})
		case r.Method == http.MethodGet && path == "/images/b:2/json":
			writeDockerJSON(t, w, image.InspectResponse{ID: "sha256:new-b"})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/stop"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/containers/"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && path == "/containers/create":
			writeDockerJSON(t, w, map[string]any{"Id": "new-" + r.URL.Query().Get("name"), "Warnings": []string{}})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newService(Config{
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

func TestRestartContainersUsingOldImagesRoutesLegacyArcaneServerThroughSelfUpdater(t *testing.T) {
	projectUpdater := &fakeProjectUpdater{
		projects: map[string]ComposeProject{
			"arcane": {ID: "project-arcane", Name: "arcane"},
		},
	}
	selfUpdater := &fakeSelfUpdater{}
	var operations []string

	dockerClient := newDockerClientForHandler(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPath(r.URL.Path)
		switch {
		case r.Method == http.MethodGet && path == "/containers/json":
			writeDockerJSON(t, w, []container.Summary{
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
			writeDockerJSON(t, w, container.InspectResponse{
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
			writeDockerJSON(t, w, image.InspectResponse{ID: "sha256:new-arcane"})
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
	service := newService(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		ProjectUpdater:       projectUpdater,
		SelfUpdater:          selfUpdater,
		LabelPolicy:          DefaultLabelPolicy(),
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
	if len(results) != 1 || results[0].Status != StatusUpdated {
		t.Fatalf("results = %#v, want one updated result", results)
	}
}

func TestRestartContainersUsingOldImagesSelfContainerIDFiresAfterStandalone(t *testing.T) {
	var operations []string
	selfUpdater := &recordingSelfUpdater{operations: &operations}

	dockerClient := newDockerClientForHandler(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPath(r.URL.Path)
		switch {
		case r.Method == http.MethodGet && path == "/containers/json":
			writeDockerJSON(t, w, []container.Summary{
				{ID: "app-id", Names: []string{"/app"}, Image: "app:1", ImageID: "sha256:old-app", State: "running"},
				{ID: "self-id", Names: []string{"/arcane"}, Image: "ghcr.io/getarcaneapp/arcane:1", ImageID: "sha256:old-arcane", State: "running"},
			})
		case r.Method == http.MethodGet && path == "/containers/app-id/json":
			writeDockerJSON(t, w, container.InspectResponse{
				ID:     "app-id",
				Name:   "/app",
				Image:  "sha256:old-app",
				Config: &container.Config{Image: "app:1"},
			})
		case r.Method == http.MethodGet && path == "/containers/self-id/json":
			writeDockerJSON(t, w, container.InspectResponse{
				ID:     "self-id",
				Name:   "/arcane",
				Image:  "sha256:old-arcane",
				Config: &container.Config{Image: "ghcr.io/getarcaneapp/arcane:1"},
			})
		case r.Method == http.MethodGet && path == "/images/app:2/json":
			writeDockerJSON(t, w, image.InspectResponse{ID: "sha256:new-app"})
		case r.Method == http.MethodGet && path == "/images/ghcr.io/getarcaneapp/arcane:2/json":
			writeDockerJSON(t, w, image.InspectResponse{ID: "sha256:new-arcane"})
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
			writeDockerJSON(t, w, map[string]any{"Id": "new-app-id", "Warnings": []string{}})
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/start"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/start")
			operations = append(operations, "start:"+id)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newService(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		SelfUpdater:          selfUpdater,
		SelfContainerID:      "self-id",
		LabelPolicy:          DefaultLabelPolicy(),
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
	assertOperationsInOrder(t, operations, []string{
		"stop:app-id",
		"start:new-app-id",
		"self-update:self-id",
	})
	if len(selfUpdater.targets) != 1 || selfUpdater.targets[0].NewImageRef != "ghcr.io/getarcaneapp/arcane:2" {
		t.Fatalf("self-update targets = %#v, want one with the new image ref", selfUpdater.targets)
	}
	statusByName := map[string]ResourceStatus{}
	for _, result := range results {
		statusByName[result.ResourceName] = result.Status
	}
	if statusByName["app"] != StatusUpdated || statusByName["arcane"] != StatusUpdated {
		t.Fatalf("results = %#v, want app and arcane updated", results)
	}
}

func TestRestartContainersUsingOldImagesVerifiesComposeServiceAfterProjectError(t *testing.T) {
	projectUpdater := &fakeProjectUpdater{
		projects: map[string]ComposeProject{"app": {ID: "project-app", Name: "app"}},
		err:      errors.New("compose exited after partial update"),
	}
	dockerClient := newDockerClientForHandler(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPath(r.URL.Path)
		switch {
		case r.Method == http.MethodGet && path == "/containers/json":
			if strings.Contains(r.URL.RawQuery, "label") {
				writeDockerJSON(t, w, []container.Summary{
					{ID: "web-new", Names: []string{"/web"}, Image: "app:2", ImageID: "sha256:new-app", State: "running"},
				})
				return
			}
			writeDockerJSON(t, w, []container.Summary{
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
			writeDockerJSON(t, w, container.InspectResponse{
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
			writeDockerJSON(t, w, image.InspectResponse{ID: "sha256:new-app"})
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newService(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		ProjectUpdater:       projectUpdater,
	})

	results, err := service.RestartContainersUsingOldImages(context.Background(), map[string]string{"sha256:old-app": "app:2"}, nil)
	if err != nil {
		t.Fatalf("RestartContainersUsingOldImages() error = %v", err)
	}
	if len(results) != 1 || results[0].Status != StatusUpdated {
		t.Fatalf("results = %#v, want compose service marked updated after verification", results)
	}
	if len(projectUpdater.updateCalls) != 1 {
		t.Fatalf("project updater calls = %#v, want one call", projectUpdater.updateCalls)
	}
}

func TestRestartContainersUsingOldImagesOperationTimeoutCancelsSlowStop(t *testing.T) {
	stopStarted := make(chan struct{})
	stopReleased := make(chan struct{})
	dockerClient := newDockerClientForHandler(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPath(r.URL.Path)
		switch {
		case r.Method == http.MethodGet && path == "/containers/json":
			writeDockerJSON(t, w, []container.Summary{
				{ID: "app-id", Names: []string{"/app"}, Image: "app:1", ImageID: "sha256:old-app", State: "running"},
			})
		case r.Method == http.MethodGet && path == "/containers/app-id/json":
			writeDockerJSON(t, w, container.InspectResponse{ID: "app-id", Name: "/app", Image: "sha256:old-app", Config: &container.Config{Image: "app:1"}})
		case r.Method == http.MethodGet && path == "/images/app:2/json":
			writeDockerJSON(t, w, image.InspectResponse{ID: "sha256:new-app"})
		case r.Method == http.MethodPost && path == "/containers/app-id/stop":
			close(stopStarted)
			<-r.Context().Done()
			close(stopReleased)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newService(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		OperationTimeout:     10 * time.Millisecond,
	})

	results, err := service.RestartContainersUsingOldImages(context.Background(), map[string]string{"sha256:old-app": "app:2"}, nil)
	if err != nil {
		t.Fatalf("RestartContainersUsingOldImages() error = %v", err)
	}
	<-stopStarted
	<-stopReleased
	if len(results) != 1 || results[0].Status != StatusFailed || !strings.Contains(results[0].Error, "context deadline exceeded") {
		t.Fatalf("results = %#v, want failed stop due to timeout", results)
	}
}

func assertOperationsInOrder(t *testing.T, operations, want []string) {
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
