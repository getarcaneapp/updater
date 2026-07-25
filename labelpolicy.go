package updater

import "go.getarcane.app/updater/labels"

// LabelPolicy decides, from a container's labels, whether the updater may touch
// it, whether the host application must update it itself, and how to stop it.
// Every field is optional; a nil func answers false (or "" for StopSignalFunc),
// and New fills the nil ones in from DefaultLabelPolicy.
type LabelPolicy struct {
	IsUpdateDisabledFunc   func(map[string]string) bool
	IsSelfUpdateTargetFunc func(map[string]string) bool
	IsAgentFunc            func(map[string]string) bool
	IsServerFunc           func(map[string]string) bool
	IsSwarmTaskFunc        func(map[string]string) bool
	StopSignalFunc         func(map[string]string) string
}

// DefaultLabelPolicy returns the Arcane-compatible label behavior New uses for
// any field a caller leaves nil.
func DefaultLabelPolicy() LabelPolicy {
	return LabelPolicy{
		IsUpdateDisabledFunc:   labels.IsUpdateDisabled,
		IsSelfUpdateTargetFunc: labels.IsArcaneContainer,
		IsAgentFunc:            labels.IsArcaneAgentContainer,
		IsServerFunc:           labels.IsArcaneServerContainer,
		IsSwarmTaskFunc:        labels.IsSwarmTask,
		StopSignalFunc:         labels.StopSignal,
	}
}

// mergeLabelPolicyDefaults fills every nil func in policy from
// DefaultLabelPolicy. The merge is per field, so a caller that overrides one
// behavior keeps the defaults for the rest.
func mergeLabelPolicyDefaults(policy LabelPolicy) LabelPolicy {
	defaults := DefaultLabelPolicy()
	if policy.IsUpdateDisabledFunc == nil {
		policy.IsUpdateDisabledFunc = defaults.IsUpdateDisabledFunc
	}
	if policy.IsSelfUpdateTargetFunc == nil {
		policy.IsSelfUpdateTargetFunc = defaults.IsSelfUpdateTargetFunc
	}
	if policy.IsAgentFunc == nil {
		policy.IsAgentFunc = defaults.IsAgentFunc
	}
	if policy.IsServerFunc == nil {
		policy.IsServerFunc = defaults.IsServerFunc
	}
	if policy.IsSwarmTaskFunc == nil {
		policy.IsSwarmTaskFunc = defaults.IsSwarmTaskFunc
	}
	if policy.StopSignalFunc == nil {
		policy.StopSignalFunc = defaults.StopSignalFunc
	}
	return policy
}

// IsUpdateDisabled reports whether labels opt the container out of updates.
func (p LabelPolicy) IsUpdateDisabled(containerLabels map[string]string) bool {
	return p.IsUpdateDisabledFunc != nil && p.IsUpdateDisabledFunc(containerLabels)
}

// IsSelfUpdateTarget reports whether labels require host self-update handling.
func (p LabelPolicy) IsSelfUpdateTarget(containerLabels map[string]string) bool {
	return p.IsSelfUpdateTargetFunc != nil && p.IsSelfUpdateTargetFunc(containerLabels)
}

// IsAgent reports whether labels identify an agent self-update target.
func (p LabelPolicy) IsAgent(containerLabels map[string]string) bool {
	return p.IsAgentFunc != nil && p.IsAgentFunc(containerLabels)
}

// IsServer reports whether labels identify a server self-update target.
func (p LabelPolicy) IsServer(containerLabels map[string]string) bool {
	return p.IsServerFunc != nil && p.IsServerFunc(containerLabels)
}

// IsSwarmTask reports whether labels identify a Docker Swarm task container.
func (p LabelPolicy) IsSwarmTask(containerLabels map[string]string) bool {
	return p.IsSwarmTaskFunc != nil && p.IsSwarmTaskFunc(containerLabels)
}

// StopSignal returns the configured stop signal for a container, or "" when the
// policy declares none.
func (p LabelPolicy) StopSignal(containerLabels map[string]string) string {
	if p.StopSignalFunc == nil {
		return ""
	}
	return p.StopSignalFunc(containerLabels)
}
