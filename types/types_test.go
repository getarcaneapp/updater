package types

import "testing"

func TestPublicWireConstantsInternal(t *testing.T) {
	tests := map[string]string{
		"ResourceTypeImage":     ResourceTypeImage,
		"ResourceTypeContainer": ResourceTypeContainer,
		"ResourceTypeProject":   ResourceTypeProject,
		"StatusChecked":         StatusChecked,
		"StatusUpdated":         StatusUpdated,
		"StatusRestarted":       StatusRestarted,
		"StatusSkipped":         StatusSkipped,
		"StatusFailed":          StatusFailed,
		"StatusUpToDate":        StatusUpToDate,
		"StatusUpdateAvailable": StatusUpdateAvailable,
	}
	want := map[string]string{
		"ResourceTypeImage":     "image",
		"ResourceTypeContainer": "container",
		"ResourceTypeProject":   "project",
		"StatusChecked":         "checked",
		"StatusUpdated":         "updated",
		"StatusRestarted":       "restarted",
		"StatusSkipped":         "skipped",
		"StatusFailed":          "failed",
		"StatusUpToDate":        "up_to_date",
		"StatusUpdateAvailable": "update_available",
	}

	for name, got := range tests {
		if got != want[name] {
			t.Fatalf("%s = %q, want %q", name, got, want[name])
		}
	}
}
