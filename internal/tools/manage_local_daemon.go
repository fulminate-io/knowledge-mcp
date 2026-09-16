package tools

import (
	"context"
	"fmt"
	"maps"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// addLocalDaemonJSON populates the LOCAL-DAEMON facts a running local daemon
// knows independent of cloud login — pid, graph_path, the local
// node/edge/vector/bm25 counts, the client-side pipeline counters +
// pipeline_enabled, transcript-upload health, in-flight/last collect runs, the
// per-graph coverage[] table, the doctor[] diagnostic block, and the calling
// request's account routing (account/account_source/session/session_source) —
// into m. It is
// called from BOTH the logged-out local status branch AND the logged-in cloud
// branch (CEO decision: always show local-daemon fields) so a logged-in user's
// Daemon Status page still populates the Doctor / Pipeline / Coverage cards + the
// header. The cloud caller layers backend/host + CLOUD graph node/edge/vector
// totals ON TOP after this returns.
//
// Degrades cleanly: when the local graph server is not up (nil LocalLiveness or
// not Healthy) the server-derived fields (pid, graph_path, local counts) are
// omitted — the web types mark them optional. pipeline_enabled always lands
// (false when no pipeline is wired). Each overlay reuses the existing
// optional-interface degrade helpers (overlayPipelineMetrics /
// transcriptUploadHealth / collectRunSnapshot / collectCoverageRows /
// doctorChecks) — one call site each, so this is the single place the Phase-1
// contract rule (every web field present on every path) is enforced.
func addLocalDaemonJSON(ctx context.Context, deps ClientDeps, m map[string]any) {
	if gc := deps.LocalLiveness(); gc != nil && gc.Healthy() {
		if status, err := gc.Status(); err == nil {
			// Copy the local server-status core (pid/graph_path + local
			// node/edge/vector/bm25 counts). A cloud caller overwrites
			// nodes/edges/binary_vectors with CLOUD totals after this returns.
			maps.Copy(m, status)
		}
	}
	_, pipelineOK := overlayPipelineMetrics(deps, m)
	m["pipeline_enabled"] = pipelineOK
	if th, ok := transcriptUploadHealth(deps); ok {
		addTranscriptHealthJSON(m, th)
	}
	if uh, ok := updateCheckHealth(deps); ok {
		addUpdateHealthJSON(m, uh)
	}
	if runs, ok := collectRunSnapshot(deps); ok {
		addCollectRunsJSON(m, runs)
	}
	rows, coverageErr := collectCoverageRows(ctx, deps)
	if len(rows) > 0 {
		m["coverage"] = rows
	}
	// The JSON arm names a walk failure on its own key rather than inside the
	// coverage[] block, whose per-row shape is pinned to exactly ten keys. A
	// consumer reading coverage[] alone would otherwise see a shorter list with
	// nothing to say a family was dropped.
	if coverageErr != nil {
		m["coverage_error"] = coverageErr.Error()
	}
	if checks, ok := doctorChecks(ctx, deps); ok {
		m["doctor"] = checks
	}
	addHarnessSessionJSON(ctx, m)
	addRequestAccountJSON(ctx, deps, m)
}

// addHarnessSessionJSON reports WHICH harness session made this very call and
// how the daemon knows: `session` (empty when unresolved), `session_source`
// (one of the session.HarnessSource* vocabulary) and `session_reason` (which
// carrier was missing, empty on a clean resolution).
//
// ALL THREE ALWAYS LAND, ON EVERY ARM manage(status) has: the logged-out local
// JSON arm, the logged-in cloud JSON arm, and the not_running JSON arm, which
// builds its own map and therefore calls this directly. An absent key and an
// unresolved session are different facts and a consumer must be able to tell
// them apart. An unresolved call reports source `none` with an empty id — the
// daemon never substitutes the transport session, the peer cwd or anything else
// for an identity it was not given.
//
// The TEXT arms render the same three facts through renderHarnessSessionText.
func addHarnessSessionJSON(ctx context.Context, m map[string]any) {
	hs := session.HarnessSessionFromContext(ctx)
	m["session"] = hs.ID
	m["session_source"] = hs.Source
	m["session_reason"] = hs.Reason
}

// renderHarnessSessionText is the TEXT render of the same three facts, one
// line. The default format of manage(status) is text, so without this an agent
// or a human running it plainly saw nothing at all about the session it was
// calling under — the JSON arms alone satisfy the web page and no one else.
//
// THE REASON RIDES ANY RENDER THAT HAS ONE, resolved or not. A reason is not
// the consolation prize for an unresolved call: the commonest degraded state in
// production is a RESOLVED one — a Codex session resolved by `_meta` carrying
// ReasonCodexHookInert, which is where every fresh Codex install sits until the
// user trusts the hook in the TUI, and Codex says nothing about it. Printing
// the id and dropping the reason there hides the only actionable half of the
// line from the one person who can act on it.
func renderHarnessSessionText(ctx context.Context) string {
	hs := session.HarnessSessionFromContext(ctx)
	line := fmt.Sprintf("\n  Harness session: none — %s", hs.Reason)
	if hs.Resolved() {
		line = fmt.Sprintf("\n  Harness session: %s (%s)", hs.ID, hs.Source)
		if hs.Reason != "" {
			line += " — " + hs.Reason
		}
	}
	return line
}

// addRequestAccountJSON records WHICH Fulminate account this status request was
// answered for and WHICH level decided it (header, session, global, none).
//
// It is emitted on EVERY json arm, including the one reporting no running local
// graph server, because it describes the calling REQUEST rather than the
// daemon's local state: a request resolves an account whether or not a local
// server is up. That is also what lets a web page detect a daemon predating
// per-request accounts — the absence of account_source means exactly that, on
// every arm, rather than "this arm does not say".
func addRequestAccountJSON(ctx context.Context, deps ClientDeps, m map[string]any) {
	account, source, err := auth.RequestAccount(ctx)
	if err != nil {
		// The ladder refused rather than resolved — an account the gateway has
		// been watched reject, or a binding store that cannot be read. The
		// status says so instead of reporting an account nothing would serve.
		account, source = "", auth.AccountSourceNone
	}
	m["account"] = account
	m["account_source"] = string(source)
	// THE REASON RIDES EVERY RENDER, empty when there is nothing to explain.
	// It is what keeps a logged-out daemon from reporting a header's account as
	// the account it answered for: the request resolved none, and this says why
	// the header did not decide it. Mirrors session_reason.
	reason := graphclient.RequestAccountReason(ctx)
	if err != nil {
		reason = err.Error()
	}
	m["account_reason"] = reason
	// THE MACHINE-WIDE SELECTION, always, beside the account that was actually
	// used. Requirement 6 names it separately from the effective account for a
	// reason a header- or session-bound caller feels immediately: with only the
	// effective account reported there is no way to tell a binding that TOOK
	// from a selection that happened to name the same account, nor to see what
	// the daemon falls back to when the binding expires. Empty when nothing is
	// selected, which is a state and not an omission.
	m["account_global"] = auth.SelectedAccount().ID(ctx)
	// AND WHETHER THE SESSION BINDINGS ARE IN EFFECT AT ALL. An unreadable
	// binding store is reported rather than swallowed: bindings do not apply
	// while it holds, and a status that stayed silent would leave a user
	// hunting a routing change that never took.
	if store := sessionAccountStore(deps); store != nil {
		if storeErr := store.Check(); storeErr != nil {
			m["session_binding_store_error"] = storeErr.Error()
		}
	}
}

// overlayPipelineMetrics reads the client-side LLM pipeline counters and
// merges them into the status map (so format=json carriers the live
// values too). Returns (Metrics, true) when the deps satisfy the
// optional pipelineMetricser interface AND the pipeline was wired;
// (zero, false) when either the type assert misses (test fakes) or the
// pipeline is disabled (--no-llm-pipeline). Callers render the disabled
// case visibly so it doesn't look like the pipeline silently failed.
func overlayPipelineMetrics(deps ClientDeps, status map[string]any) (pipeline.Metrics, bool) {
	pm, ok := deps.(pipelineMetricser)
	if !ok {
		return pipeline.Metrics{}, false
	}
	m, wired := pm.PipelineMetrics()
	if !wired {
		return pipeline.Metrics{}, false
	}
	status["summary_queued"] = float64(m.SummaryQueued)
	status["summary_running"] = float64(m.SummaryRunning)
	status["summary_succeeded"] = float64(m.SummarySucceeded)
	status["summary_failed"] = float64(m.SummaryFailed)
	status["embed_queued"] = float64(m.EmbedQueued)
	status["embed_running"] = float64(m.EmbedRunning)
	status["embed_succeeded"] = float64(m.EmbedSucceeded)
	status["embed_failed"] = float64(m.EmbedFailed)
	return m, true
}
