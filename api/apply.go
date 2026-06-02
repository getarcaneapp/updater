package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/getarcaneapp/updater/pkg/digest"
	"github.com/getarcaneapp/updater/pkg/match"
	"github.com/getarcaneapp/updater/pkg/refs"
	"github.com/getarcaneapp/updater/pkg/utils"
	"github.com/getarcaneapp/updater/types"
	"github.com/moby/moby/client"
)

// ApplyPending applies pending image updates from the configured PendingStore.
func (s *Service) ApplyPending(ctx context.Context, opts types.Options) (out *types.Result, err error) {
	start := time.Now()
	out = &types.Result{Items: []types.ResourceResult{}, StartTime: start.UTC().Format(time.RFC3339)}
	defer func() {
		out.EndTime = time.Now().UTC().Format(time.RFC3339)
		out.Duration = time.Since(start).String()
		out.Success = err == nil && out.Failed == 0
	}()

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

	plans := s.buildUpdatePlansInternal(ctx, records, usedImages)
	if len(plans) == 0 {
		return out, nil
	}

	var dcli *client.Client
	if !opts.DryRun || s.config.RegistryDigestResolver != nil {
		dcli, err = s.dockerClientInternal(ctx)
		if err != nil && !opts.DryRun {
			return nil, fmt.Errorf("docker connect: %w", err)
		}
	}
	digestChecker := digest.NewChecker(dcli, s.config.RegistryDigestResolver)
	oldIDToNewRef := map[string]string{}
	oldRefToNewRef := map[string]string{}

	for i := range plans {
		plan := plans[i]
		item := types.ResourceResult{
			ResourceID:   plan.oldRef,
			ResourceType: types.ResourceTypeImage,
			ResourceName: plan.oldRef,
			Status:       types.StatusChecked,
			OldImages:    map[string]string{"main": plan.oldRef},
			NewImages:    map[string]string{"main": plan.newRef},
		}
		out.Checked++

		if opts.DryRun {
			item.Status = types.StatusSkipped
			out.Skipped++
			out.Items = append(out.Items, item)
			_ = s.recordResultInternal(ctx, item)
			continue
		}

		if check := digestChecker.CheckImageNeedsUpdate(ctx, refs.NormalizeImageUpdateRef(plan.newRef)); check.CheckedViaAPI && check.Error == nil && !check.NeedsUpdate && !opts.Force {
			item.Status = types.StatusSkipped
			item.Error = "image already up to date"
			out.Skipped++
			out.Items = append(out.Items, item)
			_ = s.recordResultInternal(ctx, item)
			continue
		}

		if s.config.ImagePuller == nil {
			item.Status = types.StatusFailed
			item.Error = "image puller is required"
			out.Failed++
			out.Items = append(out.Items, item)
			_ = s.recordResultInternal(ctx, item)
			continue
		}
		if err := s.config.ImagePuller.PullImage(ctx, plan.newRef, io.Discard); err != nil {
			item.Status = types.StatusFailed
			item.Error = fmt.Sprintf("pull failed: %v", err)
			out.Failed++
			out.Items = append(out.Items, item)
			_ = s.recordResultInternal(ctx, item)
			continue
		}

		item.Status = types.StatusUpdated
		item.UpdateApplied = true
		item.UpdateAvailable = true
		out.Updated++
		plans[i].pulled = true
		for _, oldID := range plan.oldIDs {
			oldIDToNewRef[oldID] = plan.newRef
		}
		oldRefToNewRef[plan.oldRef] = plan.newRef
		out.Items = append(out.Items, item)
		_ = s.recordResultInternal(ctx, item)
	}

	if len(oldIDToNewRef) > 0 || len(oldRefToNewRef) > 0 {
		containerResults, restartErr := s.RestartContainersUsingOldImages(ctx, oldIDToNewRef, oldRefToNewRef)
		for _, item := range containerResults {
			s.applyResultCountInternal(out, item)
			out.Items = append(out.Items, item)
			_ = s.recordResultInternal(ctx, item)
		}
		if restartErr != nil {
			return out, restartErr
		}
	}

	for i := range plans {
		if !plans[i].pulled {
			continue
		}
		if err := s.config.PendingStore.ClearImageUpdateRecord(ctx, plans[i].record); err != nil {
			s.logger.WarnContext(ctx, "failed to clear image update record", "imageRef", plans[i].oldRef, "error", err)
		}
	}
	return out, nil
}

func (s *Service) dockerClientInternal(ctx context.Context) (*client.Client, error) {
	if s.config.DockerClientProvider == nil {
		return nil, errors.New("docker client provider is required")
	}
	dcli, err := s.config.DockerClientProvider.DockerClient(ctx)
	if err != nil {
		return nil, err
	}
	if dcli == nil {
		return nil, errors.New("docker client unavailable")
	}
	return dcli, nil
}

func (s *Service) buildUpdatePlansInternal(ctx context.Context, records []types.ImageUpdateRecord, usedImages map[string]struct{}) []updatePlan {
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
		oldIDs := []string{}
		if dcli, err := s.dockerClientInternal(ctx); err == nil {
			oldIDs, _ = digest.NewChecker(dcli, nil).GetImageIDsForRef(ctx, oldRef)
		}
		oldIDs = match.AppendImageUpdateRecordIDToOldIDs(oldIDs, record.ID)
		plans = append(plans, updatePlan{record: record, oldRef: oldRef, newRef: newRef, oldIDs: oldIDs})
	}
	return plans
}

func (s *Service) usedImagesInternal(ctx context.Context) (map[string]struct{}, error) {
	if s.config.UsedImageCollector != nil {
		return s.config.UsedImageCollector.UsedImages(ctx)
	}
	dcli, err := s.dockerClientInternal(ctx)
	if err != nil {
		return nil, err
	}

	out := map[string]struct{}{}
	excludedContainers, err := s.excludedContainerSetInternal(ctx)
	if err != nil {
		return nil, err
	}
	listResult, err := dcli.ContainerList(ctx, client.ContainerListOptions{All: false})
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
		inspectResult, inspectErr := utils.ContainerInspectWithCompatibility(ctx, dcli, summary.ID, client.ContainerInspectOptions{})
		if inspectErr != nil {
			continue
		}
		if inspectResult.Container.Config != nil && s.config.LabelPolicy.IsUpdateDisabled(inspectResult.Container.Config.Labels) {
			continue
		}
		for _, tag := range normalizedTagsForContainerInternal(ctx, dcli, inspectResult.Container) {
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
