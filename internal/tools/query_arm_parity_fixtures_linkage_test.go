// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_parity_fixtures_linkage_test.go holds the parity fixtures for the
// LINKAGE and WEB/PDF arms of InterceptQueryPracticeLinkage. They were split out
// of query_arm_parity_fixtures_test.go when the web/pdf stats arm pushed that
// file past the repo's file-length cap. The split is a file-length concern only,
// exactly like the graph/modes split the sibling header describes; every fixture
// here obeys the same rules stated there.

func queryParityLinkageWebPDFFixtures() map[armID]queryParityFixture {
	return map[armID]queryParityFixture{
		// routeLinkageClient's list-graphs gate reads id, text, mode and queries in
		// that order; all four are emptiness-gated here.
		armLinkageListGraphs: {
			entry: InterceptQueryPracticeLinkage,
			base:  map[string]any{"graph": "linkage"},
			discriminants: map[string]any{
				"graph": "linkage", "id": "", "text": "", "mode": "", "queries": []any{},
			},
		},

		armLinkageStats: {
			entry:         InterceptQueryPracticeLinkage,
			base:          map[string]any{"graph": "linkage", "mode": "stats"},
			discriminants: map[string]any{"graph": "linkage", "mode": "stats"},
		},

		armLinkageGetNode: {
			entry:         InterceptQueryPracticeLinkage,
			base:          map[string]any{"graph": "linkage", "id": qpSeedLinkage},
			discriminants: map[string]any{"graph": "linkage", "mode": "", "id": qpSeedLinkage},
		},

		// The retired arms answer from a fixed string and touch nothing —
		// precondition class (b).
		armLinkageSearchRetired: {
			entry:         InterceptQueryPracticeLinkage,
			base:          map[string]any{"graph": "linkage", "text": "probe-text"},
			discriminants: map[string]any{"graph": "linkage", "mode": "", "id": ""},
			opaque:        map[string]bool{"text": true, "queries": true},
			behavior:      qBehavesWithoutRead,
			precondition:  "class (b): a retired ranked-search arm serving a fixed message",
		},

		// The ranked read asks the CLIENT SEGMENT ENGINE, so it takes the default
		// read-positive behavior rather than class (b). `name` is in base because
		// composeRawGraphSegmentSearch refuses an empty one — a raw graph has no
		// default instance — and because that arm also refuses a name absent from
		// the collected-graph catalog, which is why the fixture seeds this instance.
		// `limit` and `fields` are
		// opaque for the same reasons the practice-search sibling gives: limit is
		// the rank cutoff k, a BOUND the probe never exceeds, and fields projects
		// only on the json render path this probe does not take.
		armWebPDFSearch: {
			entry:         InterceptQueryPracticeLinkage,
			base:          map[string]any{"graph": "web", "name": qpParityWebGraph, "text": "probe-text"},
			discriminants: map[string]any{"graph": "web", "mode": "", "id": ""},
			opaque:        map[string]bool{"text": true, "queries": true, "limit": true, "fields": true},
		},

		// The stats arm mirrors the practice-stats sibling; `name` stays OUT of
		// discriminants because it is genuinely consumed and its probe has to be
		// observable in the captured Stats target.
		armWebPDFStats: {
			entry:         InterceptQueryPracticeLinkage,
			base:          map[string]any{"graph": "web", "name": qpParityWebGraph, "mode": "stats"},
			discriminants: map[string]any{"graph": "web", "mode": "stats"},
		},

		// The modules listing takes no instance: `graph` and `mode` are the whole
		// input, so both are discriminants and the base carries nothing else.
		armWebPDFModules: {
			entry:         InterceptQueryPracticeLinkage,
			base:          map[string]any{"graph": "web", "mode": "modules"},
			discriminants: map[string]any{"graph": "web", "mode": "modules"},
		},

		// The checks stats arm shares the web/pdf arm's read set — the same
		// selector-driven body — so it takes the same fixture shape.
		//
		// NO `name` ANYWHERE, in base or discriminants, and that is the arm's own
		// contract rather than a fixture choice. The arm served two graphs and only
		// one of them carried a real instance name; checks is a singleton whose
		// selector policy admits no instance field, so with the other graph gone
		// `name` moved from the consumed set to the rejected set in
		// query_arm_registry_stats.go and this fixture must not send one.
		armBuiltinGraphStats: {
			entry:         InterceptQueryBuiltinStats,
			base:          map[string]any{"graph": "checks", "mode": "stats"},
			discriminants: map[string]any{"graph": "checks", "mode": "stats"},
		}}
}
