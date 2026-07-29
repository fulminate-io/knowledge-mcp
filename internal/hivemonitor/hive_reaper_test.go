// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"context"
	"sync"
	"testing"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// testHive is the single hive the reaper tests operate within.
const testHive = "hive1"

// memberNode builds a hive_member node in testHive for the reaper tests.
// lastSeen is formatted RFC3339Nano (the server's format); an empty lastSeen
// leaves the metadata key unset.
func memberNode(session, roles, status string, lastSeen time.Time) *knowledgev1.Node {
	md := map[string]string{"hive": testHive, "session": session, "roles": roles, "status": status}
	if !lastSeen.IsZero() {
		md["last_seen"] = lastSeen.UTC().Format(time.RFC3339Nano)
	}
	return &knowledgev1.Node{Id: "hive_member:" + testHive + ":" + session, Status: status, Metadata: md}
}

// TestHiveReaper_LifecycleGuards verifies the reaper mirrors the Monitor
// lifecycle: Start is idempotent (a second Start returns the already-started
// error) and Stop on a never-started reaper returns nil, then a started reaper
// drains within its Stop deadline.
func TestHiveReaper_LifecycleGuards(t *testing.T) {
	r := NewHiveReaper(func() []SessionSnapshot { return nil }, &fakeHive{},
		ReaperConfig{SweepInterval: 10 * time.Millisecond, MachineDownThreshold: time.Minute})

	// Stop on a never-started reaper is a no-op.
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on never-started reaper = %v, want nil", err)
	}

	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// A second Start errors (already started).
	if err := r.Start(context.Background()); err == nil {
		t.Fatal("second Start should error (already started)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Stop(ctx); err != nil {
		t.Fatalf("Stop should drain within deadline, got %v", err)
	}
	// Second Stop is a no-op.
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop = %v, want nil", err)
	}
}

// TestHiveReaper_RoleGateNoReaperNoEvict verifies the role gate's NEGATIVE case:
// a daemon whose own member node does NOT list the reaper role resolves to an
// empty reaper-hive set and issues ZERO evicts even when a stale member exists.
func TestHiveReaper_RoleGateNoReaperNoEvict(t *testing.T) {
	myHarness, snap := claudeBindingHarness(t, claudeAssistantToolUse("toolu_inflight", "Bash"))
	now := time.Now()

	hive := &fakeHive{execResp: &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{
		// MY member — NO reaper role — fresh.
		memberNode(myHarness, "worker", "active", now),
		// A genuinely stale OTHER member in the same hive.
		memberNode("harness-dead", "worker", "active", now.Add(-30*time.Minute)),
	}}}
	r := NewHiveReaper(func() []SessionSnapshot { return []SessionSnapshot{snap} },
		hive, DefaultReaperConfig())

	r.sweep(context.Background(), now)

	if hive.count() != 0 {
		t.Fatalf("no-reaper-role daemon issued %d evicts, want 0 (role gate closed)", hive.count())
	}
}

// TestHiveReaper_RoleGatePositiveSweep verifies the role gate's POSITIVE case: a
// daemon whose member node lists the reaper role resolves that member's hive
// into the sweep set, so a stale member in that hive is evicted.
func TestHiveReaper_RoleGatePositiveSweep(t *testing.T) {
	myHarness, snap := claudeBindingHarness(t, claudeAssistantToolUse("toolu_inflight", "Bash"))
	now := time.Now()

	hive := &fakeHive{execResp: &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{
		// MY member — HOLDS the reaper role — fresh (not itself reaped).
		memberNode(myHarness, "worker,reaper", "active", now),
		// A genuinely stale OTHER member in the same hive.
		memberNode("harness-dead", "worker", "active", now.Add(-30*time.Minute)),
	}}}
	r := NewHiveReaper(func() []SessionSnapshot { return []SessionSnapshot{snap} },
		hive, DefaultReaperConfig())

	r.sweep(context.Background(), now)

	if hive.count() != 1 {
		t.Fatalf("reaper-role daemon issued %d evicts, want exactly 1 (the stale member)", hive.count())
	}
	req := hive.last()
	if req.GetOp() != knowledgev1.HiveOp_HIVE_OP_EVICT {
		t.Errorf("op = %v, want HIVE_OP_EVICT", req.GetOp())
	}
	if req.GetMemberSession() != "harness-dead" {
		t.Errorf("MemberSession = %q, want the STALE member's session %q", req.GetMemberSession(), "harness-dead")
	}
	if req.GetReason() != reaperEvictReason {
		t.Errorf("Reason = %q, want %q", req.GetReason(), reaperEvictReason)
	}
}

