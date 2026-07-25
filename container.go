package updater

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
	"go.getarcane.app/updater/internal/compat"
	"go.getarcane.app/updater/internal/compose"
	"go.getarcane.app/updater/internal/digestcheck"
	"go.getarcane.app/updater/refs"
)

// UpdateContainer updates a single Docker container by pulling its latest image and recreating it.
func (s *Service) UpdateContainer(ctx context.Context, containerID string, opts Options) (out *Result, err error) {
	out, finish := newTimedResult()
	defer finish(&err)

	dockerClient, err := s.dockerClient(ctx)
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
		return nil, fmt.Errorf("%w: %s", ErrContainerNotFound, containerID)
	}

	target := containerList.Items[0]
	name := containerSummaryName(target)
	inspectResult, err := compat.ContainerInspect(ctx, dockerClient, target.ID, client.ContainerInspectOptions{})
	if err != nil {
		item := failedContainerResult(target.ID, name, fmt.Sprintf("inspect failed: %v", err))
		out.Items = append(out.Items, item)
		out.Failed++
		return out, nil
	}
	inspect := inspectResult.Container
	labels := labelsFromInspect(inspect)

	endContainerStatus := s.BeginContainerUpdate(target.ID)
	defer endContainerStatus()
	endProjectStatus := s.BeginProjectUpdate(compose.ProjectLabel(labels))
	defer endProjectStatus()

	if s.config.LabelPolicy.IsUpdateDisabled(labels) {
		item := skippedContainerResult(target.ID, name, "updates disabled by label")
		out.Items = append(out.Items, item)
		out.Checked = 1
		out.Skipped++
		return out, nil
	}
	if s.config.LabelPolicy.IsSwarmTask(labels) && !s.isSelfUpdateCandidate(target.ID, labels) {
		item := skippedContainerResult(target.ID, name, "swarm service; update at the service level")
		out.Items = append(out.Items, item)
		out.Checked = 1
		out.Skipped++
		return out, nil
	}

	configImageRef := ""
	if inspect.Config != nil {
		configImageRef = strings.TrimSpace(inspect.Config.Image)
	}
	imageRef := refs.PullableImageRef(target.Image, configImageRef, nil)
	if imageRef == "" && inspect.Image != "" {
		if imageInspect, inspectErr := dockerClient.ImageInspect(ctx, inspect.Image); inspectErr == nil {
			imageRef = refs.PullableImageRef(target.Image, configImageRef, imageInspect.RepoTags)
		}
	}
	normalizedRef := refs.NormalizeImageUpdateRef(imageRef)
	if normalizedRef == "" {
		item := skippedContainerResult(target.ID, name, "unable to resolve a pullable image reference for container")
		out.Items = append(out.Items, item)
		out.Skipped++
		return out, nil
	}

	if opts.DryRun {
		item := ResourceResult{ResourceID: target.ID, ResourceName: name, ResourceType: ResourceTypeContainer, Status: StatusSkipped, OldImage: inspect.Image, NewImage: normalizedRef}
		out.Items = append(out.Items, item)
		out.Checked = 1
		out.Skipped++
		return out, nil
	}

	if s.config.ImagePuller == nil {
		item := failedContainerResult(target.ID, name, ErrImagePullerRequired.Error())
		out.Items = append(out.Items, item)
		out.Failed++
		return out, nil
	}
	if err := s.config.ImagePuller.PullImage(ctx, normalizedRef, io.Discard); err != nil {
		item := failedContainerResult(target.ID, name, fmt.Sprintf("pull failed: %v", err))
		out.Items = append(out.Items, item)
		out.Failed++
		return out, nil
	}

	changed, compareErr := digestcheck.NewChecker(dockerClient, nil).CompareWithPulled(ctx, inspect.Image, normalizedRef)
	if compareErr == nil && !changed && !opts.Force {
		item := skippedContainerResult(target.ID, name, "image digest unchanged after pull")
		out.Items = append(out.Items, item)
		out.Checked = 1
		out.Skipped++
		s.clearPendingRecord(ctx, normalizedRef)
		return out, nil
	}

	if s.isSelfUpdateCandidate(target.ID, labels) {
		if err := s.triggerSelfUpdate(ctx, target.ID, name, normalizedRef, labels); err != nil {
			item := failedContainerResult(target.ID, name, err.Error())
			out.Items = append(out.Items, item)
			out.Failed++
			return out, nil
		}
		item := updatedContainerResult(target.ID, name, inspect.Image, normalizedRef)
		out.Items = append(out.Items, item)
		out.Checked = 1
		out.Updated++
		s.clearPendingRecord(ctx, normalizedRef)
		return out, nil
	}

	if err := s.updateComposeOrStandalone(ctx, target, inspect, normalizedRef); err != nil {
		item := failedContainerResult(target.ID, name, err.Error())
		out.Items = append(out.Items, item)
		out.Failed++
	} else {
		item := updatedContainerResult(target.ID, name, inspect.Image, normalizedRef)
		out.Items = append(out.Items, item)
		out.Updated++
		_ = s.notify(ctx, target.ID, name, imageRef, inspect.Image, normalizedRef)
		s.clearPendingRecord(ctx, normalizedRef)
	}
	out.Checked = 1
	return out, nil
}

