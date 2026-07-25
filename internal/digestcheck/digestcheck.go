// Package digestcheck compares the image digests Docker holds locally with the
// digests a registry reports, so the updater can tell a real update from a
// no-op re-pull.
package digestcheck

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"

	"github.com/moby/moby/client"
	"github.com/samber/hot"
	"go.getarcane.app/updater/digest"
	"go.getarcane.app/updater/refs"
)

// Resolver resolves a remote image digest without pulling. It is declared
// structurally so the root package's public resolver interface satisfies it
// without this package importing the root.
type Resolver interface {
	ImageDigest(ctx context.Context, imageRef string) (string, error)
}

// Checker compares local image digests with remote digests.
type Checker struct {
	dockerClient   *client.Client
	digestResolver Resolver
}

// CheckResult contains the result of a digest check.
type CheckResult struct {
	NeedsUpdate   bool
	LocalDigest   string
	RemoteDigest  string
	Error         error
	CheckedViaAPI bool
}

// NewChecker creates a digest checker. Both arguments are optional; the checks
// that need them report an error instead of panicking when they are absent.
func NewChecker(dockerClient *client.Client, digestResolver Resolver) *Checker {
	return &Checker{dockerClient: dockerClient, digestResolver: digestResolver}
}

// CheckImageNeedsUpdate compares local and remote digests for an image.
func (c *Checker) CheckImageNeedsUpdate(ctx context.Context, imageRef string) CheckResult {
	result := CheckResult{}
	if pinnedDigest, ok := digest.FromReferenceSuffix(imageRef); ok {
		result.LocalDigest = pinnedDigest
		result.RemoteDigest = pinnedDigest
		return result
	}
	if c == nil || c.dockerClient == nil {
		result.Error = errors.New("docker client unavailable")
		return result
	}

	slog.DebugContext(ctx, "CheckImageNeedsUpdate: checking image", "imageRef", imageRef, "normalizedRef", refs.NormalizeImageUpdateRef(imageRef))

	localDigest, err := c.localDigest(ctx, imageRef)
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

	remoteDigest, err := c.digestResolver.ImageDigest(ctx, imageRef)
	if err != nil {
		result.Error = err
		return result
	}

	result.RemoteDigest = remoteDigest
	result.CheckedViaAPI = true
	result.NeedsUpdate = localDigest != remoteDigest
	return result
}

// CheckImageMatchesKnownDigest reports whether the local image already carries
// a digest resolved earlier, avoiding a registry round-trip.
func (c *Checker) CheckImageMatchesKnownDigest(ctx context.Context, imageRef, knownDigest string) CheckResult {
	result := CheckResult{}

	normalizedDigest, err := digest.Normalize(knownDigest)
	if err != nil {
		result.Error = err
		return result
	}
	result.RemoteDigest = normalizedDigest

	if c == nil || c.dockerClient == nil {
		result.Error = errors.New("docker client unavailable")
		return result
	}

	inspect, err := c.dockerClient.ImageInspect(ctx, imageRef)
	if err != nil {
		result.NeedsUpdate = true
		result.Error = err
		return result
	}

	for _, repoDigest := range inspect.RepoDigests {
		localDigest, ok := digest.FromReferenceSuffix(repoDigest)
		if !ok {
			continue
		}
		result.LocalDigest = localDigest
		if localDigest == normalizedDigest {
			return result
		}
	}

	if result.LocalDigest == "" && strings.TrimSpace(inspect.ID) != "" {
		result.LocalDigest = strings.TrimSpace(inspect.ID)
	}
	result.NeedsUpdate = true
	return result
}

// CompareWithPulled compares the current container image ID with a freshly pulled image.
func (c *Checker) CompareWithPulled(ctx context.Context, containerImageID string, newImageRef string) (bool, error) {
	if c == nil || c.dockerClient == nil {
		return false, errors.New("docker client unavailable")
	}
	newInspect, err := c.dockerClient.ImageInspect(ctx, newImageRef)
	if err != nil {
		return false, fmt.Errorf("inspect new image: %w", err)
	}
	return strings.TrimSpace(containerImageID) != strings.TrimSpace(newInspect.ID), nil
}

// GetImageIDsForRef returns local image IDs associated with a reference.
func (c *Checker) GetImageIDsForRef(ctx context.Context, ref string) ([]string, error) {
	if c == nil || c.dockerClient == nil {
		return nil, errors.New("docker client unavailable")
	}

	inspect, err := c.dockerClient.ImageInspect(ctx, ref)
	if err == nil && strings.TrimSpace(inspect.ID) != "" {
		return []string{inspect.ID}, nil
	}

	imageList, err := c.dockerClient.ImageList(ctx, client.ImageListOptions{})
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

// RefIDCache memoizes Checker.GetImageIDsForRef lookups by image reference.
type RefIDCache struct {
	checker *Checker
	ids     *hot.HotCache[string, []string]
}

// NewRefIDCache creates a memoizing image-ID lookup around checker.
func NewRefIDCache(checker *Checker) *RefIDCache {
	return &RefIDCache{
		checker: checker,
		// This cache is a per-scan snapshot; eviction could make repeated
		// lookups for one ref observe different Docker image state.
		ids: hot.NewHotCache[string, []string](hot.LRU, math.MaxInt).
			WithoutLocking().
			Build(),
	}
}

// IDsForRef returns the local image IDs for ref, caching results (including
// failed lookups, cached as nil) for the lifetime of the cache.
func (c *RefIDCache) IDsForRef(ctx context.Context, ref string) []string {
	if ids, ok := c.ids.Peek(ref); ok {
		return ids
	}
	ids, _ := c.checker.GetImageIDsForRef(ctx, ref)
	c.ids.Set(ref, ids)
	return ids
}

func (c *Checker) localDigest(ctx context.Context, imageRef string) (string, error) {
	inspect, err := c.dockerClient.ImageInspect(ctx, imageRef)
	if err != nil {
		return "", fmt.Errorf("image not found locally: %w", err)
	}
	for _, repoDigest := range inspect.RepoDigests {
		if normalized, ok := digest.FromReferenceSuffix(repoDigest); ok {
			return normalized, nil
		}
	}
	if inspect.ID != "" {
		return inspect.ID, nil
	}
	return "", errors.New("no digest available for image")
}
