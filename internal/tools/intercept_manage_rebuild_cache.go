// SPDX-License-Identifier: Apache-2.0

// intercept_manage_rebuild_cache.go — client-side manage(rebuild_cache)
// intercept. rebuild_cache DROPS a builtin graph's per-graph content-hash
// caches (summary + embed) — the code graph (keyed per repo) or the knowledge
// graph (keyed on its "default" instance) — and RE-DERIVES them from the CURRENT
// base-graph nodes with ZERO model calls. It is the ONLY escape hatch for the
// caches — a FREE re-derivation, NOT a "clear" (a clear would guarantee a full
// re-pay). It serves recovery (lost/corrupted cache), manual invalidation (the
// deferred model/prompt-change lever), and backfill/migration (graphs populated
// before the feature shipped). The server does the drop + re-derive work; this
// handler only lowers the args to one IndexRequest and renders the started-ack.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// handleClientRebuildCache drives the Index rebuild_cache op. The content-hash
// caches exist for the builtin code and knowledge graphs (v1), so it requires
// graph=code (name=repo) or graph=knowledge (name defaults to "default", the one
// canonical instance — BASE layer only, no "@"-overlay names). Overlay names are
// rejected symmetrically with rebuild_segments. Fires ONE Index RPC and renders
// the started-ack.
func handleClientRebuildCache(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	ix, err := manageIndexer(deps)
	if err != nil {
		return errorResult("manage(rebuild_cache): " + err.Error())
	}
	if a.Graph != string(kgtypes.GraphCode) && a.Graph != string(kgtypes.GraphKnowledge) {
		return errorResult(`manage(rebuild_cache) requires graph="code" or graph="knowledge" — the content-hash caches are builtin-graph only`)
	}
	// The builtin knowledge graph has one canonical instance named "default"; an
	// empty name (or the "knowledge" alias) resolves to it. BASE layer only in v1 —
	// an "@"-suffixed overlay/session name is rejected, mirroring rebuild_segments
	// so the two operator levers treat overlay names symmetrically.
	if a.Graph == string(kgtypes.GraphKnowledge) {
		if strings.ContainsRune(a.Name, '@') {
			return errorResult(fmt.Sprintf(`manage(rebuild_cache): knowledge overlay name %q is not supported — overlay rebuilds not supported in v1 (base "default" layer only)`, a.Name))
		}
		if a.Name == "" || a.Name == string(kgtypes.GraphKnowledge) {
			a.Name = "default"
		}
	}
	if a.Name == "" {
		return errorResult(`manage(rebuild_cache) requires "name" — the repo whose caches to re-derive`)
	}

	resp, ierr := ix.Index(ctx, &knowledgev1.IndexRequest{
		Target:    manageGraphSelector(a.Graph, a.Name),
		Operation: knowledgev1.IndexRequest_INDEX_OP_REBUILD_CACHE,
	})
	if ierr != nil {
		return errorResult("manage(rebuild_cache): " + ierr.Error())
	}
	// The op is ASYNC: the server drops + re-derives the caches on a background
	// goroutine and acknowledges immediately (no derived count is known at
	// return). Completion is reported through TWO channels, because neither one
	// reaches every operator: the "rebuild_cache.complete" log line for whoever
	// can read the server's stderr, and the recorded outcome below — re-run this
	// op to read it — for whoever cannot.
	return textResult(fmt.Sprintf(
		"rebuild_cache started for %s/%s — dropping + re-deriving the summary/embed caches "+
			"from base nodes in the background (no model calls). Watch the server logs for "+
			"\"rebuild_cache.complete\" to confirm completion, or re-run this op to read the "+
			"recorded outcome.%s",
		a.Graph, a.Name, renderPreviousRebuildOutcome(resp.GetResultJson())))
}

// rebuildAckPayload is the shape handleClientRebuildCache reads out of the
// started-ack's result_json. Only "previous" is read; the other keys the server
// marshals are ignored here.
type rebuildAckPayload struct {
	Previous struct {
		Present bool `json:"present"`
		Outcome *struct {
			State      string `json:"state"`
			Stage      string `json:"stage"`
			Error      string `json:"error"`
			Derived    int64  `json:"derived"`
			StartedAt  string `json:"started_at"`
			FinishedAt string `json:"finished_at"`
		} `json:"outcome"`
		ReadError string `json:"read_error"`
		RawLen    int    `json:"raw_len"`
	} `json:"previous"`
}

// renderPreviousRebuildOutcome turns the started-ack's result_json into the
// trailing sentence describing the PREVIOUS rebuild, or "" when there is nothing
// to add.
//
// FOUR STATES, KEPT DISTINCT ON PURPOSE. An EMPTY result_json is a legitimate
// "this server sent no payload" and appends nothing, leaving the ack exactly as
// it was. A parseable payload with no recorded previous run says so explicitly,
// which is different from silence. A recorded outcome is named in full. And a
// payload that does NOT parse is reported LOUDLY rather than rendered as
// silence: a malformed marker is a real condition, and quietly showing the
// operator nothing would reproduce the precise defect this reporting exists to
// close. None of these fails the op — the rebuild WAS started, and saying
// otherwise would be false.
func renderPreviousRebuildOutcome(resultJSON []byte) string {
	if len(resultJSON) == 0 {
		return ""
	}
	var payload rebuildAckPayload
	if err := json.Unmarshal(resultJSON, &payload); err != nil {
		return fmt.Sprintf("\n\nWARNING: the server's rebuild acknowledgement (%d bytes) could not be parsed, "+
			"so the previous run's outcome is unknown: %v", len(resultJSON), err)
	}
	p := payload.Previous
	if p.ReadError != "" {
		return fmt.Sprintf("\n\nWARNING: a previous rebuild outcome is recorded but could not be read "+
			"(%d raw bytes): %s", p.RawLen, p.ReadError)
	}
	if !p.Present || p.Outcome == nil {
		return "\n\nNo previous rebuild outcome is recorded for this graph."
	}
	o := p.Outcome
	switch o.State {
	case "complete":
		return fmt.Sprintf("\n\nPrevious rebuild: complete — %d entries derived, finished %s.", o.Derived, o.FinishedAt)
	case "failed":
		return fmt.Sprintf("\n\nPrevious rebuild: FAILED at stage %q — %s (finished %s).", o.Stage, o.Error, o.FinishedAt)
	default:
		return fmt.Sprintf("\n\nPrevious rebuild: %s (started %s).", o.State, o.StartedAt)
	}
}
