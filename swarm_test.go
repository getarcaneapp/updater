package updater

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
)

func swarmTaskLabels() map[string]string {
	return map[string]string{
		"com.docker.swarm.service.id":   "svc-id",
		"com.docker.swarm.service.name": "svc-name",
	}
}

// swarmTaskDockerHandler serves the list/inspect/image endpoints an
// UpdateContainer run against swarm task containers touches. The list endpoint
// answers with whichever task ID appears in the request's id filter.
func swarmTaskDockerHandler(t *testing.T, taskIDs ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := dockerAPIPath(r.URL.Path)
		switch {
		case r.Method == http.MethodGet && path == "/containers/json":
			for _, taskID := range taskIDs {
				if strings.Contains(r.URL.RawQuery, taskID) {
					writeDockerJSON(t, w, []container.Summary{{
						ID: taskID, Names: []string{"/svc-name.1." + taskID}, Image: "app:1", ImageID: "sha256:old-app", State: "running", Labels: swarmTaskLabels(),
					}})
					return
				}
			}
			http.Error(w, "no matching id filter: "+r.URL.RawQuery, http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/json"):
			taskID := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
			writeDockerJSON(t, w, container.InspectResponse{
				ID: taskID, Name: "/svc-name.1." + taskID, Image: "sha256:old-app",
				Config: &container.Config{Image: "app:1", Labels: swarmTaskLabels()},
			})
		case r.Method == http.MethodGet && path == "/images/sha256:old-app/json":
			writeDockerJSON(t, w, image.InspectResponse{ID: "sha256:old-app"})
		case r.Method == http.MethodGet && (path == "/images/app:1/json" || path == "/images/docker.io/library/app:1/json"):
			writeDockerJSON(t, w, image.InspectResponse{ID: "sha256:new-app"})
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}
}

func TestUpdateContainerUpdatesSwarmService(t *testing.T) {
	store := &fakePendingStore{records: []ImageUpdateRecord{{
		ID: "sha256:old-app", Repository: "app", Tag: "1", HasUpdate: true, UpdateType: UpdateTypeDigest,
	}}}
	swarmUpdater := &fakeSwarmServiceUpdater{}
	service := newService(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: newDockerClientForHandler(t, swarmTaskDockerHandler(t, "task-1"))},
		PendingStore:         store,
		ImagePuller:          &fakePuller{},
		SwarmServiceUpdater:  swarmUpdater,
	})

	got, err := service.UpdateContainer(context.Background(), "task-1", Options{})
	if err != nil {
		t.Fatalf("UpdateContainer() error = %v", err)
	}
	if got.Checked != 1 || got.Updated != 1 || got.Skipped != 0 || got.Failed != 0 {
		t.Fatalf("UpdateContainer() counts = checked:%d updated:%d skipped:%d failed:%d", got.Checked, got.Updated, got.Skipped, got.Failed)
	}
	if len(swarmUpdater.updateCalls) != 1 || !strings.HasPrefix(swarmUpdater.updateCalls[0], "svc-id/svc-name:") {
		t.Fatalf("UpdateServiceImage calls = %#v, want one call for svc-id/svc-name", swarmUpdater.updateCalls)
	}
	if len(got.Items) != 1 || got.Items[0].Details["swarmServiceId"] != "svc-id" {
		t.Fatalf("items = %#v, want swarm service details on the container item", got.Items)
	}
	if len(store.cleared) != 1 {
		t.Fatalf("cleared records = %#v, want the applied record cleared", store.cleared)
	}
}

