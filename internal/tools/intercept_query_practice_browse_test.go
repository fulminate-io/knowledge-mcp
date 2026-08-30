// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_query_practice_browse_test.go covers the two behaviours
// intercept_query_practice_browse.go owns: the browse arm's filter/paging
// routing, and the loud segment-gap notice on a zero-hit ranked search.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// browseExecFake is a recording engine.ExecuteFn: it captures every plan the
// browse arm builds and answers with a caller-supplied response, so a subtest can
// assert on the REQUEST (what the arm routed) and drive the render's Total
// independently of the node count — which is what the pagination footer reads.
type browseExecFake struct {
	reqs []*knowledgev1.ExecuteRequest
	resp *knowledgev1.ExecuteResponse
}

func (f *browseExecFake) exec(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.reqs = append(f.reqs, req)
	return f.resp, nil
}

func (f *browseExecFake) lastPlan(t *testing.T) *knowledgev1.QueryPlan {
	t.Helper()
	require.Len(t, f.reqs, 1, "the browse must issue EXACTLY ONE Execute — no over-fetch-then-filter")
	return f.reqs[0].GetQuery()
}

// browseNode is a one-line practice node fixture.
func browseNode(id, name string, meta map[string]string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, SymbolName: name, Type: "pattern", Description: "d", Metadata: meta}
}

