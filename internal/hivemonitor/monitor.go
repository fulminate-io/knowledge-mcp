// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// errMonitorAlreadyStarted is returned by Start when the monitor is already
// running (mirrors FreshnessDaemon's already-started guard).
var errMonitorAlreadyStarted = errors.New("hivemonitor: Monitor already started")

// Monitor heartbeats the cloud lease for each active hive claim while the
// claiming worker is genuinely working, and escalates ambiguity (idle past
// grace) to the supervisor. It ALSO fires a claim-independent machine-health
// heartbeat each tick for every live, non-DEAD member (refreshing its last_seen
// so an alive-but-idle peer is not falsely reaped). It owns NO cloud logic — it
// CALLS the injected HiveCaller (the Router) for HIVE_OP_RENEW, never
// reimplementing the op. The liveness signal comes from the worker's on-disk
// transcript (deterministic, LLM-uncontrolled), bound per-tick via the session
// snapshots + the per-harness resolver.
//
// Lifecycle mirrors the server-side FreshnessDaemon SHAPE (a pattern, not an
// import — FreshnessDaemon is //go:build internal server code): an atomic
// started guard, a detached context.WithCancel, a WaitGroup, and a ticker;
// Stop cancels and drains within the caller's deadline.
type Monitor struct {
	registry  *Registry
	snapshots func() []SessionSnapshot
	hive      HiveCaller
	resolver  HarnessResolver
	escalate  EscalationFunc
	readers   map[TranscriptFormat]TranscriptReader

	cfg MonitorConfig

	// offsets tracks the per-claim transcript follow offset (keyed by msg id)
	// so each tick decodes only appended bytes. Guarded by mu (the loop is
	// single-goroutine, but Stop reads started concurrently).
	mu      sync.Mutex
	offsets map[string]int64
	// idleSince records when a claim was FIRST seen IDLE, so IDLE escalates only
	// after IdleGrace elapses (a momentary between-turns IDLE must not escalate).
	// Cleared when the claim is next seen working.
	idleSince map[string]time.Time
	// escalated records claims already escalated so a claim escalates at most
	// once (and stops renewing thereafter).
	escalated map[string]bool

	started atomic.Bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// HiveCaller is the seam the Monitor calls for both the HIVE_OP_RENEW heartbeat
// and the per-tick read of hive_member status (to populate the local ban set
// from cloud evictions). The *graphclient.Router satisfies both — Hive for the
// renew RPC and Execute for the graph read. The Monitor holds this directly so
// the dependency is explicit and a fake injects in tests.
type HiveCaller interface {
	Hive(ctx context.Context, req *knowledgev1.HiveRequest) (*knowledgev1.HiveResponse, error)
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// HarnessResolver records the (Mcp-Session-Id → harness-session-id) mapping the
// Monitor derives from the transcript binding (so the InterceptHive ban gate can
// translate a request's Mcp-Session-Id to the banned harness id), and Bans a
// harness id when the Monitor reads a cloud-side eviction. The Phase 5 *BanSet
// satisfies both; a fake injects in tests. Both are daemon-side —
// RecordResolution is derived from the deterministic binding, never
// agent-supplied.
type HarnessResolver interface {
	RecordResolution(mcpSessionID, harnessSessionID string)
	Ban(harnessSessionID string)
}

// EscalationFunc is invoked once per claim when the monitor decides the worker
// is no longer making progress (idle past grace, or blocked past the soft
// ceiling). After escalation the monitor stops renewing that claim's lease. The
// supervisor hand-off consumes this callback.
//
// handle is the tick-resolved TranscriptHandle for the claim's session: the
// supervisor needs it to format the worker transcript (FormatTranscript) and to
// act by harness session-id (ack-on-behalf / evict). It is the same handle the
// tick already resolved and passed to handleClaim — no re-resolution.
type EscalationFunc func(claim Claim, sessionID string, state LivenessState, handle TranscriptHandle)

// MonitorConfig holds the tunable intervals. Defaults come from
// DefaultMonitorConfig (renew 30s, idle grace 2min, busy soft ceiling 20min).
type MonitorConfig struct {
	// RenewInterval is the tick period — how often liveness is re-classified
	// and the lease renewed while working.
	RenewInterval time.Duration
	// IdleGrace is how long a claim may read IDLE before the monitor escalates.
	IdleGrace time.Duration
	// BusySoftCeiling is how long a claim may read BLOCKED_ON_TOOL before the
	// monitor escalates (a tool that never returns is itself a stuck signal).
	BusySoftCeiling time.Duration
}

// DefaultMonitorConfig is the v1 default cadence: renew every 30s (the server
// lease TTL is 5min, so a 30s renew has ample margin), escalate after 2min idle,
// soft-ceiling blocked-on-tool at 20min.
func DefaultMonitorConfig() MonitorConfig {
	return MonitorConfig{
		RenewInterval:   30 * time.Second,
		IdleGrace:       2 * time.Minute,
		BusySoftCeiling: 20 * time.Minute,
	}
}

// NewMonitor builds a Monitor. snapshots supplies the live session set each tick
// (wired to HTTPServer.SessionSnapshots in bootstrap to avoid an import cycle);
// hive is the renew caller (Router); resolver records mcp→harness; escalate is
// the supervisor hand-off. A nil escalate is tolerated (escalation becomes a
// log-only no-op). The two transcript readers are registered by format.
func NewMonitor(
	registry *Registry,
	snapshots func() []SessionSnapshot,
	hive HiveCaller,
	resolver HarnessResolver,
	escalate EscalationFunc,
	cfg MonitorConfig,
) *Monitor {
	return &Monitor{
		registry:  registry,
		snapshots: snapshots,
		hive:      hive,
		resolver:  resolver,
		escalate:  escalate,
		readers: map[TranscriptFormat]TranscriptReader{
			FormatClaude: NewClaudeReader(),
			FormatCodex:  NewCodexReader(),
		},
		cfg:       cfg,
		offsets:   map[string]int64{},
		idleSince: map[string]time.Time{},
		escalated: map[string]bool{},
	}
}

// Start launches the heartbeat ticker detached from the caller's ctx (a
// request-scoped ctx would prematurely cancel the monitor). Returns an error
// only if already started. Mirrors FreshnessDaemon.Start.
func (m *Monitor) Start(_ context.Context) error {
	if !m.started.CompareAndSwap(false, true) {
		return errMonitorAlreadyStarted
	}
	//nolint:gosec // cancel is held on m.cancel and fired by Stop.
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.wg.Add(1)
	go m.run()
	return nil
}

// Stop cancels the monitor and waits up to ctx.Deadline for the loop to drain.
// Idempotent: Stop on a never-started monitor returns nil. Mirrors
// FreshnessDaemon.Stop (the deferred cancel guards the WithCancel allocation).
func (m *Monitor) Stop(ctx context.Context) error {
	if !m.started.Load() {
		return nil
	}
	cancel := m.cancel
	defer cancel()
	cancel()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		m.started.Store(false)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run is the ticker loop.
func (m *Monitor) run() {
	defer m.wg.Done()
	t := time.NewTicker(m.cfg.RenewInterval)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
			m.tick(m.ctx, time.Now())
		}
	}
}

// tick runs one heartbeat sweep: for every active claim, bind its session to a
// transcript, record mcp→harness, classify liveness, and renew/escalate.
// Exported-internal so tests drive a single deterministic sweep without the
// ticker.
func (m *Monitor) tick(ctx context.Context, now time.Time) {
	// Index the live snapshots by session id for O(1) per-claim lookup.
	snapByID := map[string]SessionSnapshot{}
	for _, s := range m.snapshots() {
		snapByID[s.ID] = s
	}

	active := m.registry.ActiveSessions()

	// Machine-health heartbeat: refresh last_seen for EVERY live, non-DEAD member
	// (claim-independent), so an alive-but-idle peer is not falsely reaped. Runs
	// before the per-claim loop because it consults the same resolved snapshots.
	m.heartbeatLiveMembers(ctx, snapByID)

	// Populate the ban set from cloud-side evictions so a decision THIS daemon
	// did not make (e.g. a peer reaper's machine-down eviction) still reaches the
	// local harness-keyed gate. Deduped across the active claims' hives.
	m.populateBansFromCloud(ctx, active)

	for _, cs := range active {
		snap, ok := snapByID[cs.SessionID]
		if !ok {
			// The session went away (reconnect mints a new id; the claim's
			// session is stale) — nothing to bind this tick.
			continue
		}
		handle, err := ResolveTranscript(ctx, snap)
		if err != nil {
			slog.Warn("hivemonitor: transcript resolve error", "session", cs.SessionID, "error", err)
			continue
		}
		if !handle.Resolved() {
			// Unresolved is NOT dead — skip this claim this tick (neither renew
			// nor escalate).
			continue
		}
		// Record the (Mcp-Session-Id → harness-session-id) mapping so the
		// InterceptHive ban gate can translate. Daemon-derived, never
		// agent-supplied.
		if m.resolver != nil {
			m.resolver.RecordResolution(cs.SessionID, handle.HarnessSessionID)
		}

		reader, ok := m.readers[handle.Format]
		if !ok {
			continue
		}
		for _, claim := range cs.Claims {
			m.handleClaim(ctx, cs.SessionID, claim, handle, reader, now)
		}
	}
}

// populateBansFromCloud reads hive_member nodes whose status=='evicted' for each
// distinct hive across the active claims, and Bans each evicted member by its
// harness session-id (the member node's `session` metadata). A hive MEMBER IS a
// SESSION and the session-id is its true identity — the OS/file-sourced harness
// id the gate resolves to — so Ban-ing it keys the local set on the same id the
// gate checks. This is how a cloud-side eviction (incl. one another daemon's
// reaper made) reaches THIS daemon's local gate. Read failures are logged and
// skipped — a stale ban set is
// preferable to aborting the heartbeat tick.
func (m *Monitor) populateBansFromCloud(ctx context.Context, active []ClaimedSession) {
	if m.hive == nil || m.resolver == nil {
		return
	}
	seen := map[string]bool{}
	for _, cs := range active {
		for _, claim := range cs.Claims {
			if claim.Hive == "" || seen[claim.Hive] {
				continue
			}
			seen[claim.Hive] = true
			m.banEvictedMembers(ctx, claim.Hive)
		}
	}
}

// banEvictedMembers queries the hive_member nodes of one hive with
// status=='evicted' and Bans each by its `session` metadata (the harness id).
func (m *Monitor) banEvictedMembers(ctx context.Context, hive string) {
	resp, err := m.hive.Execute(ctx, &knowledgev1.ExecuteRequest{
		Target: &knowledgev1.GraphSelector{Graph: "knowledge"},
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Selection: &knowledgev1.Selection{
				NodeTypes: []string{"hive_member"},
				Statuses:  []string{"evicted"},
				MetadataPredicates: []*knowledgev1.MetadataPredicate{
					{Key: "hive", Op: knowledgev1.MetadataPredicate_OP_EQ, Value: hive},
				},
			},
		}},
	})
	if err != nil {
		slog.Warn("hivemonitor: read evicted members failed", "hive", hive, "error", err)
		return
	}
	for _, n := range resp.GetNodes() {
		// The member's harness session-id is the `session` metadata; fall back to
		// the node Status field for the evicted check (the predicate already
		// filtered, but be defensive against a metadata-less node).
		harness := n.GetMetadata()["session"]
		if harness == "" {
			continue
		}
		if n.GetStatus() == "evicted" || n.GetMetadata()["status"] == "evicted" {
			m.resolver.Ban(harness)
		}
	}
}

