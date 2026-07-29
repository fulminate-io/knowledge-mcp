// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// machineDownThreshold is the staleness window past which a member's last_seen
// heartbeat is treated as machine-down: the worker's machine is unreachable, so
// only a peer can notice (the dead daemon cannot self-report). Roughly 10min —
// far longer than the monitor's 30s tick, so a transient hiccup never trips it.
const machineDownThreshold = 10 * time.Minute

// reaperSweepInterval is the reaper's sweep cadence — deliberately slower than
// the 30s monitor tick: the reaper only needs to detect a stale member within
// about one sweep of crossing the 10min threshold, so a 60s cadence is ample and
// cheap (one member query per live local session for the role gate, plus one per
// reaped hive — every one of them predicate-narrowed).
const reaperSweepInterval = 60 * time.Second

// reaperEvictReason is the terminal DNF reason stamped on a machine-down
// member's in-flight work when the reaper evicts it. The reaper has no transcript
// access (the transcript is on the dead machine), so it conservatively treats the
// loss as could-not-finish and never recycles the work to pending.
const reaperEvictReason = "could not finish (worker lost — machine unreachable)"

// reaperRole is the role string a member must hold for THIS daemon to run the
// stale-member sweep on that member's hive. Cloud is the single source of role
// truth; there is no client-side role record to drift.
const reaperRole = "reaper"

// errReaperAlreadyStarted is returned by Start when the reaper is already running
// (mirrors Monitor's already-started guard).
var errReaperAlreadyStarted = errors.New("hivemonitor: HiveReaper already started")

// ReaperConfig holds the reaper's tunable timings. Defaults come from
// DefaultReaperConfig (sweep 60s, machine-down threshold 10min). Mirrors
// MonitorConfig so the two daemons configure the same way.
type ReaperConfig struct {
	// SweepInterval is the period between stale-member sweeps.
	SweepInterval time.Duration
	// MachineDownThreshold is how stale a member's last_seen may be before the
	// reaper treats it as machine-down and evicts it.
	MachineDownThreshold time.Duration
}

// DefaultReaperConfig is the v1 default cadence: sweep every 60s, treat a member
// as machine-down once its last_seen is older than 10min.
func DefaultReaperConfig() ReaperConfig {
	return ReaperConfig{
		SweepInterval:        reaperSweepInterval,
		MachineDownThreshold: machineDownThreshold,
	}
}

