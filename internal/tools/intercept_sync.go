// SPDX-License-Identifier: Apache-2.0

// intercept_sync.go — client-side `sync` intercept (push, pull, list). Sync runs
// entirely client-side and routes the bulk graph bytes through GCS to bypass
// Cloudflare's ~100 MB request-body cap; only small JSON control requests cross
// Cloudflare. The bulk bytes are asymmetric-envelope encrypted, so a leaked GCS
// object yields only ciphertext.
//   - push (local → cloud): serialize the LOCAL graph bytes via the
//     EngineService.ExportGraph read (off LocalGraphCaller), then presign → seal
//     (per-request DEK, AES-256-GCM, RSA-OAEP-wrapped to the agent key) → PUT the
//     ciphertext straight to GCS (no auth header) → confirm (the agent downloads,
//     decrypts, and ingests it via /v1/sync/push). The presign/confirm control
//     calls cross Cloudflare over the OAuth auth.Transport.
//   - pull (cloud → local): call the agent's /v1/sync/pull (the agent runs the
//     cloud ExportGraph, encrypts to GCS, and returns {download_url, dek}), GET
//     the ciphertext from GCS (no auth header), decrypt with the returned DEK,
//     then FULLY OVERWRITE the local graph via EngineService.OverwriteGraph (off
//     LocalGraphCaller).
//   - list: print the sync-eligibility table.
//
// The serialize/apply HALVES live server-side (connectAdapter.ExportGraph /
// OverwriteGraph); the agent orchestrates the GCS envelope crypto; the routing,
// transfer, and license gate live here on the client. promote was removed — it
// returns a clear error. The keychain / not-logged-in / transport-failure cases
// render the chat-visible "run knowledge login" guidance verbatim (the legacy
// is_error shape).
//
// THE PUSH FLOW LIVES IN intercept_sync_push.go. This file holds the router
// (InterceptSync), the shared syncable gate, and the PULL half; push moved out when the
// unchanged-graph pull short-circuit took this file past the 500-line cap. The split is by
// direction, the seam the bullet list above already draws, and the push half is the one
// that moved so the pull flow stays where its watermark-ordering gate reads for it.

package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/syncgcs"
)

// Control-plane JSON DTOs for the presigned-GCS sync flow. These mirror the
// agent-side request/response shapes (cmd/agent/internal/http/server/
// sync_presign.go, sync_confirm.go, sync_pull.go). The two repos cannot share a
// Go package, so the shapes are deliberately mirrored on each side. The PUSH-side
// DTOs (presign / confirm) live in intercept_sync_push.go beside the flow that is
// their only consumer; the PULL-side pair stays here with pullGraph.

// syncPullRequest is the body of POST /v1/sync/pull.
type syncPullRequest struct {
	GraphType string `json:"graph_type"`
	Name      string `json:"name"`

	// Watermark is the OPAQUE token this machine last received for (GraphType, Name),
	// read from the machine-local store (sync_watermark.go). The client never parses or
	// compares it — it forwards the bytes and the server that minted it compares by
	// exact string equality. Omitted when empty, which means "send everything".
	Watermark string `json:"watermark,omitempty"`
}

// syncPullResponse is the response of POST /v1/sync/pull. The DEK is
// base64-StdEncoded and the GCS object it unlocks is ciphertext. ObjectPath is
// the agent-minted GCS object path; the client feeds it into OpenEnvelope so the
// pull-direction AAD it computes matches what the agent sealed with.
//
// On an UNCHANGED pull the object fields are absent: Unchanged is true and there is
// nothing to download, decrypt or apply.
type syncPullResponse struct {
	DownloadURL string `json:"download_url"`
	DEK         string `json:"dek"`
	ObjectPath  string `json:"object_path"`
	Expiry      string `json:"expiry"`

	// Unchanged true means the graph's export image matches the Watermark this client
	// sent, so no object was produced and the client must apply nothing.
	Unchanged bool `json:"unchanged"`

	// Watermark is the token describing the state this response reflects, stored after
	// a successful apply and presented on the next pull. EMPTY means the server could
	// not answer; storing it is what the watermark store's empty-token delete prevents.
	Watermark string `json:"watermark"`
}

