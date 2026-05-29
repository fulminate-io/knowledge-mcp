// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/pdfcollector" // side-effect: registers "pdf" with collector.Register
	"github.com/fulminate-io/knowledge-mcp/internal/collector/web"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	clientlinker "github.com/fulminate-io/knowledge-mcp/internal/linker"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
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
func InterceptCollect(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "collect" {
		return false, kgtools.ToolResult{}
	}
	// A collect spikes the heap hard — the chunker holds every file's
	// results until upload, and the precise Go call-graph build loads a
	// whole module + its dependency closure (ASTs + type info + SSA) live
	// at once. This is a long-lived stdio daemon, so once the collect is
	// done that working set is pure garbage; force a GC + scavenge on the
	// way out so RSS drops back to baseline immediately instead of after
	// the background scavenger eventually gets to it.
	defer debug.FreeOSMemory()

	var a collectArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("invalid arguments: " + err.Error())
	}
	if a.Type == "" {
		return true, errorResult("collect: 'type' is required")
	}
	if a.Type == "logs" {
		return true, runLogsCollect(deps, a)
	}
	if a.ID == "" {
		// Prefix with "collect <type>:" so the error shape matches the
		// other collect-time errors (e.g. "collect logs: provider is
		// required") instead of the bare "'id' is required" that gave
		// no clue which tool surfaced it.
		return true, errorResult(fmt.Sprintf("collect %s: 'id' is required", a.Type))
	}

	ctx := context.Background()

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
	cloudRM := cloud.NewResolutionMap()
	ctx = cloud.WithResolutionMap(ctx, cloudRM)

	opts := collector.CollectOptions{
		Force: a.Force,
		Sink:  deps.Sink(),
	}

	if a.Type == "web" || a.Type == "pdf" {
		if a.Transformer == "recipe" {
			// Recipe runs server-side: both the source raw graph and
			// the target practice graph live there, so there's nothing
			// for the client to do except forward. Return (false, _)
			// to fall through to the JSON-RPC proxy in mcp_client.go.
			//
			// Source kind (web vs pdf) is carried by `type` and
			// validated server-side against the recipe's
			// source_graph_type metadata.
			return false, kgtools.ToolResult{}
		}
		if a.Transformer != "" || a.Recipe != "" || a.DryRun {
			return true, errorResult(fmt.Sprintf(
				"collect %s transformer=%q not supported (only \"recipe\" today). "+
					"recipe / dry_run / transformer fields require transformer=\"recipe\".",
				a.Type, a.Transformer))
		}
	}

	if a.Type == "web" {
		crawlOpts := web.CrawlOptions{
			Source:           a.ID,
			SeedURLs:         a.SeedURLs,
			FollowPatterns:   a.FollowPatterns,
			MaxDepth:         a.MaxDepth,
			MaxPages:         a.MaxPages,
			PolitenessMs:     a.PolitenessMs,
			UserAgent:        a.UserAgent,
			MaxDownloadBytes: a.MaxDownloadBytes,
		}
		crawlOpts = crawlOpts.ApplyDefaults()
		if err := web.ValidateCrawlOptions(crawlOpts); err != nil {
			return true, errorResult(err.Error())
		}
		ctx = web.WithCrawlOptions(ctx, crawlOpts)
	}

	if err := collector.Collect(ctx, a.Type, a.ID, opts); err != nil {
		// collector.Collect already wraps with "collect <type>:" — adding
		// our own "collect <type> <id>:" prefix produces a duplicate "collect
		// <type>:" stutter. Use the inner error verbatim; type information
		// survives via the pipeline's wrap.
		return true, errorResult(err.Error())
	}
	// FUL-255: post-collect linker tail-call. Replaces the server-side
	// runPostCollectLinker (excised from cmd/knowledge-server/internal/storesink/sink.go).
	// Gated on the same collector types that previously triggered the
	// server-side path. Best-effort: failures slog.Warn but the
	// user-facing textResult is unchanged.
	runPostCollectLinker(ctx, deps, a.Type)
	// FUL-288: post-collect PostPopulate tail-call, SIBLING to the linker.
	// Runs the registered postpopulate hook for the collector family over the
	// wire, enriching the per-account/per-repo graph with the structural edges
	// the linker does not own (SG/NACL rules, cross-account trust, image
	// lineage, k8s selector/cluster linkage, CICD OIDC federation, codesync
	// hierarchy). Best-effort, like the linker.
	runPostCollectPostPopulate(ctx, deps, a.Type)
	return true, textResult(fmt.Sprintf("Collected %s %s — streamed to server.", a.Type, a.ID))
}

// postPopulateGraphType maps a collector type onto the store graph type whose
// named graphs the family's PostPopulate hook reads + writes. cloud providers
// (aws/gcp/azure/k8s) all back onto GraphCloud; CICD providers (github/
// bitbucket/gitlab) onto GraphCICD; the codesync collector ("code") onto GraphCode.
// A collector type with no entry has no postpopulate hook and is a no-op.
var postPopulateGraphType = map[string]kgtypes.GraphType{
	"aws":       kgtypes.GraphCloud,
	"gcp":       kgtypes.GraphCloud,
	"azure":     kgtypes.GraphCloud,
	"k8s":       kgtypes.GraphCloud,
	"github":    kgtypes.GraphCICD,
	"bitbucket": kgtypes.GraphCICD,
	"gitlab":    kgtypes.GraphCICD,
	"code":      kgtypes.GraphCode,
}

