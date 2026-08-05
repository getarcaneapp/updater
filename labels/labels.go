// Package labels reads the container labels that tell the updater what a
// container is: whether it opts out of updates, whether it is an Arcane server
// or agent that must update itself, whether it belongs to Docker Swarm, and how
// it wants to be stopped.
package labels

import "strings"

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

// IsArcaneContainer reports whether labels identify an Arcane self-update target.
func IsArcaneContainer(labels map[string]string) bool {
	return hasTruthyLabel(labels, LabelArcane) || hasTruthyLabel(labels, LabelArcaneLegacyServer) || IsArcaneAgentContainer(labels)
}

// IsArcaneServerContainer reports whether labels identify an Arcane server.
func IsArcaneServerContainer(labels map[string]string) bool {
	return (hasTruthyLabel(labels, LabelArcane) || hasTruthyLabel(labels, LabelArcaneLegacyServer)) && !IsArcaneAgentContainer(labels)
}

// ShouldDisableArcaneServerRedeploy reports whether redeploy should be blocked for a container.
func ShouldDisableArcaneServerRedeploy(labels map[string]string, containerID, currentContainerID string, currentErr error) bool {
	if !IsArcaneServerContainer(labels) {
		return false
	}

	if currentErr != nil || strings.TrimSpace(currentContainerID) == "" {
		return true
	}

	return containerIDsMatch(containerID, currentContainerID)
}

// IsArcaneAgentContainer reports whether labels identify an Arcane agent.
func IsArcaneAgentContainer(labels map[string]string) bool {
	return hasTruthyLabel(labels, LabelArcaneAgent)
}

// IsUpdateDisabled reports whether labels opt out of updates.
func IsUpdateDisabled(labels map[string]string) bool {
	value, ok := lookupLabel(labels, LabelUpdater)
	if !ok {
		return false
	}
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "false", "0", "no", "off":
		return true
	default:
		return false
	}
}

// IsSwarmTask reports whether labels identify a Docker Swarm task.
func IsSwarmTask(labels map[string]string) bool {
	return hasNonEmptyLabel(labels, LabelSwarmServiceID) || hasNonEmptyLabel(labels, LabelSwarmServiceName)
}

// SwarmServiceID returns the owning Swarm service ID from a task's labels.
func SwarmServiceID(labels map[string]string) string {
	value, _ := lookupLabel(labels, LabelSwarmServiceID)
	return strings.TrimSpace(value)
}

// SwarmServiceName returns the owning Swarm service name from a task's labels.
func SwarmServiceName(labels map[string]string) string {
	value, _ := lookupLabel(labels, LabelSwarmServiceName)
	return strings.TrimSpace(value)
}

// StopSignal returns a custom stop signal from labels.
func StopSignal(labels map[string]string) string {
	value, ok := lookupLabel(labels, LabelStopSignal)
	if !ok {
		return ""
	}
	return strings.TrimSpace(strings.ToUpper(value))
}

func hasTruthyLabel(labels map[string]string, target string) bool {
	value, ok := lookupLabel(labels, target)
	return ok && isTruthyLabelValue(value)
}

func hasNonEmptyLabel(labels map[string]string, target string) bool {
	value, ok := lookupLabel(labels, target)
	return ok && strings.TrimSpace(value) != ""
}

func lookupLabel(labels map[string]string, target string) (string, bool) {
	for key, value := range labels {
		if strings.EqualFold(key, target) {
			return value, true
		}
	}
	return "", false
}

func isTruthyLabelValue(value string) bool {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

func containerIDsMatch(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}
