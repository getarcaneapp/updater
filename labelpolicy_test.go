package updater

import (
	"testing"

	"go.getarcane.app/updater/labels"
)

func TestDefaultLabelPolicy(t *testing.T) {
	policy := DefaultLabelPolicy()

	if !policy.IsSelfUpdateTarget(map[string]string{labels.LabelArcane: "true"}) {
		t.Fatal("Arcane server label was not treated as self-update target")
	}
	if !policy.IsSelfUpdateTarget(map[string]string{labels.LabelArcaneLegacyServer: "true"}) {
		t.Fatal("legacy Arcane server label was not treated as self-update target")
	}
	if !policy.IsSelfUpdateTarget(map[string]string{labels.LabelArcaneAgent: "1"}) {
		t.Fatal("Arcane agent label was not treated as self-update target")
	}
	if !policy.IsServer(map[string]string{labels.LabelArcaneLegacyServer: "true"}) {
		t.Fatal("legacy Arcane server label was not treated as server")
	}
	if policy.IsServer(map[string]string{labels.LabelArcaneLegacyServer: "true", labels.LabelArcaneAgent: "true"}) {
		t.Fatal("agent label did not exclude legacy Arcane server label")
	}
	if !policy.IsUpdateDisabled(map[string]string{labels.LabelUpdater: "off"}) {
		t.Fatal("updater off label was not treated as disabled")
	}
	if policy.IsUpdateDisabled(map[string]string{labels.LabelUpdater: "true"}) {
		t.Fatal("updater true label was treated as disabled")
	}
	if !policy.IsSwarmTask(map[string]string{labels.LabelSwarmServiceID: "svc"}) {
		t.Fatal("swarm service label was not detected")
	}
	if got := policy.StopSignal(map[string]string{labels.LabelStopSignal: " sigint "}); got != "SIGINT" {
		t.Fatalf("StopSignal() = %q, want SIGINT", got)
	}
}

// A caller that overrides one behavior must keep the defaults for the rest;
// the old all-or-nothing merge silently dropped them.
func TestMergeLabelPolicyDefaultsFillsPerField(t *testing.T) {
	custom := func(map[string]string) bool { return true }
	merged := mergeLabelPolicyDefaults(LabelPolicy{IsUpdateDisabledFunc: custom})

	if !merged.IsUpdateDisabled(nil) {
		t.Fatal("caller-provided IsUpdateDisabledFunc was replaced")
	}
	if !merged.IsSelfUpdateTarget(map[string]string{labels.LabelArcane: "true"}) {
		t.Fatal("IsSelfUpdateTargetFunc was not filled in from the defaults")
	}
	if !merged.IsAgent(map[string]string{labels.LabelArcaneAgent: "true"}) {
		t.Fatal("IsAgentFunc was not filled in from the defaults")
	}
	if !merged.IsServer(map[string]string{labels.LabelArcane: "true"}) {
		t.Fatal("IsServerFunc was not filled in from the defaults")
	}
	if !merged.IsSwarmTask(map[string]string{labels.LabelSwarmServiceID: "svc"}) {
		t.Fatal("IsSwarmTaskFunc was not filled in from the defaults")
	}
	if got := merged.StopSignal(map[string]string{labels.LabelStopSignal: "sigterm"}); got != "SIGTERM" {
		t.Fatalf("StopSignalFunc was not filled in from the defaults: StopSignal() = %q", got)
	}
}
