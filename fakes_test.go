package updater

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
)

// newServiceForTest builds a fully defaulted Service the way callers
// do, failing the test if the config is rejected.
func newServiceForTest(t *testing.T, config Config) *Service {
	t.Helper()
	service, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return service
}

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
	records []ImageUpdateRecord
	cleared []string
}

func (f *fakePendingStore) PendingImageUpdates(ctx context.Context) ([]ImageUpdateRecord, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return f.records, nil
}

func (f *fakePendingStore) ClearImageUpdateRecord(ctx context.Context, record ImageUpdateRecord) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	f.cleared = append(f.cleared, record.ID)
	return nil
}

type fakeRunRecorder struct {
	results []ResourceResult
}

func (f *fakeRunRecorder) RecordUpdateRun(ctx context.Context, result ResourceResult) error {
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

func (fakeDigestResolver) ImageDigest(ctx context.Context, imageRef string) (string, error) {
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

func (r *countingDigestResolver) ImageDigest(ctx context.Context, imageRef string) (string, error) {
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
	projects    map[string]ComposeProject
	updateCalls []string
	err         error
	delay       time.Duration
}

func (f *fakeProjectUpdater) ProjectByComposeName(ctx context.Context, composeName string) (ComposeProject, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return ComposeProject{}, err
		}
	}
	if project, ok := f.projects[composeName]; ok {
		return project, nil
	}
	return ComposeProject{}, errors.New("project not found")
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

type fakeSwarmServiceUpdater struct {
	updateCalls []string
	err         error
}

func (f *fakeSwarmServiceUpdater) UpdateServiceImage(ctx context.Context, serviceID, serviceName, imageRef string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	f.updateCalls = append(f.updateCalls, serviceID+"/"+serviceName+":"+imageRef)
	return f.err
}

type fakeSelfUpdater struct {
	targets []SelfUpdateTarget
}

func (f *fakeSelfUpdater) TriggerSelfUpdate(ctx context.Context, target SelfUpdateTarget) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	f.targets = append(f.targets, target)
	return nil
}

type fakeEventRecorder struct {
	events []Event
}

func (f *fakeEventRecorder) RecordEvent(ctx context.Context, event Event) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	f.events = append(f.events, event)
	return nil
}

type captureLogHandler struct {
	records []slog.Record
}

func (h *captureLogHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *captureLogHandler) Handle(_ context.Context, record slog.Record) error {
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *captureLogHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *captureLogHandler) WithGroup(string) slog.Handler {
	return h
}

type recordingSelfUpdater struct {
	operations *[]string
	targets    []SelfUpdateTarget
}

func (r *recordingSelfUpdater) TriggerSelfUpdate(ctx context.Context, target SelfUpdateTarget) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	*r.operations = append(*r.operations, "self-update:"+target.ContainerID)
	r.targets = append(r.targets, target)
	return nil
}

func newDockerClientForHandler(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	dockerClient, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.41"))
	if err != nil {
		t.Fatalf("new docker client: %v", err)
	}
	return dockerClient
}

func dockerAPIPath(path string) string {
	trimmed := strings.TrimPrefix(path, "/")
	version, rest, ok := strings.Cut(trimmed, "/")
	if ok && strings.HasPrefix(version, "v") {
		return "/" + rest
	}
	return path
}

func writeDockerJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write json response: %v", err)
	}
}

func closeHTTPConnection(t *testing.T, w http.ResponseWriter) {
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
