package match

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"go.getarcane.app/updater/pkg/digest"
	"go.getarcane.app/updater/pkg/refs"
	"go.getarcane.app/updater/pkg/utils"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
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
	if newRef, match := resolveImageRefMatchInternal(c.Image, updatedNorm); newRef != "" {
		return newRef, match
	}
	if inspect != nil && inspect.Config != nil {
		if newRef, match := resolveImageRefMatchInternal(inspect.Config.Image, updatedNorm); newRef != "" {
			return newRef, match
		}
	}
	if inspect != nil {
		if newRef, match := resolveImageRefMatchInternal(inspect.Image, updatedNorm); newRef != "" {
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
	return utils.ComposeProjectLabel(c.Labels) != "" || utils.ComposeServiceLabel(c.Labels) != ""
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
func VerifyComposeServiceUpdatedImage(ctx context.Context, dcli *client.Client, projectName, serviceName, oldImageID string) error {
	projectName = strings.TrimSpace(projectName)
	serviceName = strings.TrimSpace(serviceName)
	oldImageID = strings.TrimSpace(oldImageID)
	if dcli == nil || projectName == "" || serviceName == "" || oldImageID == "" {
		return nil
	}

	filters := make(client.Filters)
	filters = filters.Add("label", utils.ComposeProjectLabelKey+"="+projectName)
	filters = filters.Add("label", utils.ComposeServiceLabelKey+"="+serviceName)

	listResult, err := dcli.ContainerList(ctx, client.ContainerListOptions{All: false, Filters: filters})
	if err != nil {
		return fmt.Errorf("verify compose service image: list containers: %w", err)
	}
	if len(listResult.Items) == 0 {
		return fmt.Errorf("compose service %s/%s has no running container after update", projectName, serviceName)
	}

	for _, c := range listResult.Items {
		currentImageID := strings.TrimSpace(c.ImageID)
		if currentImageID == "" {
			inspectResult, inspectErr := utils.ContainerInspectWithCompatibility(ctx, dcli, c.ID, client.ContainerInspectOptions{})
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

func resolveImageRefMatchInternal(imageRef string, updatedNorm map[string]string) (newRef, match string) {
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