// TestPracticeBrowse_RoutesFilters pins that every browse filter the arm declares
// CONSUMED actually reaches the read plan, that the row cap defaults to the
// engine's own constant, and that the fan-out sentinel still fans out.
func TestPracticeBrowse_RoutesFilters(t *testing.T) {
	t.Run("bare", func(t *testing.T) {
		f := &browseExecFake{resp: &knowledgev1.ExecuteResponse{
			Nodes: []*knowledgev1.Node{browseNode("p1", "Use errgroup", nil)},
			Total: 1,
		}}
		res := practiceBrowse(opCtx(), f.exec, queryArgs{Graph: "practice", Language: "go"})
		plan := f.lastPlan(t)
		sel := plan.GetSelection()
		assert.Empty(t, sel.GetNodeType(), "a bare browse pins NO node type — practice graphs hold four")
		assert.Empty(t, sel.GetNodeTypes())
		assert.Equal(t, "go", f.reqs[0].GetTarget().GetLanguage(), "the target is keyed on language")
		assert.Equal(t, "practice", f.reqs[0].GetTarget().GetGraph())
		body := textBodyTools(res)
		assert.Contains(t, body, "## practice:go — 1 nodes")
		assert.Contains(t, body, "Use errgroup")
	})

	t.Run("type", func(t *testing.T) {
		f := &browseExecFake{resp: &knowledgev1.ExecuteResponse{}}
		practiceBrowse(opCtx(), f.exec, queryArgs{Graph: "practice", Language: "go", Type: "finding"})
		assert.Equal(t, "finding", f.lastPlan(t).GetSelection().GetNodeType())
	})

	t.Run("meta_exists", func(t *testing.T) {
		f := &browseExecFake{resp: &knowledgev1.ExecuteResponse{}}
		practiceBrowse(opCtx(), f.exec, queryArgs{
			Graph: "practice", Language: "go", Meta: map[string]string{"dsl_pattern": "*"},
		})
		preds := f.lastPlan(t).GetSelection().GetMetadataPredicates()
		require.Len(t, preds, 1)
		assert.Equal(t, "dsl_pattern", preds[0].GetKey())
		// THE LOAD-BEARING LEG. "*" is the key-PRESENCE sentinel. A hand-rolled
		// lowering that mapped it to equality-against-a-literal-asterisk would
		// compile, satisfy a key-only assertion, and return nothing in production.
		assert.Equal(t, knowledgev1.MetadataPredicate_OP_EXISTS, preds[0].GetOp(),
			`meta value "*" must lower to OP_EXISTS, never OP_EQ against a literal asterisk`)
	})

	t.Run("paging", func(t *testing.T) {
		f := &browseExecFake{resp: &knowledgev1.ExecuteResponse{
			Nodes: []*knowledgev1.Node{browseNode("p1", "A", nil), browseNode("p2", "B", nil)},
			Total: 9,
		}}
		res := practiceBrowse(opCtx(), f.exec, queryArgs{
			Graph: "practice", Language: "go", Limit: 2, Offset: 5,
		})
		plan := f.lastPlan(t)
		assert.EqualValues(t, 2, plan.GetLimit(), "the caller's limit rides the plan, not a client-side slice")
		assert.EqualValues(t, 5, plan.GetOffset())
		assert.False(t, plan.GetSkipTotal(),
			"SkipTotal must stay FALSE — renderBrowseResponse reads Total for the pagination footer")
		// The footer is the observable consequence of carrying Total: 5+2 < 9.
		assert.Contains(t, textBodyTools(res), "_Use offset=7 to see more._")
	})

	t.Run("default_cap", func(t *testing.T) {
		f := &browseExecFake{resp: &knowledgev1.ExecuteResponse{}}
		practiceBrowse(opCtx(), f.exec, queryArgs{Graph: "practice", Language: "go"})
		assert.EqualValues(t, engine.BrowseDefaultLimit, f.lastPlan(t).GetLimit(),
			"an absent limit takes the ENGINE's browse cap, not a local literal")
	})

	t.Run("lang_all", func(t *testing.T) {
		// The browse arm's second conjunct. A text-less language:"all" must still
		// reach the FAN-OUT arm; browsing a practice graph literally named "all"
		// would turn a working fan-out into an empty browse.
		//
		// WHAT THE FAN-OUT ARM NOW DOES WITH IT is refuse: an empty-text ranked
		// search has nothing to rank, so it answered with a confident zero. The
		// refusal is therefore the routing marker — only the fan-out arm emits it,
		// exactly as "No practice graphs found." used to serve that role, and a
		// browse of a graph named "all" would render "No nodes in practice:all"
		// instead.
		var execHits atomic.Int64
		gc := newInterceptHarness(t, &execHits, cannedNodesResp())
		deps := &interceptDeps{gc: gc, segMgr: newFanOutSegmentSearcher(nil)}

		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{Graph: "practice", Language: "all"})
		body := textBodyTools(res)
		assert.Equal(t, practiceFanOutNeedsText, body, "the text-less fan-out is refused, naming the calls that work")
		assert.NotContains(t, body, "practice:all")
	})

	t.Run("projection", func(t *testing.T) {
		f := &browseExecFake{resp: &knowledgev1.ExecuteResponse{
			Nodes: []*knowledgev1.Node{
				browseNode("p1", "Use errgroup", map[string]string{"dsl_pattern": "http.DefaultClient", "other": "x"}),
			},
			Total: 1,
		}}
		res := practiceBrowse(opCtx(), f.exec, queryArgs{
			Graph: "practice", Language: "go", Format: "json",
			Fields: []string{"id", "name", "metadata.dsl_pattern"},
		})
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(res)), &payload), "format:json must emit valid JSON")
		rows, ok := payload["results"].([]any)
		require.True(t, ok, "the browse-JSON envelope carries results[]")
		require.Len(t, rows, 1)
		row, ok := rows[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "p1", row["id"])
		assert.Equal(t, "Use errgroup", row["name"])
		// THIS is the metadata the text render cannot reach: it prints only the
		// meta keys used as PREDICATES, and this call supplies none.
		assert.Equal(t, "http.DefaultClient", row["metadata.dsl_pattern"])
		assert.NotContains(t, row, "other", "fields PROJECTS — an unrequested key must not ride along")
	})
}

