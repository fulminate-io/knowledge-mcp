// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
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
	ctx := session.ContextWithHarnessSessionID(context.Background(), "harness-forward")
	handled, res := InterceptHive(ctx, hiveTestDeps(f), params)
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
		ctx := session.ContextWithHarnessSessionID(context.Background(), "harness-routing")
		handled, res := InterceptHive(ctx, hiveTestDeps(f), params)
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
	// The claim registry is keyed on the MCP session-id; the harness id is the
	// separate identity the pre-flight guard requires. Deliberately different
	// values, because the two are different identities.
	ctx := session.ContextWithHarnessSessionID(
		session.ContextWithSessionID(context.Background(), sid), "harness-sess-1")

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
	ctx := session.ContextWithHarnessSessionID(
		session.ContextWithSessionID(context.Background(), "s"), "harness-empty-claim")
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

// hiveOpArgs is one agent op's minimal valid argument set — `send` needs a
// routable `to` to clear validateHiveTo, ack/fail need the msg_id they settle.
var hiveOpArgs = []struct {
	op   string
	args map[string]any
}{
	{"register", map[string]any{"op": "register", "hive": "h1", "name": "w1", "roles": []string{"worker"}}},
	{"send", map[string]any{"op": "send", "hive": "h1", "body": "x", "to": "@queue"}},
	{"claim", map[string]any{"op": "claim", "hive": "h1"}},
	{"ack", map[string]any{"op": "ack", "hive": "h1", "msg_id": "m1"}},
	{"fail", map[string]any{"op": "fail", "hive": "h1", "msg_id": "m1", "reason": "stuck"}},
}

// TestInterceptHive_MarksHiveSessionActiveOnEveryAgentOp asserts that EVERY one
// of the five agent ops — not just register and send — marks the calling MCP
// session hive-active, which is what starts the daemon's hive loops. Marking on
// claim/ack/fail too is the second recovery path after a daemon restart: the
// skill's work loop repeats claim, never re-register.
func TestInterceptHive_MarksHiveSessionActiveOnEveryAgentOp(t *testing.T) {
	for _, tc := range hiveOpArgs {
		t.Run(tc.op, func(t *testing.T) {
			reg := hivemonitor.NewRegistry()
			f := &fakeHiveCaller{}
			deps := hiveRegistryDeps{interceptTestDeps: interceptTestDeps{gc: f}, reg: reg}
			ctx := session.ContextWithHarnessSessionID(
				session.ContextWithSessionID(context.Background(), "mcp-sess-"+tc.op), "harness-"+tc.op)

			handled, res := InterceptHive(ctx, deps, hiveCall(t, tc.args))
			if !handled || res.IsError {
				t.Fatalf("op %q should be handled+success, got handled=%v err=%v content=%q",
					tc.op, handled, res.IsError, contentText(res))
			}
			if got := reg.HiveActiveCount(); got != 1 {
				t.Fatalf("after a successful %q, HiveActiveCount() = %d, want 1", tc.op, got)
			}
		})
	}
}

// TestInterceptHive_FailedOrBannedCallDoesNotMarkActive asserts the mark sits
// AFTER the RPC and AFTER the ban gate: a hive call that fails at the cloud, and
// one refused because the session is banned, both leave the loops un-started.
//
// Each subtest opens with a POSITIVE CONTROL on its own fixture — a zero from a
// fixture that could never have marked anything proves nothing.
func TestInterceptHive_FailedOrBannedCallDoesNotMarkActive(t *testing.T) {
	params := hiveCall(t, map[string]any{"op": "claim", "hive": "h1"})

	t.Run("failed rpc", func(t *testing.T) {
		const sid = "mcp-sess-rpc"
		ctx := session.ContextWithHarnessSessionID(
			session.ContextWithSessionID(context.Background(), sid), "harness-sess-rpc")

		// CONTROL: the same ctx through a SUCCEEDING caller does mark.
		okReg := hivemonitor.NewRegistry()
		okDeps := hiveRegistryDeps{interceptTestDeps: interceptTestDeps{gc: &fakeHiveCaller{}}, reg: okReg}
		if _, res := InterceptHive(ctx, okDeps, params); res.IsError {
			t.Fatalf("control: a successful claim must not error, got %q", contentText(res))
		}
		if got := okReg.HiveActiveCount(); got != 1 {
			t.Fatalf("control: HiveActiveCount() = %d, want 1 — the fixture never reaches MarkHiveActive", got)
		}

		// A failing RPC must leave the set empty.
		failReg := hivemonitor.NewRegistry()
		failDeps := hiveRegistryDeps{
			interceptTestDeps: interceptTestDeps{gc: &fakeHiveCaller{err: errors.New("cloud unreachable")}},
			reg:               failReg,
		}
		_, res := InterceptHive(ctx, failDeps, params)
		if !res.IsError {
			t.Fatal("a failed hive RPC must surface as an error result")
		}
		if got := failReg.HiveActiveCount(); got != 0 {
			t.Fatalf("after a FAILED hive call, HiveActiveCount() = %d, want 0 — the mark is above the RPC", got)
		}
	})

	t.Run("banned session", func(t *testing.T) {
		const (
			sid     = "mcp-sess-ban"
			harness = "harness-evicted"
		)
		ctx := session.ContextWithHarnessSessionID(
			session.ContextWithSessionID(context.Background(), sid), "harness-sess-ban")

		// CONTROL: the same sid, un-banned, does mark.
		okReg := hivemonitor.NewRegistry()
		okDeps := hiveRegistryDeps{
			interceptTestDeps: interceptTestDeps{gc: &fakeHiveCaller{}},
			reg:               okReg,
			ban:               hivemonitor.NewBanSet(),
		}
		if _, res := InterceptHive(ctx, okDeps, params); res.IsError {
			t.Fatalf("control: an un-banned claim must not error, got %q", contentText(res))
		}
		if got := okReg.HiveActiveCount(); got != 1 {
			t.Fatalf("control: HiveActiveCount() = %d, want 1 — the fixture never reaches MarkHiveActive", got)
		}

		// The same session, now resolving to a banned harness id, must not mark.
		ban := hivemonitor.NewBanSet()
		ban.RecordResolution(sid, harness)
		ban.Ban(harness)
		banReg := hivemonitor.NewRegistry()
		banDeps := hiveRegistryDeps{
			interceptTestDeps: interceptTestDeps{gc: &fakeHiveCaller{}},
			reg:               banReg,
			ban:               ban,
		}
		_, res := InterceptHive(ctx, banDeps, params)
		if !res.IsError {
			t.Fatal("a banned session's hive call must be refused")
		}
		if got := banReg.HiveActiveCount(); got != 0 {
			t.Fatalf("after a BANNED session's call, HiveActiveCount() = %d, want 0 — the mark is above the ban gate", got)
		}
	})
}

