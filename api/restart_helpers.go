package api

import (
	"maps"
	"net"
	"net/url"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"go.getarcane.app/updater/pkg/deps"
	"go.getarcane.app/updater/types"
)

func shouldSkipSummaryInternal(summary container.Summary, excludedContainers map[string]bool, dockerProxyName string, policy types.LabelPolicy) bool {
	for _, name := range summary.Names {
		cleanName := strings.TrimPrefix(name, "/")
		if excludedContainers[cleanName] || cleanName == dockerProxyName {
			return true
		}
	}
	if policy.IsUpdateDisabled(summary.Labels) {
		return true
	}
	return policy.IsSwarmTask(summary.Labels) && !policy.IsSelfUpdateTarget(summary.Labels)
}

func fallbackImageForPlanInternal(plan *restartPlan) string {
	if plan == nil {
		return ""
	}
	if plan.inspect != nil && plan.inspect.Config != nil && strings.TrimSpace(plan.inspect.Config.Image) != "" {
		return strings.TrimSpace(plan.inspect.Config.Image)
	}
	return strings.TrimSpace(plan.cnt.Image)
}

func orderSelfUpdateLastInternal(sorted []deps.ContainerWithDeps, plansByName map[string]*restartPlan, policy types.LabelPolicy) []deps.ContainerWithDeps {
	if len(sorted) < 2 {
		return sorted
	}

	normalCandidates := make([]deps.ContainerWithDeps, 0, len(sorted))
	agentCandidates := make([]deps.ContainerWithDeps, 0, len(sorted))
	serverCandidates := make([]deps.ContainerWithDeps, 0, len(sorted))
	for _, candidate := range sorted {
		labels := restartCandidateLabelsInternal(candidate, plansByName[candidate.Name])
		switch {
		case policy.IsAgent(labels):
			agentCandidates = append(agentCandidates, candidate)
		case policy.IsServer(labels):
			serverCandidates = append(serverCandidates, candidate)
		default:
			normalCandidates = append(normalCandidates, candidate)
		}
	}
	ordered := make([]deps.ContainerWithDeps, 0, len(sorted))
	ordered = append(ordered, normalCandidates...)
	ordered = append(ordered, agentCandidates...)
	ordered = append(ordered, serverCandidates...)
	return ordered
}

func restartCandidateLabelsInternal(candidate deps.ContainerWithDeps, plan *restartPlan) map[string]string {
	labels := map[string]string{}
	if len(candidate.Container.Labels) > 0 {
		maps.Copy(labels, candidate.Container.Labels)
	}
	if candidate.Inspect.Config != nil && len(candidate.Inspect.Config.Labels) > 0 {
		maps.Copy(labels, candidate.Inspect.Config.Labels)
	}
	if plan != nil {
		if len(plan.cnt.Labels) > 0 {
			maps.Copy(labels, plan.cnt.Labels)
		}
		if plan.inspect != nil && plan.inspect.Config != nil && len(plan.inspect.Config.Labels) > 0 {
			maps.Copy(labels, plan.inspect.Config.Labels)
		}
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}

func dockerHostInternal(dcli *client.Client) string {
	if dcli == nil {
		return ""
	}
	return dcli.DaemonHost()
}

func dockerProxyContainerNameInternal(dockerHost string) string {
	dockerHost = strings.TrimSpace(dockerHost)
	if dockerHost == "" {
		return ""
	}
	u, err := url.Parse(dockerHost)
	if err != nil || strings.ToLower(u.Scheme) != "tcp" {
		return ""
	}
	host := u.Hostname()
	if host == "" || host == "localhost" || strings.Contains(host, ".") || net.ParseIP(host) != nil {
		return ""
	}
	return host
}
