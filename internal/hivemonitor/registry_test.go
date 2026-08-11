// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"sort"
	"testing"
)

// TestRegistry_BindActiveSessionsClaimsForClear binds two claims to one session
// and one to another, asserts ActiveSessions returns both sessions and
// ClaimsFor returns the right msg_ids, then Clear removes exactly one claim
// leaving the other.
func TestRegistry_BindActiveSessionsClaimsForClear(t *testing.T) {
	r := NewRegistry()

	r.Bind("sessA", "hiveX", "m1")
	r.Bind("sessA", "hiveX", "m2")
	r.Bind("sessB", "hiveY", "m3")

	sessions := r.ActiveSessions()
	if len(sessions) != 2 {
		t.Fatalf("ActiveSessions returned %d sessions, want 2", len(sessions))
	}
	got := map[string][]string{}
	for _, s := range sessions {
		for _, c := range s.Claims {
			got[s.SessionID] = append(got[s.SessionID], c.MsgID)
		}
	}
	for _, ids := range got {
		sort.Strings(ids)
	}
	if a := got["sessA"]; len(a) != 2 || a[0] != "m1" || a[1] != "m2" {
		t.Errorf("sessA claims = %v, want [m1 m2]", a)
	}
	if b := got["sessB"]; len(b) != 1 || b[0] != "m3" {
		t.Errorf("sessB claims = %v, want [m3]", b)
	}

	// ClaimsFor returns the right msg_ids for a single session.
	aClaims := r.ClaimsFor("sessA")
	if len(aClaims) != 2 {
		t.Fatalf("ClaimsFor(sessA) = %d claims, want 2", len(aClaims))
	}
	if aClaims[0].Hive != "hiveX" {
		t.Errorf("ClaimsFor(sessA)[0].Hive = %q, want hiveX", aClaims[0].Hive)
	}

	// Clear removes exactly one claim, leaving the other.
	r.Clear("sessA", "m1")
	after := r.ClaimsFor("sessA")
	if len(after) != 1 || after[0].MsgID != "m2" {
		t.Fatalf("after Clear(sessA,m1): ClaimsFor(sessA) = %v, want [m2]", after)
	}

	// sessB is untouched; both sessions still active.
	if len(r.ActiveSessions()) != 2 {
		t.Fatalf("after clearing one of sessA's two claims, both sessions should remain active")
	}

	// Clearing the last claim drops the session entirely.
	r.Clear("sessA", "m2")
	if got := r.ClaimsFor("sessA"); got != nil {
		t.Errorf("after clearing sessA's last claim, ClaimsFor = %v, want nil", got)
	}
	if len(r.ActiveSessions()) != 1 {
		t.Errorf("after sessA emptied, ActiveSessions should report only sessB")
	}
}

// TestRegistry_BindIdempotentAndNilSafe asserts a repeat Bind of the same
// (session, msgID) does not duplicate, and that nil-Registry methods are no-ops.
func TestRegistry_BindIdempotentAndNilSafe(t *testing.T) {
	r := NewRegistry()
	r.Bind("s", "h", "m")
	r.Bind("s", "h", "m")
	if c := r.ClaimsFor("s"); len(c) != 1 {
		t.Fatalf("duplicate Bind produced %d claims, want 1", len(c))
	}

	var nilReg *Registry
	nilReg.Bind("s", "h", "m") // must not panic
	nilReg.Clear("s", "m")     // must not panic
	if nilReg.ActiveSessions() != nil {
		t.Error("nil Registry ActiveSessions should be nil")
	}
	if nilReg.ClaimsFor("s") != nil {
		t.Error("nil Registry ClaimsFor should be nil")
	}
}

