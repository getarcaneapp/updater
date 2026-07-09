package api

import (
	"context"
	"fmt"
	"slices"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"go.getarcane.app/updater/pkg/deps"
	"go.getarcane.app/updater/pkg/digest"
	"go.getarcane.app/updater/pkg/match"
	"go.getarcane.app/updater/pkg/refs"
	"go.getarcane.app/updater/pkg/utils"
	"go.getarcane.app/updater/types"
)

// RestartContainersUsingOldImages restarts running containers matching old image
// IDs or refs. If dependency sorting detects a cycle, containers are restarted
// in discovery order to preserve historical best-effort behavior.
func (s *Service) RestartContainersUsingOldImages(ctx context.Context, oldIDToNewRef map[string]string, oldRefToNewRef map[string]string) ([]types.ResourceResult, error) {
	dockerClient, err := s.dockerClientInternal(ctx)
	if err != nil {
		return nil, fmt.Errorf("docker connect: %w", err)
	}

	listResult, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{All: false})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	excludedContainers, err := s.excludedContainerSetInternal(ctx)
	if err != nil {
		return nil, err
	}
	dockerProxyName := dockerProxyContainerNameInternal(dockerHostInternal(dockerClient))

	updatedNorm := map[string]string{}
	for oldRef, newRef := range oldRefToNewRef {
		if normalizedRef := refs.NormalizeImageUpdateRef(oldRef); normalizedRef != "" {
			updatedNorm[normalizedRef] = newRef
		}
	}

	plansByName := map[string]*restartPlan{}
	markedForRestart := map[string]bool{}
	containersWithDeps := make([]deps.ContainerWithDeps, 0, len(listResult.Items))
	targetImageIDs := map[string][]string{}

	for _, summary := range listResult.Items {
		if shouldSkipSummaryInternal(summary, excludedContainers, dockerProxyName, s.config.LabelPolicy) {
			continue
		}
		if summary.Labels == nil {
			summary.Labels = map[string]string{}
		}

		name := utils.ContainerSummaryName(summary)
		containersWithDeps = append(containersWithDeps, deps.ContainerWithDeps{Container: summary, Name: name})

		var inspected *container.InspectResponse
		newRef, matchValue := match.ResolveContainerImageMatch(summary, nil, oldIDToNewRef, updatedNorm)
		if newRef == "" && match.ShouldInspectUnmatchedContainerForImageMatch(summary) {
			inspectResult, inspectErr := utils.ContainerInspectWithCompatibility(ctx, dockerClient, summary.ID, client.ContainerInspectOptions{})
			if inspectErr == nil {
				inspected = &inspectResult.Container
				newRef, matchValue = match.ResolveContainerImageMatch(summary, inspected, oldIDToNewRef, updatedNorm)
			}
		}

		if newRef != "" {
			targetIDs, cached := targetImageIDs[newRef]
			if !cached {
				targetIDs, _ = digest.NewChecker(dockerClient, nil).GetImageIDsForRef(ctx, newRef)
				targetImageIDs[newRef] = targetIDs
			}
			currentImageID := match.CurrentContainerImageID(summary, inspected)
			if currentImageID != "" && slices.Contains(targetIDs, currentImageID) {
				newRef = ""
			}
		}

		plan := &restartPlan{cnt: summary, inspect: inspected, newRef: newRef, match: matchValue, explicit: newRef != ""}
		plansByName[name] = plan
		if plan.explicit {
			markedForRestart[name] = true
		}
	}

	if len(markedForRestart) > 0 {
		for i := range containersWithDeps {
			cwd := containersWithDeps[i]
			if plan, ok := plansByName[cwd.Name]; ok && plan.inspect != nil {
				containersWithDeps[i] = deps.ExtractContainerDeps(ctx, dockerClient, cwd.Container, *plan.inspect)
				continue
			}
			inspectResult, inspectErr := utils.ContainerInspectWithCompatibility(ctx, dockerClient, cwd.Container.ID, client.ContainerInspectOptions{})
			if inspectErr != nil {
				continue
			}
			inspect := inspectResult.Container
			containersWithDeps[i] = deps.ExtractContainerDeps(ctx, dockerClient, cwd.Container, inspect)
			if plan, ok := plansByName[containersWithDeps[i].Name]; ok {
				plan.inspect = &inspect
			}
		}
	}

	for {
		added := deps.UpdateImplicitRestart(containersWithDeps, markedForRestart)
		if len(added) == 0 {
			break
		}
		for _, name := range added {
			if plan, ok := plansByName[name]; ok && plan.newRef == "" {
				plan.newRef = fallbackImageForPlanInternal(plan)
				plan.match = "dependency_restart"
				plan.implicit = true
			}
		}
	}

	candidates := make([]deps.ContainerWithDeps, 0, len(containersWithDeps))
	for _, cd := range containersWithDeps {
		if markedForRestart[cd.Name] {
			candidates = append(candidates, cd)
		}
	}
	sorter := deps.NewContainerSorter(candidates)
	sorted, sortErr := sorter.Sort()
	if sortErr != nil {
		s.logger.WarnContext(ctx, "container dependency sort failed; restarting in discovery order", "error", sortErr)
		sorted = candidates
	}
	sorted = orderSelfUpdateLastInternal(sorted, plansByName, s.config.LabelPolicy)

	composeGroups := s.buildComposeGroupsInternal(ctx, sorted, plansByName)
	processedProjects := map[string]bool{}
	projectResults := map[string]error{}
	var standaloneCandidates []deps.ContainerWithDeps
	standaloneResultIndexes := map[string]int{}
	var selfUpdateCandidates []selfUpdatePlan
	selfUpdateResultIndexes := map[string]int{}

	var results []types.ResourceResult
	for _, candidate := range sorted {
		plan := plansByName[candidate.Name]
		if plan == nil {
			continue
		}
		if plan.inspect == nil {
			inspectResult, inspectErr := utils.ContainerInspectWithCompatibility(ctx, dockerClient, plan.cnt.ID, client.ContainerInspectOptions{})
			if inspectErr != nil {
				results = append(results, failedContainerResultInternal(plan.cnt.ID, candidate.Name, fmt.Sprintf("inspect failed: %v", inspectErr)))
				continue
			}
			plan.inspect = new(inspectResult.Container)
		}

		res := standaloneRestartResultInternal(candidate, plan)
		if plan.newRef == "" {
			res.Status = types.StatusSkipped
			res.Error = "no matching updated image"
			results = append(results, res)
			continue
		}

		labels := labelsFromInspectInternal(*plan.inspect)
		func() {
			endContainerStatus := s.BeginContainerUpdate(plan.cnt.ID)
			defer endContainerStatus()
			endProjectStatus := s.BeginProjectUpdate(utils.ComposeProjectLabel(labels))
			defer endProjectStatus()

			projectName := utils.ComposeProjectLabel(labels)
			serviceName := utils.ComposeServiceLabel(labels)
			projectID := composeProjectIDInternal(projectName, composeGroups)
			if projectID != "" && serviceName != "" && !s.isSelfUpdateCandidateInternal(plan.cnt.ID, labels) {
				res = s.applyComposeServiceUpdateInternal(ctx, dockerClient, res, plan, candidate.Name, projectID, projectName, serviceName, composeGroups, processedProjects, projectResults)
				return
			}

			if s.isSelfUpdateCandidateInternal(plan.cnt.ID, labels) {
				// Defer the actual trigger until every other container has
				// been recreated: the self-updater may stop this process, so
				// it must be the last action of the run.
				selfUpdateResultIndexes[candidate.Name] = len(results)
				selfUpdateCandidates = append(selfUpdateCandidates, selfUpdatePlan{
					containerID: plan.cnt.ID,
					name:        candidate.Name,
					newRef:      plan.newRef,
					labels:      labels,
				})
				return
			}

			if err := s.validateStandaloneContainerUpdateInternal(labels); err != nil {
				res.Status = types.StatusFailed
				res.Error = err.Error()
				return
			}
			standaloneResultIndexes[candidate.Name] = len(results)
			standaloneCandidates = append(standaloneCandidates, candidate)
		}()
		results = append(results, res)
	}

	if len(standaloneCandidates) > 0 {
		standaloneResults := s.updateStandaloneRestartCandidatesInternal(ctx, dockerClient, standaloneCandidates, plansByName)
		for _, result := range standaloneResults {
			if index, ok := standaloneResultIndexes[result.ResourceName]; ok {
				results[index] = result
			}
		}
	}

	// Trigger self-updates last; candidates arrive sorted agents-first so the
	// server (which hosts this process) is the final one handled.
	for _, target := range selfUpdateCandidates {
		index, ok := selfUpdateResultIndexes[target.name]
		if !ok {
			continue
		}
		res := results[index]
		endContainerStatus := s.BeginContainerUpdate(target.containerID)
		if err := s.triggerSelfUpdateInternal(ctx, target.containerID, target.name, target.newRef, target.labels); err != nil {
			res.Status = types.StatusFailed
			res.Error = err.Error()
		} else {
			res.Status = types.StatusUpdated
			res.UpdateAvailable = true
			res.UpdateApplied = true
		}
		endContainerStatus()
		results[index] = res
	}
	return results, nil
}

