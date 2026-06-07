package digest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"go.getarcane.app/updater/pkg/refs"
	"github.com/moby/moby/client"
	ocidigest "github.com/opencontainers/go-digest"
)

// RemoteResolver resolves a remote image digest without pulling.
type RemoteResolver interface {
	GetImageDigest(ctx context.Context, imageRef string) (string, error)
}

// Checker compares local image digests with remote digests.
type Checker struct {
	dcli           *client.Client
	digestResolver RemoteResolver
}

// CheckResult contains the result of a digest check.
type CheckResult struct {
	NeedsUpdate   bool
	LocalDigest   string
	RemoteDigest  string
	Error         error
	CheckedViaAPI bool
}

// NewChecker creates a digest checker.
func NewChecker(dcli *client.Client, digestResolver RemoteResolver) *Checker {
	return &Checker{dcli: dcli, digestResolver: digestResolver}
}

// Normalize parses and canonicalizes an OCI digest.
func Normalize(value string) (string, error) {
	parsed, err := ocidigest.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("invalid OCI digest %q: %w", value, err)
	}
	return parsed.String(), nil
}

// FromReferenceSuffix returns the digest from a name@digest reference.
func FromReferenceSuffix(ref string) (string, bool) {
	_, digestValue, ok := strings.Cut(strings.TrimSpace(ref), "@")
	if !ok {
		return "", false
	}
	normalized, err := Normalize(digestValue)
	if err != nil {
		return "", false
	}
	return normalized, true
}

// CheckImageNeedsUpdate compares local and remote digests for an image.
func (c *Checker) CheckImageNeedsUpdate(ctx context.Context, imageRef string) CheckResult {
	result := CheckResult{}
	if pinnedDigest, ok := FromReferenceSuffix(imageRef); ok {
		result.LocalDigest = pinnedDigest
		result.RemoteDigest = pinnedDigest
		return result
	}
	if c == nil || c.dcli == nil {
		result.Error = errors.New("docker client unavailable")
		return result
	}

	slog.DebugContext(ctx, "CheckImageNeedsUpdate: checking image", "imageRef", imageRef, "normalizedRef", refs.NormalizeImageUpdateRef(imageRef))

	localDigest, err := c.getLocalDigestInternal(ctx, imageRef)
	if err != nil {
		result.NeedsUpdate = true
		result.Error = err
		return result
	}
	result.LocalDigest = localDigest

	if c.digestResolver == nil {
		result.Error = errors.New("remote digest resolver unavailable")
		return result
	}

	remoteDigest, err := c.digestResolver.GetImageDigest(ctx, imageRef)
	if err != nil {
		result.Error = err
		return result
	}

	result.RemoteDigest = remoteDigest
	result.CheckedViaAPI = true
	result.NeedsUpdate = localDigest != remoteDigest
	return result
}

// CompareWithPulled compares the current container image ID with a freshly pulled image.
func (c *Checker) CompareWithPulled(ctx context.Context, containerImageID string, newImageRef string) (bool, error) {
	if c == nil || c.dcli == nil {
		return false, errors.New("docker client unavailable")
	}
	newInspect, err := c.dcli.ImageInspect(ctx, newImageRef)
	if err != nil {
		return false, fmt.Errorf("inspect new image: %w", err)
	}
	return strings.TrimSpace(containerImageID) != strings.TrimSpace(newInspect.ID), nil
}

// GetImageIDsForRef returns local image IDs associated with a reference.
func (c *Checker) GetImageIDsForRef(ctx context.Context, ref string) ([]string, error) {
	if c == nil || c.dcli == nil {
		return nil, errors.New("docker client unavailable")
	}

	inspect, err := c.dcli.ImageInspect(ctx, ref)
	if err == nil && strings.TrimSpace(inspect.ID) != "" {
		return []string{inspect.ID}, nil
	}

	imageList, err := c.dcli.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return nil, err
	}

	normalizedRef := refs.NormalizeImageUpdateRef(ref)
	var ids []string
	for _, img := range imageList.Items {
		for _, tag := range img.RepoTags {
			if refs.NormalizeImageUpdateRef(tag) == normalizedRef {
				ids = append(ids, img.ID)
				break
			}
		}
	}
	return ids, nil
}

func (c *Checker) getLocalDigestInternal(ctx context.Context, imageRef string) (string, error) {
	inspect, err := c.dcli.ImageInspect(ctx, imageRef)
	if err != nil {
		return "", fmt.Errorf("image not found locally: %w", err)
	}
	for _, repoDigest := range inspect.RepoDigests {
		if normalized, ok := FromReferenceSuffix(repoDigest); ok {
			return normalized, nil
		}
	}
	if inspect.ID != "" {
		return inspect.ID, nil
	}
	return "", errors.New("no digest available for image")
}
