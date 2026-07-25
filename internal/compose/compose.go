// Package compose reads the Docker Compose labels the updater relies on to
// group containers into projects and services.
package compose

import "strings"

const (
	// ProjectLabelKey is Docker Compose's project label key.
	ProjectLabelKey = "com.docker.compose.project"
	// ServiceLabelKey is Docker Compose's service label key.
	ServiceLabelKey = "com.docker.compose.service"
	// WorkingDirLabelKey is Docker Compose's project working directory label key.
	WorkingDirLabelKey = "com.docker.compose.project.working_dir"
	// ConfigFilesLabelKey is Docker Compose's project config files label key.
	ConfigFilesLabelKey = "com.docker.compose.project.config_files"
)

// ProjectLabel returns the trimmed Docker Compose project label.
func ProjectLabel(labels map[string]string) string {
	return strings.TrimSpace(labels[ProjectLabelKey])
}

// ServiceLabel returns the trimmed Docker Compose service label.
func ServiceLabel(labels map[string]string) string {
	return strings.TrimSpace(labels[ServiceLabelKey])
}

// WorkingDirLabel returns the trimmed Docker Compose project working directory label.
func WorkingDirLabel(labels map[string]string) string {
	return strings.TrimSpace(labels[WorkingDirLabelKey])
}

// ConfigFilesLabel returns Docker Compose project config file labels.
func ConfigFilesLabel(labels map[string]string) []string {
	raw := strings.TrimSpace(labels[ConfigFilesLabelKey])
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
