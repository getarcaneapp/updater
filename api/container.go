package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"go.getarcane.app/updater/pkg/digest"
	"go.getarcane.app/updater/pkg/refs"
	"go.getarcane.app/updater/pkg/utils"
	"go.getarcane.app/updater/types"
)

// UpdateContainer updates a single Docker container by pulling its latest image and recreating it.
func (s *Service) UpdateContainer(ctx context.Context, containerID string, opts types.Options) (out *types.Result, err error) {
	out, finish := newTimedResultInternal()
	defer finish(&err)

	dockerClient, err := s.dockerClientInternal(ctx)
	if err != nil {
		return nil, fmt.Errorf("docker connect: %w", err)
	}

	filters := make(client.Filters)
	filters = filters.Add("id", strings.TrimSpace(containerID))
	containerList, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	if len(containerList.Items) == 0 {
		return nil, fmt.Errorf("container not found: %s", containerID)
	}

	target := containerList.Items[0]
	name := utils.ContainerSummaryName(target)
	inspectResult, err := utils.ContainerInspectWithCompatibility(ctx, dockerClient, target.ID, client.ContainerInspectOptions{})
	if err != nil {
		item := failedContainerResultInternal(target.ID, name, fmt.Sprintf("inspect failed: %v", err))
		out.Items = append(out.Items, item)
		out.Failed++
		return out, nil
	}
	inspect := inspectResult.Container
	labels := labelsFromInspectInternal(inspect)

	endContainerStatus := s.BeginContainerUpdate(target.ID)
	defer endContainerStatus()
	endProjectStatus := s.BeginProjectUpdate(utils.ComposeProjectLabel(labels))
	defer endProjectStatus()

	if s.config.LabelPolicy.IsUpdateDisabled(labels) {
		item := skippedContainerResultInternal(target.ID, name, "updates disabled by label")
		out.Items = append(out.Items, item)
		out.Checked = 1
		out.Skipped++
		return out, nil
	}
	if s.config.LabelPolicy.IsSwarmTask(labels) && !s.isSelfUpdateCandidateInternal(target.ID, labels) {
		item := skippedContainerResultInternal(target.ID, name, "swarm service; update at the service level")
		out.Items = append(out.Items, item)
		out.Checked = 1
		out.Skipped++
		return out, nil
	}

	configImageRef := ""
	if inspect.Config != nil {
		configImageRef = strings.TrimSpace(inspect.Config.Image)
	}
	imageRef, _ := ResolvePullableImageRef(target.Image, configImageRef, nil)
	if imageRef == "" && inspect.Image != "" {
		if imageInspect, inspectErr := dockerClient.ImageInspect(ctx, inspect.Image); inspectErr == nil {
			imageRef, _ = ResolvePullableImageRef(target.Image, configImageRef, imageInspect.RepoTags)
		}
	}
	normalizedRef := refs.NormalizeImageUpdateRef(imageRef)
	if normalizedRef == "" {
		item := skippedContainerResultInternal(target.ID, name, "unable to resolve a pullable image reference for container")
		out.Items = append(out.Items, item)
		out.Skipped++
		return out, nil
	}

	if opts.DryRun {
		item := types.ResourceResult{ResourceID: target.ID, ResourceName: name, ResourceType: types.ResourceTypeContainer, Status: types.StatusSkipped, OldImages: map[string]string{"main": inspect.Image}, NewImages: map[string]string{"main": normalizedRef}}
		out.Items = append(out.Items, item)
		out.Checked = 1
		out.Skipped++
		return out, nil
	}

	if s.config.ImagePuller == nil {
		item := failedContainerResultInternal(target.ID, name, "image puller is required")
		out.Items = append(out.Items, item)
		out.Failed++
		return out, nil
	}
	if err := s.config.ImagePuller.PullImage(ctx, normalizedRef, io.Discard); err != nil {
		item := failedContainerResultInternal(target.ID, name, fmt.Sprintf("pull failed: %v", err))
		out.Items = append(out.Items, item)
		out.Failed++
		return out, nil
	}

	changed, compareErr := digest.NewChecker(dockerClient, nil).CompareWithPulled(ctx, inspect.Image, normalizedRef)
	if compareErr == nil && !changed && !opts.Force {
		item := skippedContainerResultInternal(target.ID, name, "image digest unchanged after pull")
		out.Items = append(out.Items, item)
		out.Checked = 1
		out.Skipped++
		s.clearPendingRecordInternal(ctx, normalizedRef)
		return out, nil
	}

	if s.isSelfUpdateCandidateInternal(target.ID, labels) {
		if err := s.triggerSelfUpdateInternal(ctx, target.ID, name, normalizedRef, labels); err != nil {
			item := failedContainerResultInternal(target.ID, name, err.Error())
			out.Items = append(out.Items, item)
			out.Failed++
			return out, nil
		}
		item := updatedContainerResultInternal(target.ID, name, inspect.Image, normalizedRef)
		out.Items = append(out.Items, item)
		out.Checked = 1
		out.Updated++
		s.clearPendingRecordInternal(ctx, normalizedRef)
		return out, nil
	}

	if err := s.updateComposeOrStandaloneInternal(ctx, target, inspect, normalizedRef); err != nil {
		item := failedContainerResultInternal(target.ID, name, err.Error())
		out.Items = append(out.Items, item)
		out.Failed++
	} else {
		item := updatedContainerResultInternal(target.ID, name, inspect.Image, normalizedRef)
		out.Items = append(out.Items, item)
		out.Updated++
		_ = s.notifyInternal(ctx, target.ID, name, imageRef, inspect.Image, normalizedRef)
		s.clearPendingRecordInternal(ctx, normalizedRef)
	}
	out.Checked = 1
	return out, nil
}

