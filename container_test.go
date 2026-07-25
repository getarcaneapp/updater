package updater

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

func TestClearPendingRecord(t *testing.T) {
	latest := "1.28"
	tests := []struct {
		name        string
		appliedRef  string
		record      ImageUpdateRecord
		wantCleared bool
	}{
		{
			name:        "digest record cleared by short ref",
			appliedRef:  "nginx:1.27",
			record:      ImageUpdateRecord{ID: "digest-rec", Repository: "nginx", Tag: "1.27", HasUpdate: true, UpdateType: UpdateTypeDigest},
			wantCleared: true,
		},
		{
			name:        "digest record cleared across registry alias",
			appliedRef:  "docker.io/library/nginx:1.27",
			record:      ImageUpdateRecord{ID: "digest-rec", Repository: "nginx", Tag: "1.27", HasUpdate: true, UpdateType: UpdateTypeDigest},
			wantCleared: true,
		},
		{
			name:        "tag record kept when only old tag re-pulled",
			appliedRef:  "docker.io/library/nginx:1.27",
			record:      ImageUpdateRecord{ID: "tag-rec", Repository: "nginx", Tag: "1.27", HasUpdate: true, UpdateType: UpdateTypeTag, LatestVersion: &latest},
			wantCleared: false,
		},
		{
			name:        "tag record cleared when new tag applied",
			appliedRef:  "nginx:1.28",
			record:      ImageUpdateRecord{ID: "tag-rec", Repository: "nginx", Tag: "1.27", HasUpdate: true, UpdateType: UpdateTypeTag, LatestVersion: &latest},
			wantCleared: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakePendingStore{records: []ImageUpdateRecord{tt.record}}
			service := newServiceForTest(t, Config{PendingStore: store})

			service.clearPendingRecord(context.Background(), tt.appliedRef)

			if cleared := len(store.cleared) > 0; cleared != tt.wantCleared {
				t.Fatalf("cleared = %v (%v), want %v", cleared, store.cleared, tt.wantCleared)
			}
		})
	}
}

