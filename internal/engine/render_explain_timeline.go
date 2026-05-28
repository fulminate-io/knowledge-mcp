// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// render_explain_timeline.go ports the GENERIC (graph-neutral) explain and
// timeline renderers the InterceptQueryExplainTimeline composer
// (cmd/knowledge/internal/tools) consumes. Direct ports of the server
// formatExplainEdges (tools_query_explain.go) and the generic timeline body
// writers formatGenericTimelineFlat / formatGenericTimelineBucketed +
// extractNodeTime (tools_query_timeline.go / tools_query_timeline_extract.go).
// Operate over *knowledgev1.Node/knowledgev1.Edge — NOT the log-engine renderers.

// RenderExplainEdges ports formatExplainEdges: the "## Explain — <label>" header
// + per-edge "### Edge #n" blocks (resolved from/to names, Type, Score, Method,
// Last validated, Evidence). The composer pre-resolves endpoint names via a bulk
// hydrate and passes them in nameByID (empty fallback → truncated id).
func RenderExplainEdges(label string, edges []knowledgev1.Edge, nameByID map[string]*knowledgev1.Node) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Explain — %s\n\n", label)
	fmt.Fprintf(&sb, "%d edge(s).\n\n", len(edges))
	for i := range edges {
		e := &edges[i]
		fromName := explainEndpointName(e.FromId, nameByID)
		toName := explainEndpointName(e.ToId, nameByID)
		fmt.Fprintf(&sb, "### Edge #%d — %s -> %s\n", i+1, fromName, toName)
		fmt.Fprintf(&sb, "- Type: %s\n", e.Type)
		if e.Confidence > 0 {
			fmt.Fprintf(&sb, "- Score: %.2f\n", e.Confidence)
		}
		if e.Method != "" {
			fmt.Fprintf(&sb, "- Method: %s\n", e.Method)
		}
		if !nanosToTime(e.LastValidated).IsZero() {
			fmt.Fprintf(&sb, "- Last validated: %s\n", nanosToTime(e.LastValidated).Format("2006-01-02T15:04:05Z07:00"))
		}
		if e.Evidence != "" {
			fmt.Fprintf(&sb, "- Evidence (raw): %s\n", e.Evidence)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// RenderExplainEmpty renders the no-edges body for the single-node form
// (explainNode empty branch).
func RenderExplainEmpty(label, nodeID, filterMsg string) string {
	return fmt.Sprintf("## Explain — %s\n\n_No edges touching %s for filter: %s._\n", label, nodeID, filterMsg)
}

// explainEndpointName resolves an endpoint id to its SymbolName via the
// bulk-hydrate map (port of resolveEdgeEndpoint: SymbolName, else truncated id
// to 20 chars).
func explainEndpointName(id string, nameByID map[string]*knowledgev1.Node) string {
	if n, ok := nameByID[id]; ok && n.SymbolName != "" {
		return n.SymbolName
	}
	if len(id) > 20 {
		return id[:20]
	}
	return id
}

// TimelineEntry pairs a node with its extracted timestamp — the row shape the
// timeline renderers consume (port of the server timelineEntry).
type TimelineEntry struct {
	Node *knowledgev1.Node
	At   time.Time
}

// CollectTimelineEntries extracts the timestamp for each node via the requested
// field, dropping nodes whose field is absent/unparseable (best-effort). Port of
// collectTimelineEntries.
func CollectTimelineEntries(nodes []*knowledgev1.Node, field string) []TimelineEntry {
	out := make([]TimelineEntry, 0, len(nodes))
	for i := range nodes {
		t, ok := ExtractNodeTime(nodes[i], field)
		if !ok {
			continue
		}
		out = append(out, TimelineEntry{Node: nodes[i], At: t})
	}
	return out
}

// SortTimelineEntries sorts ascending by timestamp (the handleGenericTimeline
// order). Mutates in place.
func SortTimelineEntries(entries []TimelineEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].At.Before(entries[j].At) })
}

// ExtractNodeTime resolves a time-field reference (port of extractNodeTime):
// CreatedAt/UpdatedAt struct fields, else a metadata key parsed RFC3339Nano →
// RFC3339 → Unix seconds.
func ExtractNodeTime(n *knowledgev1.Node, field string) (time.Time, bool) {
	switch field {
	case "CreatedAt", "created_at":
		if nanosToTime(n.CreatedAt).IsZero() {
			return time.Time{}, false
		}
		return nanosToTime(n.CreatedAt), true
	case "UpdatedAt", "updated_at":
		if nanosToTime(n.UpdatedAt).IsZero() {
			return time.Time{}, false
		}
		return nanosToTime(n.UpdatedAt), true
	}
	raw := kgtypes.Value(n, field)
	if raw == "" {
		return time.Time{}, false
	}
	return parseTimelineTimestamp(raw)
}

