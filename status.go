package updater

import (
	"slices"
	"strings"
	"sync/atomic"
)

// Status returns a point-in-time updater status snapshot.
func (s *Service) Status() Status {
	containerIDs := statusSnapshot(&s.updatingContainers)
	projectIDs := statusSnapshot(&s.updatingProjects)

	return Status{
		UpdatingContainers: len(containerIDs),
		UpdatingProjects:   len(projectIDs),
		ContainerIDs:       containerIDs,
		ProjectIDs:         projectIDs,
	}
}

// BeginContainerUpdate marks a container as updating and returns a completion callback.
func (s *Service) BeginContainerUpdate(containerID string) func() {
	return s.beginStatusUpdate(containerID, &s.updatingContainers)
}

// BeginProjectUpdate marks a project as updating and returns a completion callback.
func (s *Service) BeginProjectUpdate(projectID string) func() {
	return s.beginStatusUpdate(projectID, &s.updatingProjects)
}

func (s *Service) beginStatusUpdate(id string, active *atomic.Pointer[[]string]) func() {
	id = strings.TrimSpace(id)
	if id == "" {
		return func() {}
	}

	updateStatusSnapshot(active, id, true)
	return func() {
		updateStatusSnapshot(active, id, false)
	}
}

func statusSnapshot(active *atomic.Pointer[[]string]) []string {
	if active == nil {
		return []string{}
	}
	ids := active.Load()
	if ids == nil || len(*ids) == 0 {
		return []string{}
	}
	return slices.Clone(*ids)
}

func updateStatusSnapshot(active *atomic.Pointer[[]string], id string, add bool) {
	if active == nil {
		return
	}
	for {
		currentPtr := active.Load()
		var current []string
		if currentPtr != nil {
			current = *currentPtr
		}

		index, found := slices.BinarySearch(current, id)
		if add {
			if found {
				return
			}
			next := make([]string, 0, len(current)+1)
			next = append(next, current[:index]...)
			next = append(next, id)
			next = append(next, current[index:]...)
			if active.CompareAndSwap(currentPtr, &next) {
				return
			}
			continue
		}

		if !found {
			return
		}
		next := make([]string, 0, len(current)-1)
		next = append(next, current[:index]...)
		next = append(next, current[index+1:]...)
		if active.CompareAndSwap(currentPtr, &next) {
			return
		}
	}
}
