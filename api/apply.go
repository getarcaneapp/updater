package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/moby/moby/client"
	"go.getarcane.app/updater/pkg/digest"
	"go.getarcane.app/updater/pkg/match"
	"go.getarcane.app/updater/pkg/refs"
	"go.getarcane.app/updater/pkg/utils"
	"go.getarcane.app/updater/types"
)

// ApplyPending applies pending image updates from the configured PendingStore.
func (s *Service) ApplyPending(ctx context.Context, opts types.Options) (out *types.Result, err error) {
	out, finish := newTimedResultInternal()
	defer finish(&err)

	if s.config.PendingStore == nil {
		return nil, errors.New("pending store is required")
	}

	records, err := s.config.PendingStore.PendingImageUpdates(ctx)
	if err != nil {
		return nil, fmt.Errorf("query pending image updates: %w", err)
	}
	if len(records) == 0 {
		return out, nil
	}

	usedImages, err := s.usedImagesInternal(ctx)
	if err != nil {
		return nil, fmt.Errorf("collect used images: %w", err)
	}
	if len(usedImages) == 0 {
		return out, nil
	}

	var planDockerClient *client.Client
	if dockerClient, dockerErr := s.dockerClientInternal(ctx); dockerErr == nil {
		planDockerClient = dockerClient
	}
	plans := s.buildUpdatePlansInternal(ctx, records, usedImages, digest.NewChecker(planDockerClient, nil))
	if len(plans) == 0 {
		return out, nil
	}

	var dockerClient *client.Client
	if !opts.DryRun || s.config.RegistryDigestResolver != nil {
		dockerClient, err = s.dockerClientInternal(ctx)
		if err != nil && !opts.DryRun {
			return nil, fmt.Errorf("docker connect: %w", err)
		}
	}
	digestChecker := digest.NewChecker(dockerClient, s.config.RegistryDigestResolver)
	oldIDToNewRef := map[string]string{}
	oldRefToNewRef := map[string]string{}

	for i := range plans {
		plan := &plans[i]
		item := s.executeUpdatePlanInternal(ctx, digestChecker, plan, opts)
		out.Checked++
		switch item.Status {
		case types.StatusSkipped:
			out.Skipped++
		case types.StatusFailed:
			out.Failed++
		case types.StatusUpdated:
			out.Updated++
			for _, oldID := range plan.oldIDs {
				oldIDToNewRef[oldID] = plan.newRef
			}
			oldRefToNewRef[plan.oldRef] = plan.newRef
		}
		out.Items = append(out.Items, item)
		_ = s.recordResultInternal(ctx, item)
	}

	if restartErr := s.restartAfterApplyInternal(ctx, out, plans, oldIDToNewRef, oldRefToNewRef); restartErr != nil {
		return out, restartErr
	}

	s.clearAppliedPlanRecordsInternal(ctx, plans)
	return out, nil
}

// executeUpdatePlanInternal applies a single pending-update plan: it honors
// dry-run, skips images that are already current, pulls the new reference,
// and reports the outcome. It marks plan.pulled once the pull succeeds.
func (s *Service) executeUpdatePlanInternal(ctx context.Context, digestChecker *digest.Checker, plan *updatePlan, opts types.Options) types.ResourceResult {
	item := types.ResourceResult{
		ResourceID:   plan.oldRef,
		ResourceType: types.ResourceTypeImage,
		ResourceName: plan.oldRef,
		Status:       types.StatusChecked,
		OldImages:    map[string]string{"main": plan.oldRef},
		NewImages:    map[string]string{"main": plan.newRef},
	}

	if opts.DryRun {
		item.Status = types.StatusSkipped
		return item
	}
	if !opts.Force && planImageUpToDateInternal(ctx, digestChecker, *plan) {
		item.Status = types.StatusSkipped
		item.Error = "image already up to date"
		return item
	}
	if s.config.ImagePuller == nil {
		item.Status = types.StatusFailed
		item.Error = "image puller is required"
		return item
	}
	if err := s.config.ImagePuller.PullImage(ctx, plan.newRef, io.Discard); err != nil {
		item.Status = types.StatusFailed
		item.Error = fmt.Sprintf("pull failed: %v", err)
		return item
	}
	plan.pulled = true

	if !opts.Force {
		targetIDs, targetErr := digestChecker.GetImageIDsForRef(ctx, plan.newRef)
		if targetErr == nil && imageIDsOverlapInternal(plan.oldIDs, targetIDs) {
			item.Status = types.StatusUpToDate
			item.Error = "image digest unchanged after pull"
			return item
		}
	}

	item.Status = types.StatusUpdated
	item.UpdateApplied = true
	item.UpdateAvailable = true
	return item
}

