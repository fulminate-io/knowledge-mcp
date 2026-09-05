// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd/bitbucket"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd/github"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd/gitlab"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/pdfcollector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/web"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collect_detach.go holds the 60s-detach machinery the collect intercept
// (InterceptCollect, collect.go) routes its builtin path through: the collect
// work unit, the standing-runtime seam, the wait-or-detach race, the early-return
// message, and the per-target single-flight key. Split out of collect.go to keep
// that file under the file-length cap; pure relocation, no behavior change.

// builtinCollectWork runs the builtin collector.Collect for a target plus its
// post-collect tail (repo-manifest record, cross-graph linker, postpopulate hook,
// pipeline wake, composition verdict). It is the unit collectWaitOrDetach runs
// either synchronously (rt==nil fallback) or on the standing runtime's detached
// goroutine — identical work on both paths.
//
// It returns collector.Collect's error verbatim when the collect itself fails.
// Past that point the returned error joins the postpopulate hook's error with the
// composition verdict, so a harvest that captured nothing usable reports failure.
//
// It returns the RENDERED node-type composition of the run alongside that error,
// so the caller can state what the harvest produced. Empty on the
// collector.Collect error path, where there is no composition to report.
//
// It ALSO returns the name of the graph the collect actually produced, read off
// the collector's own result. That is the OBSERVED name, and it stays distinct
// from the PREDICTED one: a pdf collect's id is a filesystem path while its
// graph name is a derived slug, so a follow-up call naming the id instead would
// name something drop_graph cannot resolve. The two agreeing is exactly what the
// gate-identity test pins.
//
// graphName is the PREDICTED name, derived once by CollectGateGraphName at the
// dispatch and threaded down rather than recomputed here, so the postpopulate
// scope and the gate identity cannot disagree about which graph this run is.
func builtinCollectWork(ctx context.Context, deps ClientDeps, a collectArgs, opts collector.CollectOptions, graphName string) (string, string, error) {
	// A collect spikes the heap hard — the chunker holds every file's parse
	// results live until upload. This is a
	// long-lived daemon process, so once the collect is done that working set is pure
	// garbage; force a GC + scavenge on the way out so RSS drops back to baseline
	// immediately. Deferred HERE (not at the InterceptCollect return) so it fires
	// at ACTUAL completion on BOTH the detached goroutine and the synchronous
	// fallback — never prematurely at the 60s handler return with the heap at peak.
	defer debug.FreeOSMemory()

	// Record the repo→path mapping in the machine-local manifest so the name→dir
	// consumers (ast cross-repo walk, branch auto-detect, the correct-dir/
	// branch-aware staleness footer) can map this repo's NAME back to where it was
	// collected from on THIS machine. Code only: the manifest keys on the
	// code-graph repo name (filepath.Base(id)), exactly how `collect` keys the
	// graph. Best-effort and machine-LOCAL — never synced.
	//
	// BEFORE the collect, not after, because the collect's own sink admits the
	// graph into the working set, and the background loops that wake on that
	// admission ask the manifest whether this machine holds the checkout. Recorded
	// afterwards, the FIRST EVER collect of a repo is evaluated as absent and
	// registers no collector until some unrelated graph is next admitted. Ordering
	// removes that window rather than compensating for it.
	//
	// It carries its OWN absolute-path guard: the previous position could rely on
	// collector.Collect having already rejected a relative path, and this one
	// cannot. THE COST, stated rather than hidden: a collect that subsequently
	// FAILS now leaves a manifest entry it previously would not have. That entry
	// maps a name to a directory that demonstrably exists, so the name→dir
	// consumers resolve it correctly; the only effect is that a failed first
	// collect leaves the repo eligible for background work it has no graph for,
	// which the loops handle as an empty graph.
	if filepath.IsAbs(a.ID) {
		recordCollectedRepo(a.Type, a.ID)
	}
	comp, err := collector.Collect(ctx, a.Type, a.ID, opts)
	if err != nil {
		// collector.Collect already wraps with "collect <type>:" — adding our own
		// "collect <type> <id>:" prefix produces a duplicate "collect <type>:"
		// stutter. Return the inner error verbatim; type information survives via
		// the pipeline's wrap.
		return "", "", err
	}
	// Post-collect linker tail-call. Replaces the former server-side
	// runPostCollectLinker that ran on the collect-write path. Gated on the same
	// collector types that previously triggered the server-side path. Best-effort:
	// failures slog.Warn but the user-facing textResult is unchanged.
	runPostCollectLinker(ctx, deps, a.Type)
	// Post-collect PostPopulate tail-call, SIBLING to the linker. Runs the
	// registered postpopulate hook for the collector family over the wire,
	// enriching the per-account/per-repo graph with the structural edges the linker
	// does not own (SG/NACL rules, cross-account trust, image lineage, k8s
	// selector/cluster linkage, CICD OIDC federation, step→code links).
	//
	// UNLIKE the linker above, this is NOT best-effort: its error is captured and
	// returned below, so a collect whose enrichment failed reports failure to the
	// caller instead of succeeding with a warn in the log.
	//
	// The collected graph name rides along because a hook that declared
	// BreadthScoped fires against THAT graph alone rather than every graph of the
	// family's type. It is the name CollectGateGraphName predicted at the dispatch,
	// threaded in rather than recomputed. Only code registers a BreadthScoped hook
	// today; every other family's hook declares BreadthFamilyBroad, whose arm
	// ignores the name.
	ppErr := runPostCollectPostPopulate(ctx, deps, a.Type, graphName)
	// Wake the LLM pipeline: the just-collected graph may have idle-backed-off its
	// scan cadence toward the hour-long ceiling, so nudge every collector to re-scan
	// now and discover + enrich the freshly-uploaded nodes instead of waiting out
	// its idle interval. Best-effort, optional capability (no pipeline wired →
	// skipped). Deliberately fired BEFORE the error return: a failed enrichment
	// hook must not also suppress the pipeline nudge, since the nodes the collect
	// DID upload still need summarizing. The SAME reasoning governs the
	// composition verdict below: the nodes were written, so the linker, the
	// postpopulate hook and this wake all run BEFORE any verdict is returned.
	if w, ok := deps.(pipelineWaker); ok {
		w.WakePipeline()
	}
	// The composition verdict — the SINGLE top-level dispatch of the per-collector
	// invariant, so a harvest that captured nothing usable reports failure instead
	// of plain success. It is here rather than inside collector.Collect because
	// the four cloud collectors call Collect recursively per cascade target, and an
	// assertion inside Collect would fire per cascade sub-collect and let a
	// sub-collect's composition fail the parent.
	//
	// DESTROY-BEFORE-PERSIST: this runs strictly AFTER sink.WriteResult. Nothing is
	// deleted, skipped or rolled back — the graph the harvest produced stays on the
	// server exactly as today, which is what made the originating incident
	// diagnosable at all. Only the REPORT changes.
	return comp.Render(), comp.GraphName, errors.Join(ppErr, collector.CheckComposition(a.Type, comp))
}