// clearPendingRecord clears the pending update record for an applied
// image ref through the same PendingStore seam ApplyPending uses — a
// single-container update must not leave its "update available" record
// behind, or the update keeps counting as pending. Only records whose
// NewImageRef matches the applied ref are cleared: a tag-update record
// (e.g. nginx:1.27 -> 1.28) stays pending when only the old tag was
// re-pulled. The records are looked up
// and cleared as stored (ID and all) rather than reconstructed, because
// stores key by ID when one is present.
func (s *Service) clearPendingRecord(ctx context.Context, imageRef string) {
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

// updateStandaloneContainer recreates a non-compose container with newRef.
func (s *Service) updateStandaloneContainer(ctx context.Context, cnt container.Summary, inspect container.InspectResponse, newRef string) error {
	labels := labelsFromInspect(inspect)
	if err := s.validateStandaloneContainerUpdate(labels); err != nil {
		return err
	}

	dockerClient, err := s.dockerClient(ctx)
	if err != nil {
		return fmt.Errorf("docker connect: %w", err)
	}

	if err := s.stopAndRemoveStandaloneContainer(ctx, dockerClient, cnt, inspect); err != nil {
		return err
	}
	return s.createStartOrRollback(ctx, dockerClient, cnt, inspect, newRef)
}

func (s *Service) createStartOrRollback(ctx context.Context, dockerClient *client.Client, cnt container.Summary, inspect container.InspectResponse, newRef string) error {
	createdID, err := s.createAndStartStandaloneContainer(ctx, dockerClient, cnt, inspect, newRef)
	if err == nil {
		return nil
	}
	s.removeFailedCreatedContainer(ctx, dockerClient, createdID, containerSummaryName(cnt))
	if rollbackErr := s.rollbackStandaloneContainer(ctx, dockerClient, cnt, inspect); rollbackErr != nil {
		return fmt.Errorf("%w; rollback failed: %w", err, rollbackErr)
	}
	return fmt.Errorf("%w; rollback succeeded", err)
}

func (s *Service) validateStandaloneContainerUpdate(labels map[string]string) error {
	if s.config.LabelPolicy.IsSelfUpdateTarget(labels) {
		return ErrSelfUpdateContainer
	}
	return nil
}

func (s *Service) stopAndRemoveStandaloneContainer(ctx context.Context, dockerClient *client.Client, cnt container.Summary, inspect container.InspectResponse) error {
	labels := labelsFromInspect(inspect)
	name := containerSummaryName(cnt)
	stopOptions := client.ContainerStopOptions{}
	if signal := s.config.LabelPolicy.StopSignal(labels); signal != "" {
		stopOptions.Signal = signal
	}
	stopCtx, cancelStop := s.opCtx(ctx)
	_, err := dockerClient.ContainerStop(stopCtx, cnt.ID, stopOptions)
	cancelStop()
	if err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	_ = s.recordEvent(ctx, "container_stop", cnt.ID, name, map[string]any{"action": "updater_stop"})

	removeCtx, cancelRemove := s.opCtx(ctx)
	_, err = dockerClient.ContainerRemove(removeCtx, cnt.ID, client.ContainerRemoveOptions{})
	cancelRemove()
	if err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	_ = s.recordEvent(ctx, "container_delete", cnt.ID, name, map[string]any{"action": "updater_delete"})
	return nil
}

func (s *Service) createAndStartStandaloneContainer(ctx context.Context, dockerClient *client.Client, cnt container.Summary, inspect container.InspectResponse, newRef string) (string, error) {
	name := containerSummaryName(cnt)
	cfg := inspect.Config
	if cfg == nil {
		cfg = &container.Config{}
	}
	cfg = cloneContainerConfig(cfg)
	cfg.Image = newRef
	if cfg.Labels != nil {
		if _, ok := cfg.Labels["com.docker.compose.image"]; ok {
			if imgInspect, inspectErr := dockerClient.ImageInspect(ctx, newRef); inspectErr == nil {
				cfg.Labels["com.docker.compose.image"] = imgInspect.ID
			}
		}
	}

	hostConfig, err := compat.PrepareRecreateHostConfig(ctx, dockerClient, inspect.HostConfig)
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

	apiVersion := compat.DetectAPIVersion(ctx, dockerClient)
	networkingConfig := buildRecreateNetworkingConfig(networkMode, inspect.NetworkSettings, apiVersion)
	containerName := strings.TrimPrefix(inspect.Name, "/")
	createCtx, cancelCreate := s.opCtx(ctx)
	resp, err := compat.ContainerCreate(createCtx, dockerClient, client.ContainerCreateOptions{
		Config:           cfg,
		HostConfig:       hostConfig,
		NetworkingConfig: networkingConfig,
		Name:             containerName,
	}, apiVersion)
	cancelCreate()
	if err != nil {
		return resp.ID, fmt.Errorf("create: %w", err)
	}
	_ = s.recordEvent(ctx, "container_create", resp.ID, name, map[string]any{"action": "updater_create", "newImageID": resp.ID})

	startCtx, cancelStart := s.opCtx(ctx)
	_, err = dockerClient.ContainerStart(startCtx, resp.ID, client.ContainerStartOptions{})
	cancelStart()
	if err != nil {
		running, inspectErr := containerRunning(ctx, dockerClient, resp.ID)
		if inspectErr != nil {
			return resp.ID, fmt.Errorf("start: %w; inspect after start error: %w", err, inspectErr)
		}
		if !running {
			return resp.ID, fmt.Errorf("start: %w", err)
		}
		s.logger.WarnContext(ctx, "container start returned error but inspect reports running", "containerID", resp.ID, "containerName", name, "error", err)
	}
	_ = s.recordEvent(ctx, "container_start", resp.ID, name, map[string]any{"action": "updater_start"})
	return resp.ID, s.recordEvent(ctx, "container_update", resp.ID, name, map[string]any{"oldContainerID": cnt.ID, "newContainerID": resp.ID, "newImage": newRef})
}

func containerRunning(ctx context.Context, dockerClient *client.Client, containerID string) (bool, error) {
	inspectResult, err := compat.ContainerInspect(ctx, dockerClient, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return false, err
	}
	state := inspectResult.Container.State
	return state != nil && state.Running, nil
}

func (s *Service) removeFailedCreatedContainer(ctx context.Context, dockerClient *client.Client, containerID, containerName string) {
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return
	}
	removeCtx, cancelRemove := s.opCtx(ctx)
	_, err := dockerClient.ContainerRemove(removeCtx, containerID, client.ContainerRemoveOptions{Force: true})
	cancelRemove()
	if err != nil {
		s.logger.WarnContext(ctx, "failed to remove container after unsuccessful recreate", "containerID", containerID, "containerName", containerName, "error", err)
		return
	}
	_ = s.recordEvent(ctx, "container_cleanup", containerID, containerName, map[string]any{"action": "updater_cleanup_failed_create"})
}

