// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// handleSimilarityPass triggers the manual topic-similarity lever ASYNCHRONOUSLY
// (thoughts(propagate, similarity:true[, link_threshold, merge_threshold])). It
// acquires the reflection single-flight guard in the trigger path (via the forcer's
// StartSimilarityPass), starts the pass on a daemon-lifetime goroutine, and returns
// IMMEDIATELY with the fetch/estimate contract — it does NOT await the pass or
// render its report to the caller (the pass outlives the 180s tool-call timeout).
// At trigger time onStarted creates the status=running event; at completion
// onComplete renders the report, persists it (status + duration_ms + rendered text)
// via FinishSimilarityEvent, and slogs the report as an audit trail. The persisted
// report is fetched by the similarity_report op by id/marker — event nodes never
// embed, so it is NOT vector-searchable. A nil forcer (reflection loop not running)
// returns a loud error; a coalesce (started=false — a pass already in flight)
// returns the "already running" message plus the SAME fetch/estimate contract and
// creates NO event (onStarted only fires on the real acquire).
func handleSimilarityPass(ctx context.Context, deps ClientDeps, a propagateArgs) kgtools.ToolResult {
	forcer := deps.SimilarityForcer()
	if forcer == nil {
		return errorResult("propagate similarity: reflection loop not running in this process — cannot run the topic-similarity lever")
	}
	// Pre-flight cancellation gate: a caller who already gave up must not start a
	// pass or leave an orphan running event behind. An ALREADY-STARTED pass is not
	// abortable this way — it runs on the PropagationLoop's daemon-lifetime context
	// by design — so this gate is the whole of what caller cancellation controls.
	if err := ctx.Err(); err != nil {
		return errorResult("propagate similarity: call cancelled before the pass started — no pass was triggered")
	}
	// Zero passed through to the lever → the HIGH package-const defaults.
	var link, merge float64
	if a.LinkThreshold != nil {
		link = float64(*a.LinkThreshold)
	}
	if a.MergeThreshold != nil {
		merge = float64(*a.MergeThreshold)
	}

	// Densify overrides; zero-value fields resolve to densify*Default inside the lever.
	var densify clientthought.DensifyParams
	if a.DensifyThreshold != nil {
		densify.Threshold = float64(*a.DensifyThreshold)
	}
	if a.DensifyK != nil {
		densify.K = int(*a.DensifyK)
	}
	if a.DensifyEdgeBudget != nil {
		densify.EdgeBudget = int(*a.DensifyEdgeBudget)
	}

	// id + startedAt flow Begin → handler closures → Finish. onStarted runs
	// SYNCHRONOUSLY inside StartSimilarityPass on the uncontended-acquire path,
	// before the goroutine launches and before StartSimilarityPass returns, so these
	// writes happen-before both onComplete's read and the response build below.
	var eventID string
	var eventStartedAt time.Time

	onStarted := func() {
		// Runs SYNCHRONOUSLY inside StartSimilarityPass, before the goroutine is
		// launched and before the call returns, so the caller's ctx is still live.
		id, st, err := forcer.BeginSimilarityEvent(ctx, link, merge)
		if err != nil {
			// Swallow: the pass still runs. The report just won't be fetchable until
			// completion writes it. Never aborts the pass or changes the response.
			slog.Error("similarity: BeginSimilarityEvent failed — pass still runs, report not fetchable until completion", "err", err)
			return
		}
		eventID = id
		eventStartedAt = st
	}

	// passCtx is supplied by the PropagationLoop that OWNS the pass goroutine — it
	// is that loop's daemon-lifetime context, the same one the pass itself runs on.
	// This write happens minutes after the handler returned, so it cannot ride the
	// caller's ctx: doing so would fail with context canceled on every pass and the
	// report would never be persisted for the similarity_report fetch op.
	onComplete := func(passCtx context.Context, rep clientthought.SimilarityReport, err error) {
		status := "completed"
		if err != nil {
			status = "failed"
		}
		rendered := renderSimilarityReport(rep)
		if eventID != "" {
			// eventStartedAt is non-zero here (Begin succeeded → eventID set).
			durationMs := time.Since(eventStartedAt).Milliseconds()
			if ferr := forcer.FinishSimilarityEvent(passCtx, eventID, eventStartedAt, link, merge, status, durationMs, rendered, headlineCounts(rep)); ferr != nil {
				// Degrade loudly: the pass completed; the report just wasn't persisted.
				slog.Error("similarity: FinishSimilarityEvent failed — pass completed, report not persisted", "err", ferr)
			}
		}
		// Audit trail: the full rendered report in the daemon log. NOT searchable —
		// the event node does not embed; this is a log line, not a vector.
		slog.Info("similarity: pass complete — rendered report follows (audit)", "status", status, "report", rendered)
	}

	if !forcer.StartSimilarityPass(link, merge, densify, onStarted, onComplete) {
		// Coalesce: a pass is already running. Same fetch/estimate contract, no event.
		return textResult("Topic similarity pass: a pass is already running — your trigger coalesced onto it (no second pass started).\n\n" +
			similarityFetchContract(similarityEstimate(forcer, ctx)))
	}
	return textResult("Topic similarity pass STARTED in the background.\n\n" +
		similarityFetchContract(similarityEstimate(forcer, ctx)))
}
