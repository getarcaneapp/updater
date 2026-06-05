package api

import (
	"context"
	"fmt"
	"slices"

	"github.com/getarcaneapp/updater/pkg/deps"
	"github.com/getarcaneapp/updater/pkg/digest"
	"github.com/getarcaneapp/updater/pkg/match"
	"github.com/getarcaneapp/updater/pkg/refs"
	"github.com/getarcaneapp/updater/pkg/utils"
	"github.com/getarcaneapp/updater/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// RestartContainersUsingOldImages restarts running containers matching old image IDs or refs.
func (s *Service) RestartContainersUsingOldImages(ctx context.Context, oldIDToNewRef map[string]string, oldRefToNewRef map[string]string) ([]types.ResourceResult, error) {
	dcli, err := s.dockerClientInternal(ctx)
	if err != nil {
		return nil, fmt.Errorf("docker connect: %w", err)
	}

	listResult, err := dcli.ContainerList(ctx, client.ContainerListOptions{All: false})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	excludedContainers, err := s.excludedContainerSetInternal(ctx)
	if err != nil {
		return nil, err
	}
	dockerProxyName := dockerProxyContainerNameInternal(dockerHostInternal(dcli))

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
			inspectResult, inspectErr := utils.ContainerInspectWithCompatibility(ctx, dcli, summary.ID, client.ContainerInspectOptions{})
			if inspectErr == nil {
				inspected = &inspectResult.Container
				newRef, matchValue = match.ResolveContainerImageMatch(summary, inspected, oldIDToNewRef, updatedNorm)
			}
		}

		if newRef != "" {
			targetIDs, cached := targetImageIDs[newRef]
			if !cached {
				targetIDs, _ = digest.NewChecker(dcli, nil).GetImageIDsForRef(ctx, newRef)
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
				containersWithDeps[i] = deps.ExtractContainerDeps(ctx, dcli, cwd.Container, *plan.inspect)
				continue
			}
			inspectResult, inspectErr := utils.ContainerInspectWithCompatibility(ctx, dcli, cwd.Container.ID, client.ContainerInspectOptions{})
			if inspectErr != nil {
				continue
			}
			inspect := inspectResult.Container
			containersWithDeps[i] = deps.ExtractContainerDeps(ctx, dcli, cwd.Container, inspect)
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
		sorted = candidates
	}
	sorted = orderSelfUpdateLastInternal(sorted, plansByName, s.config.LabelPolicy)

	composeGroups := s.buildComposeGroupsInternal(ctx, sorted, plansByName)
	processedProjects := map[string]bool{}
	projectResults := map[string]error{}
	standaloneCandidates := []deps.ContainerWithDeps{}
	standaloneResultIndexes := map[string]int{}

	var results []types.ResourceResult
	for _, candidate := range sorted {
		plan := plansByName[candidate.Name]
		if plan == nil {
			continue
		}
		if plan.inspect == nil {
			inspectResult, inspectErr := utils.ContainerInspectWithCompatibility(ctx, dcli, plan.cnt.ID, client.ContainerInspectOptions{})
			if inspectErr != nil {
				results = append(results, failedContainerResultInternal(plan.cnt.ID, candidate.Name, fmt.Sprintf("inspect failed: %v", inspectErr)))
				continue
			}
			inspect := inspectResult.Container
			plan.inspect = &inspect
		}

		res := types.ResourceResult{
			ResourceID:   plan.cnt.ID,
			ResourceName: candidate.Name,
			ResourceType: types.ResourceTypeContainer,
			Status:       types.StatusChecked,
			OldImages:    map[string]string{"main": plan.match},
			NewImages:    map[string]string{"main": refs.NormalizeImageUpdateRef(plan.newRef)},
		}
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
			if projectID != "" && serviceName != "" && !s.config.LabelPolicy.IsSelfUpdateTarget(labels) {
				if projectErr := projectResults[projectID]; projectErr != nil {
					res.Status = types.StatusFailed
					res.Error = fmt.Sprintf("project-level update failed: %v", projectErr)
					return
				}
				if !processedProjects[projectID] {
					group := composeGroups[projectID]
					projectErr := s.config.ProjectUpdater.UpdateServices(ctx, projectID, group.services)
					processedProjects[projectID] = true
					if projectErr != nil {
						projectResults[projectID] = projectErr
						res.Status = types.StatusFailed
						res.Error = fmt.Sprintf("project-level update failed: %v", projectErr)
						return
					}
				}
				if verifyErr := match.VerifyComposeServiceUpdatedImage(ctx, dcli, projectName, serviceName, match.CurrentContainerImageID(plan.cnt, plan.inspect)); verifyErr != nil {
					res.Status = types.StatusFailed
					res.Error = fmt.Sprintf("service update verification failed: %v", verifyErr)
					return
				}
				res.Status = types.StatusUpdated
				res.UpdateAvailable = true
				res.UpdateApplied = true
				_ = s.notifyInternal(ctx, plan.cnt.ID, candidate.Name, plan.newRef, plan.match, refs.NormalizeImageUpdateRef(plan.newRef))
				return
			}

			if s.config.LabelPolicy.IsSelfUpdateTarget(labels) {
				if err := s.triggerSelfUpdateInternal(ctx, plan.cnt.ID, candidate.Name, labels); err != nil {
					res.Status = types.StatusFailed
					res.Error = err.Error()
					return
				}
				res.Status = types.StatusUpdated
				res.UpdateAvailable = true
				res.UpdateApplied = true
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
		standaloneResults := s.updateStandaloneRestartCandidatesInternal(ctx, dcli, standaloneCandidates, plansByName)
		for _, result := range standaloneResults {
			if index, ok := standaloneResultIndexes[result.ResourceName]; ok {
				results[index] = result
			}
		}
	}
	return results, nil
}

func (s *Service) updateStandaloneRestartCandidatesInternal(ctx context.Context, dcli *client.Client, candidates []deps.ContainerWithDeps, plansByName map[string]*restartPlan) []types.ResourceResult {
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
		if err := s.stopAndRemoveStandaloneContainerInternal(ctx, dcli, plan.cnt, *plan.inspect); err != nil {
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

		if _, err := s.createAndStartStandaloneContainerInternal(ctx, dcli, plan.cnt, *plan.inspect, plan.newRef); err != nil {
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
