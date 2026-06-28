package refs

import "testing"

func TestNormalizeReferenceInternal(t *testing.T) {
	tests := []struct {
		name     string
		imageRef string
		want     string
	}{
		{name: "docker hub official image", imageRef: "nginx", want: "docker.io/library/nginx:latest"},
		{name: "docker registry variant", imageRef: "registry-1.docker.io/library/nginx:1.27", want: "docker.io/library/nginx:1.27"},
		{name: "custom registry", imageRef: "ghcr.io/getarcaneapp/arcane:v2", want: "ghcr.io/getarcaneapp/arcane:v2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeReference(tt.imageRef)
			if err != nil {
				t.Fatalf("NormalizeReference() error = %v", err)
			}
			if got.NormalizedRef != tt.want {
				t.Fatalf("NormalizedRef = %q, want %q", got.NormalizedRef, tt.want)
			}
		})
	}
}

func TestNormalizeImageUpdateRefSkipsDigestPinnedReferencesInternal(t *testing.T) {
	pinnedRef := "nginx@sha256:1111111111111111111111111111111111111111111111111111111111111111"

	if got := NormalizeImageUpdateRef(pinnedRef); got != "" {
		t.Fatalf("NormalizeImageUpdateRef(%q) = %q, want empty for pinned digest ref", pinnedRef, got)
	}
}

func TestIsDigestPinnedReferenceInternal(t *testing.T) {
	tests := []struct {
		name     string
		imageRef string
		want     bool
	}{
		{name: "tagged image", imageRef: "nginx:1.27", want: false},
		{name: "image id", imageRef: "sha256:2222222222222222222222222222222222222222222222222222222222222222", want: false},
		{name: "digest pinned", imageRef: "nginx@sha256:3333333333333333333333333333333333333333333333333333333333333333", want: true},
		{name: "invalid digest suffix", imageRef: "nginx@sha256:not-a-digest", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDigestPinnedReference(tt.imageRef); got != tt.want {
				t.Fatalf("IsDigestPinnedReference(%q) = %v, want %v", tt.imageRef, got, tt.want)
			}
		})
	}
}