type selfUpdatePlan struct {
	containerID string
	name        string
	newRef      string
	labels      map[string]string
}

func (s *Service) updateStandaloneRestartCandidatesInternal(ctx context.Context, dockerClient *client.Client, candidates []deps.ContainerWithDeps, plansByName map[string]*restartPlan) []types.ResourceResult {
	endStatus := make([]func(), 0, len(candidates))
	for _, candidate := range candidates {
		if plan := plansByName[candidate.Name]; plan != nil {
			endStatus = append(endStatus, s.BeginContainerUpdate(plan.cnt.ID))
		}
	}
	defer func() {
		for i := len(endStatus) - 1; i >= 0; i-- {
			endStatus[i]()
		}
	}()

	resultsByName := map[string]types.ResourceResult{}
	for i := len(candidates) - 1; i >= 0; i-- {
		candidate := candidates[i]
		plan := plansByName[candidate.Name]
		if plan == nil || plan.inspect == nil {
			continue
		}
		result := standaloneRestartResultInternal(candidate, plan)
		if err := s.stopAndRemoveStandaloneContainerInternal(ctx, dockerClient, plan.cnt, *plan.inspect); err != nil {
			result.Status = types.StatusFailed
			result.Error = err.Error()
		}
		resultsByName[candidate.Name] = result
	}

	for _, candidate := range candidates {
		plan := plansByName[candidate.Name]
		if plan == nil || plan.inspect == nil {
			continue
		}
		result, ok := resultsByName[candidate.Name]
		if !ok {
			result = standaloneRestartResultInternal(candidate, plan)
		}
		if result.Status == types.StatusFailed {
			resultsByName[candidate.Name] = result
			continue
		}

		if err := s.createStartOrRollbackInternal(ctx, dockerClient, plan.cnt, *plan.inspect, plan.newRef); err != nil {
			result.Status = types.StatusFailed
			result.Error = err.Error()
			resultsByName[candidate.Name] = result
			continue
		}

		result.UpdateApplied = true
		if plan.implicit {
			result.Status = types.StatusRestarted
		} else {
			result.Status = types.StatusUpdated
			result.UpdateAvailable = true
			_ = s.notifyInternal(ctx, plan.cnt.ID, candidate.Name, plan.newRef, plan.match, refs.NormalizeImageUpdateRef(plan.newRef))
		}
		resultsByName[candidate.Name] = result
	}

	out := make([]types.ResourceResult, 0, len(candidates))
	for _, candidate := range candidates {
		if result, ok := resultsByName[candidate.Name]; ok {
			out = append(out, result)
		}
	}
	return out
}

