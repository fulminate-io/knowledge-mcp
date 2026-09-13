// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime/debug"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/web"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/recipe"
)

// InterceptCollect intercepts the `collect` MCP tool call and runs the
// collector client-side, streaming chunks to the server via the
// RemoteUploadSink. Returns (true, result) when the call was handled; the
// caller forwards to the server only if this returns false.
//
// Every BUILT-IN collector type runs through this path: code (via
// handleClientReindexCode), github / gitlab / bitbucket / web / pdf all run
// client-side with opts.Sink = deps.Sink(). Web threads CrawlOptions through
// ctx; PDF takes id as an absolute path to a .pdf file and dispatches directly
// to collector.Collect — no per-type context plumbing is required because the
// chunker reads everything from the file itself.
//
// THE CLOUD AND LOG PROVIDER TYPES ARE NO LONGER BUILT IN. aws, gcp, azure,
// k8s, cloudwatch, loki and stackdriver are contrib collectors: separate
// binaries registered as custom graph types, collected through the
// lookupCustomCollector path below. A collect naming one of them with nothing
// registered under that name is refused by the registry with the message that
// names the custom-collector route, which is the whole reason that message
// names a route at all.
func InterceptCollect(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "collect" {
		return false, kgtools.ToolResult{}
	}
	if err := rejectUndeclaredParams("collect", "", CollectToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}

	var a collectArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("invalid arguments: " + decodeArgsError(params.Arguments, err))
	}
	if a.Type == "" {
		return true, errorResult("collect: 'type' is required")
	}
	// A RETIRED BUILTIN FAMILY IS REFUSED BY NAME, ahead of every other
	// disposition. `cloud` and `logs` named real collect types one release ago,
	// and IsBuiltinGraphType no longer claims them, so without this they would
	// fall through to the custom-collector resolver and be reported as names
	// nobody registered — an answer that reads as a typo for a call that used to
	// work. The retirement sentence names the route that replaced them.
	if reason, retired := kgtypes.RetiredGraphTypeReason(a.Type); retired {
		return true, errorResult("collect " + a.Type + ": graph type is retired: " + reason)
	}
	// Readiness gate (bind-first startup): a collect ships chunks the client-side LLM pipeline
	// drains (summary + embed + segment ship). During the bind-first wiring window
	// the pipeline is not yet wired, so a collect would upload chunks with nothing
	// to drain them. Gate it on PipelineReady so the operator retries once the
	// pipeline attaches rather than collecting into a not-yet-draining sink.
	if !deps.PipelineReady() {
		return true, errorResult("collect: daemon still starting — LLM pipeline not ready yet, retry shortly")
	}
	// Resolve the standing collect runtime via the optional seam. Present on the
	// production *client; absent on a router-less/degraded test client, in which
	// case collectWaitOrDetach falls back to a synchronous run.
	var rt *CollectRuntime
	if p, ok := deps.(collectRuntimeProvider); ok {
		rt = p.CollectRuntime()
		if destination, bound := graphclient.StorageDestination(ctx); bound {
			rt = rt.ForDestination(destination)
		}
	}

	// ctx is hoisted above the registered-type probe (which needs it for the
	// ByName wire lookup) and the builtin cascade plumbing below both reuse it.
	// Derive its base from the runtime so a DETACHED builtin run rides baseCtx (a
	// daemon Stop cancels it) rather than the caller's per-call ctx, which dies
	// with the tool call; without a runtime the base stays the caller's ctx, so a
	// synchronous collect on a degraded client dies with a cancelled call. The
	// cascade-set + resolution-map + web-crawl-opts enrichment below decorates
	// THIS ctx, and the no-arg work closure captures the fully-enriched result.
	base := ctx
	if rt != nil {
		// Adopt the runtime's CANCELLATION root but carry the query-origin
		// operation across. BaseContext is a daemon-lifetime context holding no
		// per-call values, so switching to it bare would silently drop the stamp
		// for the rest of the collect — including the registered-type ByName wire
		// lookup below, which issues a covered RPC.
		base = graphclient.WithOperation(rt.BaseContext(), graphclient.OperationForTool(params.Name))
		if destination, ok := graphclient.StorageDestination(ctx); ok {
			base = graphclient.WithDestination(base, destination)
		}
	}
	ctx = base

	// Registered (non-builtin) CUSTOM graph type: a collect whose type misses the
	// builtin collector registry but matches a registered GraphTypeDef is
	// collected by proxying that registration's MCP provider. The record is
	// resolved HERE — the lookup is a wire call and needs the enriched ctx — and
	// then travels with the collect through the SAME tail a builtin collect runs.
	// A builtin type (collector.Lookup hit) resolves to nil and the path below is
	// unchanged for it.
	customDef, lookupErr := lookupCustomCollector(ctx, deps, a.Type)
	if lookupErr != nil {
		return true, errorResult(lookupErr.Error())
	}

	// Derive the id a web collect did not supply, then refuse a collect that
	// still has none. Both live in resolveCollectID because their ORDER is the
	// contract: deriving first is what makes the id optional for web-with-seeds
	// and unchanged for every other collect.
	//
	// A CUSTOM COLLECT REACHES THIS GUARD TOO, and that is a deliberate
	// tightening: the registration name is the graph family and the collect id is
	// the INSTANCE inside it, so a custom collect with no id names no graph. The
	// retired exec contract accepted one (the id was only a graph_name default a
	// collector could override), and accepting one now would record an in-flight
	// identity with an empty name — a gate that can never match anything.
	if res, bad := resolveCollectID(&a); bad {
		return true, res
	}

	opts := collector.CollectOptions{
		Force:   a.Force,
		Promote: a.Promote,
		Sink:    deps.Sink(),
	}

	// Refuse the extract params wherever they would be dropped, rather than
	// accepting them and returning a success the caller misreads as the knob
	// having taken effect.
	if err := rejectRecipeOnlyArgs(a); err != nil {
		return true, errorResult(err.Error())
	}

	if a.Type == "web" || a.Type == "pdf" {
		if a.Transformer == "recipe" {
			// Recipe runs CLIENT-SIDE: the client reads the source raw
			// graph over the wire into an in-memory view, interprets the
			// recipe, and ships the projected practice-graph nodes back
			// through the Sink — recipe.RunRecipe owns the whole path. The
			// collect `type` (web vs pdf) is passed as the expected source
			// type so RunRecipe can reject a recipe whose source_graph_type
			// metadata does not match.
			return true, runRecipeCollect(ctx, deps, a)
		}
		if a.Transformer != "" || a.Recipe != "" || a.DryRun || a.Extract || a.RecipeBody != "" || a.MaxRows != 0 || a.MaxBytes != 0 || a.Offset != 0 {
			return true, errorResult(fmt.Sprintf(
				"collect %s transformer=%q not supported (only \"recipe\" today). "+
					"recipe / recipe_body / dry_run / extract / max_rows / max_bytes / offset / transformer fields require transformer=\"recipe\".",
				a.Type, a.Transformer))
		}
	}

	if a.Type == "web" {
		var failed bool
		var res kgtools.ToolResult
		if ctx, failed, res = withWebCrawlOptions(ctx, a); failed {
			return true, res
		}
	}

	return true, routeCollect(ctx, deps, a, opts, rt, customDef)
}

