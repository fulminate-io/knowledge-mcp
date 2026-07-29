// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// similarityReportArgs is the minimal parsed shape for thoughts(similarity_report):
// an optional id to fetch a specific past pass (default = the latest pass).
// thoughtsArgs carries no id, so this op decodes its own.
type similarityReportArgs struct {
	ID string `json:"id"`
}

// handleSimilarityReportClient is the thoughts(similarity_report) fetch op. It reads
// the latest similarity-pass event (or a specific one by id) through the forcer's
// event-read seam and renders it: running → in-progress + elapsed + estimate +
// may-take-longer; completed → the FULL rendered report verbatim (from the event
// body); failed → the failure loudly; no pass ever → a clear empty-state message.
//
// ctx is the propagated CALLER context, threaded from the tool dispatch through
// interceptThoughtsOp. That is the right context here: the single-event read is a
// bounded synchronous one-shot that finishes inside the call, so canceling the
// call correctly abandons it — unlike the async PASS in the trigger handler, whose
// completion write belongs to the loop that owns its goroutine.
func handleSimilarityReportClient(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) kgtools.ToolResult {
	// Readiness gate (bind-first startup): during the bind-first wiring window the propagation
	// loop is not yet wired and SimilarityForcer() returns nil — which would emit
	// the misleading "not running in this process" message below. Distinguish the
	// transient window so a retry succeeds.
	if !deps.PropReady() {
		return errorResult("thoughts:propagate: daemon still starting — reflection loop not ready yet, retry shortly")
	}
	forcer := deps.SimilarityForcer()
	if forcer == nil {
		return errorResult("similarity_report: reflection loop not running in this process — no similarity passes are tracked here")
	}

	var a similarityReportArgs
	if len(params.Arguments) > 0 {
		if err := json.Unmarshal(params.Arguments, &a); err != nil {
			return errorResult("similarity_report: could not parse arguments — id must be a string: " + err.Error())
		}
	}

	var (
		node  *knowledgev1.Node
		found bool
	)
	if a.ID != "" {
		node, found = forcer.SimilarityEventByID(ctx, a.ID)
	} else {
		node, found = forcer.LatestSimilarityEvent(ctx)
	}
	if !found || node == nil {
		return textResult("No similarity pass has run yet. Trigger one with:\n" +
			`    thoughts({"operation":"propagate","similarity":true})`)
	}

	switch kgtypes.Value(node, clientthought.MetaSimStatus) {
	case clientthought.SimStatusRunning:
		elapsed := ""
		if st, perr := time.Parse(time.RFC3339, kgtypes.Value(node, clientthought.MetaSimStartedAt)); perr == nil {
			elapsed = fmt.Sprintf(" (elapsed %s)", time.Since(st).Round(time.Second))
		}
		return textResult("Topic similarity pass IN PROGRESS" + elapsed + ".\n\n" +
			similarityFetchContract(similarityEstimate(forcer, ctx)))
	case clientthought.SimStatusCompleted:
		// The full rendered report lives in the event body — return it verbatim.
		return textResult(node.GetContent())
	case clientthought.SimStatusFailed:
		body := "Topic similarity pass FAILED.\n"
		if content := node.GetContent(); content != "" {
			body += "\n" + content
		}
		return errorResult(body)
	default:
		// An event with the marker but an unexpected status — surface its body so the
		// state is never silently blank.
		return textResult(node.GetContent())
	}
}