// handleClaim classifies one claim's transcript and renews or escalates.
func (m *Monitor) handleClaim(
	ctx context.Context,
	sessionID string,
	claim Claim,
	handle TranscriptHandle,
	reader TranscriptReader,
	now time.Time,
) {
	// Already escalated → stop renewing.
	m.mu.Lock()
	if m.escalated[claim.MsgID] {
		m.mu.Unlock()
		return
	}
	prevOffset := m.offsets[claim.MsgID]
	m.mu.Unlock()

	state, newOffset, err := reader.Classify(handle.Path, prevOffset)
	if err != nil {
		slog.Warn("hivemonitor: classify error", "session", sessionID, "msg", claim.MsgID, "error", err)
		return
	}
	m.mu.Lock()
	m.offsets[claim.MsgID] = newOffset
	m.mu.Unlock()

	switch {
	case state == StateDead:
		// Process gone — stop renewing (lease lapses; the peer reaper handles
		// machine-down eviction). No escalation: DEAD is not the supervisor's
		// "stuck-but-alive" case.
		m.clearIdle(claim.MsgID)
		return
	case state.Working() && !m.pastCeiling(claim, state, now):
		// Actively working within budget — renew and reset the idle timer.
		m.clearIdle(claim.MsgID)
		m.renew(ctx, handle.HarnessSessionID, claim.Hive)
	case state == StateIdle:
		// IDLE: escalate only after IdleGrace elapses (a momentary between-turns
		// IDLE is normal). Until then, neither renew nor escalate.
		if m.idlePastGrace(claim.MsgID, now) {
			m.doEscalate(claim, sessionID, state, handle)
		}
	default:
		// BLOCKED_ON_TOOL past the soft ceiling (the only remaining !Working /
		// past-ceiling case) — escalate once and stop renewing.
		m.doEscalate(claim, sessionID, state, handle)
	}
}

