// SPDX-License-Identifier: Apache-2.0

// Package foundation holds the store-free shared core of the topology
// analyzer suite that runs on the client (cmd/knowledge): the Finding / Severity
// result value-objects, the Request contract + Analyzer interface + the
// analyzer registry, the ranking/percentile helpers, and the wire-backed
// graph builder (NewGonumGraph) + read-helpers every analyzer family reuses.
//
// foundation depends ONLY on the wire proto vocabulary (gen/knowledge/v1 +
// pkg/kgtypes), the client engine (Compile/Decode), the GraphCaller seam, and
// gonum + the standard library. It imports neither the storage engine nor any
// legacy server-side topology package: analyzers fetch their nodes and edges
// over the wire through Request.Caller, never from an in-process store.
//
// The analyzer ALGORITHMS (PageRank, SCC, betweenness, …) live in the
// per-family sub-packages that import this one; foundation owns only the
// shared scaffolding those families build against.
package foundation

// TruncateTopK clips findings to the first k entries when k > 0. Returns the
// input untouched when k <= 0 (no cap) or len <= k. Used by every analyzer
// that honors Request.TopK. Exported because the analyzers that call it now
// live in sibling family packages that import foundation.
func TruncateTopK(findings []Finding, k int) []Finding {
	if k <= 0 || len(findings) <= k {
		return findings
	}
	return findings[:k]
}

// Severity classifies how urgently a Finding should be acted on.
type Severity string

// Severity levels, ordered from least to most urgent.
const (
	SeverityInfo     Severity = "info"
	SeverityNotice   Severity = "notice"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Finding is the result of running an Analyzer over a graph. Findings are
// algorithm-agnostic value objects: callers decide how to persist or render
// them. Evidence holds node IDs that justify the finding; the first element
// is treated as the "primary" evidence and is used for deduplication when
// multiple analyzers surface the same underlying issue.
type Finding struct {
	// Algorithm names the analyzer that produced this finding (e.g.
	// "pagerank", "scc", "orphan"). Used for grouping and provenance.
	Algorithm string
	// Severity classifies how urgently the finding should be acted on.
	Severity Severity
	// Title is a short human-readable headline (one line).
	Title string
	// Summary is a longer human-readable description of what the analyzer
	// found and why it matters.
	Summary string
	// Evidence holds node IDs that justify the finding. The first element is
	// the primary evidence used for dedup; subsequent elements are
	// supporting context (e.g. members of a strongly-connected component).
	Evidence []string
	// Metrics holds algorithm-specific numeric outputs (e.g. PageRank score,
	// component size, distance). Keys are analyzer-defined.
	Metrics map[string]float64
	// Metadata holds string-typed analyzer-specific context that doesn't
	// belong in Metrics (e.g. "protocol"="TCP", "port"="80", or a
	// JSON-encoded raw-data payload for matrix-style findings). Keys are
	// analyzer-defined. nil is equivalent to an empty map.
	Metadata map[string]string
}
