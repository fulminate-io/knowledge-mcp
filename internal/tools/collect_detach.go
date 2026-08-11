// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// collect_detach.go holds the 60s-detach machinery the collect intercept
// (InterceptCollect, collect.go) routes its builtin path through: the collect
// work unit, the standing-runtime seam, the wait-or-detach race, the early-return
// message, and the per-target single-flight key. Split out of collect.go to keep
// that file under the file-length cap; pure relocation, no behavior change.

// builtinCollectWork runs the builtin collector.Collect for a target plus its
// post-collect tail (repo-manifest record, cross-graph linker, postpopulate hook,
// pipeline wake). It is the unit collectWaitOrDetach runs either synchronously
// (rt==nil fallback) or on the standing runtime's detached goroutine — identical
// work on both paths — and returns collector.Collect's error verbatim.
func builtinCollectWork(ctx context.Context, deps ClientDeps, a collectArgs, opts collector.CollectOptions) error {
	// A collect spikes the heap hard — the chunker holds every file's results
	// until upload, and the precise Go call-graph build loads a whole module + its
	// dependency closure (ASTs + type info + SSA) live at once. This is a
	// long-lived stdio daemon, so once the collect is done that working set is pure
	// garbage; force a GC + scavenge on the way out so RSS drops back to baseline
	// immediately. Deferred HERE (not at the InterceptCollect return) so it fires
	// at ACTUAL completion on BOTH the detached goroutine and the synchronous
	// fallback — never prematurely at the 60s handler return with the heap at peak.
	defer debug.FreeOSMemory()

	if err := collector.Collect(ctx, a.Type, a.ID, opts); err != nil {
		// collector.Collect already wraps with "collect <type>:" — adding our own
		// "collect <type> <id>:" prefix produces a duplicate "collect <type>:"
		// stutter. Return the inner error verbatim; type information survives via
		// the pipeline's wrap.
		return err
	}
	// Record the repo→path mapping in the machine-local manifest so the name→dir
	// consumers (ast cross-repo walk, branch auto-detect, the correct-dir/
	// branch-aware staleness footer) can map this repo's NAME back to where it was
	// collected from on THIS machine. Code only: the manifest keys on the
	// code-graph repo name (filepath.Base(id)), exactly how `collect` keys the
	// graph, and a.ID is already absolute here (the code collector rejects relative
	// paths). Best-effort and machine-LOCAL — never synced.
	recordCollectedRepo(a.Type, a.ID)
	// Post-collect linker tail-call. Replaces the former server-side
	// runPostCollectLinker that ran on the collect-write path. Gated on the same
	// collector types that previously triggered the server-side path. Best-effort:
	// failures slog.Warn but the user-facing textResult is unchanged.
	runPostCollectLinker(ctx, deps, a.Type)
	// Post-collect PostPopulate tail-call, SIBLING to the linker. Runs the
	// registered postpopulate hook for the collector family over the wire,
	// enriching the per-account/per-repo graph with the structural edges the linker
	// does not own (SG/NACL rules, cross-account trust, image lineage, k8s
	// selector/cluster linkage, CICD OIDC federation, codesync hierarchy).
	// Best-effort, like the linker.
	runPostCollectPostPopulate(ctx, deps, a.Type)
	// Wake the LLM pipeline: the just-collected graph may have idle-backed-off its
	// scan cadence toward the hour-long ceiling, so nudge every collector to re-scan
	// now and discover + enrich the freshly-uploaded nodes instead of waiting out
	// its idle interval. Best-effort, optional capability (no pipeline wired →
	// skipped).
	if w, ok := deps.(pipelineWaker); ok {
		w.WakePipeline()
	}
	return nil
}

// collectRuntimeProvider is the OPTIONAL deps capability the collect interceptor
// uses to reach the standing collect runtime (mirrors the pipelineWaker seam
// below). Type-asserted rather than a required ClientDeps method so the many test
// fakes that run no runtime are unaffected; the production *client implements it.
type collectRuntimeProvider interface {
	CollectRuntime() *CollectRuntime
}