// gapCoverageFake is a programmable SegmentCoverageReader for the segment-gap
// branches. It is mutex-guarded because practiceFanOutGapNotice probes every
// enumerated graph in PARALLEL; the package's other coverage fake appends to an
// unsynchronised slice and would race here.
type gapCoverageFake struct {
	mu      sync.Mutex
	covered int
	err     error
	probes  int
	// coveredByGraph overrides covered PER GRAPH, which is what a MIXED fixture
	// needs: some graphs indexed, some dark. A uniform fake cannot tell the
	// unindexed bucket from the all-empty path, so it cannot discriminate the
	// partial-disclosure behaviour at all.
	coveredByGraph map[string]int
}

func (f *gapCoverageFake) ShippedSegmentDocCount(
	_ context.Context, _ kgtypes.GraphType, name string,
) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes++
	if n, ok := f.coveredByGraph[name]; ok {
		return n, f.err
	}
	return f.covered, f.err
}

func (f *gapCoverageFake) ResidentDocCount(kgtypes.GraphType, string) int { return 0 }

// LiveResidentDocCount answers the SAME programmed number ShippedSegmentDocCount
// does, because the practice zero-hit qualifier's LOCAL OPERAND MOVED HERE. It used
// to read the shipped count off a remote manifest; that manifest is gone, and the
// operand is now this client's live-resident read.
//
// ONE KNOB STILL DRIVES THE OPERAND, which is what keeps every fixture in this file
// meaning what its author wrote. Had this kept returning a hardcoded 0, the
// covered-greater-than-zero fixtures would have silently flipped from "a genuine
// no-match" to "the ranked index is missing" — the same assertions, inverted, with
// nothing in the diff to show it.
//
// IT COUNTS AS A PROBE for the same reason: probeCount means "the coverage operand
// was read", and the hot-path test's zero would stop meaning that if the operand
// moved to a method that did not count.
func (f *gapCoverageFake) LiveResidentDocCount(_ kgtypes.GraphType, name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes++
	if n, ok := f.coveredByGraph[name]; ok {
		return n
	}
	return f.covered
}
func (f *gapCoverageFake) RepairVerification(kgtypes.GraphType, string) (RepairVerification, bool) {
	return RepairVerification{}, false
}

func (f *gapCoverageFake) LoadRebuildState(
	kgtypes.GraphType, string,
) (int64, []searchengine.ExternalID, error) {
	return 0, nil, nil
}
func (f *gapCoverageFake) LoadMergeWatermark(kgtypes.GraphType, string) (int64, error) { return 0, nil }

func (f *gapCoverageFake) probeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probes
}

