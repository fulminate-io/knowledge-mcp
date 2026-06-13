// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// contentText concatenates a ToolResult's text content blocks.
func contentText(res kgtools.ToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// fakeHiveCaller satisfies both the Execute-only GraphCaller base interface and
// the narrow hiveCaller seam InterceptHive type-asserts to. It records the last
// forwarded HiveRequest and returns a scripted response.
type fakeHiveCaller struct {
	lastReq *knowledgev1.HiveRequest
	resp    *knowledgev1.HiveResponse
	err     error
	hiveN   int
}

func (f *fakeHiveCaller) Execute(context.Context, *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

func (f *fakeHiveCaller) Hive(_ context.Context, req *knowledgev1.HiveRequest) (*knowledgev1.HiveResponse, error) {
	f.hiveN++
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	if f.resp != nil {
		return f.resp, nil
	}
	return &knowledgev1.HiveResponse{AffectedCount: 1}, nil
}

func hiveTestDeps(gc GraphCaller) ClientDeps { return interceptTestDeps{gc: gc} }

// hiveRegistryDeps wraps interceptTestDeps but returns a real claim Registry (and
// optionally a real BanSet) so the claim-recording path (Bind on claim / Clear
// on ack) and the ban gate are exercised.
type hiveRegistryDeps struct {
	interceptTestDeps
	reg *hivemonitor.Registry
	ban *hivemonitor.BanSet
}

func (d hiveRegistryDeps) ClaimRegistry() *hivemonitor.Registry { return d.reg }
func (d hiveRegistryDeps) BanSet() *hivemonitor.BanSet          { return d.ban }

func hiveCall(t *testing.T, args map[string]any) kgtools.CallToolParams {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return kgtools.CallToolParams{Name: "hive", Arguments: raw}
}

// TestInterceptHive_FallsThroughOnNonHive asserts a non-hive tool name is not
// claimed (handled=false), so the intercept chain continues.
func TestInterceptHive_FallsThroughOnNonHive(t *testing.T) {
	f := &fakeHiveCaller{}
	handled, _ := InterceptHive(context.Background(), hiveTestDeps(f), kgtools.CallToolParams{Name: "search"})
	if handled {
		t.Fatal("InterceptHive must NOT claim a non-hive tool call")
	}
	if f.hiveN != 0 {
		t.Fatal("InterceptHive must not forward a non-hive call")
	}
}

// TestInterceptHive_ForwardsAgentOp asserts a valid agent op is forwarded via
// GraphCaller.Hive with the op mapped to the HiveOp enum and the knowledge
// target set.
func TestInterceptHive_ForwardsAgentOp(t *testing.T) {
	f := &fakeHiveCaller{resp: &knowledgev1.HiveResponse{
		AffectedCount: 1,
		Nodes:         []*knowledgev1.Node{{Id: "m1", Status: "leased", Metadata: map[string]string{"body": "task"}}},
	}}
	params := hiveCall(t, map[string]any{"op": "claim", "hive": "h1"})
	handled, res := InterceptHive(context.Background(), hiveTestDeps(f), params)
	if !handled {
		t.Fatal("InterceptHive must claim a hive call")
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %v", res.Content)
	}
	if f.hiveN != 1 {
		t.Fatalf("expected exactly one Hive forward, got %d", f.hiveN)
	}
	if f.lastReq.GetOp() != knowledgev1.HiveOp_HIVE_OP_CLAIM {
		t.Errorf("op mapped to %v, want CLAIM", f.lastReq.GetOp())
	}
	if f.lastReq.GetTarget().GetGraph() != "knowledge" {
		t.Errorf("target graph = %q, want knowledge", f.lastReq.GetTarget().GetGraph())
	}
}

// TestInterceptHive_RejectsBroadcastAndDirectTo asserts that send with to='*'
// (broadcast) or a bare name (direct) is rejected CLIENT-side, before any
// forward — the v1 work-queue forbids broadcast/direct.
func TestInterceptHive_RejectsBroadcastAndDirectTo(t *testing.T) {
	cases := []struct {
		name string
		to   string
	}{
		{"broadcast star", "*"},
		{"bare name direct", "alice"},
		{"empty to", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeHiveCaller{}
			params := hiveCall(t, map[string]any{"op": "send", "hive": "h1", "body": "x", "to": tc.to})
			handled, res := InterceptHive(context.Background(), hiveTestDeps(f), params)
			if !handled {
				t.Fatal("InterceptHive must claim the hive call even when rejecting")
			}
			if !res.IsError {
				t.Fatalf("send to=%q must be rejected client-side", tc.to)
			}
			if f.hiveN != 0 {
				t.Fatalf("a rejected `to` must NOT forward to the cloud (forwarded %d times)", f.hiveN)
			}
		})
	}
}

// TestInterceptHive_AcceptsRoleAndQueueTo asserts that @<role> and @queue ARE
// accepted on send (the two allowed v1 routing forms) and forwarded.
func TestInterceptHive_AcceptsRoleAndQueueTo(t *testing.T) {
	for _, to := range []string{"@queue", "@worker"} {
		f := &fakeHiveCaller{}
		params := hiveCall(t, map[string]any{"op": "send", "hive": "h1", "body": "x", "to": to})
		handled, res := InterceptHive(context.Background(), hiveTestDeps(f), params)
		if !handled || res.IsError {
			t.Fatalf("send to=%q must be accepted, got handled=%v err=%v", to, handled, res.IsError)
		}
		if f.hiveN != 1 {
			t.Fatalf("send to=%q must forward once, got %d", to, f.hiveN)
		}
	}
}

// TestInterceptHive_RejectsUnknownOp asserts a daemon op (renew/evict) or a
// nonsense op is rejected — the agent surface is the five agent ops only.
func TestInterceptHive_RejectsUnknownOp(t *testing.T) {
	for _, op := range []string{"renew", "evict", "recv", "bogus"} {
		f := &fakeHiveCaller{}
		params := hiveCall(t, map[string]any{"op": op, "hive": "h1"})
		handled, res := InterceptHive(context.Background(), hiveTestDeps(f), params)
		if !handled {
			t.Fatalf("op %q must be claimed (and rejected)", op)
		}
		if !res.IsError {
			t.Fatalf("op %q must be rejected — not an agent op", op)
		}
		if f.hiveN != 0 {
			t.Fatalf("rejected op %q must not forward", op)
		}
		if !strings.Contains(contentText(res), "unknown op") {
			t.Errorf("op %q error should explain unknown op, got %q", op, contentText(res))
		}
	}
}

// TestInterceptHive_RecordsClaimAndClearsOnAck drives a successful claim through
// InterceptHive with a session-stamped ctx against a fakeHiveCaller returning a
// node, and asserts the injected Registry recorded the (sessionID, hive, msg_id)
// binding; a follow-up ack with the same session+msg_id clears it.
func TestInterceptHive_RecordsClaimAndClearsOnAck(t *testing.T) {
	const (
		sid    = "mcp-sess-1"
		hive   = "h1"
		claimd = "msg-42"
	)
	reg := hivemonitor.NewRegistry()
	deps := hiveRegistryDeps{interceptTestDeps: interceptTestDeps{gc: nil}, reg: reg}
	ctx := session.ContextWithSessionID(context.Background(), sid)

	// A successful claim returning the leased node binds the claim.
	claimCaller := &fakeHiveCaller{resp: &knowledgev1.HiveResponse{
		AffectedCount: 1,
		Nodes:         []*knowledgev1.Node{{Id: claimd, Status: "leased"}},
	}}
	deps.interceptTestDeps = interceptTestDeps{gc: claimCaller}
	params := hiveCall(t, map[string]any{"op": "claim", "hive": hive})
	handled, res := InterceptHive(ctx, deps, params)
	if !handled || res.IsError {
		t.Fatalf("claim should be handled+success, got handled=%v err=%v", handled, res.IsError)
	}
	claims := reg.ClaimsFor(sid)
	if len(claims) != 1 {
		t.Fatalf("after claim, ClaimsFor(%q) = %d claims, want 1", sid, len(claims))
	}
	if claims[0].MsgID != claimd || claims[0].Hive != hive {
		t.Fatalf("recorded claim = %+v, want MsgID=%q Hive=%q", claims[0], claimd, hive)
	}

	// A follow-up ack with the same session+msg_id clears the binding.
	ackCaller := &fakeHiveCaller{resp: &knowledgev1.HiveResponse{AffectedCount: 1}}
	deps.interceptTestDeps = interceptTestDeps{gc: ackCaller}
	ackParams := hiveCall(t, map[string]any{"op": "ack", "hive": hive, "msg_id": claimd})
	handled, res = InterceptHive(ctx, deps, ackParams)
	if !handled || res.IsError {
		t.Fatalf("ack should be handled+success, got handled=%v err=%v", handled, res.IsError)
	}
	if got := reg.ClaimsFor(sid); got != nil {
		t.Fatalf("after ack, ClaimsFor(%q) = %v, want nil (binding cleared)", sid, got)
	}
}

// TestInterceptHive_EmptyClaimRecordsNothing asserts a claim that claimed no
// work (no node / zero affected) does NOT bind a phantom claim.
func TestInterceptHive_EmptyClaimRecordsNothing(t *testing.T) {
	reg := hivemonitor.NewRegistry()
	caller := &fakeHiveCaller{resp: &knowledgev1.HiveResponse{AffectedCount: 0}}
	deps := hiveRegistryDeps{interceptTestDeps: interceptTestDeps{gc: caller}, reg: reg}
	ctx := session.ContextWithSessionID(context.Background(), "s")
	params := hiveCall(t, map[string]any{"op": "claim", "hive": "h1"})
	if handled, res := InterceptHive(ctx, deps, params); !handled || res.IsError {
		t.Fatalf("empty claim should still be handled+success, got handled=%v err=%v", handled, res.IsError)
	}
	if got := reg.ClaimsFor("s"); got != nil {
		t.Fatalf("empty claim must record nothing, got %v", got)
	}
}

// TestInterceptHive_BanGateRefusesBannedSession verifies BAN ENFORCEMENT keyed
// on the HARNESS id: a call whose Mcp-Session-Id resolves to a banned HARNESS id
// is refused CLIENT-SIDE (hiveN==0, never reaching the cloud), and the refusal
// survives a reconnect that mints a fresh Mcp-Session-Id resolving to the same
// harness id.
func TestInterceptHive_BanGateRefusesBannedSession(t *testing.T) {
	const (
		mcpSID  = "mcp-sess-A"
		harness = "harness-evicted"
	)
	ban := hivemonitor.NewBanSet()
	// The monitor recorded the mcp→harness binding, then the harness id was
	// evicted/banned.
	ban.RecordResolution(mcpSID, harness)
	ban.Ban(harness)

	f := &fakeHiveCaller{}
	deps := hiveRegistryDeps{interceptTestDeps: interceptTestDeps{gc: f}, ban: ban}
	ctx := session.ContextWithSessionID(context.Background(), mcpSID)

	params := hiveCall(t, map[string]any{"op": "claim", "hive": "h1"})
	handled, res := InterceptHive(ctx, deps, params)
	if !handled {
		t.Fatal("a banned session's hive call must still be claimed (and rejected)")
	}
	if !res.IsError {
		t.Fatal("a banned session's hive call must be refused")
	}
	if f.hiveN != 0 {
		t.Fatalf("a banned session's call must NOT reach the cloud (hiveN=%d, want 0)", f.hiveN)
	}
	if !strings.Contains(contentText(res), "evicted") {
		t.Errorf("refusal should explain the eviction, got %q", contentText(res))
	}

	// Reconnect stability: a NEW Mcp-Session-Id for the same CLI session
	// re-resolves to the SAME banned harness id → still refused.
	const mcpReconnect = "mcp-sess-B"
	ban.RecordResolution(mcpReconnect, harness)
	ctx2 := session.ContextWithSessionID(context.Background(), mcpReconnect)
	if _, res2 := InterceptHive(ctx2, deps, params); !res2.IsError {
		t.Fatal("a reconnect (new Mcp-Session-Id, same harness id) must STAY banned")
	}
	if f.hiveN != 0 {
		t.Fatalf("reconnect of a banned worker must NOT reach cloud (hiveN=%d, want 0)", f.hiveN)
	}
}

// TestInterceptHive_BanGateForwardsUnbannedAndUnresolved verifies the gate is
// session-scoped, not a global off-switch: an un-banned (resolved) session's
// claim forwards normally (hiveN==1), and an UNRESOLVED session's claim also
// forwards (fail open — the monitor has not bound it yet).
func TestInterceptHive_BanGateForwardsUnbannedAndUnresolved(t *testing.T) {
	ban := hivemonitor.NewBanSet()
	ban.RecordResolution("mcp-ok", "harness-ok") // resolved but NOT banned

	// Resolved + un-banned → forwards.
	f1 := &fakeHiveCaller{}
	deps1 := hiveRegistryDeps{interceptTestDeps: interceptTestDeps{gc: f1}, ban: ban}
	ctx1 := session.ContextWithSessionID(context.Background(), "mcp-ok")
	if _, res := InterceptHive(ctx1, deps1, hiveCall(t, map[string]any{"op": "claim", "hive": "h1"})); res.IsError {
		t.Fatalf("an un-banned session must forward, got error: %v", res.Content)
	}
	if f1.hiveN != 1 {
		t.Fatalf("un-banned session forward count = %d, want 1", f1.hiveN)
	}

	// Unresolved (never recorded) → fail open, forwards.
	f2 := &fakeHiveCaller{}
	deps2 := hiveRegistryDeps{interceptTestDeps: interceptTestDeps{gc: f2}, ban: ban}
	ctx2 := session.ContextWithSessionID(context.Background(), "mcp-never-seen")
	if _, res := InterceptHive(ctx2, deps2, hiveCall(t, map[string]any{"op": "claim", "hive": "h1"})); res.IsError {
		t.Fatalf("an unresolved session must fail OPEN (forward), got error: %v", res.Content)
	}
	if f2.hiveN != 1 {
		t.Fatalf("unresolved session forward count = %d, want 1 (fail open)", f2.hiveN)
	}
}
