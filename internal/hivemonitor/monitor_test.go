// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// fakeHive records every HIVE_OP_RENEW it receives and serves a scripted
// Execute response (the hive_member status read). It also records every
// ExecuteRequest so tests can assert the READ PLAN, not just its effects —
// without that recording a member browse could lose its narrowing predicate
// and every behavioral test would stay green.
type fakeHive struct {
	mu       sync.Mutex
	reqs     []*knowledgev1.HiveRequest
	execReqs []*knowledgev1.ExecuteRequest
	execResp *knowledgev1.ExecuteResponse
}

func (f *fakeHive) Hive(_ context.Context, req *knowledgev1.HiveRequest) (*knowledgev1.HiveResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	return &knowledgev1.HiveResponse{AffectedCount: 1}, nil
}

func (f *fakeHive) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execReqs = append(f.execReqs, req)
	if f.execResp != nil {
		return f.execResp, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

func (f *fakeHive) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func (f *fakeHive) last() *knowledgev1.HiveRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		return nil
	}
	return f.reqs[len(f.reqs)-1]
}

func (f *fakeHive) all() []*knowledgev1.HiveRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*knowledgev1.HiveRequest, len(f.reqs))
	copy(out, f.reqs)
	return out
}

// fakeResolver records mcp→harness resolutions and bans.
type fakeResolver struct {
	mu     sync.Mutex
	rec    map[string]string
	banned map[string]bool
}

func newFakeResolver() *fakeResolver {
	return &fakeResolver{rec: map[string]string{}, banned: map[string]bool{}}
}

func (r *fakeResolver) RecordResolution(mcp, harness string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec[mcp] = harness
}

func (r *fakeResolver) Ban(harness string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.banned[harness] = true
}

func (r *fakeResolver) get(mcp string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec[mcp]
}

// claudeBindingHarness sets up a temp HOME holding the one claude transcript
// for the snapshot's cwd, whose final conversation line is `tailLine`, so
// ResolveTranscript binds the session off disk. Returns the harness session id
// (the transcript's filename stem) and the snapshot.
func claudeBindingHarness(t *testing.T, tailLine string) (string, SessionSnapshot) {
	t.Helper()
	home := t.TempDir()
	origHome := homeDir
	t.Cleanup(func() { homeDir = origHome })
	homeDir = func() (string, error) { return home, nil }

	const (
		cwd       = "/Users/jonathan/code/knowledge"
		harnessID = "harness-50fc2d24"
		pid       = 4242
	)
	projDir := filepath.Join(home, ".claude", "projects", "-Users-jonathan-code-knowledge")
	if err := os.MkdirAll(projDir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projDir, harnessID+".jsonl")
	body := claudeUserToolResult("toolu_prev") + "\n" + tailLine + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return harnessID, SessionSnapshot{ID: "mcp-sess", Cwd: cwd, PID: pid, Comm: "claude"}
}

// TestMonitor_ExecutingRenewsAndRecordsHarness verifies: one EXECUTING tick
// issues exactly one HIVE_OP_RENEW whose MemberSession is the resolved HARNESS
// id, and the mcp→harness mapping is recorded.
func TestMonitor_ExecutingRenewsAndRecordsHarness(t *testing.T) {
	// Tail is an in-flight tool_use → BLOCKED_ON_TOOL (a Working state → renew).
	harnessID, snap := claudeBindingHarness(t, claudeAssistantToolUse("toolu_inflight", "Bash"))

	reg := NewRegistry()
	reg.Bind(snap.ID, "hive1", "m1")

	hive := &fakeHive{}
	res := newFakeResolver()
	m := NewMonitor(reg, func() []SessionSnapshot { return []SessionSnapshot{snap} },
		hive, res, nil, DefaultMonitorConfig())

	m.tick(context.Background(), time.Now())

	if hive.count() != 1 {
		t.Fatalf("EXECUTING/working tick issued %d renews, want exactly 1", hive.count())
	}
	req := hive.last()
	if req.GetOp() != knowledgev1.HiveOp_HIVE_OP_RENEW {
		t.Errorf("op = %v, want HIVE_OP_RENEW", req.GetOp())
	}
	if req.GetMemberSession() != harnessID {
		t.Errorf("MemberSession = %q, want resolved harness id %q", req.GetMemberSession(), harnessID)
	}
	if req.GetHive() != "hive1" {
		t.Errorf("Hive = %q, want hive1", req.GetHive())
	}
	if got := res.get(snap.ID); got != harnessID {
		t.Errorf("recorded mcp→harness = %q, want %q", got, harnessID)
	}
}