// selections returns the Selection of every browse plan the fake hive has
// served, in call order. Lives here because the reaper's plan-shape assertion is
// its only consumer.
func (f *fakeHive) selections() []*knowledgev1.Selection {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*knowledgev1.Selection, 0, len(f.execReqs))
	for _, req := range f.execReqs {
		out = append(out, req.GetQuery().GetSelection())
	}
	return out
}

// TestHiveReaper_RoleGateBrowseIsSessionScoped pins the role gate's READ PLAN:
// every member browse a sweep issues must be narrowed by a metadata predicate,
// and the role gate's own browse must carry a session OP_EQ for THIS daemon's
// harness session — one browse per live local session, never a bare whole-type
// hive_member read. The hive_member set grows monotonically (eviction is an
// UPDATE, never a delete), so an unnarrowed browse reads every member ever
// registered in the account every 60s. No behavioral assertion can catch this:
// the role gate filters by session client-side either way, so the eviction
// tests stay green with or without the predicate.
func TestHiveReaper_RoleGateBrowseIsSessionScoped(t *testing.T) {
	myHarness, snap := claudeBindingHarness(t, claudeAssistantToolUse("toolu_inflight", "Bash"))
	now := time.Now()

	hive := &fakeHive{execResp: &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{
		memberNode(myHarness, "worker,reaper", "active", now),
		memberNode("harness-dead", "worker", "active", now.Add(-30*time.Minute)),
	}}}
	r := NewHiveReaper(func() []SessionSnapshot { return []SessionSnapshot{snap} },
		hive, DefaultReaperConfig())

	r.sweep(context.Background(), now)

	sels := hive.selections()
	if len(sels) == 0 {
		t.Fatal("sweep issued no member browse at all")
	}
	for i, sel := range sels {
		if len(sel.GetMetadataPredicates()) == 0 {
			t.Errorf("browse %d is an UNBOUNDED whole-type read: NodeTypes=%v with no metadata predicate",
				i, sel.GetNodeTypes())
		}
	}

	// The first browse is the role gate: one per live local session, narrowed to
	// that session's members.
	gate := sels[0]
	if got := gate.GetNodeTypes(); len(got) != 1 || got[0] != "hive_member" {
		t.Fatalf("role-gate browse NodeTypes = %v, want [hive_member]", got)
	}
	preds := gate.GetMetadataPredicates()
	if len(preds) != 1 {
		t.Fatalf("role-gate browse carries %d metadata predicates, want exactly 1 (session)", len(preds))
	}
	if preds[0].GetKey() != "session" {
		t.Errorf("role-gate predicate key = %q, want %q", preds[0].GetKey(), "session")
	}
	if preds[0].GetOp() != knowledgev1.MetadataPredicate_OP_EQ {
		t.Errorf("role-gate predicate op = %v, want OP_EQ", preds[0].GetOp())
	}
	if preds[0].GetValue() != myHarness {
		t.Errorf("role-gate predicate value = %q, want THIS daemon's harness session %q",
			preds[0].GetValue(), myHarness)
	}
}

// TestHiveReaper_FreshMemberNotReaped verifies a member whose last_seen is within
// the machine-down threshold is NOT evicted (the machine is up).
func TestHiveReaper_FreshMemberNotReaped(t *testing.T) {
	myHarness, snap := claudeBindingHarness(t, claudeAssistantToolUse("toolu_inflight", "Bash"))
	now := time.Now()

	hive := &fakeHive{execResp: &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{
		memberNode(myHarness, "reaper", "active", now),
		// A peer member whose last_seen is fresh (5min < 10min threshold).
		memberNode("harness-fresh", "worker", "active", now.Add(-5*time.Minute)),
	}}}
	r := NewHiveReaper(func() []SessionSnapshot { return []SessionSnapshot{snap} },
		hive, DefaultReaperConfig())

	r.sweep(context.Background(), now)

	if hive.count() != 0 {
		t.Fatalf("fresh member triggered %d evicts, want 0 (within threshold)", hive.count())
	}
}

