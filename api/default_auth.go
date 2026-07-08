package api

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	dockerCliConfig "github.com/docker/cli/cli/config"
	dockerCliConfigTypes "github.com/docker/cli/cli/config/types"
	dockerauthconfig "github.com/moby/moby/api/pkg/authconfig"
	dockerregistry "github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
	updaterregistry "go.getarcane.app/updater/pkg/registry"
	"go.getarcane.app/updater/pkg/utils"
)

func defaultImagePullOptionsInternal(ctx context.Context, imageRef string) (client.ImagePullOptions, error) {
	authConfig, ok, err := defaultRegistryAuthConfigInternal(ctx, imageRef)
	if err != nil {
		return client.ImagePullOptions{}, err
	}
	if !ok {
		logAnonymousRegistryFallbackInternal(ctx, imageRef, "no credentials found", nil)
		return client.ImagePullOptions{}, nil
	}

	encoded, err := dockerauthconfig.Encode(authConfig)
	if err != nil {
		return client.ImagePullOptions{}, fmt.Errorf("encode registry auth: %w", err)
	}
	return client.ImagePullOptions{
		RegistryAuth:  encoded,
		PrivilegeFunc: defaultAnonymousAuthHandlerInternal,
	}, nil
}

func defaultDigestCredentialsInternal(ctx context.Context, imageRef string) (*updaterregistry.Credentials, error) {
	authConfig, ok, err := defaultRegistryAuthConfigInternal(ctx, imageRef)
	if err != nil {
		return nil, err
	}
	if !ok {
		logAnonymousRegistryFallbackInternal(ctx, imageRef, "no credentials found", nil)
		return nil, nil
	}

	username := strings.TrimSpace(authConfig.Username)
	token := strings.TrimSpace(authConfig.Password)
	if token == "" {
		token = strings.TrimSpace(authConfig.IdentityToken)
	}
	if username == "" || token == "" {
		logAnonymousRegistryFallbackInternal(ctx, imageRef, "credentials missing username or token", nil)
		return nil, nil
	}
	return &updaterregistry.Credentials{Username: username, Token: token}, nil
}

func defaultRegistryAuthConfigInternal(ctx context.Context, imageRef string) (dockerregistry.AuthConfig, bool, error) {
	return defaultDockerConfigRegistryAuthConfigInternal(ctx, imageRef)
}

func defaultDockerConfigRegistryAuthConfigInternal(ctx context.Context, imageRef string) (dockerregistry.AuthConfig, bool, error) {
	server, err := utils.GetRegistryAddress(imageRef)
	if err != nil {
		return dockerregistry.AuthConfig{}, false, fmt.Errorf("get registry address: %w", err)
	}
	if utils.NormalizeRegistryForComparison(server) == "docker.io" {
		server = "docker.io"
	}

	configDir := strings.TrimSpace(os.Getenv(dockerCliConfig.EnvOverrideConfigDir))
	configFile, err := dockerCliConfig.Load(configDir)
	if err != nil {
		return dockerregistry.AuthConfig{}, false, fmt.Errorf("load Docker config: %w", err)
	}

	authConfig, err := configFile.GetAuthConfig(server)
	if err == nil {
		if defaultDockerConfigAuthEmptyInternal(authConfig) {
			logAnonymousRegistryFallbackInternal(ctx, imageRef, "empty credentials", nil)
			return dockerregistry.AuthConfig{}, false, nil
		}
		return dockerRegistryAuthConfigFromDockerConfigInternal(authConfig), true, nil
	}
	logAnonymousRegistryFallbackInternal(ctx, imageRef, "credential lookup failed", err)
	return dockerregistry.AuthConfig{}, false, nil
}

func dockerRegistryAuthConfigFromDockerConfigInternal(authConfig dockerCliConfigTypes.AuthConfig) dockerregistry.AuthConfig {
	return dockerregistry.AuthConfig{
		Username:      strings.TrimSpace(authConfig.Username),
		Password:      strings.TrimSpace(authConfig.Password),
		Auth:          authConfig.Auth,
		ServerAddress: strings.TrimSpace(authConfig.ServerAddress),
		IdentityToken: strings.TrimSpace(authConfig.IdentityToken),
		RegistryToken: strings.TrimSpace(authConfig.RegistryToken),
	}
}

func defaultDockerConfigAuthEmptyInternal(authConfig dockerCliConfigTypes.AuthConfig) bool {
	return strings.TrimSpace(authConfig.Username) == "" &&
		strings.TrimSpace(authConfig.Password) == "" &&
		strings.TrimSpace(authConfig.Auth) == "" &&
		strings.TrimSpace(authConfig.IdentityToken) == "" &&
		strings.TrimSpace(authConfig.RegistryToken) == ""
}

func defaultAnonymousAuthHandlerInternal(context.Context) (string, error) {
	return "", nil
}

func logAnonymousRegistryFallbackInternal(ctx context.Context, imageRef, reason string, err error) {
	args := []any{"imageRef", imageRef, "reason", reason}
	if err != nil {
		args = append(args, "error", err)
	}
	slog.DebugContext(ctx, "registry credentials unavailable; proceeding anonymously", args...)
}