// TestMonitor_AckStopsRenewAndDeadNoEscalate verifies: a claim whose session has
// acked (Registry.Clear) receives no renew on the next tick; a DEAD
// classification stops renewals WITHOUT an escalation.
func TestMonitor_AckStopsRenewAndDeadNoEscalate(t *testing.T) {
	harnessID, snap := claudeBindingHarness(t, claudeAssistantToolUse("toolu_inflight", "Bash"))
	_ = harnessID

	reg := NewRegistry()
	reg.Bind(snap.ID, "hive1", "m1")

	hive := &fakeHive{}
	var escalations int
	m := NewMonitor(reg, func() []SessionSnapshot { return []SessionSnapshot{snap} },
		hive, newFakeResolver(),
		func(Claim, string, LivenessState, TranscriptHandle) { escalations++ },
		DefaultMonitorConfig())

	// First tick renews (working).
	m.tick(context.Background(), time.Now())
	if hive.count() != 1 {
		t.Fatalf("first tick renews = %d, want 1", hive.count())
	}

	// Ack: clear the claim. Next tick must NOT renew (no active claim).
	reg.Clear(snap.ID, "m1")
	m.tick(context.Background(), time.Now())
	if hive.count() != 1 {
		t.Fatalf("after ack, renew count = %d, want still 1 (no renew for an acked claim)", hive.count())
	}

	// Re-bind, then make the transcript DEAD (remove the file): no renew, no
	// escalation.
	reg.Bind(snap.ID, "hive1", "m2")
	home, _ := homeDir()
	_ = os.RemoveAll(filepath.Join(home, ".claude"))
	m.tick(context.Background(), time.Now())
	if hive.count() != 1 {
		t.Fatalf("DEAD tick issued a renew (count=%d), want no new renew", hive.count())
	}
	if escalations != 0 {
		t.Fatalf("DEAD must NOT escalate, got %d escalations", escalations)
	}
}

// TestMonitor_IdlePastGraceEscalatesOnce verifies: IDLE past the grace window
// escalates exactly once and stops renewing thereafter.
func TestMonitor_IdlePastGraceEscalatesOnce(t *testing.T) {
	// Tail is a resolved turn (tool_use → result → end_turn) → IDLE when no new
	// bytes; but with prevOffset 0 the first classify reads "new" bytes →
	// EXECUTING. To force IDLE we pre-seed the offset to file size via a first
	// tick, then the second tick (no new bytes) is IDLE.
	_, snap := claudeBindingHarness(t,
		claudeUserToolResult("toolu_done")) // resolved tail, no in-flight tool

	reg := NewRegistry()
	reg.Bind(snap.ID, "hive1", "m1")

	hive := &fakeHive{}
	var escalations int
	cfg := MonitorConfig{RenewInterval: time.Second, IdleGrace: 0, BusySoftCeiling: time.Hour}
	m := NewMonitor(reg, func() []SessionSnapshot { return []SessionSnapshot{snap} },
		hive, newFakeResolver(),
		func(Claim, string, LivenessState, TranscriptHandle) { escalations++ },
		cfg)

	base := time.Now()
	// Tick 1: prevOffset 0 → fresh bytes → EXECUTING → renews, offset advances.
	m.tick(context.Background(), base)
	// Tick 2: no new bytes → IDLE; first-seen-idle recorded (grace=0 means
	// strictly past zero, so this tick records but does not yet escalate).
	m.tick(context.Background(), base.Add(time.Second))
	// Tick 3: still IDLE, now past the (zero) grace → escalate once.
	m.tick(context.Background(), base.Add(2*time.Second))
	// Tick 4: already escalated → no further escalation, no renew.
	renewsBefore := hive.count()
	m.tick(context.Background(), base.Add(3*time.Second))

	if escalations != 1 {
		t.Fatalf("IDLE-past-grace escalations = %d, want exactly 1", escalations)
	}
	if hive.count() != renewsBefore {
		t.Fatalf("escalated claim kept renewing (%d → %d)", renewsBefore, hive.count())
	}
}