// TestInterceptHive_BanGateForwardsUnbannedAndBanUnresolved verifies the BAN
// gate is session-scoped, not a global off-switch: an un-banned (resolved)
// session's claim forwards normally (hiveN==1), and a session the BanSet has
// never recorded also forwards (fail open — the monitor has not bound it yet).
//
// "Unresolved" here means BAN-RESOLVE unresolved: the harness id is present on
// the context but unknown to the BanSet's mcp→harness map. That is a different
// condition from an unresolved HARNESS TRANSCRIPT (no harness id at all), which
// is refused pre-flight and has its own test —
// TestInterceptHive_UnresolvedHarnessRefusedLocally. Both contexts below
// therefore carry a harness id; only the BanSet's knowledge of it varies.
func TestInterceptHive_BanGateForwardsUnbannedAndBanUnresolved(t *testing.T) {
	ban := hivemonitor.NewBanSet()
	ban.RecordResolution("mcp-ok", "harness-ok") // resolved but NOT banned

	// Resolved + un-banned → forwards.
	f1 := &fakeHiveCaller{}
	deps1 := hiveRegistryDeps{interceptTestDeps: interceptTestDeps{gc: f1}, ban: ban}
	ctx1 := session.ContextWithHarnessSessionID(
		session.ContextWithSessionID(context.Background(), "mcp-ok"), "harness-ok")
	if _, res := InterceptHive(ctx1, deps1, hiveCall(t, map[string]any{"op": "claim", "hive": "h1"})); res.IsError {
		t.Fatalf("an un-banned session must forward, got error: %v", res.Content)
	}
	if f1.hiveN != 1 {
		t.Fatalf("un-banned session forward count = %d, want 1", f1.hiveN)
	}

	// Never recorded in the BanSet → the ban gate fails open, so it forwards.
	// The BanSet is deliberately left untouched for this session; the harness id
	// on the context is what clears the pre-flight guard, not the ban map.
	f2 := &fakeHiveCaller{}
	deps2 := hiveRegistryDeps{interceptTestDeps: interceptTestDeps{gc: f2}, ban: ban}
	ctx2 := session.ContextWithHarnessSessionID(
		session.ContextWithSessionID(context.Background(), "mcp-never-seen"), "harness-never-seen")
	if _, res := InterceptHive(ctx2, deps2, hiveCall(t, map[string]any{"op": "claim", "hive": "h1"})); res.IsError {
		t.Fatalf("a session the BanSet never recorded must fail OPEN (forward), got error: %v", res.Content)
	}
	if f2.hiveN != 1 {
		t.Fatalf("ban-unresolved session forward count = %d, want 1 (fail open)", f2.hiveN)
	}
}

// TestInterceptHive_UnresolvedHarnessRefusedLocally asserts an agent op from a
// session whose HARNESS transcript has not resolved is refused before the RPC
// leaves the machine, with an error naming that cause.
//
// The zero-forward assertion is what makes this non-vacuous: without it the test
// would pass against an implementation that returned a good error AFTER shipping
// the request, which is exactly what the guard's placement prevents.
func TestInterceptHive_UnresolvedHarnessRefusedLocally(t *testing.T) {
	f := &fakeHiveCaller{}
	deps := hiveRegistryDeps{interceptTestDeps: interceptTestDeps{gc: f}, reg: hivemonitor.NewRegistry()}
	// An MCP session-id but NO harness id: the unresolved-transcript state.
	ctx := session.ContextWithSessionID(context.Background(), "mcp-no-harness")

	handled, res := InterceptHive(ctx, deps, hiveCall(t, map[string]any{
		"op": "register", "hive": "h1", "name": "w1", "roles": []string{"worker"},
	}))
	if !handled {
		t.Fatal("a refused hive call must still be claimed by the intercept")
	}
	if !res.IsError {
		t.Fatal("an agent op with no resolved harness session-id must be refused")
	}
	if !strings.Contains(contentText(res), "harness transcript has not resolved yet") {
		t.Errorf("refusal should name the transcript-resolution cause, got %q", contentText(res))
	}
	if f.hiveN != 0 {
		t.Fatalf("the refusal must happen BEFORE the RPC (hiveN=%d, want 0)", f.hiveN)
	}
}