// TestPracticeSearch_LoudWhenSegmentsAbsent pins the zero-hit discriminator: a
// missing ranked index and an un-embedded graph are LOUD, a genuine no-match and
// a genuinely empty graph stay clean, an unqualifiable zero says so, and a
// fan-out whose graphs FAILED never reports "no matches".
func TestPracticeSearch_LoudWhenSegmentsAbsent(t *testing.T) {
	// zeroHitDeps wires a practice search that returns NO hits, with programmable
	// coverage and programmable graph stats behind it.
	zeroHitDeps := func(t *testing.T, cov SegmentCoverageReader, stats *knowledgev1.GraphStats) *interceptDeps {
		t.Helper()
		gc, h := newFanOutHarnessWithHandler(t, []string{"go"})
		h.stats = stats
		return &interceptDeps{gc: gc, segMgr: &fakeSegmentSearcher{}, segCoverage: cov}
	}

	t.Run("gap_loud", func(t *testing.T) {
		cov := &gapCoverageFake{covered: 0}
		deps := zeroHitDeps(t, cov, &knowledgev1.GraphStats{NodeCount: 3117, BinaryVectorCount: 2556})
		res := gatedRoutePractice(opCtx(), deps, deps.gc, queryArgs{
			Graph: "practice", Language: "design-patterns", Text: "event",
		})
		body := textBodyTools(res)
		assert.True(t, res.IsError, "a missing ranked index is an ERROR, not data: %s", body)
		// The remedy is asserted as an INDEPENDENT literal rather than through
		// practiceRebuildHint: an assertion routed via the same constant it is
		// catching cannot fail when that constant is wrong.
		assert.Contains(t, body, `"operation":"rebuild_segments"`, "the notice names the rebuild invocation")
		// And the graph name, so the interpolation is proven to have RUN rather
		// than the bare template being echoed.
		assert.Contains(t, body, "design-patterns", "the notice names the graph")
		assert.Contains(t, body, "2556", "the notice names the embedded count")
	})

	t.Run("no_match", func(t *testing.T) {
		// covered > 0: the index exists and was searched, so the zero is the truth.
		cov := &gapCoverageFake{covered: 42}
		deps := zeroHitDeps(t, cov, &knowledgev1.GraphStats{NodeCount: 3117, BinaryVectorCount: 2556})
		res := gatedRoutePractice(opCtx(), deps, deps.gc, queryArgs{
			Graph: "practice", Language: "go", Text: "nonsense",
		})
		body := textBodyTools(res)
		assert.False(t, res.IsError, "a genuine no-match is not an error: %s", body)
		assert.NotContains(t, body, "rebuild_segments")
		assert.NotContains(t, body, "could not be qualified")
	})

	t.Run("empty_graph", func(t *testing.T) {
		cov := &gapCoverageFake{covered: 0}
		deps := zeroHitDeps(t, cov, &knowledgev1.GraphStats{NodeCount: 0, BinaryVectorCount: 0})
		res := gatedRoutePractice(opCtx(), deps, deps.gc, queryArgs{
			Graph: "practice", Language: "go", Text: "anything",
		})
		body := textBodyTools(res)
		assert.False(t, res.IsError, "a genuinely empty graph renders a clean zero: %s", body)
		assert.NotContains(t, body, "rebuild_segments")
		assert.NotContains(t, body, "could not be qualified")
	})

	t.Run("nil_seam", func(t *testing.T) {
		// No coverage seam at all: the zero stands but must NOT pass as clean.
		deps := zeroHitDeps(t, nil, &knowledgev1.GraphStats{NodeCount: 3117, BinaryVectorCount: 2556})
		res := gatedRoutePractice(opCtx(), deps, deps.gc, queryArgs{
			Graph: "practice", Language: "go", Text: "event",
		})
		body := textBodyTools(res)
		assert.False(t, res.IsError, "an unqualifiable zero is a caveat, not an error: %s", body)
		assert.Contains(t, body, "could not be qualified", "the response says the zero could not be qualified")
		assert.Contains(t, body, "seam is unwired", "and names the reason")
	})

	t.Run("hot_path", func(t *testing.T) {
		// A NON-EMPTY hit set must read NEITHER operand — the check is off the hot
		// path. This is the known-positive for the probe counter: without it,
		// "zero probes" would be indistinguishable from a counter never wired.
		cov := &gapCoverageFake{covered: 0}
		gc, h := newFanOutHarnessWithHandler(t, []string{"go"},
			practiceNode("p:go", "GoWorkerPool", "bounded goroutines"))
		h.stats = &knowledgev1.GraphStats{NodeCount: 3117, BinaryVectorCount: 2556}
		deps := &interceptDeps{
			gc:          gc,
			segMgr:      &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "p:go", Score: 0.9}}},
			segCoverage: cov,
		}
		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{
			Graph: "practice", Language: "go", Text: "pool",
		})
		assert.False(t, res.IsError, textBodyTools(res))
		assert.Contains(t, textBodyTools(res), "GoWorkerPool", "the hits render normally")
		assert.Zero(t, cov.probeCount(), "a successful search must not read the coverage operand")

		// KNOWN POSITIVE, same run: the identical fake DOES get probed once the hit
		// set is empty, so the zero above is a real never-called and not a dead fake.
		empty := zeroHitDeps(t, cov, &knowledgev1.GraphStats{NodeCount: 3117, BinaryVectorCount: 2556})
		gatedRoutePractice(opCtx(), empty, empty.gc, queryArgs{
			Graph: "practice", Language: "go", Text: "pool",
		})
		assert.Positive(t, cov.probeCount(), "an empty hit set DOES read the coverage operand")
	})

	t.Run("unembedded", func(t *testing.T) {
		cov := &gapCoverageFake{covered: 0}
		deps := zeroHitDeps(t, cov, &knowledgev1.GraphStats{NodeCount: 3117, BinaryVectorCount: 0})
		res := gatedRoutePractice(opCtx(), deps, deps.gc, queryArgs{
			Graph: "practice", Language: "go", Text: "event",
		})
		body := textBodyTools(res)
		assert.True(t, res.IsError, "an un-embedded graph is an ERROR, not data: %s", body)
		assert.Contains(t, body, "3117", "the notice names the node count")
		assert.Contains(t, body, "none can be built yet")
		// A rebuild cannot succeed with zero vectors, so the message must not
		// suggest one IN ANY FORM.
		assert.NotContains(t, body, "rebuild_segments", "an unactionable remedy must not be offered")
	})

	t.Run("fanout_fail", func(t *testing.T) {
		// One graph errors, the other returns nothing. merged is empty, and the
		// response must NAME the failure rather than report a confident no-match.
		gc, h := newFanOutHarnessWithHandler(t, []string{"go", "python"})
		h.stats = &knowledgev1.GraphStats{NodeCount: 3117, BinaryVectorCount: 2556}
		mgr := newFanOutSegmentSearcher(nil)
		mgr.errsByGr = map[string]error{"go": errors.New("segment pool unreadable")}
		deps := &interceptDeps{gc: gc, segMgr: mgr, segCoverage: &gapCoverageFake{covered: 7}}

		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{
			Graph: "practice", Language: "all", Text: "event",
		})
		body := textBodyTools(res)
		assert.True(t, res.IsError, "a fan-out whose graph failed is an ERROR: %s", body)
		assert.Contains(t, body, "go", "the response names the failed graph")
		assert.Contains(t, body, "segment pool unreadable", "and its error")
		assert.NotContains(t, strings.ToLower(body), "no matches",
			"the caller must never be told 'no matches' when the search did not run")
	})
}

