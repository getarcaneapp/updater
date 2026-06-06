package digest

import (
	"context"
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

func TestCheckerCheckImageNeedsUpdateSkipsDigestPinnedReferenceInternal(t *testing.T) {
	pinnedDigest := ocidigest.FromString("pinned-newt").String()
	imageRef := "ghcr.io/fosrl/newt@" + pinnedDigest

	got := NewChecker(nil, nil).CheckImageNeedsUpdate(context.Background(), imageRef)

	if got.Error != nil {
		t.Fatalf("CheckImageNeedsUpdate() error = %v, want nil", got.Error)
	}
	if got.NeedsUpdate {
		t.Fatal("CheckImageNeedsUpdate() NeedsUpdate = true, want false")
	}
	if got.CheckedViaAPI {
		t.Fatal("CheckImageNeedsUpdate() CheckedViaAPI = true, want false")
	}
	if got.LocalDigest != pinnedDigest {
		t.Fatalf("CheckImageNeedsUpdate() LocalDigest = %q, want %q", got.LocalDigest, pinnedDigest)
	}
	if got.RemoteDigest != pinnedDigest {
		t.Fatalf("CheckImageNeedsUpdate() RemoteDigest = %q, want %q", got.RemoteDigest, pinnedDigest)
	}
}
