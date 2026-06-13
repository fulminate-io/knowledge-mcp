// SPDX-License-Identifier: Apache-2.0

// intercept_sync.go — client-side `sync` intercept (push, pull, list). Sync runs
// entirely client-side: it orchestrates two RPC seams.
//   - push (local → cloud): fetch the serialized LOCAL graph bytes via the
//     EngineService.ExportGraph read (off LocalGraphCaller), then POST them to
//     Fulminate Cloud over the client's OAuth auth.Transport (PushGraph).
//   - pull (cloud → local): fetch the authoritative CLOUD bytes via ExportGraph
//     (off the login-routed GraphCaller), then FULLY OVERWRITE the local graph
//     via the EngineService.OverwriteGraph apply (off LocalGraphCaller).
//   - list: print the sync-eligibility table.
//
// The serialize/apply HALVES live server-side (connectAdapter.ExportGraph /
// OverwriteGraph); the routing, upload, and license gate live here on the client.
// promote was removed — it returns a clear error. The keychain / not-logged-in /
// transport-failure cases render the chat-visible "run knowledge login" guidance
// verbatim (the legacy is_error shape).

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// Exporter is the narrow ExportGraph-RPC seam the push intercept drives. The
// production graphClientCaller implements it (Call + Execute + Index + ... +
// ExportGraph); tests inject a recording fake. Mirrors Indexer — type-asserted
// from the GraphCaller so the Call-only tools.GraphCaller interface stays
// unwidened.
type Exporter interface {
	ExportGraph(ctx context.Context, req *knowledgev1.ExportGraphRequest) (*knowledgev1.ExportGraphResponse, error)
}

// Overwriter is the narrow OverwriteGraph-RPC seam the pull intercept drives to
// apply fetched cloud bytes to the LOCAL graph. The production graphClientCaller
// implements it; tests inject a recording fake. Mirrors Exporter — type-asserted
// from the LOCAL GraphCaller (never the routed one) so the apply always lands
// locally and the Call-only tools.GraphCaller interface stays unwidened.
type Overwriter interface {
	OverwriteGraph(ctx context.Context, req *knowledgev1.OverwriteGraphRequest) (*knowledgev1.OverwriteGraphResponse, error)
}

// syncTransportBuilder constructs the OAuth-backed sync Transport. It is a
// package var (defaulting to buildSyncTransport) so the push path stays a thin
// delegation to the Phase 2 builder while tests can inject an httptest-backed
// Transport without touching the real keychain.
var syncTransportBuilder = buildSyncTransport

// syncArgs is the MCP JSON payload the client reads for a sync call. graph and
// name may be empty (defaulted to knowledge/default). operation is one of
// "push" (or empty), "pull", or "list"; "promote" is rejected.
type syncArgs struct {
	Operation string `json:"operation"`
	Graph     string `json:"graph"`
	Name      string `json:"name"`
}

// InterceptSync claims the `sync` tool and routes to the push, pull, or list
// arm: push fetches the local graph bytes via ExportGraph and uploads them to
// Fulminate Cloud; pull fetches the cloud bytes via ExportGraph and applies them
// to the local graph via OverwriteGraph; list prints the eligibility table.
// Returns (false, _) for any other tool so the chain falls through. promote
// returns an error; the not-logged-in / transport-failure cases render the
// actionable login guidance. Push AND pull of a NON-builtin (custom) graph are
// gated on its GraphTypeDef syncable flag (syncableGateRejection) before any RPC
// fires; builtins skip that gate (SyncEligible is their only gate, unchanged).
func InterceptSync(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "sync" {
		return false, kgtools.ToolResult{}
	}
	var a syncArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("sync: invalid args: " + err.Error())
	}

	// list: print the sync-eligibility table. Needs the full ClientDeps (local
	// + cloud GraphCaller + the cloudStatusInfo login gate), unlike pushGraph
	// which only needs the Exporter — so the arm passes deps through.
	if a.Operation == "list" {
		return true, handleSyncList(deps)
	}

	graph := a.Graph
	if graph == "" {
		graph = string(kgtypes.GraphKnowledge)
	}
	name := a.Name
	if name == "" {
		name = "default"
	}

	// Syncable gate for push AND pull. A builtin graph skips this gate (its
	// SyncEligible membership is its only gate, unchanged). A NON-builtin (custom)
	// graph must carry a registered GraphTypeDef whose behavior declares
	// syncable=true; an unregistered type, or one with syncable false/unset, is
	// rejected BEFORE any ExportGraph/OverwriteGraph RPC fires.
	if msg := syncableGateRejection(context.Background(), deps, graph); msg != "" {
		return true, errorResult(msg)
	}

	// pull: fetch the authoritative cloud copy and FULLY OVERWRITE the local
	// graph with it. Cloud fetch routes through the login-aware GraphCaller; the
	// apply lands on the LOCAL graph via the Overwriter seam.
	if a.Operation == "pull" {
		return true, handlePull(deps, graph, name)
	}

	// promote was removed; only push/pull/list are supported.
	if a.Operation != "" && a.Operation != "push" {
		return true, errorResult(fmt.Sprintf(
			"sync: %q is not supported — sync supports push, pull, list (promote was removed)", a.Operation))
	}

	exp, err := exporterSeam(deps)
	if err != nil {
		return true, errorResult("sync: " + err.Error())
	}

	transport, err := syncTransportBuilder()
	if err != nil {
		return true, errorResult("sync: " + err.Error())
	}

	return true, pushGraph(context.Background(), exp, transport, graph, name)
}

