// Package registryhost canonicalizes container registry hosts so that image
// references and credential lookups agree on what "the same registry" means.
package registryhost

import (
	"fmt"
	"net/url"
	"strings"

	ref "github.com/distribution/reference"
)

const defaultRegistryDomain = "docker.io"
const defaultRegistryHost = "registry-1.docker.io"

// AuthAddress returns the Docker daemon auth address for an image reference.
func AuthAddress(imageRef string) (string, error) {
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

// Normalize canonicalizes a registry host for equality checks; every Docker Hub
// alias collapses to "docker.io".
func Normalize(rawURL string) string {
	registryHost := strings.ToLower(stripScheme(rawURL))
	if slash := strings.Index(registryHost, "/"); slash != -1 {
		registryHost = registryHost[:slash]
	}
	if registryHost == "docker.io" || registryHost == "registry-1.docker.io" || registryHost == "index.docker.io" {
		return defaultRegistryDomain
	}
	return registryHost
}

func stripScheme(rawURL string) string {
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
