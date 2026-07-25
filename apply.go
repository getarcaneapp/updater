package updater

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/moby/moby/client"
	"go.getarcane.app/updater/internal/compat"
	"go.getarcane.app/updater/internal/digestcheck"
	"go.getarcane.app/updater/internal/match"
	"go.getarcane.app/updater/refs"
)

// ApplyPending applies pending image updates from the configured PendingStore.
func (s *Service) ApplyPending(ctx context.Context, opts Options) (out *Result, err error) {
	out, finish := newTimedResult()
	defer finish(&err)

	if s.config.PendingStore == nil {
		return nil, ErrPendingStoreRequired
	}

	records, err := s.config.PendingStore.PendingImageUpdates(ctx)
	if err != nil {
		return nil, fmt.Errorf("query pending image updates: %w", err)
	}
	if len(records) == 0 {
		return out, nil
	}

	usedImages, err := s.usedImages(ctx)
	if err != nil {
		return nil, fmt.Errorf("collect used images: %w", err)
	}
	if len(usedImages) == 0 {
		return out, nil
	}

	var planDockerClient *client.Client
	if dockerClient, dockerErr := s.dockerClient(ctx); dockerErr == nil {
		planDockerClient = dockerClient
	}
	plans := s.buildUpdatePlans(ctx, records, usedImages, digestcheck.NewChecker(planDockerClient, nil))
	if len(plans) == 0 {
		return out, nil
	}

	var dockerClient *client.Client
	if !opts.DryRun || s.config.RegistryDigestResolver != nil {
		dockerClient, err = s.dockerClient(ctx)
		if err != nil && !opts.DryRun {
			return nil, fmt.Errorf("docker connect: %w", err)
		}
	}
	digestChecker := digestcheck.NewChecker(dockerClient, s.config.RegistryDigestResolver)
	oldIDToNewRef := map[string]string{}
	oldRefToNewRef := map[string]string{}

	for i := range plans {
		plan := &plans[i]
		item := s.executeUpdatePlan(ctx, digestChecker, plan, opts)
		out.Checked++
		switch item.Status {
		case StatusSkipped:
			out.Skipped++
		case StatusFailed:
			out.Failed++
		case StatusUpdated:
			out.Updated++
			for _, oldID := range plan.oldIDs {
				oldIDToNewRef[oldID] = plan.newRef
			}
			oldRefToNewRef[plan.oldRef] = plan.newRef
		case StatusChecked, StatusRestarted, StatusUpToDate, StatusUpdateAvailable:
			// Already counted by the unconditional out.Checked++ above.
		}
		out.Items = append(out.Items, item)
		_ = s.recordResult(ctx, item)
	}

	if restartErr := s.restartAfterApply(ctx, out, plans, oldIDToNewRef, oldRefToNewRef); restartErr != nil {
		return out, restartErr
	}

	s.clearAppliedPlanRecords(ctx, plans)
	return out, nil
}

// executeUpdatePlan applies a single pending-update plan: it honors
// dry-run, skips images that are already current, pulls the new reference,
// and reports the outcome. It marks plan.pulled once the pull succeeds.
func (s *Service) executeUpdatePlan(ctx context.Context, digestChecker *digestcheck.Checker, plan *updatePlan, opts Options) ResourceResult {
	item := ResourceResult{
		ResourceID:   plan.oldRef,
		ResourceType: ResourceTypeImage,
		ResourceName: plan.oldRef,
		Status:       StatusChecked,
		OldImage:     plan.oldRef,
		NewImage:     plan.newRef,
	}

	if opts.DryRun {
		item.Status = StatusSkipped
		return item
	}
	if !opts.Force && planImageUpToDate(ctx, digestChecker, *plan) {
		item.Status = StatusSkipped
		item.Error = "image already up to date"
		return item
	}
	if s.config.ImagePuller == nil {
		item.Status = StatusFailed
		item.Error = ErrImagePullerRequired.Error()
		return item
	}
	if err := s.config.ImagePuller.PullImage(ctx, plan.newRef, io.Discard); err != nil {
		item.Status = StatusFailed
		item.Error = fmt.Sprintf("pull failed: %v", err)
		return item
	}
	plan.pulled = true

	if !opts.Force {
		targetIDs, targetErr := digestChecker.GetImageIDsForRef(ctx, plan.newRef)
		if targetErr == nil && imageIDsOverlap(plan.oldIDs, targetIDs) {
			item.Status = StatusUpToDate
			item.Error = "image digest unchanged after pull"
			return item
		}
	}

	item.Status = StatusUpdated
	item.UpdateApplied = true
	item.UpdateAvailable = true
	return item
}

// restartAfterApply restarts containers still running the replaced
// images and folds their results into out, flagging plans whose restart failed
// so their pending records are kept.
func (s *Service) restartAfterApply(ctx context.Context, out *Result, plans []updatePlan, oldIDToNewRef, oldRefToNewRef map[string]string) error {
	if len(oldIDToNewRef) == 0 && len(oldRefToNewRef) == 0 {
		return nil
	}
	containerResults, restartErr := s.RestartContainersUsingOldImages(ctx, oldIDToNewRef, oldRefToNewRef)
	markRestartFailedPlans(plans, containerResults)
	for _, item := range containerResults {
		s.applyResultCount(out, item)
		out.Items = append(out.Items, item)
		_ = s.recordResult(ctx, item)
	}
	return restartErr
}