// routeCollect runs the pre-walk identity work and then routes the collect
// through the standing runtime: cap the synchronous wait at 60s, coalesce a
// duplicate target already in flight, and detach the run past the cap.
//
// customDef is the registered record for a CUSTOM graph type, or nil for a
// builtin one. It is the only thing that differs between the two: the identity
// derivation reads it to know the collect names a registered family, and the
// work closure runs the MCP provider instead of collector.Collect. Everything
// after that — the collision check, the wait-or-detach runtime, the linker, the
// postpopulate hook, the pipeline wake and the composition verdict — is the same
// code on both paths, which is what "custom graphs act under the same rules as
// code graphs" means concretely.
//
// Extracted from InterceptCollect to keep that dispatch function within the
// funlen budget, the same reason withWebCrawlOptions below was split out of it.
//
// work is a NO-ARG closure over the fully-enriched ctx InterceptCollect built
// (cascade set + resolution map + web-crawl opts) — Start injects no ctx, so
// there is no bare-baseCtx an implementation could substitute and drop that
// enrichment. successText is the CURRENT literal; collectWaitOrDetach suffixes it
// with the run's rendered node-type composition on the sub-60s / fallback paths,
// and a run reporting no composition returns it byte-identically.
func routeCollect(
	ctx context.Context,
	deps ClientDeps,
	a collectArgs,
	opts collector.CollectOptions,
	rt *CollectRuntime,
	customReg *resolvedCollector,
) kgtools.ToolResult {
	custom := customReg != nil
	// The name this collect will land under, plus the pre-walk collision check
	// and the legacy-graph notice — all BEFORE the work closure, so a refusal
	// costs no crawl and no parse.
	graphName, notice, err := prepareRawCollect(ctx, deps, a, custom)
	if err != nil {
		return errorResult(err.Error())
	}
	// The (family, name) pair the in-flight gate records. gateName is by
	// construction equal to graphName above — prepareRawCollect's own first
	// statement calls the same pure CollectGateGraphName on the same inputs, and
	// nothing between the two mutates a.Type, a.ID or a.SeedURLs — so what this
	// call adds is the FAMILY, which must come from the dispatcher's switch and
	// never from a bare kgtypes.GraphType(a.Type) conversion.
	gateType, gateName, gateErr := collectGateGraphIdentity(a.Type, a.ID, a.SeedURLs, custom)
	if gateErr != nil {
		return errorResult(gateErr.Error())
	}
	run := builtinCollectRunner()
	if custom {
		run = customCollectRunner(deps, customReg)
	}
	work := func() (string, string, error) { return collectWork(ctx, deps, a, opts, graphName, custom, run) }
	successText := fmt.Sprintf("Collected %s %s — streamed to server.", a.Type, a.ID)
	return collectWaitOrDetach(rt, a.Type, collectTargetKey(a.Type, a.ID), fmt.Sprintf("%s %s", a.Type, a.ID),
		gateType, gateName, successText, notice, work)
}

