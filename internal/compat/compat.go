// Package compat shims the differences between Docker Engine API versions (and
// Docker-compatible engines) that matter when the updater recreates a container.
package compat

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const networkScopedMacAddressMinAPIVersion = "1.44"
const multiEndpointContainerCreateMinAPIVersion = "1.44"

// ErrNilAPIClient is returned when a Docker API client is required but absent.
var ErrNilAPIClient = errors.New("docker api client is nil")

// DetectAPIVersion returns the configured client API version, falling back to
// the daemon's. It returns "" when neither is available.
func DetectAPIVersion(ctx context.Context, dockerClient client.APIClient) string {
	if dockerClient == nil {
		return ""
	}
	if version := strings.TrimSpace(dockerClient.ClientVersion()); version != "" {
		return version
	}
	serverVersion, err := dockerClient.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(serverVersion.APIVersion)
}

// SanitizeEndpointSettings clones endpoint settings and strips fields the
// target API version rejects at container-create time.
func SanitizeEndpointSettings(endpoints map[string]*network.EndpointSettings, apiVersion string) map[string]*network.EndpointSettings {
	if len(endpoints) == 0 {
		return nil
	}

	keepPerNetworkMAC := apiVersionAtLeast(apiVersion, networkScopedMacAddressMinAPIVersion)
	cloned := make(map[string]*network.EndpointSettings, len(endpoints))
	for networkName, endpoint := range endpoints {
		if endpoint == nil {
			cloned[networkName] = nil
			continue
		}
		endpointCopy := *endpoint
		if !keepPerNetworkMAC {
			endpointCopy.MacAddress = nil
		}
		cloned[networkName] = &endpointCopy
	}
	return cloned
}

// ContainerCreate creates a container, working around daemons that accept only
// one network endpoint at create time by connecting the remaining networks
// afterwards. A container that was created but could not be fully connected is
// still reported in the result so the caller can clean it up.
func ContainerCreate(ctx context.Context, dockerClient client.APIClient, options client.ContainerCreateOptions, apiVersion string) (client.ContainerCreateResult, error) {
	if dockerClient == nil {
		return client.ContainerCreateResult{}, ErrNilAPIClient
	}

	adjustedOptions, extraEndpoints := prepareCreateOptions(options, apiVersion)
	result, err := dockerClient.ContainerCreate(ctx, adjustedOptions)
	if err != nil {
		return client.ContainerCreateResult{}, err
	}
	if len(extraEndpoints) == 0 {
		return result, nil
	}
	if err := connectExtraNetworks(ctx, dockerClient, result.ID, extraEndpoints); err != nil {
		return result, err
	}
	return result, nil
}

// ContainerInspect wraps Docker inspect and validates the client.
func ContainerInspect(ctx context.Context, dockerClient client.APIClient, containerID string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	if dockerClient == nil {
		return client.ContainerInspectResult{}, ErrNilAPIClient
	}
	return dockerClient.ContainerInspect(ctx, containerID, options)
}

