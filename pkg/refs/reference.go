package refs

import (
	"fmt"
	"strings"

	ref "github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	"go.getarcane.app/updater/pkg/utils"
)

// Reference is a normalized Docker image reference.
type Reference struct {
	NormalizedRef string
	RegistryHost  string
	Repository    string
	Tag           string
}

// NormalizeReference parses and canonicalizes an image reference.
func NormalizeReference(imageRef string) (*Reference, error) {
	trimmed := strings.TrimSpace(imageRef)
	if before, _, ok := strings.Cut(trimmed, "@"); ok {
		trimmed = before
	}

	named, err := ref.ParseNormalizedNamed(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid image reference %q: %w", imageRef, err)
	}

	registryHost := utils.NormalizeRegistryForComparison(ref.Domain(named))
	repository := ref.Path(named)

	tag := "latest"
	if tagged, ok := named.(ref.NamedTagged); ok {
		tag = tagged.Tag()
	}

	return &Reference{
		NormalizedRef: registryHost + "/" + repository + ":" + tag,
		RegistryHost:  registryHost,
		Repository:    repository,
		Tag:           tag,
	}, nil
}

// NormalizeImageUpdateRef returns the canonical image reference key used for update match.
func NormalizeImageUpdateRef(imageRef string) string {
	if IsDigestPinnedReference(imageRef) {
		return ""
	}
	parts, err := NormalizeReference(imageRef)
	if err != nil {
		return ""
	}
	return parts.NormalizedRef
}

// IsImageIDLikeReference reports whether ref is a Docker image ID rather than a pullable tag.
func IsImageIDLikeReference(ref string) bool {
	ref = strings.ToLower(strings.TrimSpace(ref))
	return strings.HasPrefix(ref, "sha256:")
}

// IsDigestPinnedReference reports whether ref names an immutable repository digest.
func IsDigestPinnedReference(ref string) bool {
	_, digestValue, ok := strings.Cut(strings.TrimSpace(ref), "@")
	if !ok {
		return false
	}
	_, err := digest.Parse(strings.TrimSpace(digestValue))
	return err == nil
}