// clearPendingRecordInternal clears the pending update record for an applied
// image ref through the same PendingStore seam ApplyPending uses — a
// single-container update must not leave its "update available" record
// behind, or the update keeps counting as pending. Only records whose
// NewImageRef matches the applied ref are cleared: a tag-update record
// (e.g. nginx:1.27 -> 1.28) stays pending when only the old tag was
// re-pulled. The records are looked up
// and cleared as stored (ID and all) rather than reconstructed, because
// stores key by ID when one is present.
func (s *Service) clearPendingRecordInternal(ctx context.Context, imageRef string) {
	if s.config.PendingStore == nil {
		return
	}
	normalized := refs.NormalizeImageUpdateRef(imageRef)
	if normalized == "" {
		return
	}
	records, err := s.config.PendingStore.PendingImageUpdates(ctx)
	if err != nil {
		s.logger.WarnContext(ctx, "failed to load pending records to clear applied update", "imageRef", imageRef, "error", err)
		return
	}
	for _, record := range records {
		if refs.NormalizeImageUpdateRef(record.NewImageRef()) != normalized {
			continue
		}
		if err := s.config.PendingStore.ClearImageUpdateRecord(ctx, record); err != nil {
			s.logger.WarnContext(ctx, "failed to clear applied update record", "imageRef", imageRef, "error", err)
		}
	}
}

// UpdateStandaloneContainer recreates a non-compose container with newRef.
func (s *Service) UpdateStandaloneContainer(ctx context.Context, cnt container.Summary, inspect container.InspectResponse, newRef string) error {
	labels := labelsFromInspectInternal(inspect)
	if err := s.validateStandaloneContainerUpdateInternal(labels); err != nil {
		return err
	}

	dockerClient, err := s.dockerClientInternal(ctx)
	if err != nil {
		return fmt.Errorf("docker connect: %w", err)
	}

	if err := s.stopAndRemoveStandaloneContainerInternal(ctx, dockerClient, cnt, inspect); err != nil {
		return err
	}
	return s.createStartOrRollbackInternal(ctx, dockerClient, cnt, inspect, newRef)
}

