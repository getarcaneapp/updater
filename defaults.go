package updater

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/moby/moby/client"
	"go.getarcane.app/updater/refs"
	"go.getarcane.app/updater/registry"
)

// DockerClient is the built-in DockerClientProvider. It lazily opens one
// Docker client, verifies it with a ping on every hand-out, and reconnects
// after the daemon goes away. Construct one with NewDockerClientProvider.
type DockerClient struct {
	options []client.Opt
	client  atomic.Pointer[client.Client]
}

type defaultImagePuller struct {
	dockerClientProvider DockerClientProvider
}

type defaultRegistryDigestResolver struct {
	httpClient *http.Client
}

// NewDockerClientProvider returns a Docker client provider that reads the
// local Docker environment, plus any options given. The caller owns the result
// and should Close it when done — unless it was left to New to build, in which
// case Service.Close handles it.
func NewDockerClientProvider(options ...client.Opt) *DockerClient {
	return &DockerClient{options: append([]client.Opt{client.FromEnv}, options...)}
}

// NewImagePuller returns an image puller backed by Docker's ImagePull API.
func NewImagePuller(provider DockerClientProvider) ImagePuller {
	if provider == nil {
		provider = NewDockerClientProvider()
	}
	return defaultImagePuller{dockerClientProvider: provider}
}

// NewRegistryDigestResolver returns a registry HTTP digest resolver.
func NewRegistryDigestResolver() RegistryDigestResolver {
	return newRegistryDigestResolver(nil)
}

func newRegistryDigestResolver(httpClient *http.Client) RegistryDigestResolver {
	return defaultRegistryDigestResolver{httpClient: httpClient}
}

// DockerClient returns a live Docker client, opening or reopening one as
// needed. It implements DockerClientProvider.
func (p *DockerClient) DockerClient(ctx context.Context) (*client.Client, error) {
	if p == nil {
		return nil, ErrDockerClientProviderRequired
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

// Close shuts down the Docker client, if one is open. It is safe to call more
// than once, and a later DockerClient call opens a fresh connection.
func (p *DockerClient) Close() error {
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
	pullOptions, err := defaultImagePullOptions(ctx, imageRef)
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

func (r defaultRegistryDigestResolver) ImageDigest(ctx context.Context, imageRef string) (string, error) {
	parsed, err := refs.NormalizeReference(imageRef)
	if err != nil {
		return "", err
	}
	credential, err := defaultDigestCredentials(ctx, imageRef)
	if err != nil {
		return "", fmt.Errorf("registry auth: %w", err)
	}
	return registry.FetchDigest(ctx, parsed.RegistryHost, parsed.Repository, parsed.Tag, credential, r.httpClient)
}
