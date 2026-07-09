package utils

import (
	"fmt"
	"net/url"
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
	registryHost := strings.ToLower(stripRegistrySchemeInternal(rawURL))
	if slash := strings.Index(registryHost, "/"); slash != -1 {
		registryHost = registryHost[:slash]
	}
	if registryHost == "docker.io" || registryHost == "registry-1.docker.io" || registryHost == "index.docker.io" {
		return "docker.io"
	}
	return registryHost
}

// NormalizeRegistryURL canonicalizes registry URLs for Docker auth config lookups.
func NormalizeRegistryURL(rawURL string) string {
	normalized := NormalizeRegistryForComparison(rawURL)
	if normalized == "docker.io" {
		return "https://index.docker.io/v1/"
	}

	return stripRegistrySchemeInternal(rawURL)
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

func stripRegistrySchemeInternal(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimSuffix(rawURL, "/")
	}

	result := parsed.Host
	if path := parsed.EscapedPath(); path != "" {
		result += path
	}
	if parsed.RawQuery != "" {
		result += "?" + parsed.RawQuery
	}
	return strings.TrimSuffix(result, "/")
}