func (s *Service) rollbackStandaloneContainer(ctx context.Context, dockerClient *client.Client, cnt container.Summary, inspect container.InspectResponse) error {
	oldImageID := strings.TrimSpace(inspect.Image)
	if oldImageID == "" {
		return errors.New("old image ID unavailable")
	}
	rollbackID, err := s.createAndStartStandaloneContainer(ctx, dockerClient, cnt, inspect, oldImageID)
	if err != nil {
		s.removeFailedCreatedContainer(ctx, dockerClient, rollbackID, containerSummaryName(cnt))
		return err
	}
	_ = s.recordEvent(ctx, "container_rollback", rollbackID, containerSummaryName(cnt), map[string]any{
		"action":         "updater_rollback",
		"oldContainerID": cnt.ID,
		"rollbackImage":  oldImageID,
	})
	return nil
}

func (s *Service) updateComposeOrStandalone(ctx context.Context, target container.Summary, inspect container.InspectResponse, normalizedRef string) error {
	labels := labelsFromInspect(inspect)
	projectName := compose.ProjectLabel(labels)
	serviceName := compose.ServiceLabel(labels)
	if projectName != "" && serviceName != "" && s.config.ProjectUpdater != nil {
		project, err := s.config.ProjectUpdater.ProjectByComposeName(ctx, projectName)
		if err == nil {
			// The compose path was chosen; a failure here must surface instead
			// of falling back, so a partial compose up is never clobbered by a
			// standalone recreate.
			return s.config.ProjectUpdater.UpdateServices(ctx, project.ID, []string{serviceName})
		}
		s.logger.WarnContext(ctx, "compose project not resolved; falling back to standalone container update",
			"container", containerSummaryName(target),
			"project", projectName,
			"service", serviceName,
			"error", err,
		)
	}
	return s.updateStandaloneContainer(ctx, target, inspect, normalizedRef)
}

func normalizedTagsForContainer(ctx context.Context, dockerClient *client.Client, inspect container.InspectResponse) []string {
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

func buildRecreateNetworkingConfig(networkMode container.NetworkMode, settings *container.NetworkSettings, apiVersion string) *network.NetworkingConfig {
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

	sanitized := compat.SanitizeEndpointSettings(rawEndpointsConfig, apiVersion)
	if len(sanitized) == 0 {
		return nil
	}
	return &network.NetworkingConfig{EndpointsConfig: sanitized}
}

// containerSummaryName returns a displayable name for a container:
// Docker's first name, or a short ID when the container has none.
func containerSummaryName(cnt container.Summary) string {
	if len(cnt.Names) > 0 {
		if name := strings.TrimPrefix(cnt.Names[0], "/"); name != "" {
			return name
		}
	}
	if len(cnt.ID) >= 12 {
		return cnt.ID[:12]
	}
	return cnt.ID
}

func cloneContainerConfig(config *container.Config) *container.Config {
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
