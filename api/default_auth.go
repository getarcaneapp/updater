package api

import (
	"context"
	"fmt"
	"os"
	"strings"

	dockerCliConfig "github.com/docker/cli/cli/config"
	dockerCliConfigTypes "github.com/docker/cli/cli/config/types"
	updaterregistry "github.com/getarcaneapp/updater/pkg/registry"
	"github.com/getarcaneapp/updater/pkg/utils"
	dockerauthconfig "github.com/moby/moby/api/pkg/authconfig"
	dockerregistry "github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
)

func defaultImagePullOptionsInternal(imageRef string) (client.ImagePullOptions, error) {
	authConfig, ok, err := defaultRegistryAuthConfigInternal(imageRef)
	if err != nil {
		return client.ImagePullOptions{}, err
	}
	if !ok {
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

func defaultDigestCredentialsInternal(imageRef string) (*updaterregistry.Credentials, error) {
	authConfig, ok, err := defaultRegistryAuthConfigInternal(imageRef)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	username := strings.TrimSpace(authConfig.Username)
	token := strings.TrimSpace(authConfig.Password)
	if token == "" {
		token = strings.TrimSpace(authConfig.IdentityToken)
	}
	if username == "" || token == "" {
		return nil, nil
	}
	return &updaterregistry.Credentials{Username: username, Token: token}, nil
}

func defaultRegistryAuthConfigInternal(imageRef string) (dockerregistry.AuthConfig, bool, error) {
	return defaultDockerConfigRegistryAuthConfigInternal(imageRef)
}

func defaultDockerConfigRegistryAuthConfigInternal(imageRef string) (dockerregistry.AuthConfig, bool, error) {
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
			return dockerregistry.AuthConfig{}, false, nil
		}
		return dockerRegistryAuthConfigFromDockerConfigInternal(authConfig), true, nil
	}
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
