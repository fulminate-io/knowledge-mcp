// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"context"
	"fmt"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// collectEntries drains the provider's emit stream into a single slice.
// The emit callback appends each batch to the result; if the cap
// query.MaxEntries is reached the callback returns errMaxEntriesReached
// to short-circuit provider pagination.
//
// The caller owns the returned slice; provider implementations MUST NOT
// retain batch pointers after emit returns.
func collectEntries(ctx context.Context, provider wirelogs.Provider, query wirelogs.Query) ([]wirelogs.LogEntry, error) {
	if provider == nil {
		return nil, fmt.Errorf("logs: nil provider")
	}
	var entries []wirelogs.LogEntry
	limit := query.MaxEntries
	emit := func(batch []wirelogs.LogEntry) error {
		if len(batch) == 0 {
			return nil
		}
		if limit > 0 && len(entries)+len(batch) >= limit {
			remaining := limit - len(entries)
			if remaining > 0 {
				entries = append(entries, batch[:remaining]...)
			}
			return errMaxEntriesReached
		}
		entries = append(entries, batch...)
		return nil
	}
	err := provider.Collect(ctx, query, emit)
	if err != nil && err != errMaxEntriesReached {
		return nil, err
	}
	return entries, nil
}

// errMaxEntriesReached signals the emit callback hit query.MaxEntries.
// Providers MAY treat it as a terminal error and stop pagination; the
// pipeline swallows it in collectEntries.
var errMaxEntriesReached = fmt.Errorf("logs: max entries reached")

// CollectEntries pulls entries from the provider honoring query.MaxEntries.
// Exposed for the MCP client's entries-first flow: the client needs raw
// entries to derive the candidate cloud-graph set before invoking the
// FetchCloudSubgraph RPC, then drives the rest of the pipeline via
// Pipeline.CollectFromEntries. In-process callers should keep using
// Pipeline.Collect, which encapsulates this step.
func CollectEntries(ctx context.Context, provider wirelogs.Provider, query wirelogs.Query) ([]wirelogs.LogEntry, error) {
	return collectEntries(ctx, provider, query)
}

// ReclassifySeverity normalizes severity based on message-body markers,
// the same step Pipeline.Collect runs after CollectEntries. Exposed for
// the same client entries-first flow as CollectEntries.
func ReclassifySeverity(entries []wirelogs.LogEntry) []wirelogs.LogEntry {
	return reclassifySeverity(entries)
}

// reclassifySeverity rewrites entries whose carried severity is empty
// or INFO but whose message body contains an embedded level marker.
// GKE and similar platforms surface everything from stderr as ERROR, and
// some backends coerce to INFO when no structured level is extracted,
// so the message body is a more reliable signal than the wrapper.
//
// The slice is mutated in place; the returned slice is the same header
// for caller convenience.
func reclassifySeverity(entries []wirelogs.LogEntry) []wirelogs.LogEntry {
	for i := range entries {
		s := entries[i].Severity
		if s != "" && s != wirelogs.SeverityInfo {
			continue
		}
		if detected := wirelogs.DetectEmbeddedSeverity(entries[i].Message); detected != "" {
			entries[i].Severity = detected
		}
	}
	return entries
}

