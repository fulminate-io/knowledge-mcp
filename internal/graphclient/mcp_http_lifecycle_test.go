// SPDX-License-Identifier: Apache-2.0

// mcp_http_lifecycle_test.go — the hive-session lifecycle edges the HTTP
// transport owns: opening an MCP session notifies the hive lifecycle, and both
// teardown paths (DELETE /mcp and the idle reaper) end the hive session that
// session was running. The transport's own tests — routing, per-session cwd,
// cancellation isolation, plain session reaping — and the shared fixtures
// (newTestHTTPServerWithHive, injectPeerCwd, doInitialize) live in
// mcp_http_test.go.

package graphclient

import (
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
)

// TestSessionTeardownEndsHiveSession asserts BOTH MCP-session teardown paths —
// DELETE /mcp and the idle reaper — end the torn-down session's hive session,
// which is what stops the daemon's hive loops once the last one goes. There is no
// hive 'leave' op, so the MCP session going away IS the end of the hive session.
func TestSessionTeardownEndsHiveSession(t *testing.T) {
	t.Run("delete", func(t *testing.T) {
		injectPeerCwd(t, map[int]string{54321: "/Users/jonathan/code/knowledge"})
		reg := hivemonitor.NewRegistry()
		h := newTestHTTPServerWithHive(reg)

		sid := doInitialize(t, h, 54321)
		reg.MarkHiveActive(sid)

		// POSITIVE CONTROL: without it, the post-delete zero cannot be told apart
		// from a session that was never hive-active in the first place.
		if got := reg.HiveActiveCount(); got != 1 {
			t.Fatalf("before DELETE, HiveActiveCount() = %d, want 1", got)
		}

		delReq := httptest.NewRequest("DELETE", "/mcp", nil)
		delReq.Header.Set(mcpSessionHeader, sid)
		delRec := httptest.NewRecorder()
		h.handleDELETE(delRec, delReq)
		if delRec.Code != 204 {
			t.Fatalf("DELETE valid session: HTTP %d, want 204", delRec.Code)
		}
		if got := reg.HiveActiveCount(); got != 0 {
			t.Fatalf("after DELETE, HiveActiveCount() = %d, want 0 — teardown must end the hive session", got)
		}
	})

	t.Run("idle reap", func(t *testing.T) {
		injectPeerCwd(t, map[int]string{
			54321: "/Users/jonathan/code/knowledge",
			54322: "/Users/jonathan/code/agent",
		})
		reg := hivemonitor.NewRegistry()
		h := newTestHTTPServerWithHive(reg)
		h.idleTTL = 100 * time.Millisecond

		stale := doInitialize(t, h, 54321)
		fresh := doInitialize(t, h, 54322)
		reg.MarkHiveActive(stale)
		reg.MarkHiveActive(fresh)
		if got := reg.HiveActiveCount(); got != 2 {
			t.Fatalf("before the reap, HiveActiveCount() = %d, want 2", got)
		}

		now := time.Now()
		if s, ok := h.lookupSession(stale); ok {
			s.touch(now.Add(-time.Hour))
		}
		if s, ok := h.lookupSession(fresh); ok {
			s.touch(now)
		}

		if evicted := h.reapIdle(now); evicted != 1 {
			t.Fatalf("reapIdle evicted %d sessions, want 1", evicted)
		}
		// The concurrently-live hive session must survive: a reaper that ended
		// every hive session indiscriminately would read 0 here.
		if got := reg.HiveActiveCount(); got != 1 {
			t.Fatalf("after the reap, HiveActiveCount() = %d, want 1 (only the reaped session ends)", got)
		}
		// ...and the survivor is the FRESH one, not the stale one: ending fresh
		// empties the set, which it could not do if fresh were already gone.
		reg.EndHiveSession(fresh)
		if got := reg.HiveActiveCount(); got != 0 {
			t.Fatalf("ending the fresh session left HiveActiveCount() = %d, want 0 — the wrong session survived the reap", got)
		}
	})
}

// TestSessionOpenNotifiesHiveLifecycle asserts MCP session establishment
// notifies the hive lifecycle — the trigger the loop controller's post-restart
// re-detection consumes. initialize MINTS a fresh id per call and never reuses a
// client-supplied one, so two initializes must deliver two DISTINCT ids, each
// exactly once (catching both a missing notification and a double one).
//
// The hook body itself calls SessionSnapshots, which is what makes this the
// catcher for firing the notification inside ensureSession: that function holds
// the write lock for its whole body and SessionSnapshots takes the read lock on
// the same non-reentrant RWMutex, so the broken wiring blocks here forever and
// fails by timeout rather than passing green.
func TestSessionOpenNotifiesHiveLifecycle(t *testing.T) {
	injectPeerCwd(t, map[int]string{
		54321: "/Users/jonathan/code/knowledge",
		54322: "/Users/jonathan/code/agent",
	})
	reg := hivemonitor.NewRegistry()
	h := newTestHTTPServerWithHive(reg)

	var mu sync.Mutex
	var opened []string
	var unseen []string
	reg.SetSessionOpenHook(func(sessionID string) {
		snaps := h.SessionSnapshots()
		found := false
		for _, s := range snaps {
			if s.ID == sessionID {
				found = true
				break
			}
		}
		mu.Lock()
		defer mu.Unlock()
		opened = append(opened, sessionID)
		if !found {
			unseen = append(unseen, sessionID)
		}
	})

	sid1 := doInitialize(t, h, 54321)
	sid2 := doInitialize(t, h, 54322)
	if sid1 == sid2 {
		t.Fatal("initialize must mint a distinct session id per call")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(unseen) != 0 {
		t.Fatalf("the session-open hook could not see these sessions via SessionSnapshots: %v", unseen)
	}
	counts := map[string]int{}
	for _, id := range opened {
		counts[id]++
	}
	if counts[sid1] != 1 || counts[sid2] != 1 || len(opened) != 2 {
		t.Fatalf("session-open notifications = %v, want exactly one each for %s and %s", opened, sid1, sid2)
	}
}
