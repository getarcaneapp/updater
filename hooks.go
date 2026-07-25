package updater

import (
	"context"
	"maps"
	"strings"
)

func (s *Service) triggerSelfUpdate(ctx context.Context, containerID, containerName, newImageRef string, labels map[string]string) error {
	if s.config.SelfUpdater == nil {
		return ErrSelfUpdaterRequired
	}
	instanceType := "server"
	if s.config.LabelPolicy.IsAgent(labels) {
		instanceType = "agent"
	}
	_ = s.recordEvent(ctx, "self_update_trigger", containerID, containerName, map[string]any{
		"instanceType": instanceType,
		"newImage":     newImageRef,
	})
	return s.config.SelfUpdater.TriggerSelfUpdate(ctx, SelfUpdateTarget{
		ContainerID:   containerID,
		ContainerName: containerName,
		InstanceType:  instanceType,
		Labels:        maps.Clone(labels),
		NewImageRef:   newImageRef,
	})
}

// isSelfUpdateCandidate reports whether a container must be handled by
// the host SelfUpdater, either by label policy or because it is the container
// the host application itself runs in.
func (s *Service) isSelfUpdateCandidate(containerID string, labels map[string]string) bool {
	if s.config.LabelPolicy.IsSelfUpdateTarget(labels) {
		return true
	}
	selfID := strings.TrimSpace(s.config.SelfContainerID)
	return selfID != "" && (strings.HasPrefix(containerID, selfID) || strings.HasPrefix(selfID, containerID))
}

func (s *Service) recordResult(ctx context.Context, result ResourceResult) error {
	if s.config.RunRecorder == nil {
		return nil
	}
	return s.config.RunRecorder.RecordUpdateRun(ctx, result)
}

func (s *Service) notify(ctx context.Context, containerID, containerName, imageRef, oldImage, newImage string) error {
	if s.config.Notifier == nil {
		return nil
	}
	return s.config.Notifier.Notify(ctx, Notification{
		ContainerID:   containerID,
		ContainerName: containerName,
		ImageRef:      imageRef,
		OldImage:      oldImage,
		NewImage:      newImage,
	})
}

// recordEvent records a container-scoped update event; every event the
// updater emits today concerns a container, so the resource type is fixed.
func (s *Service) recordEvent(ctx context.Context, phase, resourceID, resourceName string, metadata map[string]any) error {
	if s.config.EventRecorder == nil {
		return nil
	}
	return s.config.EventRecorder.RecordEvent(ctx, Event{
		Phase:        phase,
		Severity:     "info",
		ResourceID:   resourceID,
		ResourceName: resourceName,
		ResourceType: ResourceTypeContainer,
		Metadata:     metadata,
	})
}
