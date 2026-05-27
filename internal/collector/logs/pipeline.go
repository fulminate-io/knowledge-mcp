// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"context"
	"fmt"
	"time"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// DefaultChunkWindow is the default time window used to coalesce log
// entries into a single compressed chunk.
const DefaultChunkWindow = 5 * time.Minute

// Pipeline orchestrates the end-to-end log collection flow:
//
//  1. Pull entries from the provider (batched via the emit callback)
//  2. Drain-cluster messages into templates, run language-specific
//     consolidators, and reclassify severity based on embedded markers
//  3. Group entries into LogStreams using a CardinalityTracker
//  4. Assemble compressed LogChunks per (stream, template, time-window)
//  5. Produce a CollectResult the client materializes into the log
//     graph via the standard WriteResult RPC (BCN11.1)
//
// Cloud-graph access is injected via CloudResolver and DependencyChecker
// so this package never imports cloud/. Both callbacks are nil-safe:
// when absent, the pipeline simply skips the cloud-linked stages.
//
// BCN11.2 dropped the in-process store handle entirely: every write
// flows through the client-side MaterializeLogGraph + the WriteResult
// RPC. The pipeline is now a pure transform.
type Pipeline struct {
	provider        wirelogs.Provider
	queryID         string
	cloudResolver   CloudResolver
	dependencyCheck DependencyChecker

	drainCfg      DrainConfig
	chunkWindow   time.Duration
	cardThreshold int
}

// CollectResult is the final artifact of a Pipeline.Collect run. It
// exposes the materialized graph primitives (templates, streams, chunks)
// alongside an LLM-consumable Summary and a QueryEngine tuned for
// subsequent tool queries (label overview, severity filter, etc.).
type CollectResult struct {
	// QueryID is the deterministic identifier assigned to this collection.
	// All graph writes under the logs graph share this prefix.
	QueryID string

	// TotalEntries is the number of log entries pulled from the provider.
	TotalEntries int

	// Templates are the Drain-clustered + consolidated templates for this
	// collection. Order is provider-dependent.
	Templates []*wirelogs.LogTemplate

	// Streams are the unique label-set groupings observed in the entries.
	Streams []*wirelogs.LogStream

	// Chunks are the compressed time-bounded (stream, template) blocks
	// that hold the entry timestamps and variable values.
	Chunks []*wirelogs.LogChunk

	// Summary is a natural-language overview suitable for inclusion in
	// LLM context. Populated by pipeline_summary.go.
	Summary string

	// QueryEngine is a prebuilt index over Streams, Chunks, and Templates
	// for downstream tool queries. Nil if collection produced zero entries.
	QueryEngine *QueryEngine

	// TimeRange reports the earliest and latest entry timestamps observed.
	TimeRange TimeRange

	// CorrelationsFound counts CORRELATES_WITH edges emitted during the
	// correlation stage.
	CorrelationsFound int

	// Correlations is the full slice of correlation records produced by
	// findCorrelations — confirmed and unconfirmed. Populated by Collect()
	// from the slice returned by runCorrelations. Consumers that want to
	// materialize CORRELATES_WITH edges server-side (RemoteUploadSink) or
	// surface "possibly related" pairs in summaries read this directly;
	// CorrelationsFound remains the count of StructurallyConfirmed records
	// actually written as edges by writeCorrelations when a store is
	// attached.
	Correlations []wirelogs.CorrelationResult

	// Resolutions is the full slice of stream-label → cloud-resource
	// resolutions produced by computeStreamResolutions. The client-side
	// MaterializeLogGraph turns this slice into NodeProxy entries +
	// EMITTED_BY edges that ride the standard WriteResult wire path. In
	// the in-process flow (logDB != nil) the proxies are already written
	// before this slice is captured; the slice is still populated so the
	// field has the same shape across both flows.
	Resolutions []wirelogs.ResolvedProxyEntry
}

// TimeRange bounds the earliest and latest entry timestamps in a
// CollectResult.
type TimeRange struct {
	Start time.Time
	End   time.Time
}

