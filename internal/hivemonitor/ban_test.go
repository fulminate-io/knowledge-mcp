// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import "testing"

// TestBanSet_RoundTripAndResolve verifies Ban/IsBanned/Unban round-trip on a
// harness session-id, and that Resolve maps a recorded Mcp-Session-Id to its
// harness id.
func TestBanSet_RoundTripAndResolve(t *testing.T) {
	b := NewBanSet()
	const harness = "harness-abc"

	if b.IsBanned(harness) {
		t.Fatal("IsBanned should be false before Ban")
	}
	b.Ban(harness)
	if !b.IsBanned(harness) {
		t.Fatal("IsBanned should be true after Ban")
	}
	b.Unban(harness)
	if b.IsBanned(harness) {
		t.Fatal("IsBanned should be false after Unban")
	}

	// Resolve maps a recorded Mcp-Session-Id to its harness id.
	const mcp = "mcp-sess-1"
	if _, ok := b.Resolve(mcp); ok {
		t.Fatal("Resolve should be !ok before any RecordResolution")
	}
	b.RecordResolution(mcp, harness)
	got, ok := b.Resolve(mcp)
	if !ok || got != harness {
		t.Fatalf("Resolve(%q) = (%q, %v), want (%q, true)", mcp, got, ok, harness)
	}
}

// TestBanSet_IsBannedMcpReconnectStability verifies the gate convenience: a
// banned harness id is reported banned through ANY Mcp-Session-Id that resolves
// to it (reconnect stability), and an unresolved Mcp-Session-Id fails open.
func TestBanSet_IsBannedMcpReconnectStability(t *testing.T) {
	b := NewBanSet()
	const harness = "harness-xyz"
	b.Ban(harness)

	// Unresolved session → fail open (not banned).
	if b.IsBannedMcp("mcp-unknown") {
		t.Fatal("an unresolved Mcp-Session-Id must fail OPEN (not banned)")
	}

	// Original session resolves to the banned harness id → banned.
	b.RecordResolution("mcp-first", harness)
	if !b.IsBannedMcp("mcp-first") {
		t.Fatal("mcp-first resolves to a banned harness id → must be banned")
	}

	// Reconnect mints a NEW Mcp-Session-Id for the SAME CLI session; the Monitor
	// re-resolves it to the SAME harness id → still banned.
	b.RecordResolution("mcp-reconnect", harness)
	if !b.IsBannedMcp("mcp-reconnect") {
		t.Fatal("a new Mcp-Session-Id resolving to the same harness id must STAY banned (reconnect stability)")
	}
}

// TestBanSet_NilSafe asserts nil-receiver methods are no-ops.
func TestBanSet_NilSafe(t *testing.T) {
	var b *BanSet
	b.Ban("h")
	b.Unban("h")
	b.RecordResolution("m", "h")
	if b.IsBanned("h") {
		t.Error("nil BanSet IsBanned should be false")
	}
	if _, ok := b.Resolve("m"); ok {
		t.Error("nil BanSet Resolve should be !ok")
	}
	if b.IsBannedMcp("m") {
		t.Error("nil BanSet IsBannedMcp should be false")
	}
}
