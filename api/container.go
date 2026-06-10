package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

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
	start := time.Now()
	out = &types.Result{Items: []types.ResourceResult{}, StartTime: start.UTC().Format(time.RFC3339)}
	defer func() {
		out.EndTime = time.Now().UTC().Format(time.RFC3339)
		out.Duration = time.Since(start).String()
		out.Success = err == nil && out.Failed == 0
	}()

	dcli, err := s.dockerClientInternal(ctx)
	if err != nil {
		return nil, fmt.Errorf("docker connect: %w", err)
	}

	filters := make(client.Filters)
	filters = filters.Add("id", strings.TrimSpace(containerID))
	containerList, err := dcli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	if len(containerList.Items) == 0 {
		return nil, fmt.Errorf("container not found: %s", containerID)
	}

	target := containerList.Items[0]
	name := utils.ContainerSummaryName(target)
	inspectResult, err := utils.ContainerInspectWithCompatibility(ctx, dcli, target.ID, client.ContainerInspectOptions{})
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
		if imageInspect, inspectErr := dcli.ImageInspect(ctx, inspect.Image); inspectErr == nil {
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

	changed, compareErr := digest.NewChecker(dcli, nil).CompareWithPulled(ctx, inspect.Image, normalizedRef)
	if compareErr == nil && !changed && !opts.Force {
		item := skippedContainerResultInternal(target.ID, name, "image digest unchanged after pull")
		out.Items = append(out.Items, item)
		out.Checked = 1
		out.Skipped++
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
	}
	out.Checked = 1
	return out, nil
}

// UpdateStandaloneContainer recreates a non-compose container with newRef.
func (s *Service) UpdateStandaloneContainer(ctx context.Context, cnt container.Summary, inspect container.InspectResponse, newRef string) error {
	labels := labelsFromInspectInternal(inspect)
	if err := s.validateStandaloneContainerUpdateInternal(labels); err != nil {
		return err
	}

	dcli, err := s.dockerClientInternal(ctx)
	if err != nil {
		return fmt.Errorf("docker connect: %w", err)
	}

	if err := s.stopAndRemoveStandaloneContainerInternal(ctx, dcli, cnt, inspect); err != nil {
		return err
	}
	_, err = s.createAndStartStandaloneContainerInternal(ctx, dcli, cnt, inspect, newRef)
	return err
}

func (s *Service) validateStandaloneContainerUpdateInternal(labels map[string]string) error {
	if utils.ComposeProjectLabel(labels) != "" && utils.ComposeServiceLabel(labels) != "" && !s.config.AllowComposeStandaloneFallback {
		return errors.New("compose container update requires ProjectUpdater unless standalone fallback is enabled")
	}
	if s.config.LabelPolicy.IsSelfUpdateTarget(labels) {
		return errors.New("self-update containers must use SelfUpdater")
	}
	return nil
}

func (s *Service) stopAndRemoveStandaloneContainerInternal(ctx context.Context, dcli *client.Client, cnt container.Summary, inspect container.InspectResponse) error {
	labels := labelsFromInspectInternal(inspect)
	name := utils.ContainerSummaryName(cnt)
	stopOptions := client.ContainerStopOptions{}
	if signal := s.config.LabelPolicy.StopSignal(labels); signal != "" {
		stopOptions.Signal = signal
	}
	if _, err := dcli.ContainerStop(ctx, cnt.ID, stopOptions); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	_ = s.recordEventInternal(ctx, "container_stop", cnt.ID, name, types.ResourceTypeContainer, map[string]any{"action": "updater_stop"})

	if _, err := dcli.ContainerRemove(ctx, cnt.ID, client.ContainerRemoveOptions{}); err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	_ = s.recordEventInternal(ctx, "container_delete", cnt.ID, name, types.ResourceTypeContainer, map[string]any{"action": "updater_delete"})
	return nil
}

func (s *Service) createAndStartStandaloneContainerInternal(ctx context.Context, dcli *client.Client, cnt container.Summary, inspect container.InspectResponse, newRef string) (string, error) {
	name := utils.ContainerSummaryName(cnt)
	cfg := inspect.Config
	if cfg == nil {
		cfg = &container.Config{}
	}
	cfg = cloneContainerConfigInternal(cfg)
	cfg.Image = newRef
	if cfg.Labels != nil {
		if _, ok := cfg.Labels["com.docker.compose.image"]; ok {
			if imgInspect, inspectErr := dcli.ImageInspect(ctx, newRef); inspectErr == nil {
				cfg.Labels["com.docker.compose.image"] = imgInspect.ID
			}
		}
	}

	hostConfig, _, _, err := utils.PrepareRecreateHostConfigForEngine(ctx, dcli, inspect.HostConfig)
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

	apiVersion := utils.DetectAPIVersion(ctx, dcli)
	networkingConfig := buildRecreateNetworkingConfigInternal(networkMode, inspect.NetworkSettings, apiVersion)
	containerName := strings.TrimPrefix(inspect.Name, "/")
	resp, err := utils.ContainerCreateWithCompatibilityForAPIVersion(ctx, dcli, client.ContainerCreateOptions{
		Config:           cfg,
		HostConfig:       hostConfig,
		NetworkingConfig: networkingConfig,
		Name:             containerName,
	}, apiVersion)
	if err != nil {
		return "", fmt.Errorf("create: %w", err)
	}
	_ = s.recordEventInternal(ctx, "container_create", resp.ID, name, types.ResourceTypeContainer, map[string]any{"action": "updater_create", "newImageID": resp.ID})

	if _, err := dcli.ContainerStart(ctx, resp.ID, client.ContainerStartOptions{}); err != nil {
		return resp.ID, fmt.Errorf("start: %w", err)
	}
	_ = s.recordEventInternal(ctx, "container_start", resp.ID, name, types.ResourceTypeContainer, map[string]any{"action": "updater_start"})
	return resp.ID, s.recordEventInternal(ctx, "container_update", resp.ID, name, types.ResourceTypeContainer, map[string]any{"oldContainerID": cnt.ID, "newContainerID": resp.ID, "newImage": newRef})
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
	if projectName != "" && serviceName != "" {
		if s.config.ProjectUpdater == nil {
			if !s.config.AllowComposeStandaloneFallback {
				return errors.New("compose container update requires ProjectUpdater unless standalone fallback is enabled")
			}
		} else {
			project, err := s.config.ProjectUpdater.ProjectByComposeName(ctx, projectName)
			if err != nil {
				if !s.config.AllowComposeStandaloneFallback {
					return fmt.Errorf("resolve compose project: %w", err)
				}
			} else {
				return s.config.ProjectUpdater.UpdateServices(ctx, project.ID, []string{serviceName})
			}
		}
	}
	return s.UpdateStandaloneContainer(ctx, target, inspect, normalizedRef)
}

func normalizedTagsForContainerInternal(ctx context.Context, dcli *client.Client, inspect container.InspectResponse) []string {
	seen := map[string]struct{}{}
	if dcli != nil {
		if imageInspect, err := dcli.ImageInspect(ctx, inspect.Image); err == nil {
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
