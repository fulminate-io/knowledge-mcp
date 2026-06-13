// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"sort"
	"testing"
)

// TestSessionSnapshots_ReturnsLiveSessions seeds two sessions with distinct
// cwd/pid/comm and asserts SessionSnapshots returns both, each carrying its
// stored ID, Cwd, PID, and Comm.
func TestSessionSnapshots_ReturnsLiveSessions(t *testing.T) {
	h := newTestHTTPServer()

	h.ensureSession("sess-claude", "/Users/jonathan/code/knowledge", 1111, "claude")
	h.ensureSession("sess-codex", "/Users/jonathan/code/agent", 2222, "codex")

	snaps := h.SessionSnapshots()
	if len(snaps) != 2 {
		t.Fatalf("SessionSnapshots returned %d, want 2", len(snaps))
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].ID < snaps[j].ID })

	// sess-claude sorts before sess-codex.
	c := snaps[0]
	if c.ID != "sess-claude" || c.Cwd != "/Users/jonathan/code/knowledge" || c.PID != 1111 || c.Comm != "claude" {
		t.Errorf("claude snapshot = %+v, want {sess-claude, .../knowledge, 1111, claude}", c)
	}
	x := snaps[1]
	if x.ID != "sess-codex" || x.Cwd != "/Users/jonathan/code/agent" || x.PID != 2222 || x.Comm != "codex" {
		t.Errorf("codex snapshot = %+v, want {sess-codex, .../agent, 2222, codex}", x)
	}
}

// TestSessionSnapshots_EmptyIsNil asserts no live sessions yields a nil slice
// (the monitor's range over nil is a no-op tick).
func TestSessionSnapshots_EmptyIsNil(t *testing.T) {
	h := newTestHTTPServer()
	if got := h.SessionSnapshots(); got != nil {
		t.Fatalf("SessionSnapshots with no sessions = %v, want nil", got)
	}
}
