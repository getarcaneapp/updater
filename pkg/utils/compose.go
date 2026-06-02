// Package docker contains Docker-specific helpers used by the updater.
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
)

// ComposeProjectLabel returns the trimmed Docker Compose project label.
func ComposeProjectLabel(labels map[string]string) string {
	return strings.TrimSpace(labels[ComposeProjectLabelKey])
}

// ComposeServiceLabel returns the trimmed Docker Compose service label.
func ComposeServiceLabel(labels map[string]string) string {
	return strings.TrimSpace(labels[ComposeServiceLabelKey])
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
