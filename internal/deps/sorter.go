package deps

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/container"
	"go.getarcane.app/updater/labels"
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
			if err := s.visit(c, nil); err != nil {
				return nil, err
			}
		}
	}
	return s.sorted, nil
}

// ExtractContainerDeps extracts dependency information from a container inspect
// response. name is the caller's display name for the container, which the
// returned value is keyed by.
func ExtractContainerDeps(ctx context.Context, name string, cnt container.Summary, inspect container.InspectResponse) ContainerWithDeps {
	c := ContainerWithDeps{
		Container: cnt,
		Inspect:   inspect,
		Name:      name,
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

	slog.DebugContext(ctx, "ExtractContainerDeps: extracted dependencies", "container", c.Name, "links", c.Links, "dependsOn", c.DependsOn, "networkDeps", c.NetworkDeps)
	return c
}

// UpdateImplicitRestart adds every container that depends on one already in
// markedForRestart, and returns the names it newly marked. Callers repeat the
// call until it returns nothing to reach the transitive closure.
func UpdateImplicitRestart(containers []ContainerWithDeps, markedForRestart map[string]bool) []string {
	var implicitRestarts []string
	for _, c := range containers {
		if markedForRestart[c.Name] {
			continue
		}
		if !hasMarkedDependency(markedForRestart, c.Links) &&
			!hasMarkedDependency(markedForRestart, c.DependsOn) &&
			!hasMarkedDependency(markedForRestart, c.NetworkDeps) {
			continue
		}
		markedForRestart[c.Name] = true
		implicitRestarts = append(implicitRestarts, c.Name)
	}
	return implicitRestarts
}

func (s *ContainerSorter) visit(c ContainerWithDeps, path []string) error {
	if s.marked[c.Name] {
		cycle := append(slices.Clone(path), c.Name)
		start := slices.Index(cycle, c.Name)
		if start >= 0 {
			cycle = cycle[start:]
		}
		return fmt.Errorf("circular dependency detected: %s", strings.Join(cycle, " -> "))
	}
	if s.visited[c.Name] {
		return nil
	}

	s.marked[c.Name] = true
	defer delete(s.marked, c.Name)
	path = append(path, c.Name)

	for _, depName := range s.getAllDependencies(c) {
		if idx, ok := s.nameToIndex[depName]; ok {
			if err := s.visit(s.containers[idx], path); err != nil {
				return err
			}
		}
	}

	s.visited[c.Name] = true
	s.sorted = append(s.sorted, c)
	return nil
}

func (s *ContainerSorter) getAllDependencies(c ContainerWithDeps) []string {
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

func hasMarkedDependency(markedForRestart map[string]bool, deps []string) bool {
	for _, dep := range deps {
		if markedForRestart[dep] {
			return true
		}
	}
	return false
}