// rawCollectDropDirective renders the follow-up call a RAW document collect owes
// its operator, and the empty string for every other collect.
//
// THE CLASSIFIER IS THE POINT. Raw web and pdf graphs are staging: they are
// collected so a recipe can read them into golden practice content, and once
// that content exists the raw graph is dead weight that accumulates on disk and
// in every catalog listing. Every OTHER family — code, cloud, cicd, practice,
// knowledge and the rest — is the durable artifact itself, and telling an
// operator to drop one would be telling them to destroy the thing they just
// built. The empty string for those families is a refusal to speak, not a
// degraded message.
//
// THE TARGET IS THE PRODUCED GRAPH, NEVER THE COLLECT ID. producedGraph comes
// from the collector's own CollectResult.GraphName; for pdf that is a derived
// slug and the id is a filesystem path, which drop_graph cannot resolve.
//
// AN EMPTY producedGraph YIELDS NOTHING rather than a directive carrying an
// empty target: a rendered call that would drop nothing, or be refused, is worse
// than silence because it reads as an instruction.
func rawCollectDropDirective(collectorType, producedGraph string) string {
	if producedGraph == "" {
		return ""
	}
	switch kgtypes.GraphType(collectorType) {
	case kgtypes.GraphWebRaw, kgtypes.GraphPDFRaw:
	default:
		return ""
	}
	return fmt.Sprintf(
		"This %s graph is RAW and TEMPORARY — it exists to be read into golden content, "+
			"and once that content exists it should be dropped rather than left to accumulate. "+
			"Drop it with:\n\nmanage({\"operation\":\"drop_graph\",\"graph\":%q,\"name\":%q})",
		collectorType, collectorType, producedGraph)
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
//   - completes within the cap: return successText suffixed with the run's
//     rendered composition, or the collect error. A run that reports no
//     composition returns successText byte-identically to today.
//   - fails, on either of the two arms above: return the collect error suffixed
//     with the SAME raw-graph drop directive the success arms carry. A refused
//     harvest is the one outcome that leaves behind a graph nobody wants, so it is
//     the outcome that most owes the operator the call that removes it. The
//     classifier still decides: a failed CODE collect is told nothing, and a
//     failure reporting no produced graph renders no call rather than a call with
//     an empty target.
//   - exceeds the cap: return the STILL-RUNNING message; the run finishes detached
//     under the runtime. The composition is readable for that run through
//     manage(status), which is the surface the detach decision designated for
//     detached outcomes.
//
// gt and graph are the PAIR that identifies the run: the family the collect
// lands in, and the bare graph name inside that family, both as predicted by
// collectGateGraphIdentity. Both are empty for a collector family the derivation
// does not name — after this change aws and the logs collector alone. NEITHER
// HALF IDENTIFIES THE RUN ON ITS OWN, because two families can carry the same
// name and the pipeline registers one collector per (family, name). The pair is
// recorded on the in-flight entry so the LLM pipeline can hold its gap scan off
// that graph until the collect finishes.
//
// notice is the pre-walk legacy-graph report, appended to BOTH the completed
// answer and the STILL-RUNNING one. Both, because a detached collect's caller is
// exactly as entitled to it as a fast one's — and the still-running message is
// the only answer they will get. An empty notice degrades to the text unchanged,
// which is what keeps a collect that has no legacy graphs byte-identical to
// today.
func collectWaitOrDetach(rt *CollectRuntime, collectorType, key, label string, gt kgtypes.GraphType, graph, successText, notice string, work func() (string, string, error)) kgtools.ToolResult {
	if rt == nil {
		composition, producedGraph, err := work()
		if err != nil {
			return errorResult(withComposition(err.Error(),
				rawCollectDropDirective(collectorType, producedGraph)))
		}
		return textResult(withComposition(
			withComposition(
				withComposition(successText, composition),
				rawCollectDropDirective(collectorType, producedGraph)),
			notice))
	}
	h, started, elapsed := rt.Start(key, label, gt, graph, work)
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
			return errorResult(withComposition(err.Error(),
				rawCollectDropDirective(collectorType, h.ProducedGraph())))
		}
		return textResult(withComposition(
			withComposition(
				withComposition(successText, h.Composition()),
				rawCollectDropDirective(collectorType, h.ProducedGraph())),
			notice))
	case <-t.C:
		return textResult(withComposition(stillRunningMsg(label), notice))
	}
}

