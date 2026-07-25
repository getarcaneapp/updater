package labels

import (
	"errors"
	"testing"
)

func TestShouldDisableArcaneServerRedeploy(t *testing.T) {
	tests := []struct {
		name               string
		labels             map[string]string
		containerID        string
		currentContainerID string
		currentErr         error
		want               bool
	}{
		{
			name:               "current Arcane server container",
			labels:             map[string]string{LabelArcane: "true"},
			containerID:        "abcdef1234567890",
			currentContainerID: "abcdef1234567890",
			want:               true,
		},
		{
			name:               "current legacy Arcane server container",
			labels:             map[string]string{LabelArcaneLegacyServer: "true"},
			containerID:        "abcdef1234567890",
			currentContainerID: "abcdef1234567890",
			want:               true,
		},
		{
			name:               "current Arcane server container with short detected ID",
			labels:             map[string]string{LabelArcane: "true"},
			containerID:        "abcdef1234567890",
			currentContainerID: "abcdef123456",
			want:               true,
		},
		{
			name:               "different Arcane server container",
			labels:             map[string]string{LabelArcane: "true"},
			containerID:        "abcdef1234567890",
			currentContainerID: "123456abcdef7890",
			want:               false,
		},
		{
			name:        "fail closed when current container cannot be detected",
			labels:      map[string]string{LabelArcane: "true"},
			containerID: "abcdef1234567890",
			currentErr:  errors.New("not in docker"),
			want:        true,
		},
		{
			name:               "agent container remains redeployable",
			labels:             map[string]string{LabelArcaneAgent: "true"},
			containerID:        "abcdef1234567890",
			currentContainerID: "abcdef1234567890",
			want:               false,
		},
		{
			name: "agent label excludes Arcane server label",
			labels: map[string]string{
				LabelArcane:      "true",
				LabelArcaneAgent: "true",
			},
			containerID:        "abcdef1234567890",
			currentContainerID: "abcdef1234567890",
			want:               false,
		},
		{
			name:               "non-Arcane container",
			labels:             map[string]string{"app": "demo"},
			containerID:        "abcdef1234567890",
			currentContainerID: "abcdef1234567890",
			want:               false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldDisableArcaneServerRedeploy(tt.labels, tt.containerID, tt.currentContainerID, tt.currentErr)
			if got != tt.want {
				t.Fatalf("ShouldDisableArcaneServerRedeploy() = %v, want %v", got, tt.want)
			}
		})
	}
}