func (s *Service) createStartOrRollbackInternal(ctx context.Context, dockerClient *client.Client, cnt container.Summary, inspect container.InspectResponse, newRef string) error {
	createdID, err := s.createAndStartStandaloneContainerInternal(ctx, dockerClient, cnt, inspect, newRef)
	if err == nil {
		return nil
	}
	s.removeFailedCreatedContainerInternal(ctx, dockerClient, createdID, utils.ContainerSummaryName(cnt))
	if rollbackErr := s.rollbackStandaloneContainerInternal(ctx, dockerClient, cnt, inspect); rollbackErr != nil {
		return fmt.Errorf("%w; rollback failed: %w", err, rollbackErr)
	}
	return fmt.Errorf("%w; rollback succeeded", err)
}

func (s *Service) validateStandaloneContainerUpdateInternal(labels map[string]string) error {
	if s.config.LabelPolicy.IsSelfUpdateTarget(labels) {
		return errors.New("self-update containers must use SelfUpdater")
	}
	return nil
}

func (s *Service) stopAndRemoveStandaloneContainerInternal(ctx context.Context, dockerClient *client.Client, cnt container.Summary, inspect container.InspectResponse) error {
	labels := labelsFromInspectInternal(inspect)
	name := utils.ContainerSummaryName(cnt)
	stopOptions := client.ContainerStopOptions{}
	if signal := s.config.LabelPolicy.StopSignal(labels); signal != "" {
		stopOptions.Signal = signal
	}
	stopCtx, cancelStop := s.opCtxInternal(ctx)
	_, err := dockerClient.ContainerStop(stopCtx, cnt.ID, stopOptions)
	cancelStop()
	if err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	_ = s.recordEventInternal(ctx, "container_stop", cnt.ID, name, map[string]any{"action": "updater_stop"})

	removeCtx, cancelRemove := s.opCtxInternal(ctx)
	_, err = dockerClient.ContainerRemove(removeCtx, cnt.ID, client.ContainerRemoveOptions{})
	cancelRemove()
	if err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	_ = s.recordEventInternal(ctx, "container_delete", cnt.ID, name, map[string]any{"action": "updater_delete"})
	return nil
}

func (s *Service) createAndStartStandaloneContainerInternal(ctx context.Context, dockerClient *client.Client, cnt container.Summary, inspect container.InspectResponse, newRef string) (string, error) {
	name := utils.ContainerSummaryName(cnt)
	cfg := inspect.Config
	if cfg == nil {
		cfg = &container.Config{}
	}
	cfg = cloneContainerConfigInternal(cfg)
	cfg.Image = newRef
	if cfg.Labels != nil {
		if _, ok := cfg.Labels["com.docker.compose.image"]; ok {
			if imgInspect, inspectErr := dockerClient.ImageInspect(ctx, newRef); inspectErr == nil {
				cfg.Labels["com.docker.compose.image"] = imgInspect.ID
			}
		}
	}

	hostConfig, _, _, err := utils.PrepareRecreateHostConfigForEngine(ctx, dockerClient, inspect.HostConfig)
	if err != nil {
		return "", fmt.Errorf("prepare host config: %w", err)
	}
	var networkMode container.NetworkMode
	if hostConfig != nil {
		networkMode = hostConfig.NetworkMode
	}
	if networkMode.IsHost() || networkMode.IsContainer() {
		cfg.Hostname = ""
		cfg.Domainname = ""
	}
	if networkMode.IsContainer() {
		cfg.ExposedPorts = nil
		if hostConfig != nil {
			hostConfig.PortBindings = nil
			hostConfig.PublishAllPorts = false
		}
	}

	apiVersion := utils.DetectAPIVersion(ctx, dockerClient)
	networkingConfig := buildRecreateNetworkingConfigInternal(networkMode, inspect.NetworkSettings, apiVersion)
	containerName := strings.TrimPrefix(inspect.Name, "/")
	createCtx, cancelCreate := s.opCtxInternal(ctx)
	resp, err := createStandaloneContainerInternal(createCtx, dockerClient, client.ContainerCreateOptions{
		Config:           cfg,
		HostConfig:       hostConfig,
		NetworkingConfig: networkingConfig,
		Name:             containerName,
	}, apiVersion)
	cancelCreate()
	if err != nil {
		return resp.ID, fmt.Errorf("create: %w", err)
	}
	_ = s.recordEventInternal(ctx, "container_create", resp.ID, name, map[string]any{"action": "updater_create", "newImageID": resp.ID})

	startCtx, cancelStart := s.opCtxInternal(ctx)
	_, err = dockerClient.ContainerStart(startCtx, resp.ID, client.ContainerStartOptions{})
	cancelStart()
	if err != nil {
		running, inspectErr := containerRunningInternal(ctx, dockerClient, resp.ID)
		if inspectErr != nil {
			return resp.ID, fmt.Errorf("start: %w; inspect after start error: %w", err, inspectErr)
		}
		if !running {
			return resp.ID, fmt.Errorf("start: %w", err)
		}
		s.logger.WarnContext(ctx, "container start returned error but inspect reports running", "containerID", resp.ID, "containerName", name, "error", err)
	}
	_ = s.recordEventInternal(ctx, "container_start", resp.ID, name, map[string]any{"action": "updater_start"})
	return resp.ID, s.recordEventInternal(ctx, "container_update", resp.ID, name, map[string]any{"oldContainerID": cnt.ID, "newContainerID": resp.ID, "newImage": newRef})
}