func parseTimelineTimestamp(raw string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	if sec, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(sec, 0).UTC(), true
	}
	return time.Time{}, false
}

// RenderTimelineEmpty renders the no-parseable-timestamps body.
func RenderTimelineEmpty(label, field string) string {
	return fmt.Sprintf("## Timeline — %s\n\n_No nodes carry a parseable %q timestamp._\n", label, field)
}

// RenderTimelineFlat ports formatGenericTimelineFlat: the Timeline header with
// field/T0/span + the offset/node/type/status table.
func RenderTimelineFlat(label, field string, entries []TimelineEntry) string {
	var sb strings.Builder
	t0 := entries[0].At
	tEnd := entries[len(entries)-1].At
	fmt.Fprintf(&sb, "## Timeline — %s\n\n", label)
	fmt.Fprintf(&sb, "**field:** `%s` · **T0:** %s · **span:** %s · **%d node(s)**\n\n",
		field, t0.Format(time.RFC3339), tEnd.Sub(t0).Truncate(time.Second), len(entries))
	sb.WriteString("| offset | node | type | status |\n")
	sb.WriteString("|---|---|---|---|\n")
	for _, e := range entries {
		fmt.Fprintf(&sb, "| T+%s | %s | %s | %s |\n",
			renderTimelineOffset(e.At.Sub(t0)), timelineNodeDisplay(e.Node),
			typeOrDash(kgtypes.NodeType(e.Node.Type)), statusOrDash(e.Node.Status))
	}
	return sb.String()
}

// RenderTimelineBucketed ports renderGenericTimelineBucketed +
// formatGenericTimelineBucketed: validates the bucket duration then renders the
// window/count/nodes table. Returns (body, "") on success or ("", errMsg) on a
// bad bucket.
func RenderTimelineBucketed(label, field string, entries []TimelineEntry, bucket string) (string, string) {
	dur, err := time.ParseDuration(bucket)
	if err != nil {
		return "", fmt.Sprintf("timeline %s: invalid bucket %q: %s (expected Go duration like '10s' or '1m')", label, bucket, err.Error())
	}
	if dur <= 0 {
		return "", fmt.Sprintf("timeline %s: bucket must be positive, got %q", label, bucket)
	}
	var sb strings.Builder
	t0 := entries[0].At
	fmt.Fprintf(&sb, "## Timeline (bucketed) — %s\n\n", label)
	fmt.Fprintf(&sb, "**field:** `%s` · **T0:** %s · **bucket:** %s · **%d node(s)**\n\n",
		field, t0.Format(time.RFC3339), dur.Truncate(time.Second), len(entries))
	groups := groupTimelineEntries(entries, t0, dur)
	sb.WriteString("| window | count | nodes |\n")
	sb.WriteString("|---|---|---|\n")
	for _, g := range groups {
		fmt.Fprintf(&sb, "| T+%s–T+%s | %d | %s |\n",
			renderTimelineOffset(g.offset), renderTimelineOffset(g.offset+dur),
			len(g.entries), renderTimelineBucketNodes(g.entries))
	}
	return sb.String(), ""
}

type timelineBucket struct {
	offset  time.Duration
	entries []TimelineEntry
}

func groupTimelineEntries(entries []TimelineEntry, t0 time.Time, bucket time.Duration) []timelineBucket {
	byOffset := make(map[time.Duration]*timelineBucket)
	var keys []time.Duration
	for _, e := range entries {
		off := (e.At.Sub(t0) / bucket) * bucket
		g, ok := byOffset[off]
		if !ok {
			g = &timelineBucket{offset: off}
			byOffset[off] = g
			keys = append(keys, off)
		}
		g.entries = append(g.entries, e)
	}
	slices.Sort(keys)
	out := make([]timelineBucket, 0, len(keys))
	for _, k := range keys {
		out = append(out, *byOffset[k])
	}
	return out
}

func renderTimelineBucketNodes(entries []TimelineEntry) string {
	const capN = 5
	parts := make([]string, 0, len(entries))
	for i, e := range entries {
		if i >= capN {
			parts = append(parts, fmt.Sprintf("…+%d more", len(entries)-capN))
			break
		}
		parts = append(parts, timelineNodeDisplay(e.Node))
	}
	return strings.Join(parts, ", ")
}

func timelineNodeDisplay(n *knowledgev1.Node) string {
	name := n.SymbolName
	if name == "" {
		name = n.Id
		if len(name) > 12 {
			name = name[:12]
		}
	}
	return fmt.Sprintf("`%s`", name)
}

func renderTimelineOffset(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Truncate(time.Second).String()
}

func statusOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
