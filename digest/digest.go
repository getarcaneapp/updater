// Package digest parses and canonicalizes the OCI content digests that identify
// an exact image, independent of the tag pointing at it.
package digest

import (
	"fmt"
	"strings"

	oci "github.com/opencontainers/go-digest"
)

// Normalize parses and canonicalizes an OCI digest.
func Normalize(value string) (string, error) {
	parsed, err := oci.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("invalid OCI digest %q: %w", value, err)
	}
	return parsed.String(), nil
}

// FromReferenceSuffix returns the digest from a name@digest reference. It
// reports false when ref carries no digest, or one that does not parse.
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