func createStandaloneContainerInternal(ctx context.Context, dockerClient *client.Client, options client.ContainerCreateOptions, apiVersion string) (client.ContainerCreateResult, error) {
	adjustedOptions, extraEndpoints := utils.PrepareContainerCreateOptionsForAPI(options, apiVersion)
	result, err := dockerClient.ContainerCreate(ctx, adjustedOptions)
	if err != nil {
		return client.ContainerCreateResult{}, err
	}
	if len(extraEndpoints) == 0 {
		return result, nil
	}
	if err := utils.ConnectContainerExtraNetworksForAPI(ctx, dockerClient, result.ID, extraEndpoints); err != nil {
		return result, err
	}
	return result, nil
}

func containerRunningInternal(ctx context.Context, dockerClient *client.Client, containerID string) (bool, error) {
	inspectResult, err := utils.ContainerInspectWithCompatibility(ctx, dockerClient, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return false, err
	}
	state := inspectResult.Container.State
	return state != nil && state.Running, nil
}

func (s *Service) removeFailedCreatedContainerInternal(ctx context.Context, dockerClient *client.Client, containerID, containerName string) {
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return
	}
	removeCtx, cancelRemove := s.opCtxInternal(ctx)
	_, err := dockerClient.ContainerRemove(removeCtx, containerID, client.ContainerRemoveOptions{Force: true})
	cancelRemove()
	if err != nil {
		s.logger.WarnContext(ctx, "failed to remove container after unsuccessful recreate", "containerID", containerID, "containerName", containerName, "error", err)
		return
	}
	_ = s.recordEventInternal(ctx, "container_cleanup", containerID, containerName, map[string]any{"action": "updater_cleanup_failed_create"})
}

func (s *Service) rollbackStandaloneContainerInternal(ctx context.Context, dockerClient *client.Client, cnt container.Summary, inspect container.InspectResponse) error {
	oldImageID := strings.TrimSpace(inspect.Image)
	if oldImageID == "" {
		return errors.New("old image ID unavailable")
	}
	rollbackID, err := s.createAndStartStandaloneContainerInternal(ctx, dockerClient, cnt, inspect, oldImageID)
	if err != nil {
		s.removeFailedCreatedContainerInternal(ctx, dockerClient, rollbackID, utils.ContainerSummaryName(cnt))
		return err
	}
	_ = s.recordEventInternal(ctx, "container_rollback", rollbackID, utils.ContainerSummaryName(cnt), map[string]any{
		"action":         "updater_rollback",
		"oldContainerID": cnt.ID,
		"rollbackImage":  oldImageID,
	})
	return nil
}

// ResolvePullableImageRef chooses the best pullable image reference for a container.
func ResolvePullableImageRef(summaryImage, inspectConfigImage string, repoTags []string) (ref, source string) {
	if image := strings.TrimSpace(inspectConfigImage); isMutablePullableImageRefInternal(image) {
		return image, "container_inspect_config"
	}
	if image := strings.TrimSpace(summaryImage); isMutablePullableImageRefInternal(image) {
		return image, "container_summary"
	}
	for _, tag := range repoTags {
		trimmed := strings.TrimSpace(tag)
		if trimmed == "<none>:<none>" || !isMutablePullableImageRefInternal(trimmed) {
			continue
		}
		return trimmed, "image_repo_tag"
	}
	return "", ""
}

