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

	scan := s.scanRestartCandidatesInternal(ctx, dockerClient, restartScanInput{
		containers:         listResult.Items,
		excludedContainers: excludedContainers,
		dockerProxyName:    dockerProxyContainerNameInternal(dockerHostInternal(dockerClient)),
		oldIDToNewRef:      oldIDToNewRef,
		updatedNorm:        refs.NormalizeImageUpdateRefMapKeys(oldRefToNewRef),
	})
	s.resolveRestartDependenciesInternal(ctx, dockerClient, scan)
	propagateImplicitRestartsInternal(scan)
	sorted := s.sortRestartCandidatesInternal(ctx, scan)
	return s.executeRestartPlansInternal(ctx, dockerClient, sorted, scan.plansByName)
}

type restartScanInput struct {
	containers         []container.Summary
	excludedContainers map[string]bool
	dockerProxyName    string
	oldIDToNewRef      map[string]string
	updatedNorm        map[string]string
}

// restartScan holds the discovery state shared by the restart phases: the
// per-container plans, the restart-marked set, and every eligible container
// with its dependency info.
type restartScan struct {
	plansByName      map[string]*restartPlan
	markedForRestart map[string]bool
	containers       []deps.ContainerWithDeps
}

// scanRestartCandidatesInternal builds a restart plan for every eligible
// running container, marking those whose image matches an applied update.
func (s *Service) scanRestartCandidatesInternal(ctx context.Context, dockerClient *client.Client, in restartScanInput) *restartScan {
	scan := &restartScan{
		plansByName:      map[string]*restartPlan{},
		markedForRestart: map[string]bool{},
		containers:       make([]deps.ContainerWithDeps, 0, len(in.containers)),
	}
	targetImageIDs := digest.NewRefIDCache(digest.NewChecker(dockerClient, nil))

	for _, summary := range in.containers {
		if shouldSkipSummaryInternal(summary, in.excludedContainers, in.dockerProxyName, s.config.LabelPolicy) {
			continue
		}
		if summary.Labels == nil {
			summary.Labels = map[string]string{}
		}

		name := utils.ContainerSummaryName(summary)
		scan.containers = append(scan.containers, deps.ContainerWithDeps{Container: summary, Name: name})

		inspected, newRef, matchValue := s.matchContainerImageInternal(ctx, dockerClient, summary, in.oldIDToNewRef, in.updatedNorm)
		if newRef != "" && containerOnTargetImageInternal(ctx, targetImageIDs, summary, inspected, newRef) {
			newRef = ""
		}

		plan := &restartPlan{cnt: summary, inspect: inspected, newRef: newRef, match: matchValue, explicit: newRef != ""}
		scan.plansByName[name] = plan
		if plan.explicit {
			scan.markedForRestart[name] = true
		}
	}
	return scan
}

// matchContainerImageInternal resolves the updated image ref for a container,
// falling back to an inspect-based match when the summary alone is inconclusive.
func (s *Service) matchContainerImageInternal(ctx context.Context, dockerClient *client.Client, summary container.Summary, oldIDToNewRef, updatedNorm map[string]string) (*container.InspectResponse, string, string) {
	newRef, matchValue := match.ResolveContainerImageMatch(summary, nil, oldIDToNewRef, updatedNorm)
	if newRef != "" || !match.ShouldInspectUnmatchedContainerForImageMatch(summary) {
		return nil, newRef, matchValue
	}
	inspectResult, inspectErr := utils.ContainerInspectWithCompatibility(ctx, dockerClient, summary.ID, client.ContainerInspectOptions{})
	if inspectErr != nil {
		return nil, newRef, matchValue
	}
	inspected := &inspectResult.Container
	newRef, matchValue = match.ResolveContainerImageMatch(summary, inspected, oldIDToNewRef, updatedNorm)
	return inspected, newRef, matchValue
}

// containerOnTargetImageInternal reports whether the container already runs
// one of the image IDs the updated reference resolves to.
func containerOnTargetImageInternal(ctx context.Context, targetImageIDs *digest.RefIDCache, summary container.Summary, inspected *container.InspectResponse, newRef string) bool {
	currentImageID := match.CurrentContainerImageID(summary, inspected)
	return currentImageID != "" && slices.Contains(targetImageIDs.IDsForRef(ctx, newRef), currentImageID)
}

