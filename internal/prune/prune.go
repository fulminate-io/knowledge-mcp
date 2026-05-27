// SPDX-License-Identifier: Apache-2.0

// Package prune is the client-side auto-prune runner. The stdio MCP
// client (cmd/knowledge) calls Run at startup; Run reads the
// [retention] section from the loaded config and issues a delete RPC
// per non-empty node-type window against the local knowledge-server.
//
// Server-side pruneOnStartup is gone (BCN7) — server has no retention
// policy of its own; clients drive their own retention windows via the
// existing `delete(older_than:..., type:...)` MCP tool.
package prune

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// perCallTimeout caps each per-type prune RPC against an unhealthy or
// slow server. The parent context still governs broader cancellation;
// the more-restrictive of the two wins inside context.WithTimeout.
const perCallTimeout = 60 * time.Second

// GraphCaller is the minimal seam the runner needs from a graph client: run one
// declarative ExecuteRequest. The production caller passes a
// *graphclient.GraphClient (cmd/knowledge/internal/graphclient/client.go Execute matches structurally).
// Tests inject a fake.
//
// prune fires its delete via the Execute carrier (compiled MutationPlan). Defined
// here (not imported from cmd/knowledge/internal/tools) so the prune package has
// no upward import on tools — a depth-3 internal coupling would otherwise emerge.
type GraphCaller interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// Run reads the loaded [retention] config and fires a delete RPC for the
// session retention window when configured.
//
// Returns nil when the section is absent or the field is empty; this is
// a strictly opt-in feature with no built-in defaults.
func Run(ctx context.Context, gc GraphCaller) error {
	sessions := config.RetentionSessions()
	if sessions == "" {
		slog.Debug("prune: no [retention] section configured; skipping")
		return nil
	}
	return pruneType(ctx, gc, "session", sessions)
}

// pruneType lowers a single prune-by-age delete (older_than:older, type:nodeType)
// onto the Execute carrier seam: engine.Compile("delete", ...) produces a
// MUTATION_KIND_DELETE plan with Selection{NodeType: pruneTypeAlias(type),
// FieldPredicates:[{created_at, OP_LT, Now-older}]} (the Phase-1 compileDelete
// arm; created_at OP_LT is strict so a session whose created_at == cutoff
// survives, matching legacy handlePruneHistory). The duration parse + type
// alias run client-side in compileDelete; a bad duration / unknown type yields
// ok=false → surfaced as an error here.
func pruneType(ctx context.Context, gc GraphCaller, nodeType, older string) error {
	callCtx, cancel := context.WithTimeout(ctx, perCallTimeout)
	defer cancel()

	args, err := json.Marshal(map[string]any{
		"older_than": older,
		"type":       nodeType,
	})
	if err != nil {
		return fmt.Errorf("prune: marshal %s args: %w", nodeType, err)
	}

	req, ok := engine.Compile("delete", args)
	if !ok {
		slog.Warn("prune: delete args not reducible", "type", nodeType, "older_than", older)
		return fmt.Errorf("prune: server rejected %s prune: invalid older_than/type", nodeType)
	}
	resp, err := gc.Execute(callCtx, req)
	if err != nil {
		slog.Warn("prune: delete RPC failed", "type", nodeType, "older_than", older, "error", err)
		return fmt.Errorf("prune: delete %s: %w", nodeType, err)
	}
	// Render the affected-count result ("Deleted N node(s)") for the slog line,
	// reusing the engine's delete render (matches the LLM-facing delete tool).
	res, rerr := engine.Render("delete", args, resp)
	if rerr != nil {
		slog.Info("prune: complete", "type", nodeType, "older_than", older)
		return nil
	}
	slog.Info("prune: complete", "type", nodeType, "older_than", older, "result", firstText(res))
	return nil
}

// firstText returns the first text content block from a ToolResult, or
// the empty string when none is present. Server-side handlers always
// emit a single text block on both success and error paths, so callers
// can assume the first block is the human-readable message.
func firstText(r kgtools.ToolResult) string {
	for _, b := range r.Content {
		if b.Type == "text" {
			return b.Text
		}
	}
	return ""
}
