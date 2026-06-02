// Package utils provides shared updater helper utilities.
package utils

import (
	"fmt"
	"sort"
	"strings"

	ref "github.com/distribution/reference"
)

const defaultRegistryDomain = "docker.io"
const defaultRegistryHost = "registry-1.docker.io"

// GetRegistryAddress returns the Docker daemon auth address for an image reference.
func GetRegistryAddress(imageRef string) (string, error) {
	named, err := ref.ParseNormalizedNamed(imageRef)
	if err != nil {
		return "", fmt.Errorf("parse image reference: %w", err)
	}
	addr := ref.Domain(named)
	if addr == defaultRegistryDomain {
		return defaultRegistryHost, nil
	}
	return addr, nil
}

// ExtractRegistryHost extracts the registry host from an image reference.
func ExtractRegistryHost(imageRef string) string {
	if i := strings.IndexByte(imageRef, '@'); i != -1 {
		imageRef = imageRef[:i]
	}

	hostCandidate, _, found := strings.Cut(imageRef, "/")
	if !found {
		return defaultRegistryDomain
	}
	if !strings.Contains(hostCandidate, ".") && !strings.Contains(hostCandidate, ":") {
		return defaultRegistryDomain
	}
	return hostCandidate
}

// NormalizeRegistryForComparison canonicalizes registry hosts for equality checks.
func NormalizeRegistryForComparison(rawURL string) string {
	rawURL = strings.TrimSpace(strings.ToLower(rawURL))
	rawURL = strings.TrimPrefix(rawURL, "https://")
	rawURL = strings.TrimPrefix(rawURL, "http://")
	rawURL = strings.TrimSuffix(rawURL, "/")

	if slash := strings.Index(rawURL, "/"); slash != -1 {
		rawURL = rawURL[:slash]
	}
	if rawURL == "docker.io" || rawURL == "registry-1.docker.io" || rawURL == "index.docker.io" {
		return "docker.io"
	}
	return rawURL
}

// NormalizeRegistryURL canonicalizes registry URLs for Docker auth config lookups.
func NormalizeRegistryURL(rawURL string) string {
	normalized := NormalizeRegistryForComparison(rawURL)
	if normalized == "docker.io" {
		return "https://index.docker.io/v1/"
	}

	result := strings.TrimSpace(rawURL)
	result = strings.TrimPrefix(result, "https://")
	result = strings.TrimPrefix(result, "http://")
	return strings.TrimSuffix(result, "/")
}

// IsRegistryMatch reports whether two registry host values describe the same registry.
func IsRegistryMatch(left, right string) bool {
	return NormalizeRegistryForComparison(left) == NormalizeRegistryForComparison(right)
}

// SortRegistryKeys returns a deterministic copy of registry map keys.
func SortRegistryKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
