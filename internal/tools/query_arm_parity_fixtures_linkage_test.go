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

		// The transformers/checks refusal is the same class as the retired linkage
		// arm above — a fixed message, no read — so it carries the same class (b)
		// precondition and the same opaque list.
		//
		// THE BASE DRIVES transformers RATHER THAN checks, deliberately: transformers
		// is the branch that carries `name` (the "recipes" bucket), and `name` is in
		// this arm's consumed set. A checks base would leave that cell driven against
		// a graph whose family sends no name at all, which is the shape of a
		// consumed-but-never-exercised declaration. The checks branch is covered by
		// the end-to-end parity rows in package bootstrap, which drive both graphs
		// across every published text mode.
		//
		// mode:"text" IS IN THE BASE FOR THE SAME REASON THE REGISTERED-GRAPH TWIN
		// PUTS IT THERE, and it is not cosmetic. segmentSearchClaimMode consults its
		// hasIDSelector argument ONLY on the empty-mode branch, so under a default
		// mode an id/ids probe DESELECTS the arm (the by-id read wins) and the two
		// rejection cells would be unreachable. Pinning an explicit text mode keeps
		// them reachable, which is what lets this row assert that id and ids are
		// REFUSED BY NAME rather than quietly routed elsewhere.
		armUnrankedBuiltinSearchRefused: {
			entry: InterceptQueryUnrankedBuiltin,
			base: map[string]any{
				"graph": "transformers", "name": "recipes", "mode": "text", "text": "probe-text",
			},
			discriminants: map[string]any{"graph": "transformers", "mode": "text"},
			deselecting:   queryParityThoughtFilterDeselects(),
			opaque:        map[string]bool{"text": true},
			behavior:      qBehavesWithoutRead,
			precondition:  "class (b): a ranked-search refusal arm serving a fixed message",
		},

		// The ranked read DRAINS the raw graph, so it takes the default
		// read-positive behavior rather than class (b). `name` is in base because
		// composeRawGraphSearch refuses an empty one — a raw graph has no default
		// instance — and every row would otherwise error. `limit` and `fields` are
		// opaque for the same reasons the practice-search sibling gives: limit is
		// the rank cutoff k, a BOUND the probe never exceeds, and fields projects
		// only on the json render path this probe does not take.
		armWebPDFSearch: {
			entry:         InterceptQueryPracticeLinkage,
			base:          map[string]any{"graph": "web", "name": "parity-web-graph", "text": "probe-text"},
			discriminants: map[string]any{"graph": "web", "mode": "", "id": ""},
			opaque:        map[string]bool{"text": true, "queries": true, "limit": true, "fields": true},
		},

		// The stats arm mirrors the practice-stats sibling; `name` stays OUT of
		// discriminants because it is genuinely consumed and its probe has to be
		// observable in the captured Stats target.
		armWebPDFStats: {
			entry:         InterceptQueryPracticeLinkage,
			base:          map[string]any{"graph": "web", "name": "parity-web-graph", "mode": "stats"},
			discriminants: map[string]any{"graph": "web", "mode": "stats"},
		},

		// The checks / transformers stats arm shares the web/pdf arm's read set —
		// the same selector-driven body — so it takes the same fixture shape. It is
		// driven on TRANSFORMERS rather than checks because that graph carries a
		// real instance name, which keeps `name` observable in the captured Stats
		// target exactly as the sibling above requires. `name` therefore stays OUT
		// of discriminants.
		armBuiltinGraphStats: {
			entry:         InterceptQueryBuiltinStats,
			base:          map[string]any{"graph": "transformers", "name": "recipes", "mode": "stats"},
			discriminants: map[string]any{"graph": "transformers", "mode": "stats"},
		}}
}
