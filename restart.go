package updater

import (
	"context"
	"fmt"
	"slices"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"go.getarcane.app/updater/internal/compat"
	"go.getarcane.app/updater/internal/compose"
	"go.getarcane.app/updater/internal/deps"
	"go.getarcane.app/updater/internal/digestcheck"
	"go.getarcane.app/updater/internal/match"
	"go.getarcane.app/updater/refs"
)

// RestartContainersUsingOldImages restarts running containers matching old image
// IDs or refs. If dependency sorting detects a cycle, containers are restarted
// in discovery order to preserve historical best-effort behavior.
func (s *Service) RestartContainersUsingOldImages(ctx context.Context, oldIDToNewRef map[string]string, oldRefToNewRef map[string]string) ([]ResourceResult, error) {
	dockerClient, err := s.dockerClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("docker connect: %w", err)
	}

	listResult, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{All: false})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	excludedContainers, err := s.excludedContainerSet(ctx)
	if err != nil {
		return nil, err
	}

	scan := s.scanRestartCandidates(ctx, dockerClient, restartScanInput{
		containers:         listResult.Items,
		excludedContainers: excludedContainers,
		dockerProxyName:    dockerProxyContainerName(dockerHost(dockerClient)),
		oldIDToNewRef:      oldIDToNewRef,
		updatedNorm:        refs.NormalizeImageUpdateRefMapKeys(oldRefToNewRef),
	})
	s.resolveRestartDependencies(ctx, dockerClient, scan)
	propagateImplicitRestarts(scan)
	sorted := s.sortRestartCandidates(ctx, scan)
	return s.executeRestartPlans(ctx, dockerClient, sorted, scan.plansByName)
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