// resolveRestartDependenciesInternal fills in dependency info (and inspect
// data, where missing) for every scanned container once at least one restart
// is planned.
func (s *Service) resolveRestartDependenciesInternal(ctx context.Context, dockerClient *client.Client, scan *restartScan) {
	if len(scan.markedForRestart) == 0 {
		return
	}
	for i := range scan.containers {
		cwd := scan.containers[i]
		if plan, ok := scan.plansByName[cwd.Name]; ok && plan.inspect != nil {
			scan.containers[i] = deps.ExtractContainerDeps(ctx, dockerClient, cwd.Container, *plan.inspect)
			continue
		}
		inspectResult, inspectErr := utils.ContainerInspectWithCompatibility(ctx, dockerClient, cwd.Container.ID, client.ContainerInspectOptions{})
		if inspectErr != nil {
			continue
		}
		inspect := inspectResult.Container
		scan.containers[i] = deps.ExtractContainerDeps(ctx, dockerClient, cwd.Container, inspect)
		if plan, ok := scan.plansByName[scan.containers[i].Name]; ok {
			plan.inspect = &inspect
		}
	}
}

// propagateImplicitRestartsInternal marks dependents of restarting containers
// for restart until the set stops growing.
func propagateImplicitRestartsInternal(scan *restartScan) {
	for {
		added := deps.UpdateImplicitRestart(scan.containers, scan.markedForRestart)
		if len(added) == 0 {
			return
		}
		for _, name := range added {
			if plan, ok := scan.plansByName[name]; ok && plan.newRef == "" {
				plan.newRef = fallbackImageForPlanInternal(plan)
				plan.match = "dependency_restart"
				plan.implicit = true
			}
		}
	}
}

// sortRestartCandidatesInternal orders restart-marked containers by dependency
// (falling back to discovery order on cycles) with self-update targets last.
func (s *Service) sortRestartCandidatesInternal(ctx context.Context, scan *restartScan) []deps.ContainerWithDeps {
	candidates := make([]deps.ContainerWithDeps, 0, len(scan.containers))
	for _, cd := range scan.containers {
		if scan.markedForRestart[cd.Name] {
			candidates = append(candidates, cd)
		}
	}
	sorted, sortErr := deps.NewContainerSorter(candidates).Sort()
	if sortErr != nil {
		s.logger.WarnContext(ctx, "container dependency sort failed; restarting in discovery order", "error", sortErr)
		sorted = candidates
	}
	return orderSelfUpdateLastInternal(sorted, scan.plansByName, s.config.LabelPolicy)
}

// restartRun accumulates the results and deferred work of a restart pass.
type restartRun struct {
	composeGroups        map[string]composeGroup
	processedProjects    map[string]bool
	projectResults       map[string]error
	standaloneCandidates []deps.ContainerWithDeps
	standaloneIndexes    map[string]int
	selfUpdateCandidates []selfUpdatePlan
	selfUpdateIndexes    map[string]int
	results              []types.ResourceResult
}

// executeRestartPlansInternal routes each sorted candidate to the compose,
// self-update, or standalone path and returns the merged results.
func (s *Service) executeRestartPlansInternal(ctx context.Context, dockerClient *client.Client, sorted []deps.ContainerWithDeps, plansByName map[string]*restartPlan) ([]types.ResourceResult, error) {
	run := &restartRun{
		composeGroups:     s.buildComposeGroupsInternal(ctx, sorted, plansByName),
		processedProjects: map[string]bool{},
		projectResults:    map[string]error{},
		standaloneIndexes: map[string]int{},
		selfUpdateIndexes: map[string]int{},
	}

	for _, candidate := range sorted {
		plan := plansByName[candidate.Name]
		if plan == nil {
			continue
		}
		s.dispatchRestartCandidateInternal(ctx, dockerClient, run, candidate, plan)
	}

	if len(run.standaloneCandidates) > 0 {
		standaloneResults := s.updateStandaloneRestartCandidatesInternal(ctx, dockerClient, run.standaloneCandidates, plansByName)
		for _, result := range standaloneResults {
			if index, ok := run.standaloneIndexes[result.ResourceName]; ok {
				run.results[index] = result
			}
		}
	}

	s.triggerDeferredSelfUpdatesInternal(ctx, run)
	return run.results, nil
}