func TestUpdateContainerSkipsSwarmTaskWithoutUpdater(t *testing.T) {
	service := newService(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: newDockerClientForHandler(t, swarmTaskDockerHandler(t, "task-1"))},
		PendingStore:         &fakePendingStore{},
	})

	got, err := service.UpdateContainer(context.Background(), "task-1", Options{})
	if err != nil {
		t.Fatalf("UpdateContainer() error = %v", err)
	}
	if got.Skipped != 1 || got.Updated != 0 || got.Failed != 0 {
		t.Fatalf("UpdateContainer() counts = updated:%d skipped:%d failed:%d, want the swarm task skipped", got.Updated, got.Skipped, got.Failed)
	}
	if len(got.Items) != 1 || !strings.Contains(got.Items[0].Error, "update at the service level") {
		t.Fatalf("items = %#v, want the service-level skip message", got.Items)
	}
}

func TestUpdateContainersDeduplicatesSwarmReplicas(t *testing.T) {
	swarmUpdater := &fakeSwarmServiceUpdater{}
	service := newService(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: newDockerClientForHandler(t, swarmTaskDockerHandler(t, "task-1", "task-2"))},
		PendingStore:         &fakePendingStore{},
		ImagePuller:          &fakePuller{},
		SwarmServiceUpdater:  swarmUpdater,
	})

	got, err := service.UpdateContainers(context.Background(), []string{"task-1", "task-2"}, Options{})
	if err != nil {
		t.Fatalf("UpdateContainers() error = %v", err)
	}
	if len(swarmUpdater.updateCalls) != 1 {
		t.Fatalf("UpdateServiceImage calls = %#v, want one call for both replicas", swarmUpdater.updateCalls)
	}
	if got.Checked != 2 || got.Updated != 1 || got.Skipped != 1 || got.Failed != 0 {
		t.Fatalf("UpdateContainers() counts = checked:%d updated:%d skipped:%d failed:%d", got.Checked, got.Updated, got.Skipped, got.Failed)
	}
}

func TestApplyPendingKeepsRecordWhenSwarmServiceUpdateFails(t *testing.T) {
	store := &fakePendingStore{records: []ImageUpdateRecord{{
		ID: "sha256:old-app", Repository: "app", Tag: "1", HasUpdate: true, UpdateType: UpdateTypeDigest,
	}}}
	pulled := false
	puller := &fakePuller{after: func(string) {
		pulled = true
	}}
	swarmUpdater := &fakeSwarmServiceUpdater{err: context.DeadlineExceeded}
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
			writeDockerJSON(t, w, []container.Summary{{
				ID: "task-1", Names: []string{"/svc-name.1.task-1"}, Image: "app:1", ImageID: "sha256:old-app", State: "running", Labels: swarmTaskLabels(),
			}})
		case r.Method == http.MethodGet && path == "/containers/task-1/json":
			writeDockerJSON(t, w, container.InspectResponse{
				ID: "task-1", Name: "/svc-name.1.task-1", Image: "sha256:old-app",
				Config: &container.Config{Image: "app:1", Labels: swarmTaskLabels()},
			})
		default:
			http.Error(w, "unexpected path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	service := newService(Config{
		DockerClientProvider: &fakeDockerClientProvider{client: dockerClient},
		PendingStore:         store,
		ImagePuller:          puller,
		SwarmServiceUpdater:  swarmUpdater,
		UsedImageCollector: UsedImageCollectorFunc(func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{"docker.io/library/app:1": {}}, nil
		}),
	})

	got, err := service.ApplyPending(context.Background(), Options{})
	if err != nil {
		t.Fatalf("ApplyPending() error = %v", err)
	}
	if len(swarmUpdater.updateCalls) != 1 {
		t.Fatalf("UpdateServiceImage calls = %#v, want one attempt", swarmUpdater.updateCalls)
	}
	if len(store.cleared) != 0 {
		t.Fatalf("cleared records = %#v, want none after swarm service failure; items=%#v", store.cleared, got.Items)
	}
	foundFailedTask := false
	for _, item := range got.Items {
		if item.ResourceType == ResourceTypeContainer && item.Status == StatusFailed && strings.Contains(item.Error, "swarm service update failed") {
			foundFailedTask = true
		}
	}
	if !foundFailedTask {
		t.Fatalf("ApplyPending() items = %#v, want failed swarm task result", got.Items)
	}
}
