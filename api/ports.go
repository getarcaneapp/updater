package api

import (
	"context"
	"io"
	"log/slog"

	"github.com/moby/moby/client"
	"go.getarcane.app/updater/pkg/digest"
	"go.getarcane.app/updater/types"
)

// DockerClientProvider provides Docker clients.
type DockerClientProvider interface {
	DockerClient(ctx context.Context) (*client.Client, error)
}

// ImagePuller pulls images and may write progress to progress.
type ImagePuller interface {
	PullImage(ctx context.Context, imageRef string, progress io.Writer) error
}

// PendingStore provides pending update records and clears applied records.
type PendingStore interface {
	PendingImageUpdates(ctx context.Context) ([]types.ImageUpdateRecord, error)
	ClearImageUpdateRecord(ctx context.Context, record types.ImageUpdateRecord) error
}

// RunRecorder records per-resource updater results.
type RunRecorder interface {
	RecordUpdateRun(ctx context.Context, result types.ResourceResult) error
}

// SettingsProvider provides updater settings owned by the host application.
type SettingsProvider interface {
	ExcludedContainers(ctx context.Context) ([]string, error)
}

// ProjectUpdater updates Docker Compose services owned by a host application.
type ProjectUpdater interface {
	ProjectByComposeName(ctx context.Context, composeName string) (types.ComposeProject, error)
	UpdateServices(ctx context.Context, projectID string, services []string) error
}

// SelfUpdater handles host-specific self-update targets.
type SelfUpdater interface {
	TriggerSelfUpdate(ctx context.Context, target types.SelfUpdateTarget) error
}

// Notifier receives successful update notifications.
type Notifier interface {
	Notify(ctx context.Context, notification types.Notification) error
}

// EventRecorder receives updater lifecycle events.
type EventRecorder interface {
	RecordEvent(ctx context.Context, event types.Event) error
}

// UsedImageCollector allows callers to provide their own active-image discovery.
type UsedImageCollector interface {
	UsedImages(ctx context.Context) (map[string]struct{}, error)
}

// UsedImageCollectorFunc adapts a function to UsedImageCollector.
type UsedImageCollectorFunc func(context.Context) (map[string]struct{}, error)

// UsedImages calls f(ctx).
func (f UsedImageCollectorFunc) UsedImages(ctx context.Context) (map[string]struct{}, error) {
	return f(ctx)
}

// Config configures Service.
type Config struct {
	DockerClientProvider           DockerClientProvider
	ImagePuller                    ImagePuller
	PendingStore                   PendingStore
	RunRecorder                    RunRecorder
	Settings                       SettingsProvider
	RegistryDigestResolver         digest.RemoteResolver
	ProjectUpdater                 ProjectUpdater
	SelfUpdater                    SelfUpdater
	Notifier                       Notifier
	EventRecorder                  EventRecorder
	UsedImageCollector             UsedImageCollector
	LabelPolicy                    types.LabelPolicy
	AllowComposeStandaloneFallback bool
	Logger                         *slog.Logger
}
