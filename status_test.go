package updater

import (
	"slices"
	"sync"
	"testing"
	"time"
)

func TestStatusTracksContainers(t *testing.T) {
	service := newServiceForTest(t, Config{})
	done := service.BeginContainerUpdate("abc")
	status := service.Status()
	if status.UpdatingContainers != 1 || len(status.ContainerIDs) != 1 || status.ContainerIDs[0] != "abc" {
		t.Fatalf("Status() while updating = %#v", status)
	}
	done()
	status = service.Status()
	if status.UpdatingContainers != 0 || len(status.ContainerIDs) != 0 {
		t.Fatalf("Status() after done = %#v", status)
	}
}

func TestStatusSnapshotsRemainConsistentUnderConcurrentUpdates(t *testing.T) {
	service := newServiceForTest(t, Config{})
	containerIDs := []string{"alpha", "beta", "gamma", "delta"}
	projectIDs := []string{"project-a", "project-b", "project-c"}

	stopPolling := make(chan struct{})
	errCh := make(chan string, 1)
	var poller sync.WaitGroup
	poller.Go(func() {
		for {
			select {
			case <-stopPolling:
				return
			default:
			}
			if message := statusConsistencyError(service.Status()); message != "" {
				select {
				case errCh <- message:
				default:
				}
				return
			}
		}
	})

	var workers sync.WaitGroup
	for workerID := range 16 {
		workers.Go(func() {
			for i := range 200 {
				endContainer := service.BeginContainerUpdate(containerIDs[(workerID+i)%len(containerIDs)])
				endProject := service.BeginProjectUpdate(projectIDs[(workerID+i)%len(projectIDs)])
				if message := statusConsistencyError(service.Status()); message != "" {
					select {
					case errCh <- message:
					default:
					}
					return
				}
				time.Sleep(time.Microsecond)
				endProject()
				endContainer()
			}
		})
	}
	workers.Wait()
	close(stopPolling)
	poller.Wait()

	select {
	case message := <-errCh:
		t.Fatal(message)
	default:
	}
}

func statusConsistencyError(status Status) string {
	if status.UpdatingContainers != len(status.ContainerIDs) {
		return "container status count does not match IDs"
	}
	if status.UpdatingProjects != len(status.ProjectIDs) {
		return "project status count does not match IDs"
	}
	if !slices.IsSorted(status.ContainerIDs) {
		return "container status IDs are not sorted"
	}
	if !slices.IsSorted(status.ProjectIDs) {
		return "project status IDs are not sorted"
	}
	if hasDuplicateSortedString(status.ContainerIDs) {
		return "container status IDs contain duplicates"
	}
	if hasDuplicateSortedString(status.ProjectIDs) {
		return "project status IDs contain duplicates"
	}
	return ""
}

func hasDuplicateSortedString(values []string) bool {
	for i := 1; i < len(values); i++ {
		if values[i] == values[i-1] {
			return true
		}
	}
	return false
}
