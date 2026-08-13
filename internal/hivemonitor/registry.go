// SPDX-License-Identifier: Apache-2.0

// Package hivemonitor is the CLIENT-SIDE daemon monitor for the hive
// work-queue. The agent is deliberately dumb: it claims work over the `hive`
// MCP call and acks/fails it, but it does NOT heartbeat its own liveness or
// reason about whether it is stuck. The daemon (the agent's OS, outside the
// LLM's control) does that work — it binds each MCP session to the claiming
// agent's on-disk transcript, tails that transcript to classify liveness, and
// renews the cloud lease while the worker is genuinely working. This keeps the
// liveness signal OUT of the (possibly degenerate or rogue) LLM's hands.
//
// The package is purely client-side (cmd/knowledge/internal): it reaches the
// cloud only by CALLING the existing Router.Hive RPC seam — it never reimplements
// a cloud op, and it imports no cloud symbols, so the OSS server build stays
// clean.
package hivemonitor

import (
	"sync"
	"time"
)

// Claim is one in-flight work claim held by a session: the claimed message id,
// the hive it belongs to, and when it was claimed. The daemon renews the cloud
// lease for a claim while its claiming session's transcript reads as working,
// and drops the claim from the registry when the agent acks/fails it.
type Claim struct {
	MsgID     string
	Hive      string
	ClaimedAt time.Time
}

// ClaimedSession is one session's claim set, returned by ActiveSessions so the
// monitor can iterate every live claim without holding the registry lock across
// the per-claim transcript work.
type ClaimedSession struct {
	SessionID string
	Claims    []Claim
}

// Registry records two things about each MCP session. The first is what the
// session claimed: the binding between "who claimed" (the session, carried on
// the dispatch ctx and unfakeable by the LLM) and "what they claimed" (the
// message ids the daemon must keep leased while the worker works). The second
// is whether the session is participating in a hive at all — the hive-active
// set — and that is what the daemon's hive loops are gated on: they run while
// at least one session is hive-active and are stopped when the last one ends.
// The activity hook signals only the transitions between those two states —
// empty to non-empty and back — never the repeat marks in between, and its
// consumer is expected to RE-READ HiveActiveCount rather than infer a direction
// from having been called. That re-read is what makes out-of-order delivery of
// two racing transitions safe.
//
// The Registry is IN-MEMORY ONLY and has no load-from-graph entry point, so a
// restarted daemon starts with an empty registry and renews nothing on behalf
// of a previous process. That absence is a property, not a gap: it is what
// makes a crashed member's lease lapse rather than be silently resumed.
//
// Concurrency: a plain sync.Mutex guards both maps (the client module is
// xsync-free — see graphclient/mcp_http.go). Bind/Clear/MarkHiveActive/
// EndHiveSession run on the dispatch and session paths; ActiveSessions/ClaimsFor
// run once per monitor tick and HiveActiveCount once per transition. All are
// O(small) under the lock, so a single mutex is correct and cheap; this mirrors
// httpSession's plain-mutex map idiom (graphclient/http_session.go). The
// activity hook fires OUTSIDE the lock so a slow consumer never blocks a
// concurrent hive call's map update.
type Registry struct {
	mu       sync.Mutex
	sessions map[string][]Claim
	active   map[string]struct{}
	hook     func()
}

// NewRegistry returns an empty Registry ready for use.
func NewRegistry() *Registry {
	return &Registry{
		sessions: make(map[string][]Claim),
		active:   make(map[string]struct{}),
	}
}

// Bind records that sessionID now holds a claim on msgID in hive. A repeat Bind
// of the same (session, msgID) is idempotent — it does not duplicate the claim.
// nil-safe: a nil Registry Bind is a no-op so degraded-mode callers need no
// guard.
func (r *Registry) Bind(sessionID, hive, msgID string) {
	if r == nil || sessionID == "" || msgID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.sessions[sessionID] {
		if c.MsgID == msgID {
			return
		}
	}
	r.sessions[sessionID] = append(r.sessions[sessionID], Claim{
		MsgID:     msgID,
		Hive:      hive,
		ClaimedAt: time.Now(),
	})
}