// handlePull wires the two seams the pull arm needs — the CLOUD Exporter (the
// login-routed GraphCaller, which fetches the authoritative cloud bytes) and the
// LOCAL Overwriter (the OverwriteGraph apply target) — and drives pullGraph.
// Each seam error surfaces cleanly: a cloud caller that is nil/not-logged-in
// fails at the ExportGraph call inside pullGraph; a missing local server fails at
// overwriterSeam (pull requires a local server because its destination is the
// local .bin).
func handlePull(deps ClientDeps, graph, name string) kgtools.ToolResult {
	exp, ok := deps.GraphCaller().(Exporter)
	if !ok {
		return errorResult("sync pull: graph caller is not ExportGraph-capable")
	}
	ov, err := overwriterSeam(deps)
	if err != nil {
		return errorResult("sync pull: " + err.Error())
	}
	return pullGraph(context.Background(), ov, exp, graph, name)
}

// pullGraph fetches the serialized (graph, name) bytes from the cloud via the
// routed ExportGraph RPC (GraphCaller routes cloud when logged in via
// Router.pick), then applies them to the LOCAL graph via the OverwriteGraph RPC
// (full overwrite). It is the inverse of pushGraph: push reads local + writes
// cloud; pull reads cloud + writes local. Not-logged-in surfaces from the cloud
// ExportGraph call with caller-actionable login guidance.
func pullGraph(
	ctx context.Context,
	ov Overwriter,
	exp Exporter,
	graph, name string,
) kgtools.ToolResult {
	resp, err := exp.ExportGraph(ctx, &knowledgev1.ExportGraphRequest{
		Target: manageGraphSelector(graph, name),
	})
	if err != nil {
		return errorResult(wrapPullErr(graph, name, err))
	}
	body := resp.GetGraphBytes()

	ovResp, err := ov.OverwriteGraph(ctx, &knowledgev1.OverwriteGraphRequest{
		GraphType:  graph,
		Name:       name,
		GraphBytes: body,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("sync pull: apply %s/%s: %v", graph, name, err))
	}

	return textResult(fmt.Sprintf("pulled %s/%s (%d bytes; %d nodes, %d edges)",
		graph, name, len(body), ovResp.GetNodes(), ovResp.GetEdges()))
}

// pushGraph fetches the serialized (graph, name) bytes via the ExportGraph RPC
// and POSTs them to Fulminate Cloud via the auth.Transport. It is the legacy
// syncPush (engine_sync.go) split across the wire: the server serializes (the
// ExportGraph read), the client uploads. Errors are wrapped for caller-
// actionable login guidance.
func pushGraph(
	ctx context.Context,
	exp Exporter,
	transport *auth.Transport,
	graph, name string,
) kgtools.ToolResult {
	serializeStart := time.Now()
	resp, err := exp.ExportGraph(ctx, &knowledgev1.ExportGraphRequest{
		Target: manageGraphSelector(graph, name),
	})
	if err != nil {
		return errorResult(fmt.Sprintf("sync push: export %s/%s: %v", graph, name, err))
	}
	serializeDur := time.Since(serializeStart)
	body := resp.GetGraphBytes()

	uploadStart := time.Now()
	if err := transport.PushGraph(ctx, graph, name, body); err != nil {
		return errorResult(wrapPushErr(graph, name, err))
	}
	uploadDur := time.Since(uploadStart)

	return textResult(fmt.Sprintf("pushed %s/%s (%d bytes; serialize=%s upload=%s)",
		graph, name, len(body),
		serializeDur.Round(time.Millisecond), uploadDur.Round(time.Millisecond)))
}

// exporterSeam upgrades deps.LocalGraphCaller() to the Exporter seam, or
// returns a typed error so the missing seam is loud (degraded headless mode).
// Sync push reads bytes from the LOCAL graph (the destination is cloud via
// the auth.Transport), so the routing-aware GraphCaller would be wrong here —
// the source must always be local. Mirrors manageIndexer in shape.
func exporterSeam(deps ClientDeps) (Exporter, error) {
	gc := deps.LocalGraphCaller()
	if gc == nil {
		return nil, fmt.Errorf("local graph caller unavailable — no local server is wired (cloud-first user without `knowledge install`)")
	}
	exp, ok := gc.(Exporter)
	if !ok {
		return nil, fmt.Errorf("sync requires an ExportGraph-capable graph client")
	}
	return exp, nil
}

