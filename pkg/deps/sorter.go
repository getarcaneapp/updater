package deps

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/getarcaneapp/updater/pkg/labels"
	"github.com/getarcaneapp/updater/pkg/utils"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// ContainerWithDeps represents a container and its restart dependencies.
type ContainerWithDeps struct {
	Container   container.Summary
	Inspect     container.InspectResponse
	Name        string
	Links       []string
	DependsOn   []string
	NetworkDeps []string
}

// ContainerSorter topologically sorts containers by dependency.
type ContainerSorter struct {
	containers  []ContainerWithDeps
	nameToIndex map[string]int
	visited     map[string]bool
	marked      map[string]bool
	sorted      []ContainerWithDeps
}

// NewContainerSorter creates a sorter for containers.
func NewContainerSorter(containers []ContainerWithDeps) *ContainerSorter {
	nameToIndex := make(map[string]int, len(containers))
	for i, c := range containers {
		nameToIndex[c.Name] = i
	}
	return &ContainerSorter{
		containers:  containers,
		nameToIndex: nameToIndex,
		visited:     make(map[string]bool),
		marked:      make(map[string]bool),
		sorted:      make([]ContainerWithDeps, 0, len(containers)),
	}
}

// Sort returns containers in dependency order.
func (s *ContainerSorter) Sort() ([]ContainerWithDeps, error) {
	for _, c := range s.containers {
		if !s.visited[c.Name] {
			if err := s.visitInternal(c); err != nil {
				return nil, err
			}
		}
	}
	return s.sorted, nil
}

// SortReverse returns containers in reverse dependency order.
func (s *ContainerSorter) SortReverse() ([]ContainerWithDeps, error) {
	sorted, err := s.Sort()
	if err != nil {
		return nil, err
	}
	slices.Reverse(sorted)
	return sorted, nil
}

// ExtractContainerDeps extracts dependency information from a container inspect response.
func ExtractContainerDeps(ctx context.Context, dcli *client.Client, cnt container.Summary, inspect container.InspectResponse) ContainerWithDeps {
	c := ContainerWithDeps{
		Container: cnt,
		Inspect:   inspect,
		Name:      utils.ContainerSummaryName(cnt),
	}

	if inspect.HostConfig != nil {
		for _, link := range inspect.HostConfig.Links {
			parts := strings.SplitN(link, ":", 2)
			if len(parts) > 0 {
				linkName := strings.TrimPrefix(parts[0], "/")
				c.Links = append(c.Links, linkName)
			}
		}
	}

	if inspect.Config != nil && inspect.Config.Labels != nil {
		if deps, ok := inspect.Config.Labels[labels.LabelDependsOn]; ok {
			for dep := range strings.SplitSeq(deps, ",") {
				dep = strings.TrimSpace(dep)
				if dep != "" {
					c.DependsOn = append(c.DependsOn, dep)
				}
			}
		}
	}

	if inspect.HostConfig != nil {
		networkMode := inspect.HostConfig.NetworkMode
		if networkMode.IsContainer() {
			containerRef := strings.TrimPrefix(string(networkMode), "container:")
			c.NetworkDeps = append(c.NetworkDeps, containerRef)
		}
	}

	slog.DebugContext(ctx, "ExtractContainerDeps: extracted dependencies", "container", c.Name, "links", c.Links, "dependsOn", c.DependsOn, "networkDeps", c.NetworkDeps, "dockerClient", dcli != nil)
	return c
}

// UpdateImplicitRestart marks containers that need restart because dependencies restart.
func UpdateImplicitRestart(containers []ContainerWithDeps, markedForRestart map[string]bool) []string {
	var implicitRestarts []string
	for i, c := range containers {
		if markedForRestart[c.Name] {
			continue
		}
		if !hasMarkedDependencyInternal(markedForRestart, c.Links) &&
			!hasMarkedDependencyInternal(markedForRestart, c.DependsOn) &&
			!hasMarkedDependencyInternal(markedForRestart, c.NetworkDeps) {
			continue
		}
		markedForRestart[c.Name] = true
		if containers[i].Container.Labels == nil {
			containers[i].Container.Labels = map[string]string{}
		}
		containers[i].Container.Labels["_arcane_implicit_restart"] = "true"
		implicitRestarts = append(implicitRestarts, c.Name)
	}
	return implicitRestarts
}

func (s *ContainerSorter) visitInternal(c ContainerWithDeps) error {
	if s.marked[c.Name] {
		return fmt.Errorf("circular dependency detected: %s", c.Name)
	}
	if s.visited[c.Name] {
		return nil
	}

	s.marked[c.Name] = true
	defer delete(s.marked, c.Name)

	for _, depName := range s.getAllDependenciesInternal(c) {
		if idx, ok := s.nameToIndex[depName]; ok {
			if err := s.visitInternal(s.containers[idx]); err != nil {
				return err
			}
		}
	}

	s.visited[c.Name] = true
	s.sorted = append(s.sorted, c)
	return nil
}

func (s *ContainerSorter) getAllDependenciesInternal(c ContainerWithDeps) []string {
	seen := make(map[string]struct{})
	var deps []string
	for _, group := range [][]string{c.Links, c.DependsOn, c.NetworkDeps} {
		for _, dep := range group {
			if _, ok := seen[dep]; !ok {
				seen[dep] = struct{}{}
				deps = append(deps, dep)
			}
		}
	}
	return deps
}

func hasMarkedDependencyInternal(markedForRestart map[string]bool, deps []string) bool {
	for _, dep := range deps {
		if markedForRestart[dep] {
			return true
		}
	}
	return false
}
