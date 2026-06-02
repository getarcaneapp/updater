package labels

import "testing"

func TestDefaultLabelPolicyInternal(t *testing.T) {
	policy := DefaultLabelPolicy()

	if !policy.IsSelfUpdateTarget(map[string]string{LabelArcane: "true"}) {
		t.Fatal("Arcane server label was not treated as self-update target")
	}
	if !policy.IsSelfUpdateTarget(map[string]string{LabelArcaneAgent: "1"}) {
		t.Fatal("Arcane agent label was not treated as self-update target")
	}
	if !policy.IsUpdateDisabled(map[string]string{LabelUpdater: "off"}) {
		t.Fatal("updater off label was not treated as disabled")
	}
	if policy.IsUpdateDisabled(map[string]string{LabelUpdater: "true"}) {
		t.Fatal("updater true label was treated as disabled")
	}
	if !policy.IsSwarmTask(map[string]string{LabelSwarmServiceID: "svc"}) {
		t.Fatal("swarm service label was not detected")
	}
	if got := policy.StopSignal(map[string]string{LabelStopSignal: " sigint "}); got != "SIGINT" {
		t.Fatalf("StopSignal() = %q, want SIGINT", got)
	}
}
