package digest

import (
	"testing"

	ocidigest "github.com/opencontainers/go-digest"
)

func TestFromReferenceSuffixInternal(t *testing.T) {
	want := ocidigest.FromString("arcane-reference").String()

	got, ok := FromReferenceSuffix("docker.io/library/nginx@" + want)
	if !ok {
		t.Fatal("FromReferenceSuffix() ok = false, want true")
	}
	if got != want {
		t.Fatalf("digest = %q, want %q", got, want)
	}

	if _, ok := FromReferenceSuffix("docker.io/library/nginx@sha256:bad"); ok {
		t.Fatal("FromReferenceSuffix() ok = true for invalid digest")
	}
}