// TestPracticeFanOut_DisclosesUnindexedGraphs is the MIXED-fixture gate for the
// partial fan-out: some graphs indexed, some dark, in the SAME call.
//
// A uniform fixture cannot test this. If every graph lacks an index the merge is
// empty and the pre-existing all-empty path answers, so the third bucket is never
// exercised; if every graph has one there is nothing to disclose. Only a mixed
// corpus reaches the case where results ARE returned while some named graph
// contributed nothing — which is the steady state during any heal window, and the
// one that made a cross-graph ranking look comprehensive and near-random at once.
func TestPracticeFanOut_DisclosesUnindexedGraphs(t *testing.T) {
	// dark has no ranked index; lit does. Only lit returns hits.
	fanOut := func(t *testing.T, coveredByGraph map[string]int) (string, bool) {
		t.Helper()
		gc, h := newFanOutHarnessWithHandler(t, []string{"lit", "dark"},
			practiceNode("p:lit", "LitPattern", "indexed graph hit"),
		)
		// Non-zero embedded is what makes a zero-coverage graph LOUD (an index that
		// is missing rather than a graph that is empty).
		h.stats = &knowledgev1.GraphStats{NodeCount: 3117, BinaryVectorCount: 2556}
		deps := &interceptDeps{
			gc: gc,
			segMgr: newFanOutSegmentSearcher(map[string][]searchengine.Hit{
				"lit": {{ID: "p:lit", Score: 0.9}},
			}),
			segCoverage: &gapCoverageFake{coveredByGraph: coveredByGraph},
		}
		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{
			Graph: "practice", Language: "all", Text: "pattern",
		})
		return textBodyTools(res), res.IsError
	}

	t.Run("names_only_the_unindexed_graph", func(t *testing.T) {
		body, isErr := fanOut(t, map[string]int{"lit": 5, "dark": 0})
		require.False(t, isErr, "results were returned, so this is disclosure not refusal: %s", body)

		// The results still render — this is disclosure, not suppression.
		assert.Contains(t, body, "Searched 2 practice graphs", "the fan-out header still claims both")
		assert.Contains(t, body, "LitPattern", "the indexed graph's hit is served")

		// THE FIX: the header claims two graphs, so the response must say which one
		// contributed nothing and why.
		assert.Contains(t, body, "Incomplete result set", "a partial ranking must say it is partial")
		assert.Contains(t, body, "no ranked index yet", "and name the cause")
		assert.Contains(t, body, "dark", "and name the graph that contributed nothing")

		// THE DISCRIMINATING LEG. Naming every graph would be as useless as naming
		// none: the line must single out the dark one, not list the searched set.
		darkLine := body[strings.Index(body, "Incomplete result set"):]
		assert.NotContains(t, darkLine, "lit",
			"the indexed graph must NOT be named as unindexed — the line discriminates")
	})

	t.Run("silent_when_every_graph_is_indexed", func(t *testing.T) {
		// THE KNOWN POSITIVE. Without it, a line printed unconditionally would
		// satisfy every assertion above.
		body, isErr := fanOut(t, map[string]int{"lit": 5, "dark": 7})
		require.False(t, isErr, body)
		assert.Contains(t, body, "Searched 2 practice graphs")
		assert.NotContains(t, body, "Incomplete result set",
			"a fully-indexed fan-out must not claim to be partial")
		assert.NotContains(t, body, "no ranked index yet")
	})
}