// Clear removes the claim on msgID held by sessionID. When that was the
// session's last claim, the session entry is dropped entirely so ActiveSessions
// no longer reports it. Clearing an absent claim is a no-op. nil-safe.
func (r *Registry) Clear(sessionID, msgID string) {
	if r == nil || sessionID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	claims := r.sessions[sessionID]
	kept := claims[:0:0]
	for _, c := range claims {
		if c.MsgID != msgID {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		delete(r.sessions, sessionID)
		return
	}
	r.sessions[sessionID] = kept
}

// ActiveSessions returns a snapshot copy of every session that holds at least
// one claim, each with a copy of its claim slice. The copy lets the monitor
// iterate and do per-claim transcript work without holding the lock. nil-safe
// (returns nil).
func (r *Registry) ActiveSessions() []ClaimedSession {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sessions) == 0 {
		return nil
	}
	out := make([]ClaimedSession, 0, len(r.sessions))
	for sid, claims := range r.sessions {
		cp := make([]Claim, len(claims))
		copy(cp, claims)
		out = append(out, ClaimedSession{SessionID: sid, Claims: cp})
	}
	return out
}

// ClaimsFor returns a copy of the claims held by sessionID, or nil when the
// session holds none. nil-safe.
func (r *Registry) ClaimsFor(sessionID string) []Claim {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	claims := r.sessions[sessionID]
	if len(claims) == 0 {
		return nil
	}
	cp := make([]Claim, len(claims))
	copy(cp, claims)
	return cp
}

// SetHiveActivityHook installs the single signal fired when the hive-active set
// changes between empty and non-empty. It is called once at wiring time.
//
// One hook rather than a start/stop pair is load-bearing: the hook fires
// outside the lock, so two transitions racing can deliver their invocations in
// either order, and a start/stop pair would let the losing invocation leave the
// consumer in the state it asked for. A single hook whose consumer re-reads
// HiveActiveCount is self-correcting — whichever invocation runs last observes
// the true count and reconciles to it. nil-safe.
func (r *Registry) SetHiveActivityHook(fn func()) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hook = fn
}

// MarkHiveActive records that sessionID is participating in a hive on this
// daemon. Repeat marks are idempotent — a worker calls hive many times per
// session — and the activity hook fires only on the transition out of an empty
// set, outside the lock. nil-safe.
func (r *Registry) MarkHiveActive(sessionID string) {
	if r == nil || sessionID == "" {
		return
	}
	r.mu.Lock()
	if _, ok := r.active[sessionID]; ok {
		r.mu.Unlock()
		return
	}
	first := len(r.active) == 0
	r.active[sessionID] = struct{}{}
	hook := r.hook
	r.mu.Unlock()

	if first && hook != nil {
		hook()
	}
}

// EndHiveSession records that sessionID's MCP session is gone. It drops the
// session's claims as well as its hive activity: the session can never ack them,
// so their leases must stop being renewed. The activity hook fires only on the
// transition to an empty set, outside the lock. nil-safe.
func (r *Registry) EndHiveSession(sessionID string) {
	if r == nil || sessionID == "" {
		return
	}
	r.mu.Lock()
	if _, ok := r.active[sessionID]; !ok {
		delete(r.sessions, sessionID)
		r.mu.Unlock()
		return
	}
	delete(r.active, sessionID)
	delete(r.sessions, sessionID)
	last := len(r.active) == 0
	hook := r.hook
	r.mu.Unlock()

	if last && hook != nil {
		hook()
	}
}

// HiveActiveCount returns how many sessions are currently hive-active. nil-safe
// (returns 0).
func (r *Registry) HiveActiveCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.active)
}
