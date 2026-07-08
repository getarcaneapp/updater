package api

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/moby/moby/client"
	"go.getarcane.app/updater/pkg/utils"
	"go.getarcane.app/updater/types"
)

type dockerComposeProjectUpdater struct {
	dockerClientProvider DockerClientProvider
}

type dockerComposeProjectMetadata struct {
	projectName string
	workingDir  string
	configFiles []string
}

// NewDockerComposeProjectUpdater returns a Docker Compose CLI project updater.
func NewDockerComposeProjectUpdater(provider DockerClientProvider) ProjectUpdater {
	if provider == nil {
		provider = NewDockerClientProvider()
	}
	return dockerComposeProjectUpdater{dockerClientProvider: provider}
}

func (u dockerComposeProjectUpdater) ProjectByComposeName(ctx context.Context, composeName string) (types.ComposeProject, error) {
	metadata, err := u.resolveProjectMetadataInternal(ctx, composeName)
	if err != nil {
		return types.ComposeProject{}, err
	}
	return types.ComposeProject{ID: metadata.projectName, Name: metadata.projectName}, nil
}

func (u dockerComposeProjectUpdater) UpdateServices(ctx context.Context, projectID string, services []string) error {
	metadata, err := u.resolveProjectMetadataInternal(ctx, projectID)
	if err != nil {
		return err
	}
	services = normalizeComposeServicesInternal(services)
	if len(services) == 0 {
		return errors.New("compose update requires at least one service")
	}

	args := []string{"compose", "-p", metadata.projectName}
	for _, configFile := range metadata.configFiles {
		args = append(args, "-f", configFile)
	}
	args = append(args, "up", "-d", "--no-deps", "--force-recreate")
	args = append(args, services...)

	cmd := exec.CommandContext(ctx, "docker", args...)
	if metadata.workingDir != "" {
		cmd.Dir = metadata.workingDir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose update failed: %w: %s", err, truncateComposeOutputInternal(strings.TrimSpace(string(output))))
	}
	return nil
}

func (u dockerComposeProjectUpdater) resolveProjectMetadataInternal(ctx context.Context, composeName string) (dockerComposeProjectMetadata, error) {
	composeName = strings.TrimSpace(composeName)
	if composeName == "" {
		return dockerComposeProjectMetadata{}, errors.New("compose project name is required")
	}

	dockerClient, err := u.dockerClientProvider.DockerClient(ctx)
	if err != nil {
		return dockerComposeProjectMetadata{}, fmt.Errorf("docker connect: %w", err)
	}

	filters := make(client.Filters)
	filters = filters.Add("label", utils.ComposeProjectLabelKey+"="+composeName)
	containers, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return dockerComposeProjectMetadata{}, fmt.Errorf("list compose containers: %w", err)
	}
	if len(containers.Items) == 0 {
		return dockerComposeProjectMetadata{}, fmt.Errorf("compose project not found: %s", composeName)
	}

	for _, summary := range containers.Items {
		if utils.ComposeProjectLabel(summary.Labels) != composeName {
			continue
		}
		return dockerComposeProjectMetadata{
			projectName: composeName,
			workingDir:  utils.ComposeWorkingDirLabel(summary.Labels),
			configFiles: utils.ComposeConfigFilesLabel(summary.Labels),
		}, nil
	}
	return dockerComposeProjectMetadata{}, fmt.Errorf("compose project not found: %s", composeName)
}

func normalizeComposeServicesInternal(services []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(services))
	for _, service := range services {
		service = strings.TrimSpace(service)
		if service == "" {
			continue
		}
		if _, ok := seen[service]; ok {
			continue
		}
		seen[service] = struct{}{}
		out = append(out, service)
	}
	return out
}

func truncateComposeOutputInternal(output string) string {
	const maxComposeErrorOutput = 4096
	output = strings.TrimSpace(output)
	if len(output) <= maxComposeErrorOutput {
		return output
	}
	return output[:maxComposeErrorOutput] + " (truncated)"
}