func (s *Service) applyComposeServiceUpdateInternal(
	ctx context.Context,
	dockerClient *client.Client,
	res types.ResourceResult,
	plan *restartPlan,
	containerName string,
	projectID string,
	projectName string,
	serviceName string,
	composeGroups map[string]composeGroup,
	processedProjects map[string]bool,
	projectResults map[string]error,
) types.ResourceResult {
	if !processedProjects[projectID] {
		group := composeGroups[projectID]
		opCtx, cancel := s.opCtxInternal(ctx)
		projectErr := s.config.ProjectUpdater.UpdateServices(opCtx, projectID, group.services)
		cancel()
		processedProjects[projectID] = true
		if projectErr != nil {
			projectResults[projectID] = projectErr
		}
	}

	projectErr := projectResults[projectID]
	verifyErr := match.VerifyComposeServiceUpdatedImage(ctx, dockerClient, projectName, serviceName, match.CurrentContainerImageID(plan.cnt, plan.inspect))
	if verifyErr != nil {
		res.Status = types.StatusFailed
		if projectErr != nil {
			res.Error = fmt.Sprintf("project-level update failed: %v; service update verification failed: %v", projectErr, verifyErr)
		} else {
			res.Error = fmt.Sprintf("service update verification failed: %v", verifyErr)
		}
		return res
	}

	if projectErr != nil {
		s.logger.WarnContext(ctx, "service updated despite project-level compose error", "projectID", projectID, "projectName", projectName, "serviceName", serviceName, "error", projectErr)
	}
	res.Status = types.StatusUpdated
	res.UpdateAvailable = true
	res.UpdateApplied = true
	_ = s.notifyInternal(ctx, plan.cnt.ID, containerName, plan.newRef, plan.match, refs.NormalizeImageUpdateRef(plan.newRef))
	return res
}

func standaloneRestartResultInternal(candidate deps.ContainerWithDeps, plan *restartPlan) types.ResourceResult {
	return types.ResourceResult{
		ResourceID:   plan.cnt.ID,
		ResourceName: candidate.Name,
		ResourceType: types.ResourceTypeContainer,
		Status:       types.StatusChecked,
		OldImages:    map[string]string{"main": plan.match},
		NewImages:    map[string]string{"main": refs.NormalizeImageUpdateRef(plan.newRef)},
	}
}
