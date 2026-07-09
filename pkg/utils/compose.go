package utils

import (
	"strings"

	"github.com/moby/moby/api/types/container"
)

const (
	// ComposeProjectLabelKey is Docker Compose's project label key.
	ComposeProjectLabelKey = "com.docker.compose.project"
	// ComposeServiceLabelKey is Docker Compose's service label key.
	ComposeServiceLabelKey = "com.docker.compose.service"
	// ComposeWorkingDirLabelKey is Docker Compose's project working directory label key.
	ComposeWorkingDirLabelKey = "com.docker.compose.project.working_dir"
	// ComposeConfigFilesLabelKey is Docker Compose's project config files label key.
	ComposeConfigFilesLabelKey = "com.docker.compose.project.config_files"
)

// ComposeProjectLabel returns the trimmed Docker Compose project label.
func ComposeProjectLabel(labels map[string]string) string {
	return strings.TrimSpace(labels[ComposeProjectLabelKey])
}

// ComposeServiceLabel returns the trimmed Docker Compose service label.
func ComposeServiceLabel(labels map[string]string) string {
	return strings.TrimSpace(labels[ComposeServiceLabelKey])
}

// ComposeWorkingDirLabel returns the trimmed Docker Compose project working directory label.
func ComposeWorkingDirLabel(labels map[string]string) string {
	return strings.TrimSpace(labels[ComposeWorkingDirLabelKey])
}

// ComposeConfigFilesLabel returns Docker Compose project config file labels.
func ComposeConfigFilesLabel(labels map[string]string) []string {
	raw := strings.TrimSpace(labels[ComposeConfigFilesLabelKey])
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// ContainerNameFromNames returns Docker's first container name without the leading slash.
func ContainerNameFromNames(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

// ContainerSummaryName returns a displayable container name from a Docker summary.
func ContainerSummaryName(cnt container.Summary) string {
	if name := ContainerNameFromNames(cnt.Names); name != "" {
		return name
	}
	if len(cnt.ID) >= 12 {
		return cnt.ID[:12]
	}
	return cnt.ID
}