// recordCollectedRepo upserts the just-collected code repo's name→absolute-path
// mapping into the machine-local manifest. It is a no-op for every non-code
// collector type — only code graphs are addressed by a name the name→dir
// consumers must resolve back to a directory. The name is filepath.Base(absID),
// matching how `collect` derives the code-graph name, and absID is the absolute
// path the collect ran against. Best-effort: a manifest write failure (e.g. an
// unresolvable home dir) is logged and swallowed so it never fails an otherwise-
// successful collect.
func recordCollectedRepo(collectorType, absID string) {
	if collectorType != "code" {
		return
	}
	name := filepath.Base(absID)
	if err := recordRepoDir(name, absID); err != nil {
		slog.Warn("collect: failed to record repo→path manifest entry", "repo", name, "path", absID, "error", err)
	}
}

// withWebCrawlOptions assembles the web.CrawlOptions from the collect args,
// applies defaults, validates, and stashes the result on the returned context
// for the web collector to read. It returns (ctx, false, _) on success; on a
// validation failure it returns (ctx, true, errorResult) so the caller returns
// the error immediately. Extracted from InterceptCollect to keep that dispatch
// function within the funlen budget.
func withWebCrawlOptions(ctx context.Context, a collectArgs) (context.Context, bool, kgtools.ToolResult) {
	crawlOpts := web.CrawlOptions{
		Source:            a.ID,
		SeedURLs:          a.SeedURLs,
		FollowPatterns:    a.FollowPatterns,
		MaxDepth:          a.MaxDepth,
		MaxPages:          a.MaxPages,
		MaxPathSegments:   a.MaxPathSegments,
		MaxPagesPerHost:   a.MaxPagesPerHost,
		MaxConcurrency:    a.MaxConcurrency,
		MaterializeGithub: a.MaterializeGithub,
		PolitenessMs:      a.PolitenessMs,
		UserAgent:         a.UserAgent,
		MaxDownloadBytes:  a.MaxDownloadBytes,
	}
	crawlOpts = crawlOpts.ApplyDefaults()
	if err := web.ValidateCrawlOptions(crawlOpts); err != nil {
		return ctx, true, errorResult(err.Error())
	}
	return web.WithCrawlOptions(ctx, crawlOpts), false, kgtools.ToolResult{}
}