// prepareCreateOptions rewrites create options for daemons that cannot attach
// multiple network endpoints in a single create call, returning the endpoints
// that must be connected separately.
func prepareCreateOptions(options client.ContainerCreateOptions, apiVersion string) (client.ContainerCreateOptions, map[string]*network.EndpointSettings) {
	supportsMultiEndpoint := apiVersionAtLeast(apiVersion, multiEndpointContainerCreateMinAPIVersion)
	if supportsMultiEndpoint || options.NetworkingConfig == nil || len(options.NetworkingConfig.EndpointsConfig) <= 1 {
		return options, nil
	}

	primaryNetwork := resolvePrimaryNetwork(options.HostConfig, options.NetworkingConfig.EndpointsConfig)
	if primaryNetwork == "" {
		return options, nil
	}

	adjusted := options
	if options.HostConfig != nil {
		adjusted.HostConfig = new(*options.HostConfig)
	}
	if adjusted.HostConfig == nil {
		adjusted.HostConfig = &container.HostConfig{}
	}
	if strings.TrimSpace(string(adjusted.HostConfig.NetworkMode)) == "" {
		adjusted.HostConfig.NetworkMode = container.NetworkMode(primaryNetwork)
	}
	adjusted.NetworkingConfig = &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			primaryNetwork: copyEndpointSettings(options.NetworkingConfig.EndpointsConfig[primaryNetwork]),
		},
	}

	extraEndpoints := make(map[string]*network.EndpointSettings, len(options.NetworkingConfig.EndpointsConfig)-1)
	for networkName, endpoint := range options.NetworkingConfig.EndpointsConfig {
		if networkName != primaryNetwork {
			extraEndpoints[networkName] = copyEndpointSettings(endpoint)
		}
	}
	if len(extraEndpoints) == 0 {
		return adjusted, nil
	}
	return adjusted, extraEndpoints
}

// connectExtraNetworks attaches endpoints withheld from ContainerCreate.
func connectExtraNetworks(ctx context.Context, dockerClient client.APIClient, containerID string, endpoints map[string]*network.EndpointSettings) error {
	if dockerClient == nil || strings.TrimSpace(containerID) == "" || len(endpoints) == 0 {
		return nil
	}

	networkNames := make([]string, 0, len(endpoints))
	for networkName := range endpoints {
		networkNames = append(networkNames, networkName)
	}
	slices.Sort(networkNames)

	for _, networkName := range networkNames {
		_, err := dockerClient.NetworkConnect(ctx, networkName, client.NetworkConnectOptions{
			Container:      containerID,
			EndpointConfig: copyEndpointSettings(endpoints[networkName]),
		})
		if err != nil {
			return fmt.Errorf("connect network %s: %w", networkName, err)
		}
	}
	return nil
}

// apiVersionAtLeast compares Docker API versions numerically.
func apiVersionAtLeast(current, minimum string) bool {
	cur, ok := parseAPIVersion(current)
	if !ok {
		return false
	}
	minimumVersion, ok := parseAPIVersion(minimum)
	if !ok {
		return false
	}
	for i := range cur {
		if cur[i] > minimumVersion[i] {
			return true
		}
		if cur[i] < minimumVersion[i] {
			return false
		}
	}
	return true
}

func parseAPIVersion(version string) ([3]int, bool) {
	parsed := [3]int{}
	version = strings.TrimSpace(strings.TrimPrefix(version, "v"))
	if version == "" {
		return parsed, false
	}

	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return parsed, false
	}
	for i := 0; i < len(parsed) && i < len(parts); i++ {
		part := strings.TrimSpace(parts[i])
		if part == "" {
			return [3]int{}, false
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return [3]int{}, false
		}
		parsed[i] = n
	}
	return parsed, true
}

func resolvePrimaryNetwork(hostConfig *container.HostConfig, endpoints map[string]*network.EndpointSettings) string {
	if hostConfig != nil {
		mode := strings.TrimSpace(string(hostConfig.NetworkMode))
		if mode != "" && endpoints[mode] != nil {
			return mode
		}
	}
	names := make([]string, 0, len(endpoints))
	for name := range endpoints {
		names = append(names, name)
	}
	slices.Sort(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func copyEndpointSettings(endpoint *network.EndpointSettings) *network.EndpointSettings {
	if endpoint == nil {
		return nil
	}
	copied := *endpoint
	if endpoint.IPAMConfig != nil {
		copied.IPAMConfig = endpoint.IPAMConfig.Copy()
	}
	copied.Links = slices.Clone(endpoint.Links)
	copied.Aliases = slices.Clone(endpoint.Aliases)
	copied.DriverOpts = cloneStringMap(endpoint.DriverOpts)
	return &copied
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	maps.Copy(out, values)
	return out
}