// scanRestartCandidates builds a restart plan for every eligible
// running container, marking those whose image matches an applied update.
func (s *Service) scanRestartCandidates(ctx context.Context, dockerClient *client.Client, in restartScanInput) *restartScan {
	scan := &restartScan{
		plansByName:      map[string]*restartPlan{},
		markedForRestart: map[string]bool{},
		containers:       make([]deps.ContainerWithDeps, 0, len(in.containers)),
	}
	targetImageIDs := digestcheck.NewRefIDCache(digestcheck.NewChecker(dockerClient, nil))

	for _, summary := range in.containers {
		if shouldSkipSummary(summary, in.excludedContainers, in.dockerProxyName, s.config.LabelPolicy) {
			continue
		}
		if summary.Labels == nil {
			summary.Labels = map[string]string{}
		}

		name := containerSummaryName(summary)
		scan.containers = append(scan.containers, deps.ContainerWithDeps{Container: summary, Name: name})

		inspected, newRef, matchValue := s.matchContainerImage(ctx, dockerClient, summary, in.oldIDToNewRef, in.updatedNorm)
		if newRef != "" && containerOnTargetImage(ctx, targetImageIDs, summary, inspected, newRef) {
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

// matchContainerImage resolves the updated image ref for a container,
// falling back to an inspect-based match when the summary alone is inconclusive.
func (s *Service) matchContainerImage(ctx context.Context, dockerClient *client.Client, summary container.Summary, oldIDToNewRef, updatedNorm map[string]string) (*container.InspectResponse, string, string) {
	newRef, matchValue := match.ResolveContainerImageMatch(summary, nil, oldIDToNewRef, updatedNorm)
	if newRef != "" || !match.ShouldInspectUnmatchedContainerForImageMatch(summary) {
		return nil, newRef, matchValue
	}
	inspectResult, inspectErr := compat.ContainerInspect(ctx, dockerClient, summary.ID, client.ContainerInspectOptions{})
	if inspectErr != nil {
		return nil, newRef, matchValue
	}
	inspected := &inspectResult.Container
	newRef, matchValue = match.ResolveContainerImageMatch(summary, inspected, oldIDToNewRef, updatedNorm)
	return inspected, newRef, matchValue
}

// containerOnTargetImage reports whether the container already runs
// one of the image IDs the updated reference resolves to.
func containerOnTargetImage(ctx context.Context, targetImageIDs *digestcheck.RefIDCache, summary container.Summary, inspected *container.InspectResponse, newRef string) bool {
	currentImageID := match.CurrentContainerImageID(summary, inspected)
	return currentImageID != "" && slices.Contains(targetImageIDs.IDsForRef(ctx, newRef), currentImageID)
}

// resolveRestartDependencies fills in dependency info (and inspect
// data, where missing) for every scanned container once at least one restart
// is planned.
func (s *Service) resolveRestartDependencies(ctx context.Context, dockerClient *client.Client, scan *restartScan) {
	if len(scan.markedForRestart) == 0 {
		return
	}
	for i := range scan.containers {
		cwd := scan.containers[i]
		if plan, ok := scan.plansByName[cwd.Name]; ok && plan.inspect != nil {
			scan.containers[i] = deps.ExtractContainerDeps(ctx, cwd.Name, cwd.Container, *plan.inspect)
			continue
		}
		inspectResult, inspectErr := compat.ContainerInspect(ctx, dockerClient, cwd.Container.ID, client.ContainerInspectOptions{})
		if inspectErr != nil {
			continue
		}
		inspect := inspectResult.Container
		scan.containers[i] = deps.ExtractContainerDeps(ctx, cwd.Name, cwd.Container, inspect)
		if plan, ok := scan.plansByName[scan.containers[i].Name]; ok {
			plan.inspect = &inspect
		}
	}
}

// propagateImplicitRestarts marks dependents of restarting containers
// for restart until the set stops growing.
func propagateImplicitRestarts(scan *restartScan) {
	for {
		added := deps.UpdateImplicitRestart(scan.containers, scan.markedForRestart)
		if len(added) == 0 {
			return
		}
		for _, name := range added {
			if plan, ok := scan.plansByName[name]; ok && plan.newRef == "" {
				plan.newRef = fallbackImageForPlan(plan)
				plan.match = "dependency_restart"
				plan.implicit = true
			}
		}
	}
}

// sortRestartCandidates orders restart-marked containers by dependency
// (falling back to discovery order on cycles) with self-update targets last.
func (s *Service) sortRestartCandidates(ctx context.Context, scan *restartScan) []deps.ContainerWithDeps {
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
	return orderSelfUpdateLast(sorted, scan.plansByName, s.config.LabelPolicy)
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
	results              []ResourceResult
}

// executeRestartPlans routes each sorted candidate to the compose,
// self-update, or standalone path and returns the merged results.
func (s *Service) executeRestartPlans(ctx context.Context, dockerClient *client.Client, sorted []deps.ContainerWithDeps, plansByName map[string]*restartPlan) ([]ResourceResult, error) {
	run := &restartRun{
		composeGroups:     s.buildComposeGroups(ctx, sorted, plansByName),
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
		s.dispatchRestartCandidate(ctx, dockerClient, run, candidate, plan)
	}

	if len(run.standaloneCandidates) > 0 {
		standaloneResults := s.updateStandaloneRestartCandidates(ctx, dockerClient, run.standaloneCandidates, plansByName)
		for _, result := range standaloneResults {
			if index, ok := run.standaloneIndexes[result.ResourceName]; ok {
				run.results[index] = result
			}
		}
	}

	s.triggerDeferredSelfUpdates(ctx, run)
	return run.results, nil
}

// dispatchRestartCandidate records the candidate's result and either
// applies a compose service update immediately or queues the container for the
// standalone or self-update phase.
func (s *Service) dispatchRestartCandidate(ctx context.Context, dockerClient *client.Client, run *restartRun, candidate deps.ContainerWithDeps, plan *restartPlan) {
	if plan.inspect == nil {
		inspectResult, inspectErr := compat.ContainerInspect(ctx, dockerClient, plan.cnt.ID, client.ContainerInspectOptions{})
		if inspectErr != nil {
			run.results = append(run.results, failedContainerResult(plan.cnt.ID, candidate.Name, fmt.Sprintf("inspect failed: %v", inspectErr)))
			return
		}
		plan.inspect = new(inspectResult.Container)
	}

	res := standaloneRestartResult(candidate, plan)
	if plan.newRef == "" {
		res.Status = StatusSkipped
		res.Error = "no matching updated image"
		run.results = append(run.results, res)
		return
	}

	labels := labelsFromInspect(*plan.inspect)
	endContainerStatus := s.BeginContainerUpdate(plan.cnt.ID)
	defer endContainerStatus()
	endProjectStatus := s.BeginProjectUpdate(compose.ProjectLabel(labels))
	defer endProjectStatus()

	projectName := compose.ProjectLabel(labels)
	serviceName := compose.ServiceLabel(labels)
	projectID := composeProjectID(projectName, run.composeGroups)
	selfUpdate := s.isSelfUpdateCandidate(plan.cnt.ID, labels)

	switch {
	case projectID != "" && serviceName != "" && !selfUpdate:
		res = s.applyComposeServiceUpdate(ctx, dockerClient, res, plan, candidate.Name, projectID, projectName, serviceName, run)
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
		if err := s.validateStandaloneContainerUpdate(labels); err != nil {
			res.Status = StatusFailed
			res.Error = err.Error()
			break
		}
		run.standaloneIndexes[candidate.Name] = len(run.results)
		run.standaloneCandidates = append(run.standaloneCandidates, candidate)
	}
	run.results = append(run.results, res)
}

// triggerDeferredSelfUpdates triggers queued self-updates last;
// candidates arrive sorted agents-first so the server (which hosts this
// process) is the final one handled.
func (s *Service) triggerDeferredSelfUpdates(ctx context.Context, run *restartRun) {
	for _, target := range run.selfUpdateCandidates {
		index, ok := run.selfUpdateIndexes[target.name]
		if !ok {
			continue
		}
		res := run.results[index]
		endContainerStatus := s.BeginContainerUpdate(target.containerID)
		if err := s.triggerSelfUpdate(ctx, target.containerID, target.name, target.newRef, target.labels); err != nil {
			res.Status = StatusFailed
			res.Error = err.Error()
		} else {
			res.Status = StatusUpdated
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

func (s *Service) updateStandaloneRestartCandidates(ctx context.Context, dockerClient *client.Client, candidates []deps.ContainerWithDeps, plansByName map[string]*restartPlan) []ResourceResult {
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

	resultsByName := map[string]ResourceResult{}
	for _, candidate := range slices.Backward(candidates) {
		plan := plansByName[candidate.Name]
		if plan == nil || plan.inspect == nil {
			continue
		}
		result := standaloneRestartResult(candidate, plan)
		if err := s.stopAndRemoveStandaloneContainer(ctx, dockerClient, plan.cnt, *plan.inspect); err != nil {
			result.Status = StatusFailed
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
			result = standaloneRestartResult(candidate, plan)
		}
		if result.Status == StatusFailed {
			resultsByName[candidate.Name] = result
			continue
		}

		if err := s.createStartOrRollback(ctx, dockerClient, plan.cnt, *plan.inspect, plan.newRef); err != nil {
			result.Status = StatusFailed
			result.Error = err.Error()
			resultsByName[candidate.Name] = result
			continue
		}

		result.UpdateApplied = true
		if plan.implicit {
			result.Status = StatusRestarted
		} else {
			result.Status = StatusUpdated
			result.UpdateAvailable = true
			_ = s.notify(ctx, plan.cnt.ID, candidate.Name, plan.newRef, plan.match, refs.NormalizeImageUpdateRef(plan.newRef))
		}
		resultsByName[candidate.Name] = result
	}

	out := make([]ResourceResult, 0, len(candidates))
	for _, candidate := range candidates {
		if result, ok := resultsByName[candidate.Name]; ok {
			out = append(out, result)
		}
	}
	return out
}

func (s *Service) applyComposeServiceUpdate(
	ctx context.Context,
	dockerClient *client.Client,
	res ResourceResult,
	plan *restartPlan,
	containerName string,
	projectID string,
	projectName string,
	serviceName string,
	run *restartRun,
) ResourceResult {
	if !run.processedProjects[projectID] {
		group := run.composeGroups[projectID]
		opCtx, cancel := s.opCtx(ctx)
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
		res.Status = StatusFailed
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
	res.Status = StatusUpdated
	res.UpdateAvailable = true
	res.UpdateApplied = true
	_ = s.notify(ctx, plan.cnt.ID, containerName, plan.newRef, plan.match, refs.NormalizeImageUpdateRef(plan.newRef))
	return res
}

func standaloneRestartResult(candidate deps.ContainerWithDeps, plan *restartPlan) ResourceResult {
	return ResourceResult{
		ResourceID:   plan.cnt.ID,
		ResourceName: candidate.Name,
		ResourceType: ResourceTypeContainer,
		Status:       StatusChecked,
		OldImage:     plan.match,
		NewImage:     refs.NormalizeImageUpdateRef(plan.newRef),
	}
}