// runRecipeCollect handles `collect type=web|pdf transformer=recipe`. The recipe
// transform runs CLIENT-SIDE: recipe.RunRecipe reads the source raw graph over
// the GraphCaller wire into an in-memory view, interprets the caller's inline
// body, and returns the emitted set. The collect `type` is passed as the expected
// source type, because an inline body carries none.
//
// TWO ARMS, AND THE FLAG PICKS ONE. An EXTRACT run renders the emitted rows and
// writes nothing anywhere. A LANDING run (`land:true`) writes them into the
// combined practice graph under a source hub, through the mutate route. A run
// asking for neither is refused by recipe.RunRecipe, naming both.
//
// EVERY LANDING REFUSAL RUNS BEFORE THE SOURCE IS READ. recipeRunOptions refuses
// the render params first; preflightLanding then probes the target graph and
// reads the raw graph's recorded origin, both ahead of recipe.RunRecipe. A
// refusal reached after the run has already paid for the read it was meant to
// prevent, and a refusal reached after the WRITE has already written.
func runRecipeCollect(ctx context.Context, deps ClientDeps, a collectArgs) kgtools.ToolResult {
	// recipe.RunRecipe reads the source graph into an in-memory view and holds the
	// projected result, so scavenge on the way out — mirrors builtinCollectWork's
	// heap-spike defer. Top-of-function placement wastes nothing: the sole earlier
	// return is the cheap 'recipe required' validation error below.
	defer debug.FreeOSMemory()
	opts, oerr := recipeRunOptions(a)
	if oerr != nil {
		return errorResult(oerr.Error())
	}
	// RESOLVE THE REPLAY ID INTO THE LOCAL ARGS COPY rather than threading a new
	// parameter. a is a value parameter, and the source read, the extract header's
	// `source=<type>/<id>` and the non-extract run summary all read a.ID — so one
	// assignment makes all three name the graph the run actually read.
	givenID := a.ID
	a.ID = resolveRawSourceGraphName(a.Type, givenID)

	// THE LANDING PREFLIGHT SITS HERE, between the param refusals and the run,
	// because both of its refusals are about state the run cannot change: whether
	// there is a graph to write into, and whether the raw graph knows where it
	// came from. Deciding them after the source read would pay for a read the
	// answer makes pointless.
	var pre *landingPreflight
	if a.Land {
		p, perr := preflightLanding(ctx, deps, a)
		if perr != nil {
			return errorResult(perr.Error())
		}
		pre = p
		opts.LandTarget = landingTarget()
	}

	res, err := recipe.RunRecipe(ctx, deps.GraphCaller(), a.ID, kgtypes.GraphType(a.Type), opts)
	if err != nil {
		return errorResult("collect " + a.Type + " recipe: " + err.Error() +
			rawSourceNotFoundHint(a.Type, givenID, a.ID))
	}
	if pre != nil {
		out, lerr := landRecipeRun(ctx, deps, a, pre, res)
		if lerr != nil {
			return errorResult(lerr.Error())
		}
		return textResult(renderLanding(a, out))
	}
	return textResult(renderExtract(a, res))
}

// pipelineWaker is the OPTIONAL deps capability the collect interceptor uses to
// nudge the LLM pipeline after a successful collect. Type-asserted rather than a
// required ClientDeps method so the many test fakes that run no pipeline are
// unaffected; the production *client implements it over Pipeline.WakeAll.
type pipelineWaker interface{ WakePipeline() }
