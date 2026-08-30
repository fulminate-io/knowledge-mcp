// SPDX-License-Identifier: Apache-2.0

package bootstrap

// query_unranked_builtin_parity_test.go is the END-TO-END half of the
// transformers query-rail claim, and of the checks CUTOVER that followed it. It lives here rather than in package
// tools for the reason the sibling parity harness states about itself: which arm
// out of the ORDERED chain answers a given shape is only observable by driving
// the real chain, and the arm under test is a new member of that chain.
//
// It is also the only place the fix could be observed RED. The claim arm did not
// exist before it landed, so a unit test calling it would not have compiled —
// which is not a failing test, it is no test. driveQueryParity exists on both
// sides of the change, so the IDENTICAL call renders the defect before and the
// refusal after.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// falseBM25DisclosureMarker is the footer the generic dispatch tail appended to a
// transformers/checks text query before this claim landed. It stays load-bearing
// for BOTH graphs after the checks cutover, for opposite reasons: transformers
// still carries no BM25 index, and checks must be answered by its own served arm
// rather than falling back to the tail that would assert this footer.
//
// IT IS THE DEFECT, NOT MERELY ITS SYMPTOM. The zero rows alone would read as "no
// matches"; this footer additionally ASSERTS that a BM25 arm ran. It cannot have:
// pipeline.bm25ArmEnabledFor gates the BM25 collector on
// kgtypes.HasRebuildableSegments, which excludes both graphs, so neither carries a
// BM25 index for a query to have read. A caller shown this footer has been told a
// retrieval arm answered them when none exists.
const falseBM25DisclosureMarker = "_search mode: BM25-only_"

// unrankedBuiltinRefusalMarkers are the fragments each graph's refusal must carry.
// Kept here rather than asserted as a whole string so this harness pins the
// CONTRACT (the graph is named, ranked search is declined, a working path is
// handed over) without duplicating the wording, which has one home in
// tools/intercept_search_reducible_graph.go.
var unrankedBuiltinRefusalMarkers = map[string][]string{
	"transformers": {
		"transformers", "not available",
		`query(graph:"transformers", name:"recipes", type:"recipe")`,
	},
}

// checksServedArmMarker is what the checks ranked arm answers with when the client
// segment engine is unwired, which is this harness's state. It is the SAME shape
// the knowledge and custom-graph rows assert, and asserting it POSITIVELY is what
// makes each row a statement about which arm answered rather than only about which
// one did not.
const checksServedArmMarker = "checks search: client segment engine unavailable"

// emptySearchModeFooter is the universal guard every checks row carries. The
// footer is emitted unconditionally when one is emitted at all, so an EMPTY marker
// ships an empty footer — the one failure shape common to all four modes, and the
// arm of this test that cannot be satisfied by a lucky label match.
const emptySearchModeFooter = "_search mode: _"

