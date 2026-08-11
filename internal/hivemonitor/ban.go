// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import "sync"

// BanSet enforces the hive session ban CLIENT-SIDE: the daemon refuses a banned
// worker's hive calls before they reach the cloud, so a degenerate (or rogue)
// LLM cannot escape — its own OS gates it.
//
// IDENTITY: the ban is keyed on the HARNESS session-id (the claude transcript's
// filename stem / codex rollout session_meta.id) — the file-sourced,
// LLM-uncontrolled identity from the deterministic transcript binding — NOT the
// daemon-minted, reconnect-volatile Mcp-Session-Id.
// Keying on the harness id gives reconnect-stability: a new Mcp-Session-Id for
// the same CLI session re-resolves (via the Monitor's per-tick RecordResolution)
// to the same harness id and stays banned.
//
// RESOLUTION: InterceptHive only has the Mcp-Session-Id at request time, so the
// BanSet also holds the (Mcp-Session-Id → harness-session-id) map the Monitor
// populates each tick from the transcript binding (RecordResolution — DAEMON
// DERIVED, never agent-supplied). The gate Resolves the request's
// Mcp-Session-Id to a harness id, then checks IsBanned; an unresolved session
// (claim not yet monitored) fails OPEN (a not-yet-bound session cannot have been
// evicted).
//
// Concurrency: a plain sync.Mutex guards both maps (xsync-free client module),
// mirroring the Registry idiom. Every method is O(1) under the lock.
type BanSet struct {
	mu       sync.Mutex
	banned   map[string]bool   // harness-session-id → banned
	resolved map[string]string // mcp-session-id → harness-session-id
}

// NewBanSet returns an empty BanSet ready for use.
func NewBanSet() *BanSet {
	return &BanSet{
		banned:   make(map[string]bool),
		resolved: make(map[string]string),
	}
}

// Ban marks a HARNESS session-id as banned. The supervisor (#3) calls this after
// its eviction decision (it holds the harness id from the claim it judged), and
// the Monitor's cloud-status population calls it for cloud-evicted members.
// nil-safe.
func (b *BanSet) Ban(harnessSessionID string) {
	if b == nil || harnessSessionID == "" {
		return
	}
	b.mu.Lock()
	b.banned[harnessSessionID] = true
	b.mu.Unlock()
}

// Unban clears a harness session-id's ban (a human re-introduction). nil-safe.
func (b *BanSet) Unban(harnessSessionID string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	delete(b.banned, harnessSessionID)
	b.mu.Unlock()
}

// IsBanned reports whether a HARNESS session-id is banned. nil-safe (false).
func (b *BanSet) IsBanned(harnessSessionID string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.banned[harnessSessionID]
}

// RecordResolution records the (Mcp-Session-Id → harness-session-id) mapping the
// Monitor derives from the transcript binding each tick. Implements
// HarnessResolver. A reconnect that mints a new Mcp-Session-Id for the same CLI
// session records a fresh mcp→harness entry pointing at the SAME harness id, so
// the ban survives the reconnect. nil-safe.
func (b *BanSet) RecordResolution(mcpSessionID, harnessSessionID string) {
	if b == nil || mcpSessionID == "" || harnessSessionID == "" {
		return
	}
	b.mu.Lock()
	b.resolved[mcpSessionID] = harnessSessionID
	b.mu.Unlock()
}

// Resolve maps a recorded Mcp-Session-Id to its harness session-id. ok is false
// when the session has not been resolved yet (the gate then fails open). nil-safe.
func (b *BanSet) Resolve(mcpSessionID string) (harnessSessionID string, ok bool) {
	if b == nil {
		return "", false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	h, ok := b.resolved[mcpSessionID]
	return h, ok
}

// IsBannedMcp is the gate's convenience: resolve the Mcp-Session-Id to a harness
// id and report whether that harness id is banned. An unresolved session is NOT
// banned (fail open). nil-safe.
func (b *BanSet) IsBannedMcp(mcpSessionID string) bool {
	harness, ok := b.Resolve(mcpSessionID)
	if !ok {
		return false
	}
	return b.IsBanned(harness)
}
