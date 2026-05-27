// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs/cloudresolver"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/remote"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func runLogsCollect(deps ClientDeps, a collectArgs) kgtools.ToolResult {
	ctx := context.Background()
	cfg, err := resolveLogsProviderConfig(ctx, deps, a)
	if err != nil {
		return errorResult("collect logs: " + err.Error())
	}
	provName := strings.TrimSpace(cfg["provider"])
	if provName == "" {
		return errorResult("collect logs: provider is required (set 'backend' or 'provider')")
	}
	provider, err := logwire.New(provName)
	if err != nil {
		return errorResult(fmt.Sprintf("collect logs: provider %q: %v", provName, err))
	}
	if err := provider.Configure(cfg); err != nil {
		return errorResult(fmt.Sprintf("collect logs: configure %s: %v", provName, err))
	}

	query, err := buildLogsQuery(a, provName)
	if err != nil {
		return errorResult("collect logs: " + err.Error())
	}
	queryID := computeLogsQueryID(query)

	uploader, ok := deps.Sink().(*remote.UploadSink)
	if !ok {
		return errorResult(fmt.Sprintf("collect logs: sink is %T, expected *remote.UploadSink", deps.Sink()))
	}

	// ENTRIES-FIRST FLOW: pull raw entries, derive candidate cloud
	// graphs, bulk-fetch the in-memory slice, build the resolver +
	// dep-checker over it, then run the rest of the pipeline.
	entries, err := logs.CollectEntries(ctx, provider, query)
	if err != nil {
		return errorResult(fmt.Sprintf("collect logs: collect entries: %v", err))
	}
	entries = logs.ReclassifySeverity(entries)

	// v1 ships nil typePrefixes: the server returns every cloud-resource
	// node per graph, and the in-memory resolver narrows by type via its
	// existing prefixRank logic. Wire-size narrowing is deferred to a
	// benchmarks-end-of-project ticket.
	graphNames := candidateCloudGraphNames(entries)
	subgraph, err := uploader.FetchCloudSubgraph(ctx, graphNames, nil)
	if err != nil {
		// Non-fatal: log, then proceed without cloud enrichment. The
		// pipeline still produces templates / streams / chunks;
		// correlations stay temporal-only — exactly today's behavior.
		slog.Warn("collect logs: FetchCloudSubgraph failed; running without cloud enrichment", "error", err)
		subgraph = nil
	}

	var opts []logs.PipelineOption
	if subgraph != nil {
		opts = append(opts,
			logs.WithCloudResolver(cloudresolver.NewCloudResolver(subgraph)),
			logs.WithDependencyChecker(cloudresolver.NewDependencyChecker(subgraph)),
		)
	}
	// nil dbStore = no-store mode. The client materializes the log graph
	// in memory via logs.MaterializeLogGraph below; UploadSink.WriteResult
	// then ships it via the standard wire path (CreateBatch on the server).
	pipeline := logs.NewPipeline(provider, queryID, opts...)
	result, err := pipeline.CollectFromEntries(ctx, entries, query)
	if err != nil {
		return errorResult(fmt.Sprintf("collect logs: pipeline: %v", err))
	}

	return shipLogsResult(ctx, uploader, result)
}

// shipLogsResult runs the client-side MaterializeLogGraph pure transform
// against the pipeline output and ships the resulting nodes+edges via the
// standard UploadSink.WriteResult RPC. Extracted from runLogsCollect to
// keep both functions inside the file-line / funlen budget.
func shipLogsResult(ctx context.Context, uploader *remote.UploadSink, result *logs.CollectResult) kgtools.ToolResult {
	matNodes, matEdges, err := logs.MaterializeLogGraph(
		result.QueryID,
		result.Templates,
		result.Streams,
		result.Chunks,
		result.Correlations,
		result.Resolutions,
	)
	if err != nil {
		return errorResult(fmt.Sprintf("collect logs: materialize: %v", err))
	}

	wireResult := &collectorwire.CollectResult{
		GraphType: kgtypes.GraphLogs,
		GraphName: result.QueryID,
		Nodes:     matNodes,
		Edges:     matEdges,
	}
	if err := uploader.WriteResult(ctx, "logs", wireResult); err != nil {
		return errorResult(fmt.Sprintf("collect logs: write log graph: %v", err))
	}

	return textResult(fmt.Sprintf(
		"Collected %d entries into log graph %s — %d templates, %d streams, %d chunks, %d correlations. Wrote %d nodes, %d edges.\n\n%s",
		result.TotalEntries, result.QueryID,
		len(result.Templates), len(result.Streams), len(result.Chunks), result.CorrelationsFound,
		len(matNodes), len(matEdges),
		result.Summary,
	))
}