// TestMonitor_ResumeRenewReenablesRenewal verifies: after an IDLE-past-grace
// escalation stops renewing, ResumeRenew(msgID) clears the escalated+idle state
// so a subsequent WORKING tick renews the lease again.
func TestMonitor_ResumeRenewReenablesRenewal(t *testing.T) {
	// Build a binding whose tail can be made WORKING (an in-flight tool_use)
	// while still letting an initial IDLE escalation happen. Use an in-flight
	// tool_use tail: classify reads BLOCKED_ON_TOOL (Working) on fresh bytes, but
	// with no new bytes after a primed offset it reads BLOCKED_ON_TOOL still
	// (pending). To force IDLE → escalate, use a resolved tail first, then rewrite
	// the file with an in-flight tool_use to resume working.
	home := t.TempDir()
	origHome := homeDir
	t.Cleanup(func() { homeDir = origHome })
	homeDir = func() (string, error) { return home, nil }

	const (
		cwd       = "/Users/jonathan/code/knowledge"
		harnessID = "harness-resume"
		pid       = 7373
	)
	projDir := filepath.Join(home, ".claude", "projects", "-Users-jonathan-code-knowledge")
	if err := os.MkdirAll(projDir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projDir, harnessID+".jsonl")
	writeBody := func(lines ...string) {
		var body []byte
		for _, l := range lines {
			body = append(body, l...)
			body = append(body, '\n')
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Initial: a resolved tail → IDLE when no new bytes.
	writeBody(claudeUserToolResult("toolu_done"))
	snap := SessionSnapshot{ID: "mcp-resume", Cwd: cwd, PID: pid, Comm: "claude"}

	reg := NewRegistry()
	reg.Bind(snap.ID, "hive1", "m1")
	hive := &fakeHive{}
	cfg := MonitorConfig{RenewInterval: time.Second, IdleGrace: 0, BusySoftCeiling: time.Hour}
	m := NewMonitor(reg, func() []SessionSnapshot { return []SessionSnapshot{snap} },
		hive, newFakeResolver(),
		func(_ Claim, _ string, _ LivenessState, _ TranscriptHandle) {},
		cfg)

	base := time.Now()
	m.tick(context.Background(), base)                    // EXECUTING → renew
	m.tick(context.Background(), base.Add(time.Second))   // IDLE recorded
	m.tick(context.Background(), base.Add(2*time.Second)) // IDLE past grace → escalate, stop renewing

	renewsAfterEscalate := hive.count()
	// While escalated, a further tick must NOT renew.
	m.tick(context.Background(), base.Add(3*time.Second))
	if hive.count() != renewsAfterEscalate {
		t.Fatalf("escalated claim renewed (%d → %d) before ResumeRenew", renewsAfterEscalate, hive.count())
	}

	// Resume: clear escalated+idle, and make the worker WORKING again (in-flight
	// tool_use appended), so the next tick renews.
	m.ResumeRenew("m1")
	writeBody(claudeUserToolResult("toolu_done"), claudeAssistantToolUse("toolu_new", "Bash"))
	m.tick(context.Background(), base.Add(4*time.Second))

	if hive.count() <= renewsAfterEscalate {
		t.Fatalf("ResumeRenew did not re-enable renewal: count stayed %d", hive.count())
	}
}

// TestMonitor_EscalationThreadsResolvedHandle verifies: the TranscriptHandle the
// escalation callback receives is the same handle the tick resolved — non-empty
// Path, claude format, and the harness session-id bound by the resolver.
func TestMonitor_EscalationThreadsResolvedHandle(t *testing.T) {
	harnessID, snap := claudeBindingHarness(t,
		claudeUserToolResult("toolu_done")) // resolved tail → IDLE when no new bytes

	reg := NewRegistry()
	reg.Bind(snap.ID, "hive1", "m1")

	hive := &fakeHive{}
	var got TranscriptHandle
	var escalations int
	cfg := MonitorConfig{RenewInterval: time.Second, IdleGrace: 0, BusySoftCeiling: time.Hour}
	m := NewMonitor(reg, func() []SessionSnapshot { return []SessionSnapshot{snap} },
		hive, newFakeResolver(),
		func(_ Claim, _ string, _ LivenessState, handle TranscriptHandle) {
			escalations++
			got = handle
		},
		cfg)

	base := time.Now()
	m.tick(context.Background(), base)                    // EXECUTING (fresh bytes)
	m.tick(context.Background(), base.Add(time.Second))   // IDLE recorded
	m.tick(context.Background(), base.Add(2*time.Second)) // IDLE past grace → escalate

	if escalations != 1 {
		t.Fatalf("escalations = %d, want exactly 1", escalations)
	}
	if !got.Resolved() {
		t.Fatalf("escalation handle is unresolved (empty Path)")
	}
	if got.Format != FormatClaude {
		t.Errorf("handle.Format = %q, want claude", got.Format)
	}
	if got.HarnessSessionID != harnessID {
		t.Errorf("handle.HarnessSessionID = %q, want %q (tick-resolved)", got.HarnessSessionID, harnessID)
	}
}

// TestMonitor_PopulatesBanFromCloudEviction verifies: a tick against a fake
// graph returning a status='evicted' hive_member Bans that member's (harness)
// session-id, so IsBanned is then true.
func TestMonitor_PopulatesBanFromCloudEviction(t *testing.T) {
	_, snap := claudeBindingHarness(t, claudeAssistantToolUse("toolu_inflight", "Bash"))

	reg := NewRegistry()
	reg.Bind(snap.ID, "hive1", "m1")

	const evictedHarness = "harness-evicted-1"
	hive := &fakeHive{execResp: &knowledgev1.ExecuteResponse{
		Nodes: []*knowledgev1.Node{{
			Id:       "hive_member:hive1:" + evictedHarness,
			Status:   "evicted",
			Metadata: map[string]string{"hive": "hive1", "session": evictedHarness, "status": "evicted"},
		}},
	}}

	// Use a REAL BanSet as the resolver so the populate path drives Ban and we
	// can assert IsBanned.
	ban := NewBanSet()
	m := NewMonitor(reg, func() []SessionSnapshot { return []SessionSnapshot{snap} },
		hive, ban, nil, DefaultMonitorConfig())

	if ban.IsBanned(evictedHarness) {
		t.Fatal("precondition: harness should not be banned before the tick")
	}
	m.tick(context.Background(), time.Now())
	if !ban.IsBanned(evictedHarness) {
		t.Fatalf("after a tick reading an evicted hive_member, IsBanned(%q) should be true", evictedHarness)
	}
}

// TestMonitor_HeartbeatRenewsClaimlessLiveMember verifies the machine-health
// heartbeat (Phase 1): a live, registered member that holds NO claim still has
// its last_seen refreshed each tick — the tick fires a HIVE_OP_RENEW carrying
// that member's harness session even though the claim registry is empty.
func TestMonitor_HeartbeatRenewsClaimlessLiveMember(t *testing.T) {
	harnessID, snap := claudeBindingHarness(t, claudeAssistantToolUse("toolu_inflight", "Bash"))

	// EMPTY registry: this member holds no claim.
	reg := NewRegistry()

	// The member-hive lookup returns one hive_member bound to this harness id.
	hive := &fakeHive{execResp: &knowledgev1.ExecuteResponse{
		Nodes: []*knowledgev1.Node{{
			Id:       "hive_member:hive1:" + harnessID,
			Status:   "active",
			Metadata: map[string]string{"hive": "hive1", "session": harnessID},
		}},
	}}
	m := NewMonitor(reg, func() []SessionSnapshot { return []SessionSnapshot{snap} },
		hive, newFakeResolver(), nil, DefaultMonitorConfig())

	m.tick(context.Background(), time.Now())

	if hive.count() != 1 {
		t.Fatalf("claimless live member heartbeat issued %d renews, want exactly 1", hive.count())
	}
	req := hive.last()
	if req.GetOp() != knowledgev1.HiveOp_HIVE_OP_RENEW {
		t.Errorf("op = %v, want HIVE_OP_RENEW (machine-health heartbeat)", req.GetOp())
	}
	if req.GetMemberSession() != harnessID {
		t.Errorf("MemberSession = %q, want harness id %q", req.GetMemberSession(), harnessID)
	}
	if req.GetHive() != "hive1" {
		t.Errorf("Hive = %q, want hive1", req.GetHive())
	}
}

// TestMonitor_DeadNoHeartbeat verifies the machine-down signal is preserved
// (Phase 1): a session whose transcript is gone (the process is dead — the
// readers classify a missing file as StateDead, surfaced here as an unresolved
// handle) gets NO member heartbeat. last_seen is allowed to go stale so the
// reaper can detect the machine-down. Even though the member-hive lookup would
// return a hive, the unresolved transcript short-circuits before any RENEW.
func TestMonitor_DeadNoHeartbeat(t *testing.T) {
	harnessID, snap := claudeBindingHarness(t, claudeAssistantToolUse("toolu_inflight", "Bash"))

	reg := NewRegistry()
	hive := &fakeHive{execResp: &knowledgev1.ExecuteResponse{
		Nodes: []*knowledgev1.Node{{
			Id:       "hive_member:hive1:" + harnessID,
			Status:   "active",
			Metadata: map[string]string{"hive": "hive1", "session": harnessID},
		}},
	}}
	m := NewMonitor(reg, func() []SessionSnapshot { return []SessionSnapshot{snap} },
		hive, newFakeResolver(), nil, DefaultMonitorConfig())

	// Kill the transcript: the file is gone, so ResolveTranscript returns an
	// unresolved handle (dead process) — the heartbeat must skip it.
	home, _ := homeDir()
	if err := os.RemoveAll(filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}

	m.tick(context.Background(), time.Now())

	if hive.count() != 0 {
		t.Fatalf("DEAD member heartbeat issued %d renews, want 0 (machine-down signal preserved)", hive.count())
	}
}

// TestMonitor_StopDrainsAndIdempotent verifies Stop() drains the loop within its
// deadline and is idempotent on a never-started monitor.
func TestMonitor_StopDrainsAndIdempotent(t *testing.T) {
	reg := NewRegistry()
	m := NewMonitor(reg, func() []SessionSnapshot { return nil },
		&fakeHive{}, newFakeResolver(), nil,
		MonitorConfig{RenewInterval: 10 * time.Millisecond, IdleGrace: time.Minute, BusySoftCeiling: time.Hour})

	// Idempotent on a never-started monitor.
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on never-started monitor = %v, want nil", err)
	}

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Double Start errors.
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("second Start should error")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Stop(ctx); err != nil {
		t.Fatalf("Stop should drain within deadline, got %v", err)
	}
	// Second Stop is a no-op.
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop = %v, want nil", err)
	}
}