func TestUpdateStandaloneContainerRollsBackAndRemovesDanglingCreateOnStartFailure(t *testing.T) {
	var operations []string
	var createdImages []string
	recorder := &fakeEventRecorder{}
	dockerClient := newDockerClientForHandler(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPath(r.URL.Path)
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
				writeDockerJSON(t, w, map[string]any{"Id": "new-id", "Warnings": []string{}})
				return
			}
			operations = append(operations, "create:rollback")
			writeDockerJSON(t, w, map[string]any{"Id": "rollback-id", "Warnings": []string{}})
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
	service := newService(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		EventRecorder:        recorder,
	})

	err := service.updateStandaloneContainer(context.Background(),
		container.Summary{ID: "old-id", Names: []string{"/app"}},
		container.InspectResponse{ID: "old-id", Name: "/app", Image: "sha256:old-image", Config: &container.Config{Image: "app:1"}},
		"app:2",
	)

	if err == nil {
		t.Fatal("updateStandaloneContainer() error = nil, want start failure with rollback outcome")
	}
	if !strings.Contains(err.Error(), "rollback succeeded") {
		t.Fatalf("error = %q, want rollback succeeded detail", err.Error())
	}
	if len(createdImages) != 2 || createdImages[0] != "app:2" || createdImages[1] != "sha256:old-image" {
		t.Fatalf("created images = %#v, want new ref then old image ID", createdImages)
	}
	assertOperationsInOrder(t, operations, []string{
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

func TestUpdateStandaloneContainerRemovesCreatedContainerWhenExtraNetworkConnectTimesOut(t *testing.T) {
	var operationsMu sync.Mutex
	var operations []string
	removedNew := false
	appendOperation := func(operation string) {
		operationsMu.Lock()
		defer operationsMu.Unlock()
		operations = append(operations, operation)
	}
	newContainerRemoved := func() bool {
		operationsMu.Lock()
		defer operationsMu.Unlock()
		return removedNew
	}
	dockerClient := newDockerClientForHandler(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPath(r.URL.Path)
		switch {
		case r.Method == http.MethodPost && path == "/containers/old-id/stop":
			appendOperation("stop:old-id")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && path == "/containers/old-id":
			appendOperation("remove:old-id")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && path == "/containers/create":
			var body container.Config
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			if body.Image == "app:2" {
				appendOperation("create:new")
				writeDockerJSON(t, w, map[string]any{"Id": "new-id", "Warnings": []string{}})
				return
			}
			if !newContainerRemoved() {
				http.Error(w, "name already in use", http.StatusConflict)
				return
			}
			appendOperation("create:rollback")
			writeDockerJSON(t, w, map[string]any{"Id": "rollback-id", "Warnings": []string{}})
		case r.Method == http.MethodPost && path == "/networks/secondary/connect":
			var body struct {
				Container string
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode network connect body: %v", err)
			}
			appendOperation("connect:secondary:" + body.Container)
			if body.Container != "new-id" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			time.Sleep(50 * time.Millisecond)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && path == "/containers/new-id":
			operationsMu.Lock()
			removedNew = true
			operations = append(operations, "remove:new-id")
			operationsMu.Unlock()
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && path == "/containers/rollback-id/start":
			appendOperation("start:rollback-id")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newService(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		OperationTimeout:     10 * time.Millisecond,
	})

	err := service.updateStandaloneContainer(context.Background(),
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
		t.Fatal("updateStandaloneContainer() error = nil, want network timeout with rollback outcome")
	}
	if !strings.Contains(err.Error(), "rollback succeeded") {
		t.Fatalf("error = %q, want rollback succeeded detail", err.Error())
	}
	operationsMu.Lock()
	gotOperations := slices.Clone(operations)
	operationsMu.Unlock()
	assertOperationsInOrder(t, gotOperations, []string{
		"create:new",
		"connect:secondary:new-id",
		"remove:new-id",
		"create:rollback",
		"start:rollback-id",
	})
}

func TestUpdateStandaloneContainerTreatsAmbiguousStartErrorAsSuccessWhenInspectRunning(t *testing.T) {
	var operations []string
	dockerClient := newDockerClientForHandler(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPath(r.URL.Path)
		switch {
		case r.Method == http.MethodPost && path == "/containers/old-id/stop":
			operations = append(operations, "stop:old-id")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && path == "/containers/old-id":
			operations = append(operations, "remove:old-id")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && path == "/containers/create":
			operations = append(operations, "create:"+r.URL.Query().Get("name"))
			writeDockerJSON(t, w, map[string]any{"Id": "new-id", "Warnings": []string{}})
		case r.Method == http.MethodPost && path == "/containers/new-id/start":
			operations = append(operations, "start:new-id")
			closeHTTPConnection(t, w)
		case r.Method == http.MethodGet && path == "/containers/new-id/json":
			operations = append(operations, "inspect:new-id")
			writeDockerJSON(t, w, container.InspectResponse{
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
	service := newService(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
	})

	err := service.updateStandaloneContainer(context.Background(),
		container.Summary{ID: "old-id", Names: []string{"/app"}},
		container.InspectResponse{ID: "old-id", Name: "/app", Image: "sha256:old-image", Config: &container.Config{Image: "app:1"}},
		"app:2",
	)

	if err != nil {
		t.Fatalf("updateStandaloneContainer() error = %v, want nil after inspect confirms running", err)
	}
	assertOperationsInOrder(t, operations, []string{
		"start:new-id",
		"inspect:new-id",
	})
	for _, operation := range operations {
		if operation == "remove:new-id" {
			t.Fatalf("operations = %#v, did not expect removal after inspect confirms running", operations)
		}
	}
}

func TestUpdateStandaloneContainerRollsBackAmbiguousStartErrorWhenInspectNotRunning(t *testing.T) {
	var operations []string
	dockerClient := newDockerClientForHandler(t, func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPath(r.URL.Path)
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
				writeDockerJSON(t, w, map[string]any{"Id": "new-id", "Warnings": []string{}})
				return
			}
			operations = append(operations, "create:rollback")
			writeDockerJSON(t, w, map[string]any{"Id": "rollback-id", "Warnings": []string{}})
		case r.Method == http.MethodPost && path == "/containers/new-id/start":
			operations = append(operations, "start:new-id")
			closeHTTPConnection(t, w)
		case r.Method == http.MethodGet && path == "/containers/new-id/json":
			operations = append(operations, "inspect:new-id")
			writeDockerJSON(t, w, container.InspectResponse{
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
	service := newService(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
	})

	err := service.updateStandaloneContainer(context.Background(),
		container.Summary{ID: "old-id", Names: []string{"/app"}},
		container.InspectResponse{ID: "old-id", Name: "/app", Image: "sha256:old-image", Config: &container.Config{Image: "app:1"}},
		"app:2",
	)

	if err == nil {
		t.Fatal("updateStandaloneContainer() error = nil, want ambiguous start failure with rollback outcome")
	}
	if !strings.Contains(err.Error(), "rollback succeeded") {
		t.Fatalf("error = %q, want rollback succeeded detail", err.Error())
	}
	assertOperationsInOrder(t, operations, []string{
		"start:new-id",
		"inspect:new-id",
		"remove:new-id",
		"create:rollback",
		"start:rollback-id",
	})
}

func TestServiceFallsBackToStandaloneWhenComposeProjectUnresolved(t *testing.T) {
	projectUpdater := &fakeProjectUpdater{projects: map[string]ComposeProject{}}
	service := newServiceForTest(t, Config{
		DockerClientProvider: &fakeDockerClientProvider{err: errors.New("no docker in test")},
		ProjectUpdater:       projectUpdater,
	})
	err := service.updateComposeOrStandalone(context.Background(), container.Summary{
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
		t.Fatalf("updateComposeOrStandalone() error = %v, want standalone-path docker connect error", err)
	}
	if len(projectUpdater.updateCalls) != 0 {
		t.Fatalf("UpdateServices called %v times for unresolved project, want 0", len(projectUpdater.updateCalls))
	}
}