// dispatchRestartCandidateInternal records the candidate's result and either
// applies a compose service update immediately or queues the container for the
// standalone or self-update phase.
func (s *Service) dispatchRestartCandidateInternal(ctx context.Context, dockerClient *client.Client, run *restartRun, candidate deps.ContainerWithDeps, plan *restartPlan) {
	if plan.inspect == nil {
		inspectResult, inspectErr := utils.ContainerInspectWithCompatibility(ctx, dockerClient, plan.cnt.ID, client.ContainerInspectOptions{})
		if inspectErr != nil {
			run.results = append(run.results, failedContainerResultInternal(plan.cnt.ID, candidate.Name, fmt.Sprintf("inspect failed: %v", inspectErr)))
			return
		}
		plan.inspect = new(inspectResult.Container)
	}

	res := standaloneRestartResultInternal(candidate, plan)
	if plan.newRef == "" {
		res.Status = types.StatusSkipped
		res.Error = "no matching updated image"
		run.results = append(run.results, res)
		return
	}

	labels := labelsFromInspectInternal(*plan.inspect)
	endContainerStatus := s.BeginContainerUpdate(plan.cnt.ID)
	defer endContainerStatus()
	endProjectStatus := s.BeginProjectUpdate(utils.ComposeProjectLabel(labels))
	defer endProjectStatus()

	projectName := utils.ComposeProjectLabel(labels)
	serviceName := utils.ComposeServiceLabel(labels)
	projectID := composeProjectIDInternal(projectName, run.composeGroups)
	selfUpdate := s.isSelfUpdateCandidateInternal(plan.cnt.ID, labels)

	switch {
	case projectID != "" && serviceName != "" && !selfUpdate:
		res = s.applyComposeServiceUpdateInternal(ctx, dockerClient, res, plan, candidate.Name, projectID, projectName, serviceName, run)
	case selfUpdate:
		// Defer the actual trigger until every other container has been
		// recreated: the self-updater may stop this process, so it must be
		// the last action of the run.
		run.selfUpdateIndexes[candidate.Name] = len(run.results)
		run.selfUpdateCandidates = append(run.selfUpdateCandidates, selfUpdatePlan{
			containerID: plan.cnt.ID,
			name:        candidate.Name,
			newRef:      plan.newRef,
			labels:      labels,
		})
	default:
		if err := s.validateStandaloneContainerUpdateInternal(labels); err != nil {
			res.Status = types.StatusFailed
			res.Error = err.Error()
			break
		}
		run.standaloneIndexes[candidate.Name] = len(run.results)
		run.standaloneCandidates = append(run.standaloneCandidates, candidate)
	}
	run.results = append(run.results, res)
}

// triggerDeferredSelfUpdatesInternal triggers queued self-updates last;
// candidates arrive sorted agents-first so the server (which hosts this
// process) is the final one handled.
func (s *Service) triggerDeferredSelfUpdatesInternal(ctx context.Context, run *restartRun) {
	for _, target := range run.selfUpdateCandidates {
		index, ok := run.selfUpdateIndexes[target.name]
		if !ok {
			continue
		}
		res := run.results[index]
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
		run.results[index] = res
	}
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
		for _, end := range slices.Backward(endStatus) {
			end()
		}
	}()

	resultsByName := map[string]types.ResourceResult{}
	for _, candidate := range slices.Backward(candidates) {
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
	run *restartRun,
) types.ResourceResult {
	if !run.processedProjects[projectID] {
		group := run.composeGroups[projectID]
		opCtx, cancel := s.opCtxInternal(ctx)
		projectErr := s.config.ProjectUpdater.UpdateServices(opCtx, projectID, group.services)
		cancel()
		run.processedProjects[projectID] = true
		if projectErr != nil {
			run.projectResults[projectID] = projectErr
		}
	}

	projectErr := run.projectResults[projectID]
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
