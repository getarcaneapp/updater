package api

import (
	"context"
	"errors"
	"maps"
	"strings"

	"go.getarcane.app/updater/types"
)

func (s *Service) triggerSelfUpdateInternal(ctx context.Context, containerID, containerName, newImageRef string, labels map[string]string) error {
	if s.config.SelfUpdater == nil {
		return errors.New("self-update requires SelfUpdater")
	}
	instanceType := "server"
	if s.config.LabelPolicy.IsAgent(labels) {
		instanceType = "agent"
	}
	_ = s.recordEventInternal(ctx, "self_update_trigger", containerID, containerName, map[string]any{
		"instanceType": instanceType,
		"newImage":     newImageRef,
	})
	return s.config.SelfUpdater.TriggerSelfUpdate(ctx, types.SelfUpdateTarget{
		ContainerID:   containerID,
		ContainerName: containerName,
		InstanceType:  instanceType,
		Labels:        maps.Clone(labels),
		NewImageRef:   newImageRef,
	})
}

// isSelfUpdateCandidateInternal reports whether a container must be handled by
// the host SelfUpdater, either by label policy or because it is the container
// the host application itself runs in.
func (s *Service) isSelfUpdateCandidateInternal(containerID string, labels map[string]string) bool {
	if s.config.LabelPolicy.IsSelfUpdateTarget(labels) {
		return true
	}
	selfID := strings.TrimSpace(s.config.SelfContainerID)
	return selfID != "" && (strings.HasPrefix(containerID, selfID) || strings.HasPrefix(selfID, containerID))
}

func (s *Service) recordResultInternal(ctx context.Context, result types.ResourceResult) error {
	if s.config.RunRecorder == nil {
		return nil
	}
	return s.config.RunRecorder.RecordUpdateRun(ctx, result)
}

func (s *Service) notifyInternal(ctx context.Context, containerID, containerName, imageRef, oldImage, newImage string) error {
	if s.config.Notifier == nil {
		return nil
	}
	return s.config.Notifier.Notify(ctx, types.Notification{
		ContainerID:   containerID,
		ContainerName: containerName,
		ImageRef:      imageRef,
		OldImage:      oldImage,
		NewImage:      newImage,
	})
}

// recordEventInternal records a container-scoped update event; every event the
// updater emits today concerns a container, so the resource type is fixed.
func (s *Service) recordEventInternal(ctx context.Context, phase, resourceID, resourceName string, metadata map[string]any) error {
	if s.config.EventRecorder == nil {
		return nil
	}
	return s.config.EventRecorder.RecordEvent(ctx, types.Event{
		Phase:        phase,
		Severity:     "info",
		ResourceID:   resourceID,
		ResourceName: resourceName,
		ResourceType: types.ResourceTypeContainer,
		Metadata:     metadata,
	})
}
