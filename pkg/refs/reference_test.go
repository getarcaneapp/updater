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
