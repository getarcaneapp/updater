package updater

import (
	"log/slog"
	"time"
)

// Config configures a Service. Every field is optional: New fills in a
// Docker-backed default for each port that has one, and leaves the rest nil —
// a nil optional port simply disables the behavior it provides.
type Config struct {
	// DockerClientProvider supplies the Docker client every operation uses.
	// Defaults to NewDockerClientProvider(), which reads the standard Docker
	// environment variables.
	DockerClientProvider DockerClientProvider
	// ImagePuller pulls images. Defaults to NewImagePuller(DockerClientProvider).
	ImagePuller ImagePuller
	// PendingStore supplies the pending updates ApplyPending works through.
	// Defaults to NewMemoryPendingStore(), which starts empty.
	PendingStore PendingStore
	// RegistryDigestResolver checks registries for newer digests. Defaults to
	// NewRegistryDigestResolver(), which uses the local Docker credentials.
	RegistryDigestResolver RegistryDigestResolver
	// ProjectUpdater updates Docker Compose services. Defaults to
	// NewDockerComposeProjectUpdater(DockerClientProvider), which shells out to
	// the docker compose CLI.
	ProjectUpdater ProjectUpdater
	// LabelPolicy decides which containers the updater may touch. Each nil func
	// is filled in from DefaultLabelPolicy.
	LabelPolicy LabelPolicy

	// SwarmServiceUpdater is optional. When nil, Swarm task containers are
	// skipped ("update at the service level"); when set, the updater applies
	// image updates by mutating each task's owning Swarm service through it,
	// once per service no matter how many task replicas are involved.
	SwarmServiceUpdater SwarmServiceUpdater

	// RunRecorder is optional; when nil, per-resource results are not recorded.
	RunRecorder RunRecorder
	// Settings is optional; when nil, no containers are excluded.
	Settings SettingsProvider
	// SelfUpdater is optional. When nil, containers that need host self-update
	// handling fail with an error instead of being updated.
	SelfUpdater SelfUpdater
	// Notifier is optional; when nil, successful updates are not announced.
	Notifier Notifier
	// EventRecorder is optional; when nil, lifecycle events are dropped.
	EventRecorder EventRecorder
	// UsedImageCollector is optional. When nil, ApplyPending discovers active
	// images by listing running containers.
	UsedImageCollector UsedImageCollector

	// SelfContainerID is the ID (or ID prefix) of the container the host
	// application itself runs in. When set, that container is always routed
	// through the SelfUpdater even if its labels do not mark it as a
	// self-update target. Optional.
	SelfContainerID string
	// OperationTimeout optionally bounds individual Docker mutation operations
	// and compose project updates. Zero leaves caller context deadlines unchanged.
	OperationTimeout time.Duration
	// Logger receives the updater's diagnostics. Defaults to slog.Default().
	Logger *slog.Logger
}