// runPostCollectPostPopulate fires the registered PostPopulate hook for the
// collector family after a successful collect, mirroring runPostCollectLinker's
// shape EXACTLY: it gates on the families that register a hook (the cloud/cicd/
// code collector types), grabs the GraphCaller wire seam, nil-skips with a
// slog.Warn, and is best-effort (hook failure → slog.Warn, the caller's
// user-facing textResult is unchanged).
//
// The hook key is the COLLECTOR TYPE (a.Type) — the registered keys are exactly
// the collector Name() values (aws/gcp/azure/k8s/github/bitbucket/gitlab/code), NOT a
// graph-name prefix (cloud graph names carry no family prefix: aws=accountID,
// gcp=projectID, azure=subscriptionID, k8s=contextName — all share GraphCloud).
// The orchestrator enumerates every graph of the family's graph type via
// postpopulate.ListGraphNames and fires the hook against each: a single cloud
// collect can cascade multiple provider graphs, and each hook self-filters by
// graph CONTENT (the resolveClusterLinkage-style silent no-op), so firing across
// all graphs of the type is safe — it enriches the graphs it owns and no-ops on
// the rest. All-graphs enumeration + idempotent re-run mirrors clientlinker.RunAll.
func runPostCollectPostPopulate(ctx context.Context, deps ClientDeps, collectorType string) {
	hook, ok := postpopulate.Lookup(collectorType)
	if !ok {
		// No hook registered for this collector type (e.g. web, pdf, logs, or
		// any sub-collector type) — nothing to enrich.
		return
	}
	graphType, ok := postPopulateGraphType[collectorType]
	if !ok {
		// A hook is registered but we have no graph-type mapping — defensive
		// skip (should not happen for the in-scope families).
		slog.Warn("post-collect postpopulate: no graph-type mapping (skipping)", "collector", collectorType)
		return
	}
	// Post-collect postpopulate runs on the freshly-written LOCAL graph —
	// route explicitly through LocalGraphCaller so a logged-in user still
	// re-reads the local graph here (the routing-aware GraphCaller would
	// land on cloud, which lacks the just-collected data).
	gc := deps.LocalGraphCaller()
	if gc == nil {
		slog.Warn("post-collect postpopulate: LocalGraphCaller unavailable (skipping)", "collector", collectorType)
		return
	}
	names, err := postpopulate.ListGraphNames(ctx, gc, graphType)
	if err != nil {
		slog.Warn("post-collect postpopulate: enumerate graphs failed", "collector", collectorType, "graphType", graphType, "error", err)
		return
	}
	for _, name := range names {
		if err := hook(ctx, gc, name); err != nil {
			slog.Warn("post-collect postpopulate hook failed", "collector", collectorType, "graph", name, "error", err)
		}
	}
}

// postCollectLinkerTypes is the set of collector types that trigger the
// post-collect cross-graph linker. Mirrors the pre-FUL-255 server-side
// gate at pkg/storesink/sink.go (GraphCloud / GraphCICD) plus the
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
// never fails an otherwise-successful collect.
func runPostCollectLinker(ctx context.Context, deps ClientDeps, collectorType string) {
	if !postCollectLinkerTypes[collectorType] {
		return
	}
	// Post-collect linker walks the freshly-written LOCAL graph — route
	// explicitly through LocalGraphCaller so a logged-in user still re-reads
	// the local graph here (the routing-aware GraphCaller would land on
	// cloud, which lacks the just-collected data).
	gc := deps.LocalGraphCaller()
	if gc == nil {
		slog.Warn("post-collect linker: LocalGraphCaller unavailable (skipping)", "collector", collectorType)
		return
	}
	res, err := clientlinker.RunAll(ctx, gc, clientlinker.LinkOptions{})
	if err != nil {
		slog.Warn("post-collect linker failed", "collector", collectorType, "error", err)
		return
	}
	if len(res.Errors) > 0 {
		slog.Warn("post-collect linker reported errors", "collector", collectorType, "errors", res.Errors)
	}
}

// collectArgs mirrors the server-side collectArgs struct. Replicated to
// avoid dragging domains/tools into the client binary. Type-specific fields
// are zero-valued when type does not match and ignored by other dispatch
// paths.
type collectArgs struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Force bool   `json:"force"`

	// Web-specific. Threaded through ctx via web.WithCrawlOptions.
	SeedURLs         []string `json:"seed_urls,omitempty"`
	FollowPatterns   []string `json:"follow_patterns,omitempty"`
	MaxDepth         int      `json:"max_depth,omitempty"`
	MaxPages         int      `json:"max_pages,omitempty"`
	PolitenessMs     int      `json:"politeness_ms,omitempty"`
	UserAgent        string   `json:"user_agent,omitempty"`
	MaxDownloadBytes int64    `json:"max_download_bytes,omitempty"`

	// Web transformer dispatch — currently returns an error until a
	// RunTransformer RPC is added. Fields parsed so the error message can
	// name them rather than "unknown argument".
	Transformer string `json:"transformer,omitempty"`
	Recipe      string `json:"recipe,omitempty"`
	DryRun      bool   `json:"dry_run,omitempty"`

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