// idlePastGrace records the first-seen-idle time for the claim and reports
// whether IdleGrace has elapsed since.
func (m *Monitor) idlePastGrace(msgID string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	first, seen := m.idleSince[msgID]
	if !seen {
		m.idleSince[msgID] = now
		return false
	}
	return now.Sub(first) > m.cfg.IdleGrace
}

// clearIdle resets the claim's idle timer (called when it is seen working/dead).
func (m *Monitor) clearIdle(msgID string) {
	m.mu.Lock()
	delete(m.idleSince, msgID)
	m.mu.Unlock()
}

// ResumeRenew un-escalates a claim so the heartbeat resumes on the next tick. It
// clears BOTH the escalated flag (set by doEscalate, which then stops renewing)
// AND the idle timer (mirroring clearIdle) under m.mu, so the next tick that sees
// the claim working renews again from a clean slate.
//
// This is the conservative cost-asymmetry path: when the supervisor's verdict is
// non-terminal (still working / low confidence / a format-or-judge error), the
// claim must keep its lease alive rather than be reclaimed on uncertainty.
//
// It resets timer/flag state ONLY — it does NOT issue an inline HIVE_OP_RENEW;
// the next tick performs the renew, and an inline renew here would double-renew.
func (m *Monitor) ResumeRenew(msgID string) {
	m.mu.Lock()
	delete(m.escalated, msgID)
	delete(m.idleSince, msgID)
	m.mu.Unlock()
}