// TestQueryDispatchParity_UnrankedBuiltinsRefused drives every text-search shape
// the query schema publishes for the graph that carries no ranked index, and
// asserts each is answered by the self-describing refusal rather than by the
// vacuous zero the generic dispatch tail produced.
//
// IT COVERED TWO GRAPHS AND NOW COVERS ONE. checks is served after the cutover;
// its four rows moved to the sibling test below rather than being dropped, so the
// same four published modes are still driven for it — only the expectation moved
// from refused to served.
//
// THE MODE SET IS THE PUBLISHED ONE, not a sample: "" (the default), "text",
// "hybrid" and "recent" are exactly the text-bearing modes segmentSearchClaimMode
// claims, and query_schema.go publishes no "vector". Driving all four is what
// makes "no text-search shape still reaches the silent zero" a checked statement
// rather than an asserted one.
//
// transformers carries name:"recipes" because that is how a caller addresses the
// recipe bucket.
func TestQueryDispatchParity_UnrankedBuiltinsRefused(t *testing.T) {
	for _, graph := range []string{"transformers"} {
		for _, mode := range []string{"", "text", "hybrid", "recent"} {
			name := graph + "/mode=" + mode
			if mode == "" {
				name = graph + "/mode=default"
			}
			t.Run(name, func(t *testing.T) {
				c, eng := newParityClient(t)
				args := map[string]any{"graph": graph, "text": "probe-text"}
				if graph == "transformers" {
					args["name"] = "recipes"
				}
				if mode != "" {
					args["mode"] = mode
				}

				got := driveQueryParity(t, c, eng, args)
				t.Logf("observed: handled=%v execDelta=%d body=%q", got.handled, got.execDelta, got.body)

				require.Truef(t, got.handled,
					"a %s text search must be CLAIMED by the chain, not left to the generic dispatch tail", graph)
				assert.NotContainsf(t, got.body, genericDenyMarker,
					"%s must be REFUSED by name, not denied as an unrecognized shape", graph)

				// The defect, stated as its own assertion so a regression names itself.
				assert.NotContainsf(t, got.body, falseBM25DisclosureMarker,
					"%s must not claim a BM25 arm ran — it carries no BM25 segments", graph)
				assert.NotContainsf(t, got.body, "0 results",
					"%s must not render an empty result set where the truth is that no index exists", graph)

				for _, want := range unrankedBuiltinRefusalMarkers[graph] {
					assert.Containsf(t, got.body, want,
						"the %s refusal must carry %q", graph, want)
				}

				// A refusal that costs a read is not a refusal, it is a read whose
				// result was discarded. Zero is asserted against the counting engine
				// rather than inferred from the rendered text.
				assert.Zerof(t, got.execDelta,
					"%s refusal must issue NO read (observed %d)", graph, got.execDelta)
			})
		}
	}
}

// TestQueryDispatchParity_ChecksServedOnEveryPublishedMode is the checks half of
// the same harness after the cutover: the SAME four published text-bearing modes,
// with the expectation moved from refused to SERVED.
//
// THE POSITIVE MARKER IS THE POINT. "does not carry the refusal" alone would stay
// green if the arm stopped answering for some unrelated reason and the call fell
// through to a tail that happened to say nothing recognizable; asserting WHICH arm
// answered is what makes each row a statement about routing.
//
// THE FOOTER GUARD IS PER-ROW AND DERIVED, NEVER A MODE-TO-LABEL TABLE. The label
// is produced by the render composer from whether a query vector was genuinely
// attached and whether rerank ran — "vector+rerank", "vector", "BM25-only" — not
// from the mode name, and "recency" is not a footer label at all. Under this
// harness the segment engine is unwired, so the arm answers before composing any
// footer and no label is emitted; the guard that HOLDS for every row regardless is
// that an EMPTY footer never ships, which is the one failure shape common to all
// four modes.
//
// checks carries NO instance name because it addresses no instance at all
// (graphsel.InstanceField returns FieldNone for it).
func TestQueryDispatchParity_ChecksServedOnEveryPublishedMode(t *testing.T) {
	for _, mode := range []string{"", "text", "hybrid", "recent"} {
		name := "mode=" + mode
		if mode == "" {
			name = "mode=default"
		}
		t.Run(name, func(t *testing.T) {
			c, eng := newParityClient(t)
			args := map[string]any{"graph": "checks", "text": "probe-text"}
			if mode != "" {
				args["mode"] = mode
			}

			got := driveQueryParity(t, c, eng, args)
			t.Logf("observed: handled=%v execDelta=%d body=%q", got.handled, got.execDelta, got.body)

			require.True(t, got.handled,
				"a checks text search must be CLAIMED by the chain, not left to the generic dispatch tail")
			assert.NotContains(t, got.body, genericDenyMarker,
				"checks must be answered by its own arm, not denied as an unrecognized shape")
			assert.Contains(t, got.body, checksServedArmMarker,
				"the checks ranked arm must be the one that answered")

			// The pre-claim defect, still forbidden: falling back to the tail would
			// assert a BM25 arm ran over a graph this arm was supposed to serve.
			assert.NotContains(t, got.body, falseBM25DisclosureMarker,
				"checks must not be answered by the generic tail's false BM25 disclosure")
			assert.NotContains(t, got.body, emptySearchModeFooter,
				"an empty search-mode footer ships a disclosure that discloses nothing")

			// The retired refusal must not come back under any mode.
			assert.NotContains(t, got.body, "not available yet",
				"the checks refusal is retired; a row still carrying it means the cutover did not reach this mode")
		})
	}
}

