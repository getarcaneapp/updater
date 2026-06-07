package api

import (
	"context"
	"errors"
	"maps"

	"go.getarcane.app/updater/types"
)

func (s *Service) triggerSelfUpdateInternal(ctx context.Context, containerID, containerName string, labels map[string]string) error {
	if s.config.SelfUpdater == nil {
		return errors.New("self-update requires SelfUpdater")
	}
	instanceType := "server"
	if s.config.LabelPolicy.IsAgent(labels) {
		instanceType = "agent"
	}
	return s.config.SelfUpdater.TriggerSelfUpdate(ctx, types.SelfUpdateTarget{
		ContainerID:   containerID,
		ContainerName: containerName,
		InstanceType:  instanceType,
		Labels:        maps.Clone(labels),
	})
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

func (s *Service) recordEventInternal(ctx context.Context, phase, resourceID, resourceName, resourceType string, metadata map[string]any) error {
	if s.config.EventRecorder == nil {
		return nil
	}
	return s.config.EventRecorder.RecordEvent(ctx, types.Event{
		Phase:        phase,
		Severity:     "info",
		ResourceID:   resourceID,
		ResourceName: resourceName,
		ResourceType: resourceType,
		Metadata:     metadata,
	})
}
