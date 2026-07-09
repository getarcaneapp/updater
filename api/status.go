package api

import (
	"slices"
	"strings"
	"sync/atomic"

	"go.getarcane.app/updater/types"
)

// Status returns a point-in-time updater status snapshot.
func (s *Service) Status() types.Status {
	containerIDs := statusSnapshotInternal(&s.updatingContainers)
	projectIDs := statusSnapshotInternal(&s.updatingProjects)

	return types.Status{
		UpdatingContainers: len(containerIDs),
		UpdatingProjects:   len(projectIDs),
		ContainerIDs:       containerIDs,
		ProjectIDs:         projectIDs,
	}
}

// BeginContainerUpdate marks a container as updating and returns a completion callback.
func (s *Service) BeginContainerUpdate(containerID string) func() {
	return s.beginStatusUpdateInternal(containerID, &s.updatingContainers)
}

// BeginProjectUpdate marks a project as updating and returns a completion callback.
func (s *Service) BeginProjectUpdate(projectID string) func() {
	return s.beginStatusUpdateInternal(projectID, &s.updatingProjects)
}

func (s *Service) beginStatusUpdateInternal(id string, active *atomic.Pointer[[]string]) func() {
	id = strings.TrimSpace(id)
	if id == "" {
		return func() {}
	}

	updateStatusSnapshotInternal(active, id, true)
	return func() {
		updateStatusSnapshotInternal(active, id, false)
	}
}

func statusSnapshotInternal(active *atomic.Pointer[[]string]) []string {
	if active == nil {
		return []string{}
	}
	ids := active.Load()
	if ids == nil || len(*ids) == 0 {
		return []string{}
	}
	return slices.Clone(*ids)
}

func updateStatusSnapshotInternal(active *atomic.Pointer[[]string], id string, add bool) {
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