// TestQueryDispatchParity_UnrankedBuiltinNonSearchShapesStillFallThrough is the
// other half of the claim, and the more important one for anybody following the
// refusal's own advice.
//
// Each refusal HANDS THE CALLER A BROWSE. If the new arm claimed those browse
// shapes too, the message would route its reader straight back into itself — a
// closed loop with no way out, which is worse than the silent zero it replaced.
// So the shapes the refusals name are driven here and asserted NOT to reach it.
//
// The by-id row carries a text field deliberately: a by-id read stays a lookup
// even when text rides along, and segmentSearchClaimMode's hasIDSelector argument
// is what enforces that. Without this row the id precedence would be unpinned.
//
// THE STATS SHAPE IS NO LONGER ONE OF THESE ROWS, and its removal is the honest
// half of a re-expression rather than a deletion. It used to sit here asserting
// fall-through, and it was green FOR THE WRONG REASON: mode:stats on transformers
// met the generic deny, which carries no refusal markers either. The no-loop
// rationale above never applied to it — a refusal advising a browse is not what
// answers a stats call — and it dissolves entirely now that stats genuinely
// serves. Its replacement is
// TestQueryDispatchParity_TransformersStatsIsServedNotFallenThrough below, which
// asserts the SERVED shape and keeps the still-true refusal-marker clause.
func TestQueryDispatchParity_UnrankedBuiltinNonSearchShapesStillFallThrough(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"transformers recipe browse (the path the refusal names)",
			map[string]any{"graph": "transformers", "name": "recipes", "type": "recipe"}},
		{"checks plural-type browse (the path the refusal names)",
			map[string]any{"graph": "checks", "types": []string{"finding", "example"}}},
		{"checks by-id read wins over a text field",
			map[string]any{"graph": "checks", "id": "some-check-id", "text": "probe-text"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, eng := newParityClient(t)
			got := driveQueryParity(t, c, eng, tc.args)
			t.Logf("observed: handled=%v execDelta=%d body=%q", got.handled, got.execDelta, got.body)

			for _, marker := range unrankedBuiltinRefusalMarkers["transformers"] {
				if marker == "transformers" {
					continue // the graph name legitimately appears in a transformers render
				}
				assert.NotContains(t, got.body, marker,
					"a non-search shape must NOT be answered by the refusal — the refusal names this shape as the way out")
			}
			assert.NotContains(t, got.body, "not available yet",
				"a non-search shape must NOT be answered by the retired checks refusal")
			assert.NotContains(t, got.body, checksServedArmMarker,
				"a non-search shape must NOT be claimed by the checks RANKED arm either — a browse "+
					"answered by the search arm is a clean render of a different operation")
		})
	}
}

