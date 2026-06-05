package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/getarcaneapp/updater/pkg/digest"
	"github.com/getarcaneapp/updater/pkg/refs"
	"github.com/getarcaneapp/updater/pkg/registry"
	"github.com/moby/moby/client"
)

type defaultDockerClientProvider struct {
	options []client.Opt
}

type defaultImagePuller struct {
	dockerClientProvider DockerClientProvider
}

type defaultRegistryDigestResolver struct {
	httpClient *http.Client
}

// NewDefaultService constructs a service with the built-in Docker-backed defaults.
func NewDefaultService() *Service {
	return NewService(Config{})
}

// NewDockerClientProvider returns a Docker client provider that uses the local Docker environment.
func NewDockerClientProvider(options ...client.Opt) DockerClientProvider {
	return defaultDockerClientProvider{options: append([]client.Opt{client.FromEnv}, options...)}
}

// NewImagePuller returns an image puller backed by Docker's ImagePull API.
func NewImagePuller(provider DockerClientProvider) ImagePuller {
	if provider == nil {
		provider = NewDockerClientProvider()
	}
	return defaultImagePuller{dockerClientProvider: provider}
}

// NewRegistryDigestResolver returns a registry HTTP digest resolver.
func NewRegistryDigestResolver() digest.RemoteResolver {
	return newRegistryDigestResolverInternal(nil)
}

func newRegistryDigestResolverInternal(httpClient *http.Client) digest.RemoteResolver {
	return defaultRegistryDigestResolver{httpClient: httpClient}
}

func (p defaultDockerClientProvider) DockerClient(ctx context.Context) (*client.Client, error) {
	dcli, err := client.New(p.options...)
	if err != nil {
		return nil, err
	}
	if _, err := dcli.Ping(ctx, client.PingOptions{}); err != nil {
		return nil, fmt.Errorf("ping docker daemon: %w", err)
	}
	return dcli, nil
}

func (p defaultImagePuller) PullImage(ctx context.Context, imageRef string, progress io.Writer) error {
	dcli, err := p.dockerClientProvider.DockerClient(ctx)
	if err != nil {
		return fmt.Errorf("docker connect: %w", err)
	}
	pullOptions, err := defaultImagePullOptionsInternal(imageRef)
	if err != nil {
		return fmt.Errorf("registry auth: %w", err)
	}
	resp, err := dcli.ImagePull(ctx, imageRef, pullOptions)
	if err != nil {
		return err
	}

	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return err
		}
		if progress == nil {
			continue
		}
		if err := json.NewEncoder(progress).Encode(msg); err != nil {
			_ = resp.Close()
			return fmt.Errorf("write pull progress: %w", err)
		}
	}
	return nil
}

func (r defaultRegistryDigestResolver) GetImageDigest(ctx context.Context, imageRef string) (string, error) {
	parsed, err := refs.NormalizeReference(imageRef)
	if err != nil {
		return "", err
	}
	credential, err := defaultDigestCredentialsInternal(imageRef)
	if err != nil {
		return "", fmt.Errorf("registry auth: %w", err)
	}
	return registry.FetchDigest(ctx, parsed.RegistryHost, parsed.Repository, parsed.Tag, credential, r.httpClient)
}
