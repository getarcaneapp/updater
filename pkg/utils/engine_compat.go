package utils

import (
	"context"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

// EngineCompatibilityInfo describes recreate-time engine compatibility inputs.
type EngineCompatibilityInfo struct {
	Name          string
	CgroupVersion string
}

// PrepareRecreateHostConfigForEngine clones hostConfig and removes incompatible fields.
func PrepareRecreateHostConfigForEngine(ctx context.Context, dockerClient *client.Client, hostConfig *container.HostConfig) (*container.HostConfig, bool, EngineCompatibilityInfo, error) {
	if hostConfig == nil {
		return nil, false, EngineCompatibilityInfo{}, nil
	}

	cloned := new(*hostConfig)
	if dockerClient == nil {
		return cloned, false, EngineCompatibilityInfo{}, nil
	}

	serverVersion, err := dockerClient.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return cloned, false, EngineCompatibilityInfo{}, err
	}
	infoResult, err := dockerClient.Info(ctx, client.InfoOptions{})
	if err != nil {
		return cloned, false, EngineCompatibilityInfo{}, err
	}

	engineInfo := detectEngineCompatibilityInfoInternal(serverVersion, infoResult.Info)
	sanitized := sanitizeRecreateHostConfigInternal(cloned, engineInfo)
	return cloned, sanitized, engineInfo, nil
}

func detectEngineCompatibilityInfoInternal(version client.ServerVersionResult, info system.Info) EngineCompatibilityInfo {
	return EngineCompatibilityInfo{
		Name:          detectEngineNameInternal(version, info),
		CgroupVersion: strings.TrimSpace(info.CgroupVersion),
	}
}

func detectEngineNameInternal(version client.ServerVersionResult, info system.Info) string {
	candidates := []string{version.Platform.Name}
	for _, component := range version.Components {
		candidates = append(candidates, component.Name)
		for _, value := range component.Details {
			candidates = append(candidates, value)
		}
	}
	candidates = append(candidates, info.ServerVersion, info.OperatingSystem)

	for _, candidate := range candidates {
		if name := normalizeEngineNameInternal(candidate); name != "" {
			return name
		}
	}
	return ""
}

func normalizeEngineNameInternal(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(normalized, "podman"):
		return "podman"
	case strings.Contains(normalized, "docker"):
		return "docker"
	default:
		return ""
	}
}

func sanitizeRecreateHostConfigInternal(hostConfig *container.HostConfig, engineInfo EngineCompatibilityInfo) bool {
	if hostConfig == nil {
		return false
	}
	if !strings.EqualFold(engineInfo.Name, "podman") || strings.TrimPrefix(strings.ToLower(engineInfo.CgroupVersion), "v") != "2" {
		return false
	}
	if hostConfig.MemorySwappiness == nil {
		return false
	}
	hostConfig.MemorySwappiness = nil
	return true
}
