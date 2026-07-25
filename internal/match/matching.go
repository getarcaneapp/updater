package match

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"go.getarcane.app/updater/digest"
	"go.getarcane.app/updater/internal/compat"
	"go.getarcane.app/updater/internal/compose"
	"go.getarcane.app/updater/refs"
)

// AppendImageUpdateRecordIDToOldIDs includes SHA-like update record IDs in the old-image match set.
func AppendImageUpdateRecordIDToOldIDs(oldIDs []string, recordID string) []string {
	recordID = strings.TrimSpace(recordID)
	if !refs.IsImageIDLikeReference(recordID) {
		return oldIDs
	}
	if slices.Contains(oldIDs, recordID) {
		return oldIDs
	}
	return append(oldIDs, recordID)
}

// ResolveContainerImageMatch finds the new image reference for a running container.
func ResolveContainerImageMatch(c container.Summary, inspect *container.InspectResponse, oldIDToNewRef map[string]string, updatedNorm map[string]string) (newRef, match string) {
	if c.ImageID != "" {
		if nr, ok := oldIDToNewRef[c.ImageID]; ok {
			return nr, c.ImageID
		}
	}
	if inspect != nil && inspect.Image != "" {
		if nr, ok := oldIDToNewRef[inspect.Image]; ok {
			return nr, inspect.Image
		}
	}
	if newRef, match := resolveImageRefMatch(c.Image, updatedNorm); newRef != "" {
		return newRef, match
	}
	if inspect != nil && inspect.Config != nil {
		if newRef, match := resolveImageRefMatch(inspect.Config.Image, updatedNorm); newRef != "" {
			return newRef, match
		}
	}
	if inspect != nil {
		if newRef, match := resolveImageRefMatch(inspect.Image, updatedNorm); newRef != "" {
			return newRef, match
		}
	}
	return "", ""
}

// ShouldInspectUnmatchedContainerForImageMatch reports whether inspect may recover a tag match.
func ShouldInspectUnmatchedContainerForImageMatch(c container.Summary) bool {
	imageRef := strings.TrimSpace(c.Image)
	if imageRef == "" || refs.IsImageIDLikeReference(imageRef) {
		return true
	}
	if _, isDigestRef := digest.FromReferenceSuffix(imageRef); !isDigestRef {
		return false
	}
	return compose.ProjectLabel(c.Labels) != "" || compose.ServiceLabel(c.Labels) != ""
}

// CurrentContainerImageID returns the best available image ID for a container.
func CurrentContainerImageID(c container.Summary, inspect *container.InspectResponse) string {
	if imageID := strings.TrimSpace(c.ImageID); imageID != "" {
		return imageID
	}
	if inspect != nil {
		return strings.TrimSpace(inspect.Image)
	}
	return ""
}

// VerifyComposeServiceUpdatedImage verifies that a compose service no longer runs oldImageID.
func VerifyComposeServiceUpdatedImage(ctx context.Context, dockerClient *client.Client, projectName, serviceName, oldImageID string) error {
	projectName = strings.TrimSpace(projectName)
	serviceName = strings.TrimSpace(serviceName)
	oldImageID = strings.TrimSpace(oldImageID)
	if dockerClient == nil || projectName == "" || serviceName == "" || oldImageID == "" {
		return nil
	}

	filters := make(client.Filters)
	filters = filters.Add("label", compose.ProjectLabelKey+"="+projectName)
	filters = filters.Add("label", compose.ServiceLabelKey+"="+serviceName)

	listResult, err := dockerClient.ContainerList(ctx, client.ContainerListOptions{All: false, Filters: filters})
	if err != nil {
		return fmt.Errorf("verify compose service image: list containers: %w", err)
	}
	if len(listResult.Items) == 0 {
		return fmt.Errorf("compose service %s/%s has no running container after update", projectName, serviceName)
	}

	for _, c := range listResult.Items {
		currentImageID := strings.TrimSpace(c.ImageID)
		if currentImageID == "" {
			inspectResult, inspectErr := compat.ContainerInspect(ctx, dockerClient, c.ID, client.ContainerInspectOptions{})
			if inspectErr != nil {
				return fmt.Errorf("verify compose service image: inspect container %s: %w", c.ID, inspectErr)
			}
			currentImageID = strings.TrimSpace(inspectResult.Container.Image)
		}
		if currentImageID == oldImageID {
			return fmt.Errorf("compose service %s/%s still running old image %s after update", projectName, serviceName, oldImageID)
		}
	}
	return nil
}

func resolveImageRefMatch(imageRef string, updatedNorm map[string]string) (newRef, match string) {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" || refs.IsImageIDLikeReference(imageRef) {
		return "", ""
	}
	norm := refs.NormalizeImageUpdateRef(imageRef)
	if norm == "" {
		return "", ""
	}
	if nr, ok := updatedNorm[norm]; ok {
		return nr, norm
	}
	return "", ""
}
