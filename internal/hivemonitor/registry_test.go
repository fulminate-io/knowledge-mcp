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
