// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TopoStats holds per-node topology metrics computed externally and
// surfaced to the rerank doc text via the KNOWLEDGE_TOPO_HINTS env var.
// The fields are populated by experiment harnesses (e.g. rankeval) — the
// production code path leaves the global map empty and emits no hint.
//
// Note: caller-name signaling is NOT here. Callers are decorated at
// rerank time by augmentCallerHints (query_executor_search.go) on the
// candidate Node directly via the "callers" metadata key — that path
// is production-default, no env var, no sidecar map. TopoStats covers
// the remaining experimental signals still gated behind the env var.
//
// Callee/sibling name lists are capped at experimental limits (typically
// 3) to bound token growth in the rerank doc text. PackageSummary is
// truncated to ~80 chars for the same reason.
type TopoStats struct {
	FanIn          int
	FanOut         int
	CalleeNames    []string // up to 3 outgoing-CALLS target symbol names
	PackageSummary string   // file-level summary, truncated to ~80 chars
	Siblings       []string // up to 3 same-file symbol names (excluding self)
}

// topologyHints is a sidecar map keyed by node ID. Set once by an
// experiment harness via SetTopologyHints; renderCodeForRerank reads
// from it under the env-var-gated topologyHint() helper. Atomic so
// the map can be swapped without locking the read path.
var topologyHints atomic.Pointer[map[string]TopoStats]

// topologyHint formats a one-line hint for inclusion in the rerank doc
// text. Gated by the KNOWLEDGE_TOPO_HINTS env var, which takes a
// comma-separated list of signal names. Empty env var → no hint emitted.
//
// Modes:
//   - role: emit a Go-doc-style sentence classifying the function's
//     structural role (wrapper / implementation / orchestrator / leaf).
//     Phrasing matches code documentation conventions so Voyage's
//     cross-encoder (trained on web + code text) can lift it.
//   - fanin / fanout / body_lines / exported: raw structural stats.
//     Less effective than role because cross-encoders don't
//     differentially weight bag-of-words structural tokens; included
//     here as ablations.
//
// The role mode is the primary signal — it expresses topology in
// natural code-doc language ("convenience wrapper delegating to a
// helper") rather than raw counts ("[called from 5 places]"). The
// raw modes are kept for A/B comparison.
func topologyHint(n *knowledgev1.Node) string {
	mode := os.Getenv("KNOWLEDGE_TOPO_HINTS")
	if mode == "" {
		return ""
	}
	var stats TopoStats
	if m := topologyHints.Load(); m != nil {
		stats = (*m)[n.Id]
	}
	var parts []string
	docStyle := false
	for f := range strings.SplitSeq(mode, ",") {
		fragment, isDoc := topologyHintFragment(strings.TrimSpace(f), stats, n)
		if isDoc {
			docStyle = true
		}
		if fragment != "" {
			parts = append(parts, fragment)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	// docStyle modes (role/delegate/package/siblings) format as Go-doc comment
	// lines so the cross-encoder reads them as natural code documentation.
	// Raw-stat modes use a tag block to keep their experimental shape distinct.
	if docStyle {
		return "// " + strings.Join(parts, "; ")
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// topologyHintFragment renders one signal fragment for the comma-separated
// KNOWLEDGE_TOPO_HINTS mode list. Returns the fragment text and whether
// the fragment uses Go-doc comment phrasing (docStyle); a single docStyle
// signal in the mode list flips the entire hint to comment formatting.
// Empty fragment string when the signal has nothing to emit for this node.
func topologyHintFragment(name string, stats TopoStats, n *knowledgev1.Node) (string, bool) {
	switch name {
	case "role":
		return topologyRole(stats, n), true
	case "delegate":
		return delegateHint(stats), true
	case "package":
		if stats.PackageSummary != "" {
			return "package: " + stats.PackageSummary, true
		}
		return "", true
	case "siblings":
		if len(stats.Siblings) > 0 {
			return "alongside " + strings.Join(stats.Siblings, ", "), true
		}
		return "", true
	case "fanin":
		return fanCountHint(stats.FanIn, n.Id, "called from %d places", "no inbound calls"), false
	case "fanout":
		return fanCountHint(stats.FanOut, n.Id, "calls %d functions", "leaf function"), false
	case "body_lines":
		if bl := n.EndLine - n.StartLine; bl > 0 {
			return fmt.Sprintf("%d-line body", bl), false
		}
		return "", false
	case "exported":
		if n.IsExported {
			return "public API", false
		}
		return "internal helper", false
	}
	return "", false
}

// delegateHint phrases an outgoing-call summary for fan-out 1..3. Above 3
// the language stops being load-bearing — the cross-encoder treats a long
// callee list as bag-of-tokens noise rather than a delegation signal.
func delegateHint(stats TopoStats) string {
	if stats.FanOut == 1 && len(stats.CalleeNames) >= 1 {
		return "delegates to " + stats.CalleeNames[0]
	}
	if stats.FanOut > 1 && stats.FanOut <= 3 && len(stats.CalleeNames) > 0 {
		return "delegates to " + strings.Join(stats.CalleeNames, ", ")
	}
	return ""
}

// fanCountHint formats fan-in / fan-out raw counts. The zero-value fork
// uses hasTopologyData to distinguish "no inbound calls" (real signal) from
// "topology data missing" (silent skip) — only emit zeroPhrase in the
// former case.
func fanCountHint(count int, id, nonZeroFmt, zeroPhrase string) string {
	if count > 0 {
		return fmt.Sprintf(nonZeroFmt, count)
	}
	if hasTopologyData(id) {
		return zeroPhrase
	}
	return ""
}

// topologyRole classifies a node's structural role from its TopoStats and
// body length, returning a Go-doc-style English clause. Empty string when
// the node doesn't match any clear role (mid-graph generic function).
//
// The classification is intentionally heuristic and conservative — only
// emit when the role is unambiguous, since wrong classifications add
// misleading noise to the rerank doc text.
func topologyRole(stats TopoStats, n *knowledgev1.Node) string {
	if !hasTopologyData(n.Id) {
		return ""
	}
	bodyLines := n.EndLine - n.StartLine
	switch {
	case stats.FanOut <= 1 && bodyLines >= 1 && bodyLines <= 5 && stats.FanIn >= 1:
		return "convenience wrapper delegating to a helper"
	case stats.FanOut == 0 && stats.FanIn >= 3:
		return "leaf utility used by multiple call sites"
	case stats.FanOut >= 4 && stats.FanIn <= 1 && bodyLines >= 15:
		return "implementation function with multiple internal calls"
	case stats.FanOut >= 4 && stats.FanIn >= 2:
		return "orchestrator coordinating multiple helpers"
	case stats.FanIn >= 5 && bodyLines >= 10:
		return "widely-used helper"
	case n.IsExported && stats.FanIn == 0:
		return "public API entry point"
	}
	return ""
}

// hasTopologyData reports whether the topology sidecar map is populated
// AND contains an entry for this node ID. Used to disambiguate "fan-in
// = 0 because data is missing" from "fan-in = 0 because the node has no
// inbound calls" — only the latter should emit "no inbound calls".
func hasTopologyData(id string) bool {
	m := topologyHints.Load()
	if m == nil {
		return false
	}
	_, ok := (*m)[id]
	return ok
}