func (s *Service) clearAppliedPlanRecords(ctx context.Context, plans []updatePlan) {
	for i := range plans {
		if !plans[i].pulled {
			continue
		}
		if plans[i].restartFailed {
			s.logger.WarnContext(ctx, "keeping image update record after restart failure", "imageRef", plans[i].oldRef, "newRef", plans[i].newRef)
			continue
		}
		if err := s.config.PendingStore.ClearImageUpdateRecord(ctx, plans[i].record); err != nil {
			s.logger.WarnContext(ctx, "failed to clear image update record", "imageRef", plans[i].oldRef, "error", err)
		}
	}
}

func markRestartFailedPlans(plans []updatePlan, results []ResourceResult) {
	failedRefs := restartFailedImageRefs(results)
	if len(failedRefs) == 0 {
		return
	}

	for i := range plans {
		if !plans[i].pulled {
			continue
		}
		newRef := refs.NormalizeImageUpdateRef(plans[i].newRef)
		if _, failed := failedRefs[newRef]; failed {
			plans[i].restartFailed = true
		}
	}
}

func restartFailedImageRefs(results []ResourceResult) map[string]struct{} {
	failedRefs := map[string]struct{}{}
	for _, result := range results {
		if result.Status != StatusFailed {
			continue
		}
		if newRef := refs.NormalizeImageUpdateRef(result.NewImage); newRef != "" {
			failedRefs[newRef] = struct{}{}
		}
	}
	return failedRefs
}

func planImageUpToDate(ctx context.Context, checker *digestcheck.Checker, plan updatePlan) bool {
	imageRef := refs.NormalizeImageUpdateRef(plan.newRef)
	if plan.record.IsDigestUpdate() && plan.record.LatestDigest != nil {
		if knownDigest := strings.TrimSpace(*plan.record.LatestDigest); knownDigest != "" {
			check := checker.CheckImageMatchesKnownDigest(ctx, imageRef, knownDigest)
			return check.Error == nil && !check.NeedsUpdate
		}
	}

	check := checker.CheckImageNeedsUpdate(ctx, imageRef)
	return check.CheckedViaAPI && check.Error == nil && !check.NeedsUpdate
}

func (s *Service) dockerClient(ctx context.Context) (*client.Client, error) {
	if s.config.DockerClientProvider == nil {
		return nil, ErrDockerClientProviderRequired
	}
	dockerClient, err := s.config.DockerClientProvider.DockerClient(ctx)
	if err != nil {
		return nil, err
	}
	if dockerClient == nil {
		return nil, ErrNilDockerClient
	}
	return dockerClient, nil
}

func (s *Service) buildUpdatePlans(ctx context.Context, records []ImageUpdateRecord, usedImages map[string]struct{}, digestChecker *digestcheck.Checker) []updatePlan {
	var plans []updatePlan
	for _, record := range records {
		if !record.NeedsUpdate() {
			continue
		}
		oldRef := record.ImageRef()
		if oldRef == "" {
			continue
		}
		oldNorm := refs.NormalizeImageUpdateRef(oldRef)
		if oldNorm == "" {
			s.logger.DebugContext(ctx, "skipping invalid pending image reference", "imageRef", oldRef)
			continue
		}
		if _, ok := usedImages[oldNorm]; !ok {
			continue
		}

		newRef := record.NewImageRef()
		var oldIDs []string
		if digestChecker != nil {
			oldIDs, _ = digestChecker.GetImageIDsForRef(ctx, oldRef)
		}
		oldIDs = match.AppendImageUpdateRecordIDToOldIDs(oldIDs, record.ID)
		plans = append(plans, updatePlan{record: record, oldRef: oldRef, newRef: newRef, oldIDs: oldIDs})
	}
	return plans
}

func imageIDsOverlap(oldIDs, newIDs []string) bool {
	seen := map[string]struct{}{}
	for _, oldID := range oldIDs {
		oldID = strings.TrimSpace(oldID)
		if oldID != "" {
			seen[oldID] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return false
	}
	for _, newID := range newIDs {
		if _, ok := seen[strings.TrimSpace(newID)]; ok {
			return true
		}
	}
	return false
}

func (s *Service) usedImages(ctx context.Context) (map[string]struct{}, error) {
	if s.config.UsedImageCollector != nil {
		return s.config.UsedImageCollector.UsedImages(ctx)
	}
	dockerClient, err := s.dockerClient(ctx)
	if err != nil {
		return nil, err
	}

	out := map[string]struct{}{}
	excludedContainers, err := s.excludedContainerSet(ctx)
	if err != nil {
		return nil, err
	}
	listResult, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{All: false})
	if err != nil {
		return nil, err
	}
	for _, summary := range listResult.Items {
		if shouldSkipSummary(summary, excludedContainers, "", s.config.LabelPolicy) {
			continue
		}
		if imageRef := refs.NormalizeImageUpdateRef(summary.Image); imageRef != "" {
			out[imageRef] = struct{}{}
			continue
		}
		inspectResult, inspectErr := compat.ContainerInspect(ctx, dockerClient, summary.ID, client.ContainerInspectOptions{})
		if inspectErr != nil {
			continue
		}
		if inspectResult.Container.Config != nil && s.config.LabelPolicy.IsUpdateDisabled(inspectResult.Container.Config.Labels) {
			continue
		}
		for _, tag := range normalizedTagsForContainer(ctx, dockerClient, inspectResult.Container) {
			out[tag] = struct{}{}
		}
	}
	return out, nil
}

func (s *Service) excludedContainerSet(ctx context.Context) (map[string]bool, error) {
	if s.config.Settings == nil {
		return nil, nil
	}
	excluded, err := s.config.Settings.ExcludedContainers(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(excluded))
	for _, name := range excluded {
		name = strings.TrimSpace(name)
		if name != "" {
			out[name] = true
		}
	}
	return out, nil
}