// syncableGateRejection returns a non-empty rejection message when a push/pull of
// `graph` must be refused on the syncable axis, or "" when it may proceed. A
// builtin graph ALWAYS proceeds here (its SyncEligible membership is its only
// gate, unchanged). A NON-builtin (custom) graph proceeds ONLY when a registered
// GraphTypeDef resolves (crud.ByName found) AND its behavior cascade declares
// syncable=true; an unregistered type, a missing registry (degraded client), or a
// syncable false/unset def is rejected. Mirrors the collect.go:192 ByName gate.
func syncableGateRejection(ctx context.Context, deps ClientDeps, graph string) string {
	if kgtypes.IsBuiltinGraphType(graph) {
		return ""
	}
	crud := deps.GraphTypeCRUD()
	if crud == nil {
		return fmt.Sprintf("sync: graph type %q is not syncable (registry unavailable — cannot confirm a registered, syncable GraphTypeDef)", graph)
	}
	def, found, _ := crud.ByName(ctx, graph)
	if !found || !def.GetBehavior().GetSyncable() {
		return fmt.Sprintf("sync: graph type %q is not syncable (its GraphTypeDef sets syncable=false / unset, or the type is unregistered)", graph)
	}
	return ""
}

// overwriterSeam upgrades deps.LocalGraphCaller() to the Overwriter seam, or
// returns a typed error so the missing seam is loud (degraded headless mode).
// Sync pull's destination is the LOCAL .bin, so the apply must ALWAYS be local —
// the routing-aware GraphCaller would be wrong here (it has no OverwriteGraph
// forwarder anyway). Mirrors exporterSeam in shape: same nil-guard, type-assert,
// degraded-mode wording.
func overwriterSeam(deps ClientDeps) (Overwriter, error) {
	gc := deps.LocalGraphCaller()
	if gc == nil {
		return nil, fmt.Errorf("local graph caller unavailable — no local server is wired (cloud-first user without `knowledge install`)")
	}
	ov, ok := gc.(Overwriter)
	if !ok {
		return nil, fmt.Errorf("sync requires an OverwriteGraph-capable graph client")
	}
	return ov, nil
}

// wrapPushErr produces a caller-actionable message for common auth failure modes
// on top of the underlying transport error. 401 means the refresh flow could not
// re-auth; 403 usually means the OAuth session lacks the `sync` scope.
// auth.ErrNotFound surfaces as "not logged in". Other errors are surfaced
// verbatim. Ported from the legacy server-side wrapSyncErr (engine_sync.go) so
// the chat-visible login guidance survives the move to the client.
func wrapPushErr(graph, name string, err error) string {
	if errors.Is(err, auth.ErrNotFound) {
		return fmt.Sprintf("sync push %s/%s: not logged in — run 'knowledge login' to authenticate",
			graph, name)
	}
	if se, ok := errors.AsType[*auth.SyncHTTPError](err); ok {
		switch se.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Sprintf("sync push %s/%s: authentication failed — run 'knowledge login' to refresh credentials (HTTP 401)",
				graph, name)
		case http.StatusForbidden:
			return fmt.Sprintf("sync push %s/%s: server rejected request — your login may lack the 'sync' scope (HTTP 403)",
				graph, name)
		}
	}
	return fmt.Sprintf("sync push %s/%s: %v", graph, name, err)
}

// wrapPullErr produces a caller-actionable message for the cloud-fetch failure
// modes of sync pull, mirroring wrapPushErr. A nil cloud caller or a not-logged-in
// session surfaces as login guidance; 401/403 map to refresh/scope guidance;
// other errors surface verbatim. Pull fetches from the cloud over the routed
// ExportGraph, so the not-logged-in case is the common one.
func wrapPullErr(graph, name string, err error) string {
	if errors.Is(err, auth.ErrNotFound) {
		return fmt.Sprintf("sync pull %s/%s: not logged in — run 'knowledge login' to authenticate",
			graph, name)
	}
	if se, ok := errors.AsType[*auth.SyncHTTPError](err); ok {
		switch se.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Sprintf("sync pull %s/%s: authentication failed — run 'knowledge login' to refresh credentials (HTTP 401)",
				graph, name)
		case http.StatusForbidden:
			return fmt.Sprintf("sync pull %s/%s: server rejected request — your login may lack the 'sync' scope (HTTP 403)",
				graph, name)
		}
	}
	return fmt.Sprintf("sync pull %s/%s: %v", graph, name, err)
}
