package updater

import (
	"errors"
	"testing"
	"time"
)

func TestNewRejectsInvalidConfig(t *testing.T) {
	_, err := New(Config{OperationTimeout: -time.Second})
	if err == nil {
		t.Fatal("New() error = nil, want a ConfigError for a negative OperationTimeout")
	}

	var configErr *ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("New() error = %T, want *ConfigError", err)
	}
	if configErr.Field != "OperationTimeout" {
		t.Fatalf("ConfigError.Field = %q, want OperationTimeout", configErr.Field)
	}
}

// Close must shut down only the Docker client New created; a caller-supplied
// provider belongs to the caller.
func TestCloseOnlyClosesOwnedDockerClient(t *testing.T) {
	owned, err := New(Config{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if owned.ownedDockerClient == nil {
		t.Fatal("New() with a zero Config did not create its own Docker client")
	}
	if err := owned.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := owned.Close(); err != nil {
		t.Fatalf("second Close() error = %v, want nil", err)
	}

	borrowed, err := New(Config{DockerClientProvider: &fakeDockerClientProvider{}})
	if err != nil {
		t.Fatalf("New() with a custom provider error = %v", err)
	}
	if borrowed.ownedDockerClient != nil {
		t.Fatal("New() claimed ownership of a caller-supplied Docker client provider")
	}
	if err := borrowed.Close(); err != nil {
		t.Fatalf("Close() with a custom provider error = %v", err)
	}
}

func TestNewAppliesGenericDockerDefaults(t *testing.T) {
	service := newServiceForTest(t, Config{})
	if _, ok := service.config.DockerClientProvider.(*DockerClient); !ok {
		t.Fatalf("DockerClientProvider = %T, want built-in provider", service.config.DockerClientProvider)
	}
	if _, ok := service.config.ImagePuller.(defaultImagePuller); !ok {
		t.Fatalf("ImagePuller = %T, want built-in puller", service.config.ImagePuller)
	}
	if _, ok := service.config.PendingStore.(*memoryPendingStore); !ok {
		t.Fatalf("PendingStore = %T, want built-in memory store", service.config.PendingStore)
	}
	if _, ok := service.config.RegistryDigestResolver.(defaultRegistryDigestResolver); !ok {
		t.Fatalf("RegistryDigestResolver = %T, want built-in resolver", service.config.RegistryDigestResolver)
	}
	if _, ok := service.config.ProjectUpdater.(dockerComposeProjectUpdater); !ok {
		t.Fatalf("ProjectUpdater = %T, want built-in compose updater", service.config.ProjectUpdater)
	}
}

func TestNewKeepsCustomDockerAdapters(t *testing.T) {
	puller := &fakePuller{}
	store := &fakePendingStore{}
	service := newServiceForTest(t, Config{
		DockerClientProvider:   &fakeDockerClientProvider{err: errors.New("custom")},
		ImagePuller:            puller,
		PendingStore:           store,
		RegistryDigestResolver: fakeDigestResolver{},
	})
	if _, ok := service.config.DockerClientProvider.(*fakeDockerClientProvider); !ok {
		t.Fatalf("DockerClientProvider = %T, want custom provider", service.config.DockerClientProvider)
	}
	if service.config.ImagePuller != puller {
		t.Fatalf("ImagePuller was replaced")
	}
	if service.config.PendingStore != store {
		t.Fatalf("PendingStore was replaced")
	}
	if _, ok := service.config.RegistryDigestResolver.(fakeDigestResolver); !ok {
		t.Fatalf("RegistryDigestResolver = %T, want custom resolver", service.config.RegistryDigestResolver)
	}
}