// TestRegistry_HiveSessionLifecycleTransitions asserts the activity hook fires
// exactly once when the hive-active set leaves empty and exactly once when it
// returns to empty — never on the marks and ends between — while
// HiveActiveCount tracks the set throughout.
func TestRegistry_HiveSessionLifecycleTransitions(t *testing.T) {
	r := NewRegistry()
	fired := 0
	r.SetHiveActivityHook(func() { fired++ })

	r.MarkHiveActive("s1")
	if fired != 1 || r.HiveActiveCount() != 1 {
		t.Fatalf("after first MarkHiveActive: fired=%d count=%d, want 1 and 1", fired, r.HiveActiveCount())
	}

	r.MarkHiveActive("s1")
	if fired != 1 || r.HiveActiveCount() != 1 {
		t.Fatalf("repeat MarkHiveActive(s1): fired=%d count=%d, want 1 and 1", fired, r.HiveActiveCount())
	}

	r.MarkHiveActive("s2")
	if fired != 1 || r.HiveActiveCount() != 2 {
		t.Fatalf("MarkHiveActive(s2) is no 0->1 transition: fired=%d count=%d, want 1 and 2", fired, r.HiveActiveCount())
	}

	r.EndHiveSession("s1")
	if fired != 1 || r.HiveActiveCount() != 1 {
		t.Fatalf("EndHiveSession(s1) leaves s2 active: fired=%d count=%d, want 1 and 1", fired, r.HiveActiveCount())
	}

	r.EndHiveSession("s2")
	if fired != 2 || r.HiveActiveCount() != 0 {
		t.Fatalf("EndHiveSession(s2) is the 1->0 transition: fired=%d count=%d, want 2 and 0", fired, r.HiveActiveCount())
	}

	r.EndHiveSession("s2")
	if fired != 2 || r.HiveActiveCount() != 0 {
		t.Fatalf("repeat EndHiveSession(s2): fired=%d count=%d, want 2 and 0", fired, r.HiveActiveCount())
	}
}

// TestRegistry_EndHiveSessionDropsClaims asserts a session ending drops its
// claims as well as its activity — a session that is gone can never ack, so its
// lease must not keep being renewed.
func TestRegistry_EndHiveSessionDropsClaims(t *testing.T) {
	r := NewRegistry()
	r.Bind("s1", "hive1", "msg1")
	r.MarkHiveActive("s1")

	r.EndHiveSession("s1")

	for _, s := range r.ActiveSessions() {
		if s.SessionID == "s1" {
			t.Fatalf("after EndHiveSession(s1), ActiveSessions still reports s1 with claims %v", s.Claims)
		}
	}
	if got := r.ClaimsFor("s1"); got != nil {
		t.Errorf("after EndHiveSession(s1), ClaimsFor(s1) = %v, want nil", got)
	}
}

// TestRegistry_NoteSessionOpenedFiresHook asserts the session-open notification
// delivers each session id to the hook and records nothing in the Registry.
func TestRegistry_NoteSessionOpenedFiresHook(t *testing.T) {
	r := NewRegistry()
	var seen []string
	r.SetSessionOpenHook(func(sessionID string) { seen = append(seen, sessionID) })

	r.NoteSessionOpened("s1")
	r.NoteSessionOpened("s2")
	r.NoteSessionOpened("")

	if len(seen) != 2 || seen[0] != "s1" || seen[1] != "s2" {
		t.Fatalf("session-open hook saw %v, want [s1 s2]", seen)
	}
	if r.HiveActiveCount() != 0 {
		t.Errorf("NoteSessionOpened recorded state: HiveActiveCount = %d, want 0", r.HiveActiveCount())
	}
}

// TestRegistry_HiveActivityNilSafe asserts the hive-activity methods are no-ops
// on a nil Registry, matching the Bind/Clear/ActiveSessions/ClaimsFor contract.
// The positive control comes first so the nil assertions below cannot pass
// against a probe that never worked.
func TestRegistry_HiveActivityNilSafe(t *testing.T) {
	real := NewRegistry()
	real.MarkHiveActive("s")
	if real.HiveActiveCount() != 1 {
		t.Fatalf("positive control: HiveActiveCount = %d after MarkHiveActive, want 1", real.HiveActiveCount())
	}

	var nilReg *Registry
	nilReg.SetHiveActivityHook(func() {})      // must not panic
	nilReg.SetSessionOpenHook(func(string) {}) // must not panic
	nilReg.MarkHiveActive("s")                 // must not panic
	nilReg.EndHiveSession("s")                 // must not panic
	nilReg.NoteSessionOpened("s")              // must not panic
	if nilReg.HiveActiveCount() != 0 {
		t.Errorf("nil Registry HiveActiveCount = %d, want 0", nilReg.HiveActiveCount())
	}
}
