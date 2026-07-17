package api

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/moby/moby/client"
	"go.getarcane.app/updater/pkg/digest"
	"go.getarcane.app/updater/pkg/refs"
	"go.getarcane.app/updater/pkg/registry"
)

type defaultDockerClientProvider struct {
	options []client.Opt
	client  atomic.Pointer[client.Client]
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
	return &defaultDockerClientProvider{options: append([]client.Opt{client.FromEnv}, options...)}
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

func (p *defaultDockerClientProvider) DockerClient(ctx context.Context) (*client.Client, error) {
	if p == nil {
		return nil, errors.New("docker client provider is nil")
	}
	if dockerClient := p.client.Load(); dockerClient != nil {
		if _, err := dockerClient.Ping(ctx, client.PingOptions{}); err != nil {
			if p.client.CompareAndSwap(dockerClient, nil) {
				closeErr := dockerClient.Close()
				if closeErr != nil {
					return nil, fmt.Errorf("ping docker daemon: %w", errors.Join(err, closeErr))
				}
			}
			return nil, fmt.Errorf("ping docker daemon: %w", err)
		}
		return dockerClient, nil
	}

	dockerClient, err := client.New(p.options...)
	if err != nil {
		return nil, err
	}
	if _, err := dockerClient.Ping(ctx, client.PingOptions{}); err != nil {
		if closeErr := dockerClient.Close(); closeErr != nil {
			return nil, fmt.Errorf("ping docker daemon: %w", errors.Join(err, closeErr))
		}
		return nil, fmt.Errorf("ping docker daemon: %w", err)
	}
	if p.client.CompareAndSwap(nil, dockerClient) {
		return dockerClient, nil
	}
	winner := p.client.Load()
	if closeErr := dockerClient.Close(); closeErr != nil {
		return nil, fmt.Errorf("close unused docker client: %w", closeErr)
	}
	if winner == nil {
		return p.DockerClient(ctx)
	}
	return winner, nil
}

func (p *defaultDockerClientProvider) Close() error {
	if p == nil {
		return nil
	}
	dockerClient := p.client.Swap(nil)
	if dockerClient == nil {
		return nil
	}
	return dockerClient.Close()
}

func (p defaultImagePuller) PullImage(ctx context.Context, imageRef string, progress io.Writer) error {
	dockerClient, err := p.dockerClientProvider.DockerClient(ctx)
	if err != nil {
		return fmt.Errorf("docker connect: %w", err)
	}
	pullOptions, err := defaultImagePullOptionsInternal(ctx, imageRef)
	if err != nil {
		return fmt.Errorf("registry auth: %w", err)
	}
	resp, err := dockerClient.ImagePull(ctx, imageRef, pullOptions)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Close() }()

	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return err
		}
		if progress == nil {
			continue
		}
		if err := json.MarshalWrite(progress, msg); err != nil {
			_ = resp.Close()
			return fmt.Errorf("write pull progress: %w", err)
		}
		if _, err := io.WriteString(progress, "\n"); err != nil {
			_ = resp.Close()
			return fmt.Errorf("terminate pull progress: %w", err)
		}
	}
	return nil
}

func (r defaultRegistryDigestResolver) GetImageDigest(ctx context.Context, imageRef string) (string, error) {
	parsed, err := refs.NormalizeReference(imageRef)
	if err != nil {
		return "", err
	}
	credential, err := defaultDigestCredentialsInternal(ctx, imageRef)
	if err != nil {
		return "", fmt.Errorf("registry auth: %w", err)
	}
	return registry.FetchDigest(ctx, parsed.RegistryHost, parsed.Repository, parsed.Tag, credential, r.httpClient)
}