// pastCeiling reports whether a working claim has exceeded its time budget for
// the current state: BLOCKED_ON_TOOL past BusySoftCeiling. EXECUTING has no
// ceiling (active production is never escalated). IDLE is handled separately
// with the IdleGrace window.
func (m *Monitor) pastCeiling(claim Claim, state LivenessState, now time.Time) bool {
	if state == StateBlockedOnTool {
		return now.Sub(claim.ClaimedAt) > m.cfg.BusySoftCeiling
	}
	return false
}

// renew issues exactly one HIVE_OP_RENEW for the claim, carrying the HARNESS
// session-id as MemberSession (the daemon-op target — NOT the caller identity).
func (m *Monitor) renew(ctx context.Context, harnessSessionID, hive string) {
	if m.hive == nil || harnessSessionID == "" {
		return
	}
	_, err := m.hive.Hive(ctx, &knowledgev1.HiveRequest{
		Op:            knowledgev1.HiveOp_HIVE_OP_RENEW,
		Target:        &knowledgev1.GraphSelector{Graph: "knowledge"},
		Hive:          hive,
		MemberSession: harnessSessionID,
	})
	if err != nil {
		slog.Warn("hivemonitor: renew failed", "hive", hive, "member", harnessSessionID, "error", err)
	}
}

// doEscalate fires the escalation callback once for the claim and records it so
// no further renew/escalate happens for that claim. handle is the tick-resolved
// transcript binding, forwarded to the callback so the supervisor can format the
// transcript and act by harness session-id.
func (m *Monitor) doEscalate(claim Claim, sessionID string, state LivenessState, handle TranscriptHandle) {
	m.mu.Lock()
	if m.escalated[claim.MsgID] {
		m.mu.Unlock()
		return
	}
	m.escalated[claim.MsgID] = true
	m.mu.Unlock()

	slog.Info("hivemonitor: escalating claim", "session", sessionID, "msg", claim.MsgID, "state", state.String())
	if m.escalate != nil {
		m.escalate(claim, sessionID, state, handle)
	}
}
