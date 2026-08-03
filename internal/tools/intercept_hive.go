// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptHive claims the `hive` MCP call and forwards it to
// the cloud work-queue over the Hive RPC. It mirrors the minimal claim-by-name
// shape of InterceptAssemble: gate on the tool name, parse the op + args,
// enforce the client-side session BAN (refuse a banned worker's calls before
// they leave the machine), build a knowledgev1.HiveRequest, forward via the
// GraphCaller's Hive seam, record the claim in the daemon's Registry, and render
// the response. Non-hive calls return (false, _) so the chain continues.
//
// The hive is cloud-only: the request routes to the per-account knowledge graph
// (target Graph="knowledge"); the ACCOUNT is resolved SERVER-side from the
// request credential, never client-supplied. A self-hosted (unauthenticated)
// caller's local OSS server fails loud (CodeUnimplemented), which surfaces here
// as an error result.
//
// The agent surface is the FIVE agent ops only (register/send/claim/ack/fail).
// The daemon ops (renew/evict) are not reachable through this intercept — they
// are not in the tool schema and the op map below does not accept them.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// hiveCaller is the narrow Hive-RPC seam InterceptHive forwards through. The
// production graphClientCaller / *Router both satisfy it (they expose Hive);
// type-asserted UP from the Execute-only base GraphCaller so the base interface
// stays unwidened (mirrors the Indexer / Exporter narrow-seam idiom).
type hiveCaller interface {
	Hive(ctx context.Context, req *knowledgev1.HiveRequest) (*knowledgev1.HiveResponse, error)
}

// hiveArgs is the agent-facing hive tool argument shape (the five agent ops).
type hiveArgs struct {
	Op       string   `json:"op"`
	Hive     string   `json:"hive"`
	Name     string   `json:"name"`
	Roles    []string `json:"roles"`
	Body     string   `json:"body"`
	To       string   `json:"to"`
	Priority string   `json:"priority"`
	ReplyTo  string   `json:"reply_to"`
	MsgID    string   `json:"msg_id"`
	Result   string   `json:"result"`
	Reason   string   `json:"reason"`
}

// agentHiveOps maps the five agent op strings to their HiveOp enum value. The
// daemon ops (renew/evict) are DELIBERATELY absent — they cannot be reached
// through the agent tool surface.
var agentHiveOps = map[string]knowledgev1.HiveOp{
	"register": knowledgev1.HiveOp_HIVE_OP_REGISTER,
	"send":     knowledgev1.HiveOp_HIVE_OP_SEND,
	"claim":    knowledgev1.HiveOp_HIVE_OP_CLAIM,
	"ack":      knowledgev1.HiveOp_HIVE_OP_ACK,
	"fail":     knowledgev1.HiveOp_HIVE_OP_FAIL,
}