// resolveLogsProviderConfig builds the provider config map either by
// looking up the named log_backend node via the standard query path
// (deps.GraphClient().Call) or from inline collectArgs fields. The
// returned map is the input to provider.Configure.
func resolveLogsProviderConfig(ctx context.Context, deps ClientDeps, a collectArgs) (map[string]string, error) {
	name := strings.TrimSpace(a.Backend)
	if name == "" {
		return map[string]string{
			"provider":     a.Provider,
			"url":          a.URL,
			"auth_type":    a.AuthType,
			"credential":   a.Credential,
			"kube_context": a.KubeContext,
		}, nil
	}
	// Query by type=log-backend and filter by SymbolName client-side, via the
	// Execute carrier seam. This handles both new records (where node ID == name)
	// and legacy UUID-keyed records that pre-date the deterministic-ID write path.
	args, err := json.Marshal(map[string]any{"type": "log-backend", "limit": 0})
	if err != nil {
		return nil, fmt.Errorf("marshal query args: %w", err)
	}
	resp, err := executeQuery(ctx, deps.GraphCaller(), args)
	if err != nil {
		return nil, fmt.Errorf("list log_backends: %w", err)
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		return nil, fmt.Errorf("list log_backends decode: %w", derr)
	}
	cfg, err := parseLogBackendByName(nodes, name)
	if err != nil {
		return nil, fmt.Errorf("log_backend %q: %w", name, err)
	}
	return cfg, nil
}

// resultText extracts the first text content block from a tool result.
func resultText(r kgtools.ToolResult) string {
	for _, c := range r.Content {
		if c.Type == "text" {
			return c.Text
		}
	}
	return ""
}

// parseLogBackendByName reads the JSON list returned by query(type=log-backend,
// format=json) and finds the entry whose SymbolName matches name. Filters
// client-side rather than relying on node ID = SymbolName, so legacy
// UUID-keyed records keep working.
func parseLogBackendByName(nodes []*knowledgev1.Node, name string) (map[string]string, error) {
	for _, n := range nodes {
		if n.SymbolName != name {
			continue
		}
		cred := kgtypes.Value(n, "credential")
		cfg := map[string]string{
			"provider":     kgtypes.Value(n, "provider"),
			"url":          kgtypes.Value(n, "url"),
			"auth_type":    kgtypes.Value(n, "auth_type"),
			"credential":   cred,
			"kube_context": kgtypes.Value(n, "kube_context"),
		}
		// Auth-type-specific config slots. Providers (stackdriver, loki, etc.)
		// read keys like service_account_path / bearer_token / api_key and
		// fall back to the generic credential slot only when the typed key is
		// absent. Surfacing the typed slot here keeps providers from having
		// to interpret auth_type themselves.
		switch kgtypes.Value(n, "auth_type") {
		case "service_account_path":
			cfg["service_account_path"] = cred
		case "service_account":
			cfg["credentials_json"] = cred
		case "bearer":
			cfg["bearer_token"] = cred
		case "basic":
			cfg["basic_auth"] = cred
		case "api_key":
			cfg["api_key"] = cred
		}
		return cfg, nil
	}
	return nil, fmt.Errorf("not found among %d configured backends", len(nodes))
}

// buildLogsQuery converts collectArgs into a logwire.Query, parsing RFC3339
// timestamps and propagating filters verbatim.
func buildLogsQuery(a collectArgs, provName string) (logwire.Query, error) {
	q := logwire.Query{
		Provider:     provName,
		Source:       a.Source,
		TextFilter:   a.TextFilter,
		FieldFilters: a.Filters,
		SeverityMin:  a.SeverityMin,
		MaxEntries:   a.MaxEntries,
		RawQuery:     a.RawQuery,
	}
	if s := strings.TrimSpace(a.Start); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return q, fmt.Errorf("invalid 'start' timestamp (RFC3339): %w", err)
		}
		q.StartTime = t
	}
	if s := strings.TrimSpace(a.End); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return q, fmt.Errorf("invalid 'end' timestamp (RFC3339): %w", err)
		}
		q.EndTime = t
	}
	return q, nil
}

// candidateCloudGraphNames is the v1 stub: per the user-locked design,
// the client always passes nil so the server returns every loaded cloud
// graph filtered only by type_prefixes. Wire-size narrowing optimization
// (deriving candidate graph names from entry labels) is deferred to a
// benchmarks-end-of-project ticket. The signature accepts []logwire.LogEntry
// so the call site doesn't change when a future revision adds narrowing.
func candidateCloudGraphNames(entries []logwire.LogEntry) []string {
	return nil
}

// computeLogsQueryID returns a deterministic identifier for a Query so
// repeated runs over the same window land in the same on-disk log graph
// (~/.knowledge/logs/<query_id>.bin) on the server.
func computeLogsQueryID(q logwire.Query) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s|%d|%s",
		q.Provider, q.Source,
		q.StartTime.UTC().Format(time.RFC3339Nano),
		q.EndTime.UTC().Format(time.RFC3339Nano),
		q.TextFilter, q.SeverityMin, q.MaxEntries, q.RawQuery)
	keys := make([]string, 0, len(q.FieldFilters))
	for k := range q.FieldFilters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "|%s=%s", k, q.FieldFilters[k])
	}
	sum := hex.EncodeToString(h.Sum(nil))[:16]
	src := strings.ToLower(strings.ReplaceAll(q.Source, "/", "_"))
	if len(src) > 32 {
		src = src[:32]
	}
	if src == "" {
		return fmt.Sprintf("%s-%s", q.Provider, sum)
	}
	return fmt.Sprintf("%s-%s-%s", q.Provider, src, sum)
}