// TestPracticeBrowse_TruncationNotice pins that the practice browse discloses the
// SERVER'S truncation verdict. The arm issues its own Execute and renders
// directly, so it never passes through engine.Render — the single place every
// compiled tool's response picks up the notice. Without the disclosure a browse
// the row ceiling clamped renders as a complete-looking list with rows silently
// missing.
//
// TWO POLARITIES IN ONE TEST. The untruncated leg is what refuses a notice
// appended unconditionally, which a truncated-only assertion would accept.
func TestPracticeBrowse_TruncationNotice(t *testing.T) {
	browse := func(t *testing.T, truncated bool) kgtools.ToolResult {
		t.Helper()
		f := &browseExecFake{resp: &knowledgev1.ExecuteResponse{
			Nodes:     []*knowledgev1.Node{browseNode("p1", "Use errgroup", nil)},
			Total:     1,
			Truncated: truncated,
		}}
		return practiceBrowse(opCtx(), f.exec, queryArgs{Graph: "practice", Language: "go"})
	}

	t.Run("truncated_response_discloses", func(t *testing.T) {
		res := browse(t, true)
		require.False(t, res.IsError, textBodyTools(res))
		assert.Contains(t, textBodyTools(res), serverRowCeilingSentence,
			"practiceBrowse dropped the server's truncation verdict: the rendered result carries no "+
				"row-ceiling disclosure, so a clamped browse reads as a complete one")
	})

	t.Run("whole_response_stays_silent", func(t *testing.T) {
		res := browse(t, false)
		require.False(t, res.IsError, textBodyTools(res))
		assert.NotContains(t, textBodyTools(res), serverRowCeilingSentence,
			"an untruncated browse must not claim to be partial")
	})
}

// serverRowCeilingSentence is the fragment of engine.truncationNotice's product
// copy the disclosure assertions anchor on. It is deliberately the SERVER-ROW-
// CEILING wording rather than the shorter "may be incomplete": the client-side
// limit-clamp notices (recallLimitClampNotice, searchLimitClampNotice) and
// plan_tree's subtree variant all carry that shorter fragment, so an assertion
// anchored on it would pass on a different notice entirely.
const serverRowCeilingSentence = "the server row ceiling engaged, so this result may be incomplete"
