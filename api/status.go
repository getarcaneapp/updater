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
	return s.beginStatusUpdateInternal(containerID, s.updatingContainers)
}

// BeginProjectUpdate marks a project as updating and returns a completion callback.
func (s *Service) BeginProjectUpdate(projectID string) func() {
	return s.beginStatusUpdateInternal(projectID, s.updatingProjects)
}

func (s *Service) beginStatusUpdateInternal(id string, active map[string]bool) func() {
	id = strings.TrimSpace(id)
	if id == "" {
		return func() {}
	}

	s.statusMu.Lock()
	active[id] = true
	s.statusMu.Unlock()

	return func() {
		s.statusMu.Lock()
		delete(active, id)
		s.statusMu.Unlock()
	}
}