// HiveReaper is the deterministic, client-side, peer-run machine-down sweep. Any
// daemon whose member holds the reaper role periodically scans the hives it
// reaps for members whose last_seen heartbeat is stale past the machine-down
// threshold, and terminally DNFs their in-flight work + evicts them
// (HIVE_OP_EVICT). It is purely a timestamp comparison — no LLM, no transcript
// reading, no recycle-to-pending. ANY/ALL members may hold the reaper role, so
// multiple peer reapers may sweep the same dead member concurrently; every
// operation is idempotent (the server's EvictHiveMember is an unconditional
// member SET plus a status='leased'-guarded work UPDATE), so concurrent sweeps
// net to exactly one terminal-block.
//
// Lifecycle mirrors the Monitor SHAPE (atomic started guard, detached
// context.WithCancel, WaitGroup, ticker); Stop cancels and drains within the
// caller's deadline. It shares the existing HiveCaller seam (Hive for EVICT,
// Execute for the member queries) — no duplicate interface. The struct name is
// HiveReaper to avoid colliding with the unrelated idle-MCP-session reaper in
// the HTTP layer.
type HiveReaper struct {
	hive      HiveCaller
	snapshots func() []SessionSnapshot

	cfg ReaperConfig

	started atomic.Bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewHiveReaper builds a HiveReaper. snapshots supplies the live session set each
// sweep (wired to HTTPServer.SessionSnapshots, same as the Monitor); hive is the
// EVICT/Execute caller (the Router). Mirrors NewMonitor.
func NewHiveReaper(snapshots func() []SessionSnapshot, hive HiveCaller, cfg ReaperConfig) *HiveReaper {
	return &HiveReaper{
		hive:      hive,
		snapshots: snapshots,
		cfg:       cfg,
	}
}

// Start launches the sweep ticker detached from the caller's ctx (a
// request-scoped ctx would prematurely cancel the reaper). Returns an error only
// if already started. Mirrors Monitor.Start.
func (r *HiveReaper) Start(_ context.Context) error {
	if !r.started.CompareAndSwap(false, true) {
		return errReaperAlreadyStarted
	}
	//nolint:gosec // cancel is held on r.cancel and fired by Stop.
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.wg.Add(1)
	go r.run()
	return nil
}

// Stop cancels the reaper and waits up to ctx.Deadline for the loop to drain.
// Idempotent: Stop on a never-started reaper returns nil. Mirrors Monitor.Stop.
func (r *HiveReaper) Stop(ctx context.Context) error {
	if !r.started.Load() {
		return nil
	}
	cancel := r.cancel
	defer cancel()
	cancel()
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		r.started.Store(false)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run is the sweep ticker loop.
func (r *HiveReaper) run() {
	defer r.wg.Done()
	t := time.NewTicker(r.cfg.SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-t.C:
			r.sweep(r.ctx, time.Now())
		}
	}
}

// sweep runs one machine-down pass: resolve the hives THIS daemon reaps (the
// role gate), then for each such hive evict every member whose last_seen is
// stale past the threshold. Exported-internal so tests drive one deterministic
// sweep without the ticker (mirrors Monitor.tick).
func (r *HiveReaper) sweep(ctx context.Context, now time.Time) {
	for _, hive := range r.reaperHives(ctx) {
		r.sweepHive(ctx, hive, now)
	}
}

// reaperHives builds the set of hives THIS daemon must reap — the ROLE GATE. It
// resolves the harness session-id of every live local session, reads THAT
// SESSION's hive_member nodes, and keeps the hive of each returned member that
// holds the reaper role. A daemon none of whose members hold the reaper role
// yields an empty set and sweeps nothing. Cloud is the single source of role
// truth — no client-side role record to drift. Read failures are logged and the
// session is skipped (one unreadable session must not abort the sweep).
//
// The read is one browse PER LIVE LOCAL SESSION, each narrowed by a session
// OP_EQ predicate — memberHivesFor's shape. It is deliberately N small browses
// rather than one: the wire has no OP_IN, and the alternative (a bare whole-type
// hive_member browse) reads every member ever registered in the account, a set
// that only grows because eviction is a status UPDATE and never a delete.
func (r *HiveReaper) reaperHives(ctx context.Context) []string {
	if r.hive == nil || r.snapshots == nil {
		return nil
	}
	var mySessions []string
	seenSession := map[string]bool{}
	for _, snap := range r.snapshots() {
		handle, err := ResolveTranscript(ctx, snap)
		if err != nil {
			slog.Warn("hive reaper: transcript resolve error", "session", snap.ID, "error", err)
			continue
		}
		if handle.Resolved() && !seenSession[handle.HarnessSessionID] {
			seenSession[handle.HarnessSessionID] = true
			mySessions = append(mySessions, handle.HarnessSessionID)
		}
	}

	seen := map[string]bool{}
	var hives []string
	for _, session := range mySessions {
		resp, err := r.hive.Execute(ctx, &knowledgev1.ExecuteRequest{
			Target: &knowledgev1.GraphSelector{Graph: "knowledge"},
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{
					NodeTypes: []string{"hive_member"},
					MetadataPredicates: []*knowledgev1.MetadataPredicate{
						{Key: "session", Op: knowledgev1.MetadataPredicate_OP_EQ, Value: session},
					},
				},
			}},
		})
		if err != nil {
			slog.Warn("hive reaper: read members for role gate failed", "session", session, "error", err)
			continue
		}
		for _, n := range resp.GetNodes() {
			md := n.GetMetadata()
			// The predicate narrows server-side; a server that ignored it must not
			// widen this daemon's reap set, so the session is re-checked here.
			if md["session"] != session || !hasReaperRole(md["roles"]) {
				continue
			}
			hive := md["hive"]
			if hive == "" || seen[hive] {
				continue
			}
			seen[hive] = true
			hives = append(hives, hive)
		}
	}
	return hives
}

