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
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/pdfcollector" // side-effect: registers "pdf" with collector.Register
	"github.com/fulminate-io/knowledge-mcp/internal/collector/web"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	clientlinker "github.com/fulminate-io/knowledge-mcp/internal/linker"
	"github.com/fulminate-io/knowledge-mcp/internal/recipe"
)

// InterceptCollect intercepts the `collect` MCP tool call and runs the
// collector client-side, streaming chunks to the server via the
// RemoteUploadSink. Returns (true, result) when the call was handled; the
// caller forwards to the server only if this returns false.
//
// Every collector type runs through this path: code (via
// handleClientReindexCode), aws / gcp / azure / k8s / github / gitlab /
// bitbucket / web / logs / pdf all run client-side with opts.Sink =
// deps.Sink(). Web threads CrawlOptions through ctx; logs reads the
// configured backend node from the server (via the standard query path)
// to obtain the plaintext-after-decryption credential, runs logs.Pipeline
// locally in no-store mode, runs the client-side MaterializeLogGraph
// pure transform, and ships the resulting nodes+edges via the standard
// UploadSink.WriteResult — same wire path as code/cloud/cicd. PDF
// takes id as an absolute path to a .pdf file and dispatches directly
// to collector.Collect — no per-type context plumbing is required
// because the chunker reads everything from the file itself.
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
	// Readiness gate (bind-first startup): a collect ships chunks the client-side LLM pipeline
	// drains (summary + embed + segment ship). During the bind-first wiring window
	// the pipeline is not yet wired, so a collect would upload chunks with nothing
	// to drain them. Gate it on PipelineReady so the operator retries once the
	// pipeline attaches rather than collecting into a not-yet-draining sink.
	if !deps.PipelineReady() {
		return true, errorResult("collect: daemon still starting — LLM pipeline not ready yet, retry shortly")
	}
	if a.Type == "logs" {
		return true, runLogsCollect(ctx, deps, a)
	}

	// Resolve the standing collect runtime via the optional seam. Present on the
	// production *client; absent on a router-less/degraded test client, in which
	// case collectWaitOrDetach falls back to a synchronous run.
	var rt *CollectRuntime
	if p, ok := deps.(collectRuntimeProvider); ok {
		rt = p.CollectRuntime()
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
	}
	ctx = base

	// Registered (non-builtin) graph type: a collect whose type misses the
	// builtin collector registry but matches a registered GraphTypeDef runs the
	// external collector plugin. This probe sits BEFORE the `a.ID == ""` guard
	// so a params-only registered collector (key inside params, empty top-level
	// id) is accepted; builtin types (collector.Lookup hit) fall through to the
	// guard + collector.Collect unchanged.
	if handled, res := tryRegisteredCollect(ctx, deps, a); handled {
		return true, res
	}

	if a.ID == "" {
		// Prefix with "collect <type>:" so the error shape matches the
		// other collect-time errors (e.g. "collect logs: provider is
		// required") instead of the bare "'id' is required" that gave
		// no clue which tool surfaced it.
		return true, errorResult(fmt.Sprintf("collect %s: 'id' is required", a.Type))
	}

	// Cloud collectors use a cascade set for cross-provider dedup; the cicd path
	// has no matching infrastructure.
	cloudCS := cloud.NewCascadeSet()
	cloudCS.Mark(a.Type, a.ID)
	ctx = cloud.WithCascadeSet(ctx, cloudCS)

	// ResolutionMap travels alongside the cascade set so cascade targets
	// with a lossy ID (e.g. AKS kubeconfig context) can recover the
	// provider-canonical resource ID downstream. AKS subcollectors
	// populate CollectTarget.ResolutionID; cascade dispatch loops call
	// rm.Record(t.Id, t.ResolutionID) before invoking the cascade.
	ctx = cloud.WithResolutionMap(ctx, cloud.NewResolutionMap())

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
		if a.Transformer != "" || a.Recipe != "" || a.DryRun || a.Extract || a.RecipeBody != "" || a.MaxRows != 0 || a.MaxBytes != 0 {
			return true, errorResult(fmt.Sprintf(
				"collect %s transformer=%q not supported (only \"recipe\" today). "+
					"recipe / recipe_body / dry_run / extract / max_rows / max_bytes / transformer fields require transformer=\"recipe\".",
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

	// Route the builtin collect through the standing runtime: cap the synchronous
	// wait at 60s, coalesce a duplicate target already in flight, and detach the
	// run past the cap. work is a NO-ARG closure over the fully-enriched ctx built
	// above (cascade set + resolution map + web-crawl opts) — Start injects no ctx,
	// so there is no bare-baseCtx an implementation could substitute and drop that
	// enrichment. successText is the CURRENT literal; collectWaitOrDetach suffixes
	// it with the run's rendered node-type composition on the sub-60s / fallback
	// paths, and a run reporting no composition returns it byte-identically.
	work := func() (string, error) { return builtinCollectWork(ctx, deps, a, opts) }
	successText := fmt.Sprintf("Collected %s %s — streamed to server.", a.Type, a.ID)
	return true, collectWaitOrDetach(rt, collectTargetKey(a.Type, a.ID), fmt.Sprintf("%s %s", a.Type, a.ID),
		CollectGateGraphName(a.Type, a.ID), successText, work)
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
		Source:           a.ID,
		SeedURLs:         a.SeedURLs,
		FollowPatterns:   a.FollowPatterns,
		MaxDepth:         a.MaxDepth,
		MaxPages:         a.MaxPages,
		MaxPathSegments:  a.MaxPathSegments,
		MaxPagesPerHost:  a.MaxPagesPerHost,
		PolitenessMs:     a.PolitenessMs,
		UserAgent:        a.UserAgent,
		MaxDownloadBytes: a.MaxDownloadBytes,
	}
	crawlOpts = crawlOpts.ApplyDefaults()
	if err := web.ValidateCrawlOptions(crawlOpts); err != nil {
		return ctx, true, errorResult(err.Error())
	}
	return web.WithCrawlOptions(ctx, crawlOpts), false, kgtools.ToolResult{}
}

// runRecipeCollect handles `collect type=web|pdf transformer=recipe`. The recipe
// transform runs CLIENT-SIDE: recipe.RunRecipe reads the source raw graph over
// the GraphCaller wire into an in-memory view, interprets the named recipe, and
// ships the projected practice-graph nodes/edges back through the Sink. The
// collect `type` is passed as the expected source type so a recipe whose
// source_graph_type does not match (e.g. a pdf collect against a web-source
// recipe) is rejected with a typed error. DryRun previews the projection and
// returns Stats without writing.
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
	res, err := recipe.RunRecipe(ctx, deps.GraphCaller(), deps.Sink(), a.ID, kgtypes.GraphType(a.Type), opts)
	if err != nil {
		return errorResult("collect " + a.Type + " recipe: " + err.Error())
	}
	if a.Extract {
		return textResult(renderExtract(a, res))
	}
	verb := "Ran"
	if a.DryRun {
		verb = "Dry-ran"
	}
	return textResult(fmt.Sprintf(
		"%s recipe %q over %s/%s — emitted %d nodes (skipped %d, force-deleted %d, lookups %d/%d, link misses %d) in %dms.",
		verb, a.Recipe, a.Type, a.ID,
		res.Stats.NodesEmitted, res.Stats.SkippedChunks, res.Stats.ForceDeleted,
		res.Stats.LookupsResolved, res.Stats.LookupsResolved+res.Stats.LookupMisses,
		res.Stats.LinkMisses, res.Stats.ElapsedMillis,
	))
}