// processEntries runs Drain clustering over entries, then applies the
// default consolidators. It returns the consolidated templates and a
// parallel slice of per-entry template IDs — the latter is essential
// for chunk assembly because AddMessage is not safe to re-run (the
// clustering state evolves with each call and later templates can
// absorb earlier ones).
//
// Template IDs are captured after all clustering is complete (not at
// AddMessage time) because Drain recomputes a template's ID whenever
// the pattern is broadened with new wildcards. Capturing the pointer
// first and reading .ID afterwards guarantees every entry in the same
// cluster resolves to the same final ID.
//
// Entries with an empty message contribute no template and get an
// empty string in the parallel slice; chunk assembly must skip those.
func processEntries(entries []wirelogs.LogEntry, drainCfg DrainConfig) ([]*wirelogs.LogTemplate, []string) {
	drain := NewDrainEngine(drainCfg)
	entryTemplates := make([]*wirelogs.LogTemplate, len(entries))

	// Pass 1: cluster every entry, capturing the *wirelogs.LogTemplate pointer
	// so we can resolve to the cluster's final ID after all merges.
	for i, e := range entries {
		entryTemplates[i] = drain.AddMessage(e)
	}

	templates := drain.Templates()

	// Pass 2: consolidate language-specific noise (Go stacks, Python
	// tracebacks, ...). Consolidators may drop or merge templates.
	before := templatesByID(templates)
	consolidated := RunConsolidators(DefaultConsolidators(), templates)
	after := templatesByID(consolidated)
	remap := buildTemplateRemap(before, after)

	// Resolve per-entry template pointers to final IDs; apply the
	// consolidator remap if the entry's original template was dropped.
	entryTemplateIDs := make([]string, len(entries))
	for i, tpl := range entryTemplates {
		if tpl == nil {
			continue
		}
		id := tpl.ID
		if mapped, ok := remap[id]; ok {
			id = mapped
		}
		entryTemplateIDs[i] = id
	}

	return consolidated, entryTemplateIDs
}

// templatesByID indexes templates by their ID for diff comparisons.
func templatesByID(templates []*wirelogs.LogTemplate) map[string]*wirelogs.LogTemplate {
	m := make(map[string]*wirelogs.LogTemplate, len(templates))
	for _, t := range templates {
		if t == nil {
			continue
		}
		m[t.ID] = t
	}
	return m
}

// buildTemplateRemap finds template IDs that disappeared after
// consolidation and maps them to the surviving template whose pattern
// best matches. A template is considered "remapped" to its replacement
// when the replacement's Pattern is a prefix match or contains the old
// pattern's pattern — consolidators merge trace fragments under a
// header template, so the surviving one subsumes them.
//
// Entries whose template was dropped without a clear replacement keep
// their original ID (chunk assembly will skip them).
func buildTemplateRemap(before, after map[string]*wirelogs.LogTemplate) map[string]string {
	if len(before) == 0 || len(after) == 0 {
		return nil
	}
	remap := make(map[string]string)
	for id := range before {
		if _, kept := after[id]; kept {
			continue
		}
		// Find the most recent surviving template; consolidators merge
		// forward so the newest one typically absorbs the older fragment.
		best := ""
		for afterID := range after {
			best = afterID
		}
		if best != "" {
			remap[id] = best
		}
	}
	return remap
}

// buildStreams groups entries into LogStreams via a two-pass
// CardinalityTracker walk. Pass 1 observes every label value so the
// tracker can classify keys as low- or high-cardinality. Pass 2 builds
// one wirelogs.LogStream per unique label fingerprint and records which stream
// each entry belongs to.
//
// Returns the deduplicated streams and a parallel slice of per-entry
// stream IDs. Threshold of 0 falls back to DefaultCardinalityThreshold.
func buildStreams(entries []wirelogs.LogEntry, threshold int) ([]*wirelogs.LogStream, []string) {
	tracker := NewCardinalityTracker(threshold)
	for _, e := range entries {
		for k, v := range e.Labels {
			tracker.Observe(k, v)
		}
	}

	streamsByID := make(map[string]*wirelogs.LogStream)
	entryStreamIDs := make([]string, len(entries))
	for i, e := range entries {
		labels := e.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		id := FingerprintLabels(labels)
		entryStreamIDs[i] = id
		if _, exists := streamsByID[id]; exists {
			continue
		}
		streamsByID[id] = NewLogStream(labels, tracker)
	}

	streams := make([]*wirelogs.LogStream, 0, len(streamsByID))
	for _, s := range streamsByID {
		streams = append(streams, s)
	}
	return streams, entryStreamIDs
}
