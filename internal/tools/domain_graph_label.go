// SPDX-License-Identifier: Apache-2.0

// domain_graph_label.go — the render label for a graph target, split out of
// intercept_query_correlations_pivot.go when that file crossed the 500-line cap.
//
// IT SITS ALONE BECAUSE IT SERVES TWO ARMS. The composite-mode headers and the
// practice browse both take their label from it, so it belongs to neither file
// it is called from.

package tools

// domainGraphLabel returns a human label for the target graph used in the
// composite-mode headers and by the practice browse.
//
// IT IS NOT A PER-FAMILY PARTITION and must not become one. Three families share
// ONE arm and it probes three fields in a fixed order, returning the first that
// is set — which is a different construct from engine.queryGraphLabelFor, whose
// arms read a DIFFERENT field per family. That is why the practice singleton
// needed no rewrite here: a read with no instance field set already falls through
// to the bare family name.
//
// `source` is probed LAST rather than not at all, because the practice browse
// takes its header from this function: without it a hub-scoped browse and a
// whole-graph browse would render the same word, and requirement 5's marker
// would be missing from the one arm most likely to carry it. It sits after
// `language` so a legacy read still names the graph that answered.
func domainGraphLabel(a queryArgs) string {
	switch a.Graph {
	case "", "knowledge":
		return "knowledge"
	case "practice", "linkage", "code":
		if a.Repo != "" {
			return a.Graph + ":" + a.Repo
		}
		if a.Language != "" {
			return a.Graph + ":" + a.Language
		}
		if a.Source != "" {
			return a.Graph + ":" + a.Source
		}
		return a.Graph
	default:
		return a.Graph
	}
}