// tryRegisteredCollect runs the external collector plugin for a registered
// (non-builtin) graph type. It returns (false, _) when a.Type is a builtin
// collector OR no registered GraphTypeDef matches it — the caller then continues
// the builtin path unchanged. On a registered match it execs the binary via
// externalcollector.RunExternal and ships the result through deps.Sink(),
// returning (true, result) so the caller returns immediately.
//
// The registered branch does NOT enter cloud cascade dispatch (RunExternal ->
// Sink().WriteResult is a direct ship), so it deliberately skips the CascadeSet /
// ResolutionMap plumbing the builtin cloud path needs.
func tryRegisteredCollect(ctx context.Context, deps ClientDeps, a collectArgs) (bool, kgtools.ToolResult) {
	if _, lookErr := collector.Lookup(a.Type); lookErr == nil {
		return false, kgtools.ToolResult{} // builtin type — caller owns it.
	}
	if deps.GraphTypeCRUD() == nil {
		return false, kgtools.ToolResult{}
	}
	def, found, _ := deps.GraphTypeCRUD().ByName(ctx, a.Type)
	if !found {
		return false, kgtools.ToolResult{}
	}
	// The registered external collector holds the full CollectResult in memory
	// until deps.Sink().WriteResult below, so scavenge on the way out. Placed AFTER
	// the three early return-false guards (builtin type / nil CRUD / not-found) so
	// a builtin collect — which returns at the first guard, synchronously, on the
	// front of every `collect` — does not eat a useless STW GC (builtinCollectWork
	// already scavenges its own path).
	defer debug.FreeOSMemory()
	res, err := externalcollector.RunExternal(ctx, def, a.Params, a.ID)
	if err != nil {
		return true, errorResult(err.Error())
	}
	if err := deps.Sink().WriteResult(ctx, a.Type, res); err != nil {
		return true, errorResult(err.Error())
	}
	return true, textResult(fmt.Sprintf("Collected %s — streamed to server.", a.Type))
}

