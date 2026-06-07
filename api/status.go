package api

import (
	"slices"
	"strings"

	"go.getarcane.app/updater/types"
)

// Status returns a point-in-time updater status snapshot.
func (s *Service) Status() types.Status {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()

	containerIDs := make([]string, 0, len(s.updatingContainers))
	for id := range s.updatingContainers {
		containerIDs = append(containerIDs, id)
	}
	projectIDs := make([]string, 0, len(s.updatingProjects))
	for id := range s.updatingProjects {
		projectIDs = append(projectIDs, id)
	}
	slices.Sort(containerIDs)
	slices.Sort(projectIDs)

	return types.Status{
		UpdatingContainers: len(s.updatingContainers),
		UpdatingProjects:   len(s.updatingProjects),
		ContainerIDs:       containerIDs,
		ProjectIDs:         projectIDs,
	}
}

// BeginContainerUpdate marks a container as updating and returns a completion callback.
func (s *Service) BeginContainerUpdate(containerID string) func() {
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return func() {}
	}

	s.statusMu.Lock()
	s.updatingContainers[containerID] = true
	s.statusMu.Unlock()

	return func() {
		s.statusMu.Lock()
		delete(s.updatingContainers, containerID)
		s.statusMu.Unlock()
	}
}

// BeginProjectUpdate marks a project as updating and returns a completion callback.
func (s *Service) BeginProjectUpdate(projectID string) func() {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return func() {}
	}

	s.statusMu.Lock()
	s.updatingProjects[projectID] = true
	s.statusMu.Unlock()

	return func() {
		s.statusMu.Lock()
		delete(s.updatingProjects, projectID)
		s.statusMu.Unlock()
	}
}