// NewPipeline constructs a Pipeline for the given provider and query ID.
// The queryID is used to namespace graph writes and should be
// deterministic per query (e.g., sha256 of the wirelogs.Query fields).
//
// Options may override defaults:
//   - WithCloudResolver / WithDependencyChecker for cloud linkage
//
// BCN11.2: the previous store.DB argument is gone. The pipeline is now a
// pure transform; the client (cmd/knowledge/internal/tools.runLogsCollect)
// runs MaterializeLogGraph on the returned CollectResult and ships the
// node/edge slices via the standard IngestService.WriteResult RPC.
func NewPipeline(provider wirelogs.Provider, queryID string, opts ...PipelineOption) *Pipeline {
	p := &Pipeline{
		provider:      provider,
		queryID:       queryID,
		drainCfg:      DefaultDrainConfig(),
		chunkWindow:   DefaultChunkWindow,
		cardThreshold: DefaultCardinalityThreshold,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	return p
}

// CollectFromEntries runs the pipeline against pre-collected entries.
// Used by the MCP client which fetches entries first so it can derive
// the cloud-graph slice it needs for in-memory CloudResolver +
// DependencyChecker before correlation runs. wirelogs.Provider on this Pipeline
// is ignored; entries supplied by the caller are authoritative, so
// callers may construct a Pipeline without a provider for this flow
// (or with a provider it has already drained).
//
// Severity reclassification has already run in Collect; callers that
// invoke CollectFromEntries directly should reclassify themselves
// (logs.ReclassifySeverity is exposed for that purpose). Likewise for
// entry collection — see logs.CollectEntries for the matching seam.
func (p *Pipeline) CollectFromEntries(ctx context.Context, entries []wirelogs.LogEntry, query wirelogs.Query) (*CollectResult, error) {
	templates, entryTemplateIDs := processEntries(entries, p.drainCfg)
	streams, entryStreamIDs := buildStreams(entries, p.cardThreshold)

	chunks, err := assembleChunks(entries, entryStreamIDs, entryTemplateIDs, templates, p.chunkWindow)
	if err != nil {
		return nil, fmt.Errorf("logs: assemble chunks: %w", err)
	}

	// BCN11.2: pipeline is a pure transform — no DB writes. The client
	// materializes the result via MaterializeLogGraph + WriteResult RPC.
	correlations, resolutions := p.runCorrelations(ctx, templates, streams, chunks)

	engine := NewQueryEngine(streams, chunks, templates)
	RegisterEngine(p.queryID, engine)

	tmplByID := templatesByID(templates)
	agg := BuildAggregationSummary(streams, chunks, tmplByID)

	confirmed := 0
	for _, c := range correlations {
		if c.StructurallyConfirmed {
			confirmed++
		}
	}

	return &CollectResult{
		QueryID:      p.queryID,
		TotalEntries: len(entries),
		Templates:    templates,
		Streams:      streams,
		Chunks:       chunks,
		QueryEngine:  engine,
		TimeRange:    computeTimeRange(entries),
		Summary:      buildSummary(templates, streams, chunks, correlations, agg, query),
		// CorrelationsFound previously counted edges written by
		// writeCorrelations in the now-gone in-process flow. Post-
		// BCN11.2 the source of truth is the full Correlations slice
		// the client materializes into edges — count the
		// structurally-confirmed entries (the ones that materialize
		// into CORRELATES_WITH edges) so callers see the same number
		// they always did.
		CorrelationsFound: confirmed,
		Correlations:      correlations,
		Resolutions:       resolutions,
	}, nil
}

// runCorrelations handles the optional cloud-linkage stages: it computes
// stream-label resolutions and runs temporal correlations. BCN11.2
// dropped the in-process DB-write half; the returned slices ride the
// CollectResult to the client, which materializes proxy + EMITTED_BY +
// CORRELATES_WITH edges via the standard WriteResult RPC.
//
// Returns (correlations, resolutions). The full correlations slice is
// returned (confirmed + unconfirmed) so the summary section can surface
// "possibly related" pairs even when no edges materialize.
//
// The proxyMap that drives findCorrelations's per-pair ResourceA /
// ResourceB diagnostic is built unconditionally from resolutions using
// the (Account, ResourceID) pair (joined by ":"); this is what the
// CORRELATES_WITH Edge.Evidence resources= field carries on the wire.
func (p *Pipeline) runCorrelations(
	ctx context.Context,
	templates []*wirelogs.LogTemplate,
	streams []*wirelogs.LogStream,
	chunks []*wirelogs.LogChunk,
) ([]wirelogs.CorrelationResult, []wirelogs.ResolvedProxyEntry) {
	resolutions := computeStreamResolutions(ctx, streams, p.cloudResolver)

	// proxyMap is the diagnostic Account:ResourceID pair surfaced via
	// wirelogs.CorrelationResult.ResourceA/B and persisted in CORRELATES_WITH
	// Edge.Evidence.
	proxyMap := make(map[string]string, len(resolutions))
	for _, r := range resolutions {
		proxyMap[r.LabelValue] = r.Account + ":" + r.ResourceID
	}

	correlations := findCorrelations(ctx, templates, chunks, streams, proxyMap, p.cloudResolver, p.dependencyCheck)
	return correlations, resolutions
}

// computeTimeRange scans entries for the earliest and latest timestamps.
// Zero-timestamp entries are skipped so unset fields do not pull Start to
// the Unix epoch. An empty slice returns a zero-value TimeRange.
func computeTimeRange(entries []wirelogs.LogEntry) TimeRange {
	var tr TimeRange
	for _, e := range entries {
		if e.Timestamp.IsZero() {
			continue
		}
		if tr.Start.IsZero() || e.Timestamp.Before(tr.Start) {
			tr.Start = e.Timestamp
		}
		if e.Timestamp.After(tr.End) {
			tr.End = e.Timestamp
		}
	}
	return tr
}