// InterceptHive routes hive calls to the cloud work-queue. Returns (handled,
// result). When the call is not `hive`, returns (false, _) so the chain
// continues.
//
// ctx carries the MCP session-id (session.SessionIDFromContext) so a successful
// claim Binds the (session, hive, msg_id) into the client-side claim Registry —
// the binding the daemon Monitor renews while the worker works — and a
// successful ack/fail Clears it. The Registry is nil in degraded/test fixtures;
// its methods are nil-safe so the Bind/Clear are unconditional no-ops there.
func InterceptHive(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "hive" {
		return false, kgtools.ToolResult{}
	}
	// Above the GraphCaller type-assert: a caller's typo must be reported as a
	// typo even on a degraded client that cannot serve the call.
	if err := rejectUndeclaredParams("hive", "", HiveToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, kgtools.ErrorResult("hive: graph client unavailable")
	}
	hc, ok := gc.(hiveCaller)
	if !ok {
		return true, kgtools.ErrorResult("hive: graph client does not support the hive RPC")
	}

	var args hiveArgs
	if len(params.Arguments) > 0 {
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return true, kgtools.ErrorResult(fmt.Sprintf("hive: invalid arguments: %v", err))
		}
	}

	op, known := agentHiveOps[args.Op]
	if !known {
		return true, unknownOperationResult("hive", args.Op,
			[]string{"register", "send", "claim", "ack", "fail"})
	}

	// Defense-in-depth `to` validation for send: v1 work routing accepts ONLY
	// '@<role>' or '@queue'. A bare name (no '@') is direct addressing and '*' is
	// broadcast — both are the deferred messaging layer and are forbidden here
	// (the schema documents this; the intercept enforces it client-side).
	if op == knowledgev1.HiveOp_HIVE_OP_SEND {
		if verr := validateHiveTo(args.To); verr != nil {
			return true, kgtools.ErrorResult(verr.Error())
		}
	}

	sid := session.SessionIDFromContext(ctx)

	// BAN GATE (client-side, before the cloud RPC): the daemon refuses a banned
	// worker's hive calls before they leave the machine, so a degenerate/rogue
	// LLM cannot escape. The ban key is the HARNESS session-id (env/OS-sourced,
	// LLM-unfakeable); the request only carries the Mcp-Session-Id, so the gate
	// Resolves it to the harness id via the monitor-maintained map. A resolved +
	// banned harness id is refused here; an UNRESOLVED session fails OPEN (the
	// monitor has not bound it yet, so it cannot have been evicted). Keying on
	// the harness id (not the Mcp-Session-Id) gives reconnect stability: a fresh
	// Mcp-Session-Id re-resolves to the same banned harness id.
	if ban := deps.BanSet(); ban != nil {
		if harness, ok := ban.Resolve(sid); ok && ban.IsBanned(harness) {
			return true, kgtools.ErrorResult(
				"hive: this worker session has been evicted from the hive — a new human-initiated session is required to rejoin")
		}
	}

	req := &knowledgev1.HiveRequest{
		Op: op,
		// Account is resolved SERVER-side from the request credential; the client
		// names only the graph family (knowledge), never the account.
		Target:   &knowledgev1.GraphSelector{Graph: "knowledge"},
		Hive:     args.Hive,
		Name:     args.Name,
		Roles:    args.Roles,
		Body:     args.Body,
		To:       args.To,
		Priority: args.Priority,
		ReplyTo:  args.ReplyTo,
		MsgId:    args.MsgID,
		Result:   args.Result,
		Reason:   args.Reason,
	}

	resp, err := hc.Hive(ctx, req)
	if err != nil {
		return true, kgtools.ErrorResult(fmt.Sprintf("hive %s: %v", args.Op, err))
	}

	// Record the claim-lifecycle transition into the client-side claim
	// Registry, keyed on the unfakeable MCP session-id from ctx. The daemon
	// Monitor reads these bindings each tick to renew the cloud lease for live
	// claims; ack/fail drops the binding so the Monitor stops renewing. nil
	// Registry / nil-safe methods make this a no-op in degraded/test fixtures.
	reg := deps.ClaimRegistry()
	switch op {
	case knowledgev1.HiveOp_HIVE_OP_CLAIM:
		// A claim only binds when it actually claimed a node (an empty claim
		// returns no node and holds no lease).
		if resp != nil && resp.GetAffectedCount() > 0 && len(resp.GetNodes()) > 0 {
			reg.Bind(sid, args.Hive, resp.GetNodes()[0].GetId())
		}
	case knowledgev1.HiveOp_HIVE_OP_ACK, knowledgev1.HiveOp_HIVE_OP_FAIL:
		reg.Clear(sid, args.MsgID)
	default:
		// register/send hold no claim — nothing to record.
	}

	return true, kgtools.TextResult(renderHiveResponse(args.Op, resp))
}

// validateHiveTo rejects a `to` value that is not '@queue' or '@<role>'. A '*'
// broadcast or a bare '<name>' direct address is forbidden in v1 (work-queue
// only). An empty `to` is also rejected on send — work must be routed.
func validateHiveTo(to string) error {
	if to == "" {
		return fmt.Errorf("hive send: `to` is required — route work with '@queue' or '@<role>'")
	}
	if to == "*" {
		return fmt.Errorf("hive send: `to`='*' (broadcast) is not allowed — use '@queue' or '@<role>'")
	}
	if !strings.HasPrefix(to, "@") {
		return fmt.Errorf("hive send: `to`=%q is direct addressing — only '@<role>' or '@queue' are allowed (no bare names)", to)
	}
	return nil
}

// renderHiveResponse formats a HiveResponse for the agent. claim returns the
// claimed work item (or an empty-claim note); register/send/ack/fail return a
// short ack with the affected count and any returned node id.
func renderHiveResponse(op string, resp *knowledgev1.HiveResponse) string {
	if resp == nil {
		return fmt.Sprintf("hive %s: ok", op)
	}
	if op == "claim" {
		if resp.GetAffectedCount() == 0 || len(resp.GetNodes()) == 0 {
			return "hive claim: no eligible work to claim right now"
		}
		n := resp.GetNodes()[0]
		var b strings.Builder
		fmt.Fprintf(&b, "hive claim: claimed message %s (status=%s)\n", n.GetId(), n.GetStatus())
		if body := n.GetMetadata()["body"]; body != "" {
			fmt.Fprintf(&b, "body: %s\n", body)
		}
		if prio := n.GetMetadata()["priority"]; prio != "" {
			fmt.Fprintf(&b, "priority: %s\n", prio)
		}
		return strings.TrimRight(b.String(), "\n")
	}
	if len(resp.GetNodes()) > 0 {
		return fmt.Sprintf("hive %s: ok (node %s, affected %d)", op, resp.GetNodes()[0].GetId(), resp.GetAffectedCount())
	}
	return fmt.Sprintf("hive %s: ok (affected %d)", op, resp.GetAffectedCount())
}