// pipelineWaker is the OPTIONAL deps capability the collect interceptor uses to
// nudge the LLM pipeline after a successful collect. Type-asserted rather than a
// required ClientDeps method so the many test fakes that run no pipeline are
// unaffected; the production *client implements it over Pipeline.WakeAll.
type pipelineWaker interface{ WakePipeline() }

// postCollectLinkerTypes is the set of collector types that trigger the
// post-collect cross-graph linker. Mirrors the prior server-side
// gate (GraphCloud / GraphCICD) plus the
// collector-type extensions the linker exercises (aws/gcp/azure/k8s
// land in cloud; github/gitlab/bitbucket land in cicd).
var postCollectLinkerTypes = map[string]bool{
	"aws": true, "gcp": true, "azure": true, "k8s": true,
	"github": true, "gitlab": true, "bitbucket": true,
	"cicd": true,
}

// runPostCollectLinker runs the client cross-graph linker (clientlinker.RunAll)
// in-process after a successful collector.Collect for cloud/CI-CD-shaped
// collector types — the IDENTICAL call handleClientLinker (manage.go) makes.
// This replaces the prior manage(link) self-bounce (client → server manage(link)
// → which is itself client-intercepted → the same RunAll), pure round-trip
// overhead. Best-effort: slog.Warn on nil-caller, run error, or per-sub-linker
// errors, but the caller's textResult is returned unchanged so the linker tail
// never fails an otherwise-successful collect. Like the postpopulate tail it runs
// under a non-admitting operation, so walking every graph of the families it
// links cannot earn any of them a place in the working set.
func runPostCollectLinker(ctx context.Context, deps ClientDeps, collectorType string) {
	if !postCollectLinkerTypes[collectorType] {
		return
	}
	// Post-collect linker follows the data: under the locked model the collect
	// sink wrote to cloud when logged in (local otherwise), so the cross-graph
	// linker walks through the SAME login-routed GraphCaller — the
	// just-collected nodes live wherever the sink put them.
	gc := deps.GraphCaller()
	if gc == nil {
		slog.Warn("post-collect linker: GraphCaller unavailable (skipping)", "collector", collectorType)
		return
	}
	ctx = graphclient.WithOperation(ctx, graphclient.OpPostCollectFanout)
	res, err := clientlinker.RunAll(ctx, gc, clientlinker.LinkOptions{})
	if err != nil {
		slog.Warn("post-collect linker failed", "collector", collectorType, "error", err)
		return
	}
	if len(res.Errors) > 0 {
		slog.Warn("post-collect linker reported errors", "collector", collectorType, "errors", res.Errors)
	}
}

