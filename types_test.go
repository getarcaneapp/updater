package updater

import (
	"testing"
	"time"
)

// The constant values are a wire contract: hosts persist them, so a changed
// value silently invalidates their stored data.
func TestPublicWireConstants(t *testing.T) {
	resourceTypes := map[string]ResourceType{
		"ResourceTypeImage":     ResourceTypeImage,
		"ResourceTypeContainer": ResourceTypeContainer,
		"ResourceTypeProject":   ResourceTypeProject,
	}
	wantResourceTypes := map[string]string{
		"ResourceTypeImage":     "image",
		"ResourceTypeContainer": "container",
		"ResourceTypeProject":   "project",
	}
	for name, got := range resourceTypes {
		if string(got) != wantResourceTypes[name] {
			t.Fatalf("%s = %q, want %q", name, got, wantResourceTypes[name])
		}
	}

	statuses := map[string]ResourceStatus{
		"StatusChecked":         StatusChecked,
		"StatusUpdated":         StatusUpdated,
		"StatusRestarted":       StatusRestarted,
		"StatusSkipped":         StatusSkipped,
		"StatusFailed":          StatusFailed,
		"StatusUpToDate":        StatusUpToDate,
		"StatusUpdateAvailable": StatusUpdateAvailable,
	}
	wantStatuses := map[string]string{
		"StatusChecked":         "checked",
		"StatusUpdated":         "updated",
		"StatusRestarted":       "restarted",
		"StatusSkipped":         "skipped",
		"StatusFailed":          "failed",
		"StatusUpToDate":        "up_to_date",
		"StatusUpdateAvailable": "update_available",
	}
	for name, got := range statuses {
		if string(got) != wantStatuses[name] {
			t.Fatalf("%s = %q, want %q", name, got, wantStatuses[name])
		}
	}

	updateTypes := map[string]UpdateType{
		"UpdateTypeDigest": UpdateTypeDigest,
		"UpdateTypeTag":    UpdateTypeTag,
	}
	wantUpdateTypes := map[string]string{
		"UpdateTypeDigest": "digest",
		"UpdateTypeTag":    "tag",
	}
	for name, got := range updateTypes {
		if string(got) != wantUpdateTypes[name] {
			t.Fatalf("%s = %q, want %q", name, got, wantUpdateTypes[name])
		}
	}
}

func TestResultDuration(t *testing.T) {
	start := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)

	result := Result{StartTime: start, EndTime: start.Add(90 * time.Second)}
	if got := result.Duration(); got != 90*time.Second {
		t.Fatalf("Duration() = %v, want 90s", got)
	}

	if got := (Result{StartTime: start}).Duration(); got != 0 {
		t.Fatalf("Duration() of an unfinished run = %v, want 0", got)
	}
	if got := (Result{}).Duration(); got != 0 {
		t.Fatalf("Duration() of a zero Result = %v, want 0", got)
	}
}
