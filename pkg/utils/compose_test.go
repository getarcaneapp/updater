package utils

import (
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestComposeLabelsInternal(t *testing.T) {
	labels := map[string]string{
		ComposeProjectLabelKey: " app ",
		ComposeServiceLabelKey: "web",
	}
	if got := ComposeProjectLabel(labels); got != "app" {
		t.Fatalf("ComposeProjectLabel() = %q, want app", got)
	}
	if got := ComposeServiceLabel(labels); got != "web" {
		t.Fatalf("ComposeServiceLabel() = %q, want web", got)
	}
}

func TestContainerSummaryNameInternal(t *testing.T) {
	got := ContainerSummaryName(container.Summary{ID: "1234567890abcdef", Names: []string{"/web"}})
	if got != "web" {
		t.Fatalf("ContainerSummaryName() = %q, want web", got)
	}

	got = ContainerSummaryName(container.Summary{ID: "1234567890abcdef"})
	if got != "1234567890ab" {
		t.Fatalf("ContainerSummaryName() fallback = %q, want short id", got)
	}
}
