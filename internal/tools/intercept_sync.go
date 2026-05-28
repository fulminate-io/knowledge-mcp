// SPDX-License-Identifier: Apache-2.0

// intercept_sync.go — client-side `sync` intercept, PUSH-ONLY. Sync runs
// entirely client-side now: the handler fetches the serialized OSS graph bytes
// from the server via the EngineService.ExportGraph read, then POSTs them to
// Fulminate Cloud over the client's OAuth auth.Transport (PushGraph). The
// serialize HALF lives server-side (connectAdapter.ExportGraph); the upload +
// license gate live here on the client. Bidirectional pull/promote were dropped
// by the decision — they return a clear push-only error. The keychain /
// not-logged-in / transport-failure cases render the chat-visible
// "run knowledge login" guidance verbatim (the legacy is_error shape).

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

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

// syncTransportBuilder constructs the OAuth-backed sync Transport. It is a
// package var (defaulting to buildSyncTransport) so the push path stays a thin
// delegation to the Phase 2 builder while tests can inject an httptest-backed
// Transport without touching the real keychain.
var syncTransportBuilder = buildSyncTransport

// syncArgs is the MCP JSON payload the client reads for a sync call. graph and
// name may be empty (defaulted to knowledge/default). Sync is push-only —
// operation must be "push" (or empty); "pull"/"promote" are rejected.
type syncArgs struct {
	Operation string `json:"operation"`
	Graph     string `json:"graph"`
	Name      string `json:"name"`
}

// InterceptSync claims the `sync` tool and runs the client-side push: it fetches
// the serialized graph bytes via the ExportGraph RPC, then uploads them to
// Fulminate Cloud over the OAuth Transport. Returns (false, _) for any other
// tool so the chain falls through. pull/promote return a push-only error; the
// not-logged-in / transport-failure cases render the actionable login guidance.
func InterceptSync(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "sync" {
		return false, kgtools.ToolResult{}
	}
	var a syncArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("sync: invalid args: " + err.Error())
	}

	// Push-only: pull/promote were dropped by the decision.
	if a.Operation != "" && a.Operation != "push" {
		return true, errorResult(fmt.Sprintf(
			"sync: %q is not supported — sync is now push-only (pull/promote were removed)", a.Operation))
	}

	graph := a.Graph
	if graph == "" {
		graph = string(kgtypes.GraphKnowledge)
	}
	name := a.Name
	if name == "" {
		name = "default"
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
	resp, err := exp.ExportGraph(ctx, &knowledgev1.ExportGraphRequest{
		Target: manageGraphSelector(graph, name),
	})
	if err != nil {
		return errorResult(fmt.Sprintf("sync push: export %s/%s: %v", graph, name, err))
	}
	body := resp.GetGraphBytes()
	if err := transport.PushGraph(ctx, graph, name, body); err != nil {
		return errorResult(wrapPushErr(graph, name, err))
	}
	return textResult(fmt.Sprintf("pushed %s/%s (%d bytes)", graph, name, len(body)))
}

// exporterSeam upgrades deps.GraphCaller() to the Exporter seam, or returns a
// typed error so the missing seam is loud (degraded headless mode). Mirrors
// manageIndexer.
func exporterSeam(deps ClientDeps) (Exporter, error) {
	gc := deps.GraphCaller()
	if gc == nil {
		return nil, fmt.Errorf("GraphCaller is unavailable — the client is running in degraded mode")
	}
	exp, ok := gc.(Exporter)
	if !ok {
		return nil, fmt.Errorf("sync requires an ExportGraph-capable graph client")
	}
	return exp, nil
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
	var se *auth.SyncHTTPError
	if errors.As(err, &se) {
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
