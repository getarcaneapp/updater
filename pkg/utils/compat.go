package utils

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

// DetectAPIVersion returns the configured client API version or daemon API version.
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

// SupportsCreatePerNetworkMACAddress reports whether Docker create supports endpoint MAC addresses.
func SupportsCreatePerNetworkMACAddress(apiVersion string) bool {
	return IsAPIVersionAtLeast(apiVersion, networkScopedMacAddressMinAPIVersion)
}

// SupportsCreateMultiEndpointNetworking reports whether create supports multiple endpoints.
func SupportsCreateMultiEndpointNetworking(apiVersion string) bool {
	return IsAPIVersionAtLeast(apiVersion, multiEndpointContainerCreateMinAPIVersion)
}

// IsAPIVersionAtLeast compares Docker API versions numerically.
func IsAPIVersionAtLeast(current, minimum string) bool {
	cur, ok := parseAPIVersionInternal(current)
	if !ok {
		return false
	}
	minimumVersion, ok := parseAPIVersionInternal(minimum)
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

// SanitizeContainerCreateEndpointSettingsForAPI clones endpoint settings and strips unsupported fields.
func SanitizeContainerCreateEndpointSettingsForAPI(endpoints map[string]*network.EndpointSettings, apiVersion string) map[string]*network.EndpointSettings {
	if len(endpoints) == 0 {
		return nil
	}

	keepPerNetworkMAC := SupportsCreatePerNetworkMACAddress(apiVersion)
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

// PrepareContainerCreateOptionsForAPI rewrites create options for older daemon APIs.
func PrepareContainerCreateOptionsForAPI(options client.ContainerCreateOptions, apiVersion string) (client.ContainerCreateOptions, map[string]*network.EndpointSettings) {
	if SupportsCreateMultiEndpointNetworking(apiVersion) || options.NetworkingConfig == nil || len(options.NetworkingConfig.EndpointsConfig) <= 1 {
		return options, nil
	}

	primaryNetwork := resolvePrimaryContainerCreateNetworkInternal(options.HostConfig, options.NetworkingConfig.EndpointsConfig)
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
			primaryNetwork: copyEndpointSettingsInternal(options.NetworkingConfig.EndpointsConfig[primaryNetwork]),
		},
	}

	extraEndpoints := make(map[string]*network.EndpointSettings, len(options.NetworkingConfig.EndpointsConfig)-1)
	for networkName, endpoint := range options.NetworkingConfig.EndpointsConfig {
		if networkName != primaryNetwork {
			extraEndpoints[networkName] = copyEndpointSettingsInternal(endpoint)
		}
	}
	if len(extraEndpoints) == 0 {
		return adjusted, nil
	}
	return adjusted, extraEndpoints
}

// ConnectContainerExtraNetworksForAPI attaches endpoints withheld from ContainerCreate.
func ConnectContainerExtraNetworksForAPI(ctx context.Context, dockerClient client.APIClient, containerID string, endpoints map[string]*network.EndpointSettings) error {
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
			EndpointConfig: copyEndpointSettingsInternal(endpoints[networkName]),
		})
		if err != nil {
			return fmt.Errorf("connect network %s: %w", networkName, err)
		}
	}
	return nil
}

// ContainerCreateWithCompatibility applies Docker API compatibility shims before create.
func ContainerCreateWithCompatibility(ctx context.Context, dockerClient client.APIClient, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	return ContainerCreateWithCompatibilityForAPIVersion(ctx, dockerClient, options, DetectAPIVersion(ctx, dockerClient))
}

// ContainerCreateWithCompatibilityForAPIVersion applies Docker API compatibility shims before create.
func ContainerCreateWithCompatibilityForAPIVersion(ctx context.Context, dockerClient client.APIClient, options client.ContainerCreateOptions, apiVersion string) (client.ContainerCreateResult, error) {
	if dockerClient == nil {
		return client.ContainerCreateResult{}, errors.New("docker api client is nil")
	}

	adjustedOptions, extraEndpoints := PrepareContainerCreateOptionsForAPI(options, apiVersion)
	result, err := dockerClient.ContainerCreate(ctx, adjustedOptions)
	if err != nil {
		return client.ContainerCreateResult{}, err
	}
	if len(extraEndpoints) == 0 {
		return result, nil
	}
	if err := ConnectContainerExtraNetworksForAPI(ctx, dockerClient, result.ID, extraEndpoints); err != nil {
		_, _ = dockerClient.ContainerRemove(ctx, result.ID, client.ContainerRemoveOptions{Force: true})
		return client.ContainerCreateResult{}, err
	}
	return result, nil
}

// ContainerInspectWithCompatibility wraps Docker inspect and validates the client.
func ContainerInspectWithCompatibility(ctx context.Context, apiClient client.APIClient, containerID string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	if apiClient == nil {
		return client.ContainerInspectResult{}, errors.New("docker api client is nil")
	}
	return apiClient.ContainerInspect(ctx, containerID, options)
}

func parseAPIVersionInternal(version string) ([3]int, bool) {
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

func resolvePrimaryContainerCreateNetworkInternal(hostConfig *container.HostConfig, endpoints map[string]*network.EndpointSettings) string {
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

func copyEndpointSettingsInternal(endpoint *network.EndpointSettings) *network.EndpointSettings {
	if endpoint == nil {
		return nil
	}
	copied := *endpoint
	if endpoint.IPAMConfig != nil {
		copied.IPAMConfig = endpoint.IPAMConfig.Copy()
	}
	copied.Links = slices.Clone(endpoint.Links)
	copied.Aliases = slices.Clone(endpoint.Aliases)
	copied.DriverOpts = cloneStringMapInternal(endpoint.DriverOpts)
	return &copied
}

func cloneStringMapInternal(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	maps.Copy(out, values)
	return out
}