// collectWaitOrDetach launches work under the standing collect runtime and caps
// the caller's synchronous wait at rt.DetachAfter() (60s). Behavior:
//   - rt==nil (degraded/router-less client): run work SYNCHRONOUSLY — today's
//     exact behavior (the FreeOSMemory scavenge now fires from work's defer).
//   - already running (single-flight coalesce): return the "already running" text
//     and spawn nothing.
//   - completes within the cap: return successText (byte-identical to today) or the
//     collect error.
//   - exceeds the cap: return the STILL-RUNNING message; the run finishes detached
//     under the runtime.
//
// graph is the BARE code-graph name the run targets (empty for non-code
// collectors). It is recorded on the in-flight entry so the LLM pipeline can hold
// its gap scan off that graph until the collect finishes.
func collectWaitOrDetach(rt *CollectRuntime, key, label, graph, successText string, work func() error) kgtools.ToolResult {
	if rt == nil {
		if err := work(); err != nil {
			return errorResult(err.Error())
		}
		return textResult(successText)
	}
	h, started, elapsed := rt.Start(key, label, graph, work)
	if !started {
		return textResult(fmt.Sprintf(
			"collect of %s already running, started %s ago — not starting a duplicate.", label, elapsed.Round(time.Second)))
	}
	// Race the run against the detach timer with a STOPPED timer (not time.After),
	// so a fast collect winning the race releases the 60s timer immediately via the
	// deferred Stop rather than leaking it until it fires.
	t := time.NewTimer(rt.DetachAfter())
	defer t.Stop()
	select {
	case <-h.Done():
		if err := h.Err(); err != nil {
			return errorResult(err.Error())
		}
		return textResult(successText)
	case <-t.C:
		return textResult(stillRunningMsg(label))
	}
}

// stillRunningMsg is the all-good early-return the collect handler returns when a
// collect exceeds the 60s synchronous-wait cap. It states the run is NOT aborted
// and points at the verbatim copy-pasteable manage(status) call so the calling LLM
// can watch it finish — the long-run identification signal (mirrors the
// copy-pasteable-follow-up contract of the similarity lever handler).
func stillRunningMsg(label string) string {
	return fmt.Sprintf(
		"Collect of %s is STILL RUNNING and will continue in the background — it is NOT aborted, everything is fine. "+
			"It exceeded the %s synchronous-wait cap, so this call returned early while the collect finishes under the daemon. "+
			"View progress / completion with:\n\nmanage({\"operation\":\"status\"})",
		label, collectDetachThreshold.Round(time.Second))
}

// collectTargetKey builds the per-target single-flight key: collector type + a NUL
// separator + a normalized id. Code targets are filepath.Clean'd (an absolute repo
// path — trailing-slash/dot-safe); every other type (URLs, cloud accounts, pdf
// paths) keeps its id verbatim, since filepath.Clean would corrupt a URL.
func collectTargetKey(collectorType, id string) string {
	normID := id
	if collectorType == "code" {
		normID = filepath.Clean(id)
	}
	return collectorType + "\x00" + normID
}

// CollectGateGraphName derives the graph identity recorded alongside a collect
// run, so the LLM pipeline can hold its gap scan off that graph until the collect
// finishes. Code targets only — every other collector type gets "" and gates
// nothing.
//
// BARE BASE NAME, DELIBERATELY UNQUALIFIED BY BRANCH: this is exactly how the code
// collector names the graph it produces, and the pipeline registers one collector
// per base name. Appending a branch would produce a name no collector can ever
// carry, leaving the gate permanently inert.
//
// EXPORTED so a test can pin BOTH sides of that equality to production code — the
// name recorded here against the name the real collector emits — instead of
// hardcoding an expected string and comparing it against itself.
func CollectGateGraphName(collectorType, id string) string {
	if collectorType != "code" {
		return ""
	}
	return filepath.Base(filepath.Clean(id))
}