// restartAfterApplyInternal restarts containers still running the replaced
// images and folds their results into out, flagging plans whose restart failed
// so their pending records are kept.
func (s *Service) restartAfterApplyInternal(ctx context.Context, out *types.Result, plans []updatePlan, oldIDToNewRef, oldRefToNewRef map[string]string) error {
	if len(oldIDToNewRef) == 0 && len(oldRefToNewRef) == 0 {
		return nil
	}
	containerResults, restartErr := s.RestartContainersUsingOldImages(ctx, oldIDToNewRef, oldRefToNewRef)
	markRestartFailedPlansInternal(plans, containerResults)
	for _, item := range containerResults {
		s.applyResultCountInternal(out, item)
		out.Items = append(out.Items, item)
		_ = s.recordResultInternal(ctx, item)
	}
	return restartErr
}

func (s *Service) clearAppliedPlanRecordsInternal(ctx context.Context, plans []updatePlan) {
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

func markRestartFailedPlansInternal(plans []updatePlan, results []types.ResourceResult) {
	failedRefs := restartFailedImageRefsInternal(results)
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

func restartFailedImageRefsInternal(results []types.ResourceResult) map[string]struct{} {
	failedRefs := map[string]struct{}{}
	for _, result := range results {
		if result.Status != types.StatusFailed {
			continue
		}
		if newRef := refs.NormalizeImageUpdateRef(result.NewImages["main"]); newRef != "" {
			failedRefs[newRef] = struct{}{}
		}
	}
	return failedRefs
}

func planImageUpToDateInternal(ctx context.Context, checker *digest.Checker, plan updatePlan) bool {
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

func (s *Service) dockerClientInternal(ctx context.Context) (*client.Client, error) {
	if s.config.DockerClientProvider == nil {
		return nil, errors.New("docker client provider is required")
	}
	dockerClient, err := s.config.DockerClientProvider.DockerClient(ctx)
	if err != nil {
		return nil, err
	}
	if dockerClient == nil {
		return nil, errors.New("docker client unavailable")
	}
	return dockerClient, nil
}

func (s *Service) buildUpdatePlansInternal(ctx context.Context, records []types.ImageUpdateRecord, usedImages map[string]struct{}, digestChecker *digest.Checker) []updatePlan {
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

func imageIDsOverlapInternal(oldIDs, newIDs []string) bool {
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

func (s *Service) usedImagesInternal(ctx context.Context) (map[string]struct{}, error) {
	if s.config.UsedImageCollector != nil {
		return s.config.UsedImageCollector.UsedImages(ctx)
	}
	dockerClient, err := s.dockerClientInternal(ctx)
	if err != nil {
		return nil, err
	}

	out := map[string]struct{}{}
	excludedContainers, err := s.excludedContainerSetInternal(ctx)
	if err != nil {
		return nil, err
	}
	listResult, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{All: false})
	if err != nil {
		return nil, err
	}
	for _, summary := range listResult.Items {
		if shouldSkipSummaryInternal(summary, excludedContainers, "", s.config.LabelPolicy) {
			continue
		}
		if imageRef := refs.NormalizeImageUpdateRef(summary.Image); imageRef != "" {
			out[imageRef] = struct{}{}
			continue
		}
		inspectResult, inspectErr := utils.ContainerInspectWithCompatibility(ctx, dockerClient, summary.ID, client.ContainerInspectOptions{})
		if inspectErr != nil {
			continue
		}
		if inspectResult.Container.Config != nil && s.config.LabelPolicy.IsUpdateDisabled(inspectResult.Container.Config.Labels) {
			continue
		}
		for _, tag := range normalizedTagsForContainerInternal(ctx, dockerClient, inspectResult.Container) {
			out[tag] = struct{}{}
		}
	}
	return out, nil
}

func (s *Service) excludedContainerSetInternal(ctx context.Context) (map[string]bool, error) {
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