func isMutablePullableImageRefInternal(imageRef string) bool {
	imageRef = strings.TrimSpace(imageRef)
	return imageRef != "" && !refs.IsImageIDLikeReference(imageRef) && !refs.IsDigestPinnedReference(imageRef)
}

func (s *Service) updateComposeOrStandaloneInternal(ctx context.Context, target container.Summary, inspect container.InspectResponse, normalizedRef string) error {
	labels := labelsFromInspectInternal(inspect)
	projectName := utils.ComposeProjectLabel(labels)
	serviceName := utils.ComposeServiceLabel(labels)
	if projectName != "" && serviceName != "" && s.config.ProjectUpdater != nil {
		project, err := s.config.ProjectUpdater.ProjectByComposeName(ctx, projectName)
		if err == nil {
			// The compose path was chosen; a failure here must surface instead
			// of falling back, so a partial compose up is never clobbered by a
			// standalone recreate.
			return s.config.ProjectUpdater.UpdateServices(ctx, project.ID, []string{serviceName})
		}
		s.logger.WarnContext(ctx, "compose project not resolved; falling back to standalone container update",
			"container", utils.ContainerSummaryName(target),
			"project", projectName,
			"service", serviceName,
			"error", err,
		)
	}
	return s.UpdateStandaloneContainer(ctx, target, inspect, normalizedRef)
}

func normalizedTagsForContainerInternal(ctx context.Context, dockerClient *client.Client, inspect container.InspectResponse) []string {
	seen := map[string]struct{}{}
	if dockerClient != nil {
		if imageInspect, err := dockerClient.ImageInspect(ctx, inspect.Image); err == nil {
			for _, tag := range imageInspect.RepoTags {
				if normalized := refs.NormalizeImageUpdateRef(tag); normalized != "" {
					seen[normalized] = struct{}{}
				}
			}
		}
	}
	if inspect.Config != nil {
		if normalized := refs.NormalizeImageUpdateRef(inspect.Config.Image); normalized != "" {
			seen[normalized] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for tag := range seen {
		out = append(out, tag)
	}
	return out
}

func buildRecreateNetworkingConfigInternal(networkMode container.NetworkMode, settings *container.NetworkSettings, apiVersion string) *network.NetworkingConfig {
	if networkMode.IsContainer() || settings == nil || len(settings.Networks) == 0 {
		return nil
	}

	rawEndpointsConfig := make(map[string]*network.EndpointSettings, len(settings.Networks))
	for networkName, endpoint := range settings.Networks {
		if endpoint == nil {
			rawEndpointsConfig[networkName] = &network.EndpointSettings{}
			continue
		}
		rawEndpointsConfig[networkName] = &network.EndpointSettings{
			IPAMConfig: endpoint.IPAMConfig.Copy(),
			Links:      slices.Clone(endpoint.Links),
			Aliases:    slices.Clone(endpoint.Aliases),
			DriverOpts: maps.Clone(endpoint.DriverOpts),
			GwPriority: endpoint.GwPriority,
			MacAddress: endpoint.MacAddress,
		}
	}

	sanitized := utils.SanitizeContainerCreateEndpointSettingsForAPI(rawEndpointsConfig, apiVersion)
	if len(sanitized) == 0 {
		return nil
	}
	return &network.NetworkingConfig{EndpointsConfig: sanitized}
}

func cloneContainerConfigInternal(config *container.Config) *container.Config {
	if config == nil {
		return nil
	}
	copied := new(*config)
	copied.Env = slices.Clone(config.Env)
	copied.Cmd = slices.Clone(config.Cmd)
	copied.Entrypoint = slices.Clone(config.Entrypoint)
	copied.Labels = maps.Clone(config.Labels)
	return copied
}
