package api

import (
	"go.getarcane.app/updater/types"
	"github.com/moby/moby/api/types/container"
)

func (s *Service) applyResultCountInternal(out *types.Result, item types.ResourceResult) {
	switch item.Status {
	case types.StatusUpdated:
		out.Updated++
	case types.StatusRestarted:
		out.Restarted++
	case types.StatusSkipped:
		out.Skipped++
	case types.StatusFailed:
		out.Failed++
	default:
		out.Checked++
	}
}

func failedContainerResultInternal(id, name, message string) types.ResourceResult {
	return types.ResourceResult{ResourceID: id, ResourceName: name, ResourceType: types.ResourceTypeContainer, Status: types.StatusFailed, Error: message}
}

func skippedContainerResultInternal(id, name, message string) types.ResourceResult {
	return types.ResourceResult{ResourceID: id, ResourceName: name, ResourceType: types.ResourceTypeContainer, Status: types.StatusSkipped, Error: message}
}

func updatedContainerResultInternal(id, name, oldImage, newImage string) types.ResourceResult {
	return types.ResourceResult{
		ResourceID:      id,
		ResourceName:    name,
		ResourceType:    types.ResourceTypeContainer,
		Status:          types.StatusUpdated,
		UpdateAvailable: true,
		UpdateApplied:   true,
		OldImages:       map[string]string{"main": oldImage},
		NewImages:       map[string]string{"main": newImage},
	}
}

func labelsFromInspectInternal(inspect container.InspectResponse) map[string]string {
	if inspect.Config == nil || len(inspect.Config.Labels) == 0 {
		return nil
	}
	return inspect.Config.Labels
}