// collectArgs is the client-side collect command argument contract. It lives
// only in the client binary (collection runs client-side after the binary
// split). Type-specific fields are zero-valued when type does not match and
// ignored by other dispatch paths.
type collectArgs struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Force   bool   `json:"force"`
	Promote bool   `json:"promote,omitempty"` // code only: force base + repoint default branch to the collected branch

	// Params is the generic param passthrough for a registered (non-builtin)
	// graph-type collector: the registered external binary carries all of its
	// domain params inside this single object, validated against the collector's
	// param_schema before exec. The built-in collectors (code/web/logs/pdf/
	// cloud) ignore it and read their typed fields below instead.
	Params map[string]any `json:"params,omitempty"`

	// Web-specific. Threaded through ctx via web.WithCrawlOptions.
	SeedURLs         []string `json:"seed_urls,omitempty"`
	FollowPatterns   []string `json:"follow_patterns,omitempty"`
	MaxDepth         int      `json:"max_depth,omitempty"`
	MaxPages         int      `json:"max_pages,omitempty"`
	MaxPathSegments  int      `json:"max_path_segments,omitempty"`
	MaxPagesPerHost  int      `json:"max_pages_per_host,omitempty"`
	PolitenessMs     int      `json:"politeness_ms,omitempty"`
	UserAgent        string   `json:"user_agent,omitempty"`
	MaxDownloadBytes int64    `json:"max_download_bytes,omitempty"`

	// Web/PDF transformer dispatch. Transformer="recipe" runs the recipe
	// transform CLIENT-SIDE via recipe.RunRecipe (see runRecipeCollect):
	// Recipe names the recipe in the GraphTransformers/recipes bucket, and
	// DryRun previews the projection without writing. Any other Transformer
	// value is rejected; the fields are parsed so the error message can name
	// them rather than "unknown argument".
	// Extract turns a recipe run into EXTRACT MODE: nothing is written and the
	// emitted rows come back for inspection, bounded by MaxRows and MaxBytes.
	// Body is an INLINE recipe body run instead of a saved one, and requires
	// Extract. MaxBytes deliberately does NOT travel through recipe.Options —
	// only the renderer knows rendered sizes, so the byte cap is applied there.
	Transformer string `json:"transformer,omitempty"`
	Recipe      string `json:"recipe,omitempty"`
	DryRun      bool   `json:"dry_run,omitempty"`
	Extract     bool   `json:"extract,omitempty"`
	RecipeBody  string `json:"recipe_body,omitempty"`
	MaxRows     int    `json:"max_rows,omitempty"`
	MaxBytes    int    `json:"max_bytes,omitempty"`

	// Logs-specific. The collector resolves the provider config either by
	// reading the configured log_backend node (when Backend is set) or
	// from the inline fields below. All other fields shape the logwire.Query
	// passed to provider.Collect.
	Backend     string            `json:"backend,omitempty"`
	Provider    string            `json:"provider,omitempty"`
	URL         string            `json:"url,omitempty"`
	Credential  string            `json:"credential,omitempty"`
	AuthType    string            `json:"auth_type,omitempty"`
	KubeContext string            `json:"kube_context,omitempty"`
	Source      string            `json:"source,omitempty"`
	Start       string            `json:"start,omitempty"`
	End         string            `json:"end,omitempty"`
	TextFilter  string            `json:"text_filter,omitempty"`
	SeverityMin string            `json:"severity_min,omitempty"`
	MaxEntries  int               `json:"max_entries,omitempty"`
	Filters     map[string]string `json:"filters,omitempty"`
	RawQuery    string            `json:"raw_query,omitempty"`
}

// runLogsCollect handles the `collect` tool when type=logs. The flow:
//
//  1. Resolve the provider config (from a configured log_backend node,
//     looked up via the standard query path, OR from inline fields).
//  2. Instantiate the provider and Configure it with the resolved map.
//  3. Build a logwire.Query from the args.
//  4. Run logs.Pipeline.Collect locally in no-store mode (the client has
//     no DB; the materialized graph rides the wire to the server).
//  5. Run the client-side MaterializeLogGraph pure transform to convert
//     templates / streams / chunks / correlations / resolutions into a
//     ([]*knowledgev1.Node, []kgwire.BatchEdge) batch.
//  6. Ship the batch via UploadSink.WriteResult — same wire as code,
//     cloud, and cicd collectors.
//
// Credentials never leave this function — they are pulled from the
// log_backend node into the in-process provider config and consumed by
// provider.Configure. Tool I/O carries the backend NAME, never the
// credential value.
