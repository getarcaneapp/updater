package updater

import (
	"context"
	"io"

	"github.com/moby/moby/client"
)

// DockerClientProvider provides Docker clients. The provider owns the returned
// client's lifecycle; Service never closes returned clients. Providers may
// return the same client across calls.
type DockerClientProvider interface {
	DockerClient(ctx context.Context) (*client.Client, error)
}

// ImagePuller pulls images and may write progress to progress. A nil progress
// writer means the caller does not want progress output.
type ImagePuller interface {
	PullImage(ctx context.Context, imageRef string, progress io.Writer) error
}

// PendingStore provides pending update records and clears applied records.
type PendingStore interface {
	PendingImageUpdates(ctx context.Context) ([]ImageUpdateRecord, error)
	ClearImageUpdateRecord(ctx context.Context, record ImageUpdateRecord) error
}

// RegistryDigestResolver resolves an image's current digest from its registry,
// without pulling it.
type RegistryDigestResolver interface {
	ImageDigest(ctx context.Context, imageRef string) (string, error)
}

// RunRecorder records per-resource updater results.
type RunRecorder interface {
	RecordUpdateRun(ctx context.Context, result ResourceResult) error
}

// SettingsProvider provides updater settings owned by the host application.
type SettingsProvider interface {
	ExcludedContainers(ctx context.Context) ([]string, error)
}

// ProjectUpdater updates Docker Compose services owned by a host application.
type ProjectUpdater interface {
	ProjectByComposeName(ctx context.Context, composeName string) (ComposeProject, error)
	UpdateServices(ctx context.Context, projectID string, services []string) error
}

// SwarmServiceUpdater updates Docker Swarm services owned by a host
// application. serviceID and serviceName come from the task container's
// com.docker.swarm.service.* labels; either may be empty, never both.
type SwarmServiceUpdater interface {
	UpdateServiceImage(ctx context.Context, serviceID, serviceName, imageRef string) error
}

// SelfUpdater handles host-specific self-update targets: containers the updater
// must not recreate itself, typically because the host application runs in one.
type SelfUpdater interface {
	TriggerSelfUpdate(ctx context.Context, target SelfUpdateTarget) error
}

// Notifier receives successful update notifications.
type Notifier interface {
	Notify(ctx context.Context, notification Notification) error
}

// EventRecorder receives updater lifecycle events.
type EventRecorder interface {
	RecordEvent(ctx context.Context, event Event) error
}

// UsedImageCollector allows callers to provide their own active-image
// discovery, replacing the default container-list scan.
type UsedImageCollector interface {
	UsedImages(ctx context.Context) (map[string]struct{}, error)
}

// UsedImageCollectorFunc adapts a function to UsedImageCollector.
type UsedImageCollectorFunc func(context.Context) (map[string]struct{}, error)

// UsedImages calls f(ctx).
func (f UsedImageCollectorFunc) UsedImages(ctx context.Context) (map[string]struct{}, error) {
	return f(ctx)
}
