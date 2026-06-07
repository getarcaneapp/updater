package labels

import (
	"strings"

	"github.com/getarcaneapp/updater/types"
)

const (
	// LabelArcane identifies an Arcane server container.
	LabelArcane = "com.getarcaneapp.arcane"
	// LabelArcaneLegacyServer identifies pre-migration Arcane server containers.
	LabelArcaneLegacyServer = "com.getarcaneapp.arcane.server"
	// LabelArcaneAgent identifies an Arcane agent container.
	LabelArcaneAgent = "com.getarcaneapp.arcane.agent"
	// LabelUpdater controls updater participation.
	LabelUpdater = "com.getarcaneapp.arcane.updater"
	// LabelSwarmServiceID identifies a Docker Swarm task.
	LabelSwarmServiceID = "com.docker.swarm.service.id"
	// LabelSwarmServiceName identifies a Docker Swarm task.
	LabelSwarmServiceName = "com.docker.swarm.service.name"
	// LabelDependsOn declares updater restart dependencies.
	LabelDependsOn = "com.getarcaneapp.arcane.depends-on"
	// LabelStopSignal declares a custom stop signal.
	LabelStopSignal = "com.getarcaneapp.arcane.stop-signal"
)

// DefaultLabelPolicy returns Arcane-compatible updater label behavior.
func DefaultLabelPolicy() types.LabelPolicy {
	return types.LabelPolicy{
		IsUpdateDisabledFunc:   IsUpdateDisabled,
		IsSelfUpdateTargetFunc: IsArcaneContainer,
		IsAgentFunc:            IsArcaneAgentContainer,
		IsServerFunc:           IsArcaneServerContainer,
		IsSwarmTaskFunc:        IsSwarmTask,
		StopSignalFunc:         GetStopSignal,
	}
}

// IsArcaneContainer reports whether labels identify an Arcane self-update target.
func IsArcaneContainer(labels map[string]string) bool {
	return hasTruthyLabelInternal(labels, LabelArcane) || hasTruthyLabelInternal(labels, LabelArcaneLegacyServer) || IsArcaneAgentContainer(labels)
}

// IsArcaneServerContainer reports whether labels identify an Arcane server.
func IsArcaneServerContainer(labels map[string]string) bool {
	return (hasTruthyLabelInternal(labels, LabelArcane) || hasTruthyLabelInternal(labels, LabelArcaneLegacyServer)) && !IsArcaneAgentContainer(labels)
}

// ShouldDisableArcaneServerRedeploy reports whether redeploy should be blocked for a container.
func ShouldDisableArcaneServerRedeploy(labels map[string]string, containerID, currentContainerID string, currentErr error) bool {
	if !IsArcaneServerContainer(labels) {
		return false
	}

	if currentErr != nil || strings.TrimSpace(currentContainerID) == "" {
		return true
	}

	return containerIDsMatchInternal(containerID, currentContainerID)
}

// IsArcaneAgentContainer reports whether labels identify an Arcane agent.
func IsArcaneAgentContainer(labels map[string]string) bool {
	return hasTruthyLabelInternal(labels, LabelArcaneAgent)
}

// IsUpdateDisabled reports whether labels opt out of updates.
func IsUpdateDisabled(labels map[string]string) bool {
	if labels == nil {
		return false
	}
	for key, value := range labels {
		if strings.EqualFold(key, LabelUpdater) {
			switch strings.TrimSpace(strings.ToLower(value)) {
			case "false", "0", "no", "off":
				return true
			default:
				return false
			}
		}
	}
	return false
}

// IsSwarmTask reports whether labels identify a Docker Swarm task.
func IsSwarmTask(labels map[string]string) bool {
	return hasNonEmptyLabelInternal(labels, LabelSwarmServiceID) || hasNonEmptyLabelInternal(labels, LabelSwarmServiceName)
}

// GetStopSignal returns a custom stop signal from labels.
func GetStopSignal(labels map[string]string) string {
	if labels == nil {
		return ""
	}
	for key, value := range labels {
		if strings.EqualFold(key, LabelStopSignal) {
			return strings.TrimSpace(strings.ToUpper(value))
		}
	}
	return ""
}

func hasTruthyLabelInternal(labels map[string]string, target string) bool {
	if labels == nil {
		return false
	}
	for key, value := range labels {
		if strings.EqualFold(key, target) && isTruthyLabelValueInternal(value) {
			return true
		}
	}
	return false
}

func hasNonEmptyLabelInternal(labels map[string]string, target string) bool {
	if labels == nil {
		return false
	}
	for key, value := range labels {
		if strings.EqualFold(key, target) && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func isTruthyLabelValueInternal(value string) bool {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

func containerIDsMatchInternal(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}
