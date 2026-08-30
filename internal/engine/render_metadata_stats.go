// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// render_metadata_stats.go ports the server's since-removed metadata_stats
// row-builder + table renderer + JSON payload. The composer
// (cmd/knowledge/internal/tools) reads the
// MetadataStats RPC's BOTH typed carriers — resp.GetMetadataStats() (the typed
// proto stats blob) + resp.GetOverrideConfig() (the typed proto override) — and
// threads them into BuildMetadataStatsRows → RecommendAction (the relocated
// CLIENT-side recommend cluster in recommend.go). The two highest-precedence
// RecommendAction rules are the ForceEdge/ForceScalar override checks, so a nil
// OverrideConfig would silently mis-recommend pinned keys — threading the typed
// override through here is load-bearing (the proto getters are nil-safe).

// MetadataStatsRow is the per-key row (port of the server metadataStatsRow). The
// JSON tags match the server payload shape so the format=json output is stable.
type MetadataStatsRow struct {
	Key                   string `json:"key"`
	DistinctValues        int64  `json:"distinct_values"`
	TotalWrites           int64  `json:"total_writes"`
	MedianNodesPerValue   int64  `json:"median_nodes_per_value"`
	CurrentRepresentation string `json:"current_representation"`
	RecommendedAction     string `json:"recommended_action"`
}

// BuildMetadataStatsRows ports the server buildMetadataStatsRows over the proto
// carriers: per key, build a shadow KeyStats stamped with the Live* distinct /
// median values, feed it to RecommendAction(shadow, override, key) — threading
// the OverrideConfig so operator-pinned keys surface FORCE_SCALAR/FORCE_EDGE.
// Rows sorted by TotalWrites desc (key tiebreak). nil stats → nil rows.
func BuildMetadataStatsRows(stats *knowledgev1.MetadataStats, override *knowledgev1.OverrideConfig) []MetadataStatsRow {
	keys := snapshotKeys(stats)
	if len(keys) == 0 {
		return nil
	}
	rows := make([]MetadataStatsRow, 0, len(keys))
	for _, key := range keys {
		ks := stats.GetKeys()[key]
		liveDistinct := liveDistinctValues(ks)
		liveMedian := liveMedianNodesPerValue(ks)
		rec := RecommendAction(liveKeyStatsFor(ks, liveDistinct, liveMedian), override, key)
		rows = append(rows, MetadataStatsRow{
			Key:                   key,
			DistinctValues:        liveDistinct,
			TotalWrites:           keyStatsWrites(ks),
			MedianNodesPerValue:   liveMedian,
			CurrentRepresentation: representationLabel(ks),
			RecommendedAction:     string(rec),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].TotalWrites != rows[j].TotalWrites {
			return rows[i].TotalWrites > rows[j].TotalWrites
		}
		return rows[i].Key < rows[j].Key
	})
	return rows
}

// snapshotKeys returns the metadata keys tracked in stats. nil stats → nil
// (the proto GetKeys() is nil-safe; ranging a nil map is fine). Free-func port
// of store.MetadataStats.SnapshotKeys.
func snapshotKeys(stats *knowledgev1.MetadataStats) []string {
	keys := stats.GetKeys()
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	return out
}

func keyStatsWrites(ks *knowledgev1.KeyStats) int64 {
	return ks.GetTotalWrites()
}

// liveKeyStatsFor returns a shadow *knowledgev1.KeyStats carrying the Live*
// distinct/median values + the source representation so RecommendAction's
// thresholds compare against current numbers (port of the server liveKeyStatsFor).
// Builds a fresh proto pointer rather than copying ks — copying a populated proto
// value trips copylocks (the message embeds a mutex). nil ks → nil shadow.
func liveKeyStatsFor(ks *knowledgev1.KeyStats, liveDistinct, liveMedian int64) *knowledgev1.KeyStats {
	if ks == nil {
		return nil
	}
	return &knowledgev1.KeyStats{
		DistinctValues:        liveDistinct,
		MedianNodesPerValue:   liveMedian,
		CurrentRepresentation: ks.GetCurrentRepresentation(),
	}
}

// representationLabel maps the KeyStats representation to a label (port). Reads
// the proto current_representation string accessor.
func representationLabel(ks *knowledgev1.KeyStats) string {
	if ks == nil {
		return "scalar (auto)"
	}
	switch ks.GetCurrentRepresentation() {
	case RepresentationEdge:
		return "edge"
	case RepresentationScalar:
		return "scalar"
	default:
		return "scalar (auto)"
	}
}

// RenderMetadataStatsTable ports the server renderMetadataStatsTable markdown.
func RenderMetadataStatsTable(label string, rows []MetadataStatsRow) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Metadata stats — %s graph (%d keys)\n\n", label, len(rows))
	sb.WriteString("| Key | Distinct Values | Total Writes | Median Nodes/Value | Current Rep | Recommended Action |\n")
	sb.WriteString("|---|---:|---:|---:|---|---|\n")
	for _, row := range rows {
		fmt.Fprintf(&sb, "| %s | %d | %d | %d | %s | %s |\n",
			row.Key, row.DistinctValues, row.TotalWrites, row.MedianNodesPerValue,
			row.CurrentRepresentation, row.RecommendedAction)
	}
	return sb.String()
}

// MetadataStatsJSONPayload ports the server metadataStatsJSONPayload — the
// {graph, stats[, name, language, account]} format=json shape. A nil rows slice
// is normalized to an empty array so the JSON shape is stable.
func MetadataStatsJSONPayload(label, name, language, account string, rows []MetadataStatsRow) map[string]any {
	if rows == nil {
		rows = []MetadataStatsRow{}
	}
	payload := map[string]any{"graph": label, "stats": rows}
	if name != "" {
		payload["name"] = name
	}
	if language != "" {
		payload["language"] = language
	}
	if account != "" {
		payload["account"] = account
	}
	return payload
}