// TestQueryDispatchParity_TransformersStatsIsServedNotFallenThrough is the
// re-expression of the row retired above. It keeps the clause that stayed true —
// a stats render carries none of the search-refusal markers — and replaces the
// fall-through claim with the assertion that now discriminates: the call is
// CLAIMED and answered with a stats render, not met by the generic deny.
func TestQueryDispatchParity_TransformersStatsIsServedNotFallenThrough(t *testing.T) {
	c, eng := newParityClient(t)
	got := driveQueryParity(t, c, eng, map[string]any{
		"graph": "transformers", "name": "recipes", "mode": "stats",
	})
	t.Logf("observed: handled=%v execDelta=%d body=%q", got.handled, got.execDelta, got.body)

	assert.True(t, got.handled, "mode:stats on transformers is CLAIMED by the stats arm")
	assert.NotContains(t, got.body, genericDenyMarker,
		"the shape used to fall through to the generic deny — that is the defect this asserts is gone")
	assert.Contains(t, got.body, transformersStatsHeaderPrefix,
		"and it is answered by a stats render, not merely claimed")

	// THE CLAUSE THAT STAYED TRUE, carried over verbatim in intent: a stats render
	// is not the search refusal.
	for _, marker := range unrankedBuiltinRefusalMarkers["transformers"] {
		if marker == "transformers" {
			continue // the graph name legitimately appears in a transformers render
		}
		assert.NotContains(t, got.body, marker,
			"a stats render must NOT carry the ranked-search refusal")
	}
}

// TestQueryDispatchParity_OtherFamiliesUnchangedByTheUnrankedRefusal is the
// CONTROL set: one representative of every other family whose query text-search
// has its own arm, asserted POSITIVELY — each must still land on the arm that
// owns it, and none may be answered by the new refusal.
//
// Positive assertion matters here. "Does not contain the refusal" alone would
// stay green if a family stopped being served for some unrelated reason, so each
// row also names the marker proving WHICH arm answered.
func TestQueryDispatchParity_OtherFamiliesUnchangedByTheUnrankedRefusal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   map[string]any
		marker string
		why    string
	}{
		{
			name:   "knowledge",
			args:   map[string]any{"graph": "knowledge", "text": "probe-text"},
			marker: knowledgeSearchArmMarker,
			why:    "the knowledge search arm answers before any RPC when the segment engine is unwired",
		},
		{
			name:   "registered custom graph",
			args:   map[string]any{"graph": "hellograph", "name": "demo", "text": "probe-text"},
			marker: registeredGraphSearchArmMarker,
			why:    "the custom-graph arm is the twin the new arm was modeled on; it must keep its own answer",
		},
		{
			name:   "linkage",
			args:   map[string]any{"graph": "linkage", "text": "probe-text"},
			marker: "retired",
			why:    "linkage's own retired refusal is the template these two were built on and must still fire",
		},
		{
			name: "practice",
			args: map[string]any{"graph": "practice", "text": "probe-text"},
			// A phrase ONLY the practice list-graphs arm emits, not the bare word
			// "practice": a marker of "practice" alone would stay green if this row
			// were answered by something else that happened to mention the graph.
			//
			// It used to be the handler SYMBOL "InterceptQueryPracticeLinkage",
			// which the generic accounting tail renders. That tail is now replaced
			// on this arm by a message naming the call that works, so the symbol is
			// gone from the body. The replacement marker is strictly more
			// arm-identifying — no other arm can emit this sentence — and it is
			// text a caller actually reads rather than an internal Go symbol.
			marker: `graph:"practice" with no language is the practice-graph ENUMERATION`,
			why:    "practice text search stays on the practice/linkage entry point, which answers it by name",
		},
		{
			name:   "cloud",
			args:   map[string]any{"graph": "cloud", "account": "acct", "text": "probe-text"},
			marker: "cloud",
			why:    "cloud text search stays on the cloud/cicd arm",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, eng := newParityClient(t)
			got := driveQueryParity(t, c, eng, tc.args)
			t.Logf("observed: handled=%v execDelta=%d body=%q", got.handled, got.execDelta, got.body)

			assert.Containsf(t, strings.ToLower(got.body), strings.ToLower(tc.marker),
				"%s: %s", tc.name, tc.why)
			assert.NotContainsf(t, got.body, "not available yet",
				"%s must not be answered by the checks refusal", tc.name)
			assert.NotContainsf(t, got.body, `query(graph:"transformers", name:"recipes", type:"recipe")`,
				"%s must not be answered by the transformers refusal", tc.name)
		})
	}
}