// The Exporter seam (the LOCAL ExportGraph source) lives in intercept_sync_push.go
// beside the push flow that drives it.

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
func InterceptSync(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "sync" {
		return false, kgtools.ToolResult{}
	}
	if err := rejectUndeclaredParams("sync", "", SyncToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	var a syncArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("sync: invalid args: " + err.Error())
	}

	// list: print the sync-eligibility table. Needs the full ClientDeps (local
	// + cloud GraphCaller + the cloudStatusInfo login gate), unlike pushGraph
	// which only needs the Exporter — so the arm passes deps through.
	if a.Operation == "list" {
		return true, handleSyncList(ctx, deps)
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
	if msg := syncableGateRejection(ctx, deps, graph); msg != "" {
		return true, errorResult(msg)
	}

	// pull: fetch the authoritative cloud copy and FULLY OVERWRITE the local
	// graph with it. Cloud fetch routes through the login-aware GraphCaller; the
	// apply lands on the LOCAL graph via the Overwriter seam.
	if a.Operation == "pull" {
		return true, handlePull(ctx, deps, graph, name)
	}

	// promote was removed; only push/pull/list are supported. The note rides
	// AFTER the canonical list so the actionable history survives the shared
	// wording.
	if a.Operation != "" && a.Operation != "push" {
		return true, errorResult(unknownOperationMessage("sync", a.Operation,
			[]string{"push", "pull", "list"}) + " (promote was removed)")
	}

	exp, err := exporterSeam(deps)
	if err != nil {
		return true, errorResult("sync: " + err.Error())
	}

	transport, err := syncTransportBuilder()
	if err != nil {
		return true, errorResult("sync: " + err.Error())
	}

	return true, pushGraph(ctx, exp, transport, graph, name)
}

// handlePull wires the two seams the pull arm needs — the control-plane
// auth.Transport (the Bearer-authenticated /v1/sync channel that requests the
// agent-orchestrated pull) and the LOCAL Overwriter (the OverwriteGraph apply
// target) — and drives pullGraph. The cloud bytes no longer route through a cloud
// ExportGraph caller: the agent exports + encrypts them to GCS, and pullGraph
// GETs the ciphertext. Each seam error surfaces cleanly: a not-logged-in session
// fails at the pull control call inside pullGraph; a missing local server fails
// at overwriterSeam (pull requires a local server because its destination is the
// local .bin).
func handlePull(ctx context.Context, deps ClientDeps, graph, name string) kgtools.ToolResult {
	ov, err := overwriterSeam(deps)
	if err != nil {
		return errorResult("sync pull: " + err.Error())
	}
	transport, err := syncTransportBuilder()
	if err != nil {
		return errorResult("sync pull: " + err.Error())
	}
	return pullGraph(ctx, ov, transport, graph, name)
}

// pullGraph fetches the authoritative (graph, name) bytes from Fulminate Cloud
// through GCS (off Cloudflare's body cap): it calls the agent's /v1/sync/pull
// control endpoint over the Bearer-authenticated channel (the agent runs the
// cloud ExportGraph, encrypts with a fresh per-request DEK, uploads to GCS, and
// returns {download_url, dek}), GETs the ciphertext straight from GCS with NO
// auth header, decrypts it with the returned DEK (OpenEnvelope), then applies it
// to the LOCAL graph via the OverwriteGraph RPC (full overwrite). It is the
// inverse of pushGraph: push reads local + writes cloud; pull reads cloud +
// writes local. Not-logged-in surfaces from the pull control call with
// caller-actionable login guidance.
//
// IT SENDS THE MACHINE-LOCAL WATERMARK AND RETURNS EARLY WHEN THE GRAPH HAS NOT MOVED.
// The control call carries the token this machine last stored for (graph, name); when the
// response reports unchanged, the download, the decrypt and the apply are ALL skipped and
// the stored token is left exactly as it was. On the changed path the new token is stored
// AFTER the apply succeeds — never before it and never in a defer, because a token stored
// for bytes that were not applied makes every later pull answer "unchanged" for a graph
// this machine does not hold.
func pullGraph(
	ctx context.Context,
	ov Overwriter,
	transport *auth.Transport,
	graph, name string,
) kgtools.ToolResult {
	// (1) Request the agent-orchestrated pull: download URL + the DEK. The stored
	// watermark rides along so an unchanged graph can be answered without producing an
	// object at all.
	stored := defaultSyncWatermarkStore.Load(graph, name)
	pullReqBody, err := json.Marshal(syncPullRequest{GraphType: graph, Name: name, Watermark: stored})
	if err != nil {
		return errorResult(fmt.Sprintf("sync pull: marshal pull request: %v", err))
	}
	pullRaw, err := transport.SyncControlJSON(ctx, "pull", pullReqBody)
	if err != nil {
		return errorResult(wrapPullErr(graph, name, err))
	}
	var pull syncPullResponse
	if err := json.Unmarshal(pullRaw, &pull); err != nil {
		return errorResult(fmt.Sprintf("sync pull: decode pull response: %v", err))
	}

	// UNCHANGED: return before the download, the decrypt and the apply. Skipping those
	// three steps IS the feature — a version that downloaded and then discarded would
	// save nothing. The text deliberately says "unchanged" and deliberately omits the
	// "bytes;" the applied path reports, so an operator can never read a skipped pull as
	// an applied one.
	if pull.Unchanged {
		return textResult(fmt.Sprintf("pulled %s/%s: unchanged (nothing applied)", graph, name))
	}

	dek, err := base64.StdEncoding.DecodeString(pull.DEK)
	if err != nil {
		return errorResult(fmt.Sprintf("sync pull: decode DEK: %v", err))
	}

	// (2) GET the ciphertext straight from GCS with NO auth header.
	envelope, err := syncgcs.GetObject(ctx, pull.DownloadURL)
	if err != nil {
		return errorResult(fmt.Sprintf("sync pull: download %s/%s from GCS: %v", graph, name, err))
	}

	// (3) Decrypt with the returned DEK (pull shape [nonce][ciphertext]).
	body, err := syncgcs.OpenEnvelope(envelope, dek, pull.ObjectPath)
	if err != nil {
		return errorResult(fmt.Sprintf("sync pull: decrypt %s/%s: %v", graph, name, err))
	}

	// (4) Apply to the LOCAL graph via OverwriteGraph (full overwrite).
	ovResp, err := ov.OverwriteGraph(ctx, &knowledgev1.OverwriteGraphRequest{
		GraphType:  graph,
		Name:       name,
		GraphBytes: body,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("sync pull: apply %s/%s: %v", graph, name, err))
	}

	// (5) Advance the stored watermark ONLY AFTER the apply succeeded, and NEVER in a
	// defer. A watermark saved before (or regardless of) a successful apply means this
	// machine believes it holds bytes it never applied, and every later pull is answered
	// "unchanged" — silent, permanent staleness. The error above returns before reaching
	// here, which is exactly the point: a failed apply must leave the stored token alone.
	//
	// An EMPTY pull.Watermark is the server's "cannot answer" signal and reaches Save
	// deliberately: Save deletes the key on an empty token, so the next pull sends
	// nothing and receives a full export.
	_ = defaultSyncWatermarkStore.Save(graph, name, pull.Watermark)

	return textResult(fmt.Sprintf("pulled %s/%s (%d bytes; %d nodes, %d edges)",
		graph, name, len(body), ovResp.GetNodes(), ovResp.GetEdges()))
}

// syncableGateRejection returns a non-empty rejection message when a push/pull of
// `graph` must be refused on the syncable axis, or "" when it may proceed. A
// builtin graph ALWAYS proceeds here (its SyncEligible membership is its only
// gate, unchanged). A NON-builtin (custom) graph proceeds ONLY when a registered
// GraphTypeDef resolves (crud.ByName found) AND its behavior cascade declares
// syncable=true; an unregistered type, a missing registry (degraded client), or a
// syncable false/unset def is rejected. Mirrors the collect.go:192 ByName gate.
//
// It stays HERE rather than moving with either direction's flow: InterceptSync
// applies it to push AND pull alike, so it belongs with the router, not a half.
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
