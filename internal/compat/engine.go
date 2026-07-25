package compat

import (
	"context"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

// engineInfo describes the recreate-time engine compatibility inputs.
type engineInfo struct {
	name          string
	cgroupVersion string
}

// PrepareRecreateHostConfig clones hostConfig and removes fields the running
// engine would reject when the container is recreated. A nil hostConfig yields
// a nil clone. Errors from engine detection are returned alongside the clone,
// which is then unsanitized but still usable.
func PrepareRecreateHostConfig(ctx context.Context, dockerClient client.APIClient, hostConfig *container.HostConfig) (*container.HostConfig, error) {
	if hostConfig == nil {
		return nil, nil
	}

	cloned := new(*hostConfig)
	if dockerClient == nil {
		return cloned, nil
	}

	serverVersion, err := dockerClient.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return cloned, err
	}
	infoResult, err := dockerClient.Info(ctx, client.InfoOptions{})
	if err != nil {
		return cloned, err
	}

	sanitizeRecreateHostConfig(cloned, detectEngineInfo(serverVersion, infoResult.Info))
	return cloned, nil
}

func detectEngineInfo(version client.ServerVersionResult, info system.Info) engineInfo {
	return engineInfo{
		name:          detectEngineName(version, info),
		cgroupVersion: strings.TrimSpace(info.CgroupVersion),
	}
}

func detectEngineName(version client.ServerVersionResult, info system.Info) string {
	candidates := []string{version.Platform.Name}
	for _, component := range version.Components {
		candidates = append(candidates, component.Name)
		for _, value := range component.Details {
			candidates = append(candidates, value)
		}
	}
	candidates = append(candidates, info.ServerVersion, info.OperatingSystem)

	for _, candidate := range candidates {
		if name := normalizeEngineName(candidate); name != "" {
			return name
		}
	}
	return ""
}

func normalizeEngineName(value string) string {
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

// sanitizeRecreateHostConfig drops MemorySwappiness on cgroup v2 Podman, which
// rejects it. It reports whether it changed hostConfig.
func sanitizeRecreateHostConfig(hostConfig *container.HostConfig, engine engineInfo) bool {
	if hostConfig == nil {
		return false
	}
	if !strings.EqualFold(engine.name, "podman") || strings.TrimPrefix(strings.ToLower(engine.cgroupVersion), "v") != "2" {
		return false
	}
	if hostConfig.MemorySwappiness == nil {
		return false
	}
	hostConfig.MemorySwappiness = nil
	return true
}