// withComposition suffixes a collect RESULT text — success or failure alike —
// with a rendered addendum: the run's node-type composition, so the caller sees
// WHAT the harvest produced without a follow-up query, or the raw-graph drop
// directive, so a caller left holding a staging graph is told how to remove it.
// An empty addendum degrades to the result text unchanged — the same
// empty-degrades-to-nothing contract the manage(status) renderers carry.
func withComposition(successText, composition string) string {
	if composition == "" {
		return successText
	}
	return successText + " " + composition
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

// CollectGateGraphName is THE ONE PRODUCTION DERIVATION of the graph name a
// collect will produce. It is computed from the REQUEST alone — the collector
// type, the id and the seed URLs — and is therefore available BEFORE the walk,
// which is what lets a caller act on the name without paying for a crawl or a
// parse first.
//
// Its four consumers all read this one answer rather than deriving their own:
// the in-flight gate identity recorded on the collect runtime, the graph a
// BreadthScoped postpopulate hook fires against, the collision precheck that
// refuses a collect landing on another source's graph, and the name reported
// back to the caller. A second derivation anywhere would drift from this one
// silently, and a predicted name matching no collector gates nothing at all.
//
// The per-family rules live WITH their families and are called here, never
// re-implemented: pdfcollector.SourceSlug, web.GraphName, and each cicd
// provider's own GraphName.
//   - code: the bare base name, DELIBERATELY UNQUALIFIED BY BRANCH. This is
//     exactly how the code collector names the graph it produces, and the
//     pipeline registers one collector per base name; appending a branch would
//     produce a name no collector can ever carry, leaving the gate inert.
//   - pdf: an id that is not absolute is an ERROR naming it. PDFCollector.Collect
//     refuses a relative path too, so a name derived from one would name a graph
//     that can never exist.
//   - web: web.GraphName, which returns an explicit id verbatim and otherwise
//     derives the name from the first seed URL's host.
//   - gcp, azure and k8s: the collect id VERBATIM. Each of those collectors names
//     its graph after the id it was handed — gcp's resolveProject returns a
//     non-empty id unchanged, azure assigns it to subscriptionID, and k8s
//     resolves it as the requested kube context — and the dispatch's own
//     'id' is required guard runs before this derivation's call site, so the
//     empty-id fallbacks those three carry are unreachable from here and the
//     derivation is EXACT rather than approximate.
//   - github, gitlab and bitbucket: each provider package's own GraphName, the
//     same function that provider's collector fills CollectResult.GraphName from.
//   - AWS DELIBERATELY DERIVES NOTHING, and its absence is a decision rather than
//     an omission: the aws collector discards the collect id entirely and names
//     its graph from the account id an STS GetCallerIdentity call returns DURING
//     the walk. A name derived from the request would not merely be unhelpful, it
//     would be WRONG — it would hold the gap-scan gate over a graph nobody is
//     collecting, for up to collectGateMaxHold, while the real account graph
//     scanned unguarded. Do not add an aws arm believing it was forgotten.
//   - every remaining collector type: the empty string and no error.
//
// EXPORTED so a test can pin BOTH sides of the identity equality to production
// code — the name predicted here against the name the real collector emits —
// instead of hardcoding an expected string and comparing it against itself.
func CollectGateGraphName(collectorType, id string, seedURLs []string) (string, error) {
	switch collectorType {
	case "code":
		return filepath.Base(filepath.Clean(id)), nil
	case string(kgtypes.GraphPDFRaw):
		if !filepath.IsAbs(id) {
			return "", fmt.Errorf(
				"collect pdf: id %q must be an absolute path; the graph is named after the file, "+
					"and a relative path names a graph the collect itself would refuse", id)
		}
		return pdfcollector.SourceSlug(id), nil
	case string(kgtypes.GraphWebRaw):
		return web.GraphName(id, seedURLs)
	// Plain string literals rather than the neighboring `case
	// string(kgtypes.GraphPDFRaw)` spelling: pdf and web are collector types that
	// ARE graph types, and these are not — gcp collects into the cloud family — so
	// there is no constant to spell them with.
	case "gcp", "azure", "k8s":
		return id, nil
	case "github":
		return github.GraphName(id), nil
	case "gitlab":
		return gitlab.GraphName(id), nil
	case "bitbucket":
		return bitbucket.GraphName(id), nil
	default:
		return "", nil
	}
}

// collectGateGraphIdentity is the FAMILY HALF of the collect gate's identity,
// paired with the name half CollectGateGraphName derives. It returns the (family,
// name) pair a collect records on the in-flight runtime, or a zero pair when this
// collector family derives no name at all.
//
// IT DELEGATES THE NAME rather than re-deriving it: a second derivation would
// drift from CollectGateGraphName silently, and a predicted name matching no
// collector's name gates nothing.
//
// WHY A SWITCH RATHER THAN kgtypes.GraphType(collectorType). For code, web and
// pdf the collector type string and the graph type string are equal
// (kgtypes.GraphCode is "code", GraphWebRaw is "web", GraphPDFRaw is "pdf"), so a
// bare conversion would compile and pass for them. For the six cloud and cicd
// families it is FALSE and not close: "gcp" collects into the cloud family and
// "github" into cicd, so a conversion would mint the graph types "gcp" and
// "github", which no collector and no pipeline collector registration ever
// carries — a recorded identity that can never match, which is the exact silent
// inertness this gate exists to avoid.
//
// THE DEFAULT ARM ERRORS rather than returning a zero family, per the repo
// invariant that bad input always errors rather than degrading. It is UNREACHABLE
// TODAY BY CONSTRUCTION — every collector type the name derivation names has an
// arm here — and that is the point: the next collector added to the name half
// cannot reach production without declaring its family here too.
func collectGateGraphIdentity(collectorType, id string, seedURLs []string) (kgtypes.GraphType, string, error) {
	name, err := CollectGateGraphName(collectorType, id, seedURLs)
	if err != nil || name == "" {
		return "", "", err
	}
	switch collectorType {
	case "code":
		return kgtypes.GraphCode, name, nil
	case "web":
		return kgtypes.GraphWebRaw, name, nil
	case "pdf":
		return kgtypes.GraphPDFRaw, name, nil
	case "gcp", "azure", "k8s":
		return kgtypes.GraphCloud, name, nil
	case "github", "gitlab", "bitbucket":
		return kgtypes.GraphCICD, name, nil
	default:
		return "", "", fmt.Errorf("collect %s: the graph-name derivation named a graph but no graph type is declared for this collector type", collectorType)
	}
}