// hasReaperRole reports whether the comma-joined roles metadata contains the
// reaper role. Mirrors LookupHiveMember's comma-split of the roles metadata.
func hasReaperRole(roles string) bool {
	if roles == "" {
		return false
	}
	return slices.Contains(strings.Split(roles, ","), reaperRole)
}

// sweepHive evicts every machine-down member of one hive. It reads the hive's
// hive_member nodes, skips already-evicted members (a cheap idempotency
// optimization — NOT the correctness guarantee, which rests on EvictHiveMember's
// idempotency), parses each member's last_seen, and issues one HIVE_OP_EVICT for
// any whose last_seen is older than the machine-down threshold. A member with an
// empty or unparseable last_seen is SKIPPED (missing data never triggers a DNF).
// An evict RPC error is logged and the sweep continues to the next member — one
// dead member must not abort the sweep.
func (r *HiveReaper) sweepHive(ctx context.Context, hive string, now time.Time) {
	resp, err := r.hive.Execute(ctx, &knowledgev1.ExecuteRequest{
		Target: &knowledgev1.GraphSelector{Graph: "knowledge"},
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Selection: &knowledgev1.Selection{
				NodeTypes: []string{"hive_member"},
				MetadataPredicates: []*knowledgev1.MetadataPredicate{
					{Key: "hive", Op: knowledgev1.MetadataPredicate_OP_EQ, Value: hive},
				},
			},
		}},
	})
	if err != nil {
		slog.Warn("hive reaper: read members failed", "hive", hive, "error", err)
		return
	}
	for _, n := range resp.GetNodes() {
		md := n.GetMetadata()
		// Cheap idempotency pre-filter: an already-evicted member is gone, no work.
		if n.GetStatus() == "evicted" || md["status"] == "evicted" {
			continue
		}
		lastSeen, ok := parseLastSeen(md["last_seen"])
		if !ok {
			// Empty/unparseable last_seen → missing data, never a DNF.
			continue
		}
		if now.Sub(lastSeen) <= r.cfg.MachineDownThreshold {
			continue // fresh enough — machine is up.
		}
		r.evict(ctx, hive, md["session"])
	}
}

// parseLastSeen parses a member's last_seen metadata, written by the server as
// RFC3339Nano. Returns ok=false for an empty or unparseable value so the caller
// skips the member rather than evicting on missing data.
func parseLastSeen(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// evict issues one HIVE_OP_EVICT for a machine-down member. MemberSession is the
// STALE member's harness session (the daemon op acts on ANOTHER member, not the
// caller); Reason is the fixed machine-down DNF reason. The server's
// EvictHiveMember terminal-blocks the member's in-flight leased work and marks
// the member evicted — never recycling to pending. An evict error is logged and
// swallowed so the sweep continues.
func (r *HiveReaper) evict(ctx context.Context, hive, memberSession string) {
	if r.hive == nil || memberSession == "" {
		return
	}
	_, err := r.hive.Hive(ctx, &knowledgev1.HiveRequest{
		Op:            knowledgev1.HiveOp_HIVE_OP_EVICT,
		Target:        &knowledgev1.GraphSelector{Graph: "knowledge"},
		Hive:          hive,
		MemberSession: memberSession,
		Reason:        reaperEvictReason,
	})
	if err != nil {
		slog.Warn("hive reaper: evict failed", "hive", hive, "member", memberSession, "error", err)
		return
	}
	slog.Info("hive reaper: evicted machine-down member (terminal DNF, no recycle)",
		"hive", hive, "member", memberSession, "reason", reaperEvictReason)
}