// TestHiveReaper_BadLastSeenSkipped verifies a member with an empty or
// unparseable last_seen is SKIPPED (missing data never triggers a DNF), even
// when the reaper sweeps that member's hive.
func TestHiveReaper_BadLastSeenSkipped(t *testing.T) {
	myHarness, snap := claudeBindingHarness(t, claudeAssistantToolUse("toolu_inflight", "Bash"))
	now := time.Now()

	// Two ill-formed members: one with no last_seen at all, one with garbage.
	noLastSeen := memberNode("harness-nolastseen", "worker", "active", time.Time{})
	garbage := memberNode("harness-garbage", "worker", "active", time.Time{})
	garbage.Metadata["last_seen"] = "not-a-timestamp"

	hive := &fakeHive{execResp: &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{
		memberNode(myHarness, "reaper", "active", now),
		noLastSeen,
		garbage,
	}}}
	r := NewHiveReaper(func() []SessionSnapshot { return []SessionSnapshot{snap} },
		hive, DefaultReaperConfig())

	r.sweep(context.Background(), now)

	if hive.count() != 0 {
		t.Fatalf("ill-formed last_seen triggered %d evicts, want 0 (missing data never DNFs)", hive.count())
	}
}

// TestHiveReaper_AlreadyEvictedSkipped verifies the cheap idempotency
// pre-filter: a stale member that is ALREADY status='evicted' is skipped (no
// redundant EVICT). This is an optimization on top of EvictHiveMember's
// idempotency, not the correctness guarantee.
func TestHiveReaper_AlreadyEvictedSkipped(t *testing.T) {
	myHarness, snap := claudeBindingHarness(t, claudeAssistantToolUse("toolu_inflight", "Bash"))
	now := time.Now()

	hive := &fakeHive{execResp: &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{
		memberNode(myHarness, "reaper", "active", now),
		// Stale AND already evicted — must be skipped (no redundant EVICT).
		memberNode("harness-gone", "worker", "evicted", now.Add(-30*time.Minute)),
	}}}
	r := NewHiveReaper(func() []SessionSnapshot { return []SessionSnapshot{snap} },
		hive, DefaultReaperConfig())

	r.sweep(context.Background(), now)

	if hive.count() != 0 {
		t.Fatalf("already-evicted stale member triggered %d evicts, want 0 (pre-filter skip)", hive.count())
	}
}

// TestHiveReaper_ConcurrentSweepsWellFormed verifies the CLIENT side of the
// idempotency story: two peer reapers sweeping the SAME stale member
// concurrently each issue a well-formed EVICT (same MemberSession + reason) with
// no panic/race. The reaper layer does not coordinate — both fire; the
// no-double-EFFECT guarantee is the cloud op's (EvictHiveMember), asserted in the
// server-side idempotency test. Run under -race.
func TestHiveReaper_ConcurrentSweepsWellFormed(t *testing.T) {
	myHarness, snap := claudeBindingHarness(t, claudeAssistantToolUse("toolu_inflight", "Bash"))
	now := time.Now()

	hive := &fakeHive{execResp: &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{
		memberNode(myHarness, "reaper", "active", now),
		memberNode("harness-dead", "worker", "active", now.Add(-30*time.Minute)),
	}}}
	r := NewHiveReaper(func() []SessionSnapshot { return []SessionSnapshot{snap} },
		hive, DefaultReaperConfig())

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			r.sweep(context.Background(), now)
		})
	}
	wg.Wait()

	// Both reapers issued an EVICT for the same member (the reaper does not
	// coordinate); each must be well-formed.
	if hive.count() != 2 {
		t.Fatalf("two concurrent sweeps issued %d evicts, want 2 (one per reaper)", hive.count())
	}
	for _, req := range hive.all() {
		if req.GetOp() != knowledgev1.HiveOp_HIVE_OP_EVICT {
			t.Errorf("op = %v, want HIVE_OP_EVICT", req.GetOp())
		}
		if req.GetMemberSession() != "harness-dead" {
			t.Errorf("MemberSession = %q, want the stale member %q", req.GetMemberSession(), "harness-dead")
		}
		if req.GetReason() != reaperEvictReason {
			t.Errorf("Reason = %q, want %q", req.GetReason(), reaperEvictReason)
		}
	}
}
