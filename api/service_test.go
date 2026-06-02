package api

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/getarcaneapp/updater/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
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
