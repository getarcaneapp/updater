package updater

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/moby/moby/api/types/container"
)

// Service coordinates Docker image updates, container recreation, and the host
// adapters configured through Config. A Service is safe for concurrent use.
type Service struct {
	config Config
	logger *slog.Logger

	// ownedDockerClient is the default provider this Service constructed, and
	// is therefore the only one Close may shut down. A caller-supplied provider
	// belongs to the caller.
	ownedDockerClient *DockerClient

	updatingContainers atomic.Pointer[[]string]
	updatingProjects   atomic.Pointer[[]string]
}

type updatePlan struct {
	record        ImageUpdateRecord
	oldRef        string
	newRef        string
	oldIDs        []string
	pulled        bool
	restartFailed bool
}

type restartPlan struct {
	cnt      container.Summary
	inspect  *container.InspectResponse
	newRef   string
	match    string
	explicit bool
	implicit bool
}

// New constructs an updater service. The zero Config is valid and yields a
// service backed entirely by the local Docker environment:
//
//	service, err := updater.New(updater.Config{})
//
// New fills in a default for every port that has one, merges each nil
// LabelPolicy func from DefaultLabelPolicy, and then validates the result. It
// returns a ConfigError when the configuration cannot produce a usable service.
func New(config Config) (*Service, error) {
	ownedDockerClient := applyConfigDefaults(&config)
	if err := config.validate(); err != nil {
		return nil, err
	}
	service := newService(config)
	service.ownedDockerClient = ownedDockerClient
	return service, nil
}

// newService assembles a Service from config as given, without filling
// in any port defaults. New applies the defaults first; tests use this directly
// to run against their own fakes with the remaining ports left nil.
func newService(config Config) *Service {
	config.LabelPolicy = mergeLabelPolicyDefaults(config.LabelPolicy)
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	service := &Service{
		config: config,
		logger: logger,
	}
	service.updatingContainers.Store(&[]string{})
	service.updatingProjects.Store(&[]string{})
	return service
}

// Close releases the Docker client New created for this Service. It is a no-op
// when the caller supplied their own DockerClientProvider, because that
// provider's lifecycle belongs to the caller. Close is safe to call more than
// once.
func (s *Service) Close() error {
	if s == nil || s.ownedDockerClient == nil {
		return nil
	}
	return s.ownedDockerClient.Close()
}

// applyConfigDefaults fills in the Docker-backed default for every port
// config leaves nil. It returns the Docker client it created, if any, so the
// Service knows which client it owns.
func applyConfigDefaults(config *Config) *DockerClient {
	var owned *DockerClient
	if config.DockerClientProvider == nil {
		owned = NewDockerClientProvider()
		config.DockerClientProvider = owned
	}
	if config.ImagePuller == nil {
		config.ImagePuller = NewImagePuller(config.DockerClientProvider)
	}
	if config.PendingStore == nil {
		config.PendingStore = NewMemoryPendingStore()
	}
	if config.RegistryDigestResolver == nil {
		config.RegistryDigestResolver = NewRegistryDigestResolver()
	}
	if config.ProjectUpdater == nil {
		config.ProjectUpdater = NewDockerComposeProjectUpdater(config.DockerClientProvider)
	}
	return owned
}

func (s *Service) opCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if s == nil || s.config.OperationTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, s.config.OperationTimeout)
}
