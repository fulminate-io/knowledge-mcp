// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import "testing"

// TestLivenessStates_DistinctNamedValues asserts the four real states are
// distinct named values and that Working() encodes the BLOCKED_ON_TOOL-is-
// working linchpin (EXECUTING + BLOCKED_ON_TOOL work; IDLE + DEAD do not).
func TestLivenessStates_DistinctNamedValues(t *testing.T) {
	states := []LivenessState{StateExecuting, StateBlockedOnTool, StateIdle, StateDead}
	seen := map[LivenessState]bool{}
	for _, s := range states {
		if seen[s] {
			t.Fatalf("duplicate LivenessState value %d (%s)", s, s)
		}
		seen[s] = true
	}
	if len(seen) != 4 {
		t.Fatalf("expected 4 distinct states, got %d", len(seen))
	}

	if !StateExecuting.Working() || !StateBlockedOnTool.Working() {
		t.Error("EXECUTING and BLOCKED_ON_TOOL must both report Working()=true")
	}
	if StateIdle.Working() || StateDead.Working() {
		t.Error("IDLE and DEAD must report Working()=false")
	}

	want := map[LivenessState]string{
		StateExecuting:     "EXECUTING",
		StateBlockedOnTool: "BLOCKED_ON_TOOL",
		StateIdle:          "IDLE",
		StateDead:          "DEAD",
		StateUnknown:       "UNKNOWN",
	}
	for s, name := range want {
		if s.String() != name {
			t.Errorf("%d.String() = %q, want %q", s, s.String(), name)
		}
	}
}
