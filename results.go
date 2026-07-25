package updater

import (
	"time"

	"github.com/moby/moby/api/types/container"
)

func newTimedResult() (*Result, func(*error)) {
	out := &Result{
		Items:     make([]ResourceResult, 0),
		StartTime: time.Now().UTC(),
	}
	return out, func(err *error) {
		out.EndTime = time.Now().UTC()
		out.Success = (err == nil || *err == nil) && out.Failed == 0
	}
}

func (s *Service) applyResultCount(out *Result, item ResourceResult) {
	switch item.Status {
	case StatusUpdated:
		out.Updated++
	case StatusRestarted:
		out.Restarted++
	case StatusSkipped:
		out.Skipped++
	case StatusFailed:
		out.Failed++
	case StatusChecked, StatusUpToDate, StatusUpdateAvailable:
		out.Checked++
	}
}

func failedContainerResult(id, name, message string) ResourceResult {
	return ResourceResult{ResourceID: id, ResourceName: name, ResourceType: ResourceTypeContainer, Status: StatusFailed, Error: message}
}

func skippedContainerResult(id, name, message string) ResourceResult {
	return ResourceResult{ResourceID: id, ResourceName: name, ResourceType: ResourceTypeContainer, Status: StatusSkipped, Error: message}
}

func updatedContainerResult(id, name, oldImage, newImage string) ResourceResult {
	return ResourceResult{
		ResourceID:      id,
		ResourceName:    name,
		ResourceType:    ResourceTypeContainer,
		Status:          StatusUpdated,
		UpdateAvailable: true,
		UpdateApplied:   true,
		OldImage:        oldImage,
		NewImage:        newImage,
	}
}

func labelsFromInspect(inspect container.InspectResponse) map[string]string {
	if inspect.Config == nil || len(inspect.Config.Labels) == 0 {
		return nil
	}
	return inspect.Config.Labels
}
