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
		res := practiceBrowse(opCtx(), f.exec, queryArgs{Graph: "practice"})
		plan := f.lastPlan(t)
		sel := plan.GetSelection()
		assert.Empty(t, sel.GetNodeType(), "a bare browse pins NO node type — the practice graph holds four")
		assert.Empty(t, sel.GetNodeTypes())
		// THE TARGET CARRIES NO INSTANCE FIELD, which is the singleton's whole
		// addressing convention. It used to be keyed on `language`, and a target
		// still carrying one would mean this arm had kept composing the selector
		// its own gate now refuses.
		assert.Empty(t, f.reqs[0].GetTarget().GetLanguage(), "no language is composed")
		assert.Empty(t, f.reqs[0].GetTarget().GetName(), "and no name either")
		assert.Equal(t, "practice", f.reqs[0].GetTarget().GetGraph())
		body := textBodyTools(res)
		assert.Contains(t, body, "## practice — 1 nodes")
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
			Graph: "practice", Limit: 2, Offset: 5,
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
		practiceBrowse(opCtx(), f.exec, queryArgs{Graph: "practice"})
		assert.EqualValues(t, engine.BrowseDefaultLimit, f.lastPlan(t).GetLimit(),
			"an absent limit takes the ENGINE's browse cap, not a local literal")
	})

	t.Run("language_is_refused_before_the_browse_reads_the_payload", func(t *testing.T) {
		// `language` is refused ahead of the browse arm, for every value, and the
		// assertion is on the message rather than on IsError alone.
		//
		// THE SECOND ASSERTION IS THE DISCRIMINATING ONE. A value like "all" is not
		// a graph name, so a change that simply deleted the refusal would browse a
		// practice graph literally named "all" and render an empty result under a
		// "practice:all" header — a confident zero rather than a refusal. The
		// absence of that header is what separates the two outcomes.
		for _, value := range []string{"all", "go", "default"} {
			t.Run(value, func(t *testing.T) {
				var execHits atomic.Int64
				gc := newInterceptHarness(t, &execHits, cannedNodesResp())
				deps := &interceptDeps{gc: gc, segMgr: newFanOutSegmentSearcher(nil)}

				res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{Graph: "practice", Language: value})
				body := textBodyTools(res)
				assert.True(t, res.IsError, "`language` is refused, not browsed")
				assert.Equal(t, practiceLanguageRefusedOnRead(practiceHubParamFree), body,
					"the refusal is the one shared read spelling, naming the hub selector")
				assert.NotContains(t, body, "practice:"+value)
				assert.Zero(t, execHits.Load(), "the refusal costs no read")
			})
		}
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
// branches. It STAYS mutex-guarded now that the parallel per-graph gap probe it
// was written for is gone: probeCount is read by the assertions while the
// coverage methods are called from the search path, and the package's other
// coverage fake appends to an unsynchronised slice. Keeping the guard costs
// nothing and the race it prevents is real whenever a caller probes off the
// test goroutine.
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

// BM25DegradeCounts reports no drops: this fake exercises the coverage counts,
// not the drop census.
func (f *gapCoverageFake) BM25DegradeCounts(kgtypes.GraphType, string) map[string]int { return nil }

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
		gc, h := newFanOutHarnessWithHandler(t, []string{"default"})
		h.stats = stats
		return &interceptDeps{gc: gc, segMgr: &fakeSegmentSearcher{}, segCoverage: cov}
	}

	t.Run("gap_loud", func(t *testing.T) {
		cov := &gapCoverageFake{covered: 0}
		deps := zeroHitDeps(t, cov, &knowledgev1.GraphStats{NodeCount: 3117, BinaryVectorCount: 2556})
		res := gatedRoutePractice(opCtx(), deps, deps.gc, queryArgs{
			Graph: "practice", Text: "event",
		})
		body := textBodyTools(res)
		assert.True(t, res.IsError, "a missing ranked index is an ERROR, not data: %s", body)
		// The remedy is asserted as an INDEPENDENT literal rather than through
		// practiceRebuildHint: an assertion routed via the same constant it is
		// catching cannot fail when that constant is wrong.
		assert.Contains(t, body, `"operation":"rebuild_segments"`, "the notice names the rebuild invocation")
		// And the graph name, so the interpolation is proven to have RUN rather
		// than the bare template being echoed.
		assert.Contains(t, body, "\"default\"", "the notice names the graph it probed")
		assert.Contains(t, body, "2556", "the notice names the embedded count")
	})

	t.Run("no_match", func(t *testing.T) {
		// covered > 0: the index exists and was searched, so the zero is the truth.
		cov := &gapCoverageFake{covered: 42}
		deps := zeroHitDeps(t, cov, &knowledgev1.GraphStats{NodeCount: 3117, BinaryVectorCount: 2556})
		res := gatedRoutePractice(opCtx(), deps, deps.gc, queryArgs{
			Graph: "practice", Text: "nonsense",
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
			Graph: "practice", Text: "anything",
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
			Graph: "practice", Text: "event",
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
			Graph: "practice", Text: "pool",
		})
		assert.False(t, res.IsError, textBodyTools(res))
		assert.Contains(t, textBodyTools(res), "GoWorkerPool", "the hits render normally")
		assert.Zero(t, cov.probeCount(), "a successful search must not read the coverage operand")

		// KNOWN POSITIVE, same run: the identical fake DOES get probed once the hit
		// set is empty, so the zero above is a real never-called and not a dead fake.
		empty := zeroHitDeps(t, cov, &knowledgev1.GraphStats{NodeCount: 3117, BinaryVectorCount: 2556})
		gatedRoutePractice(opCtx(), empty, empty.gc, queryArgs{
			Graph: "practice", Text: "pool",
		})
		assert.Positive(t, cov.probeCount(), "an empty hit set DOES read the coverage operand")
	})

	t.Run("unembedded", func(t *testing.T) {
		cov := &gapCoverageFake{covered: 0}
		deps := zeroHitDeps(t, cov, &knowledgev1.GraphStats{NodeCount: 3117, BinaryVectorCount: 0})
		res := gatedRoutePractice(opCtx(), deps, deps.gc, queryArgs{
			Graph: "practice", Text: "event",
		})
		body := textBodyTools(res)
		assert.True(t, res.IsError, "an un-embedded graph is an ERROR, not data: %s", body)
		assert.Contains(t, body, "3117", "the notice names the node count")
		assert.Contains(t, body, "none can be built yet")
		// A rebuild cannot succeed with zero vectors, so the message must not
		// suggest one IN ANY FORM.
		assert.NotContains(t, body, "rebuild_segments", "an unactionable remedy must not be offered")
	})

	t.Run("engine_failure_is_named_not_reported_as_a_zero", func(t *testing.T) {
		// The combined graph's engine errors. The response must NAME the failure
		// rather than report a confident no-match — the distinction between "the
		// search ran and found nothing" and "the search did not run" is the whole
		// contract, and it is what the retired fan-out's per-graph failure bucket
		// existed to preserve across N graphs. There is one graph now, so it is one
		// error, and collapsing it into an empty result is the same lie.
		gc, h := newFanOutHarnessWithHandler(t, []string{"default"})
		h.stats = &knowledgev1.GraphStats{NodeCount: 3117, BinaryVectorCount: 2556}
		mgr := newFanOutSegmentSearcher(nil)
		mgr.errsByGr = map[string]error{"default": errors.New("segment pool unreadable")}
		deps := &interceptDeps{gc: gc, segMgr: mgr, segCoverage: &gapCoverageFake{covered: 7}}

		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{
			Graph: "practice", Text: "event",
		})
		body := textBodyTools(res)
		assert.True(t, res.IsError, "a search whose engine failed is an ERROR: %s", body)
		assert.Contains(t, body, "segment pool unreadable", "the response names the engine error")
		assert.NotContains(t, strings.ToLower(body), "no matches",
			"the caller must never be told 'no matches' when the search did not run")
	})
}

// THE CROSS-GRAPH DISCLOSURE TEST RETIRED WITH THE FAN-OUT IT COVERED.
// TestPracticeFanOut_DisclosesUnindexedGraphs asserted that a scatter-gather
// naming N practice graphs said which of them had no ranked index, because a
// header claiming eight graphs while three had zero segments made a partial
// ranking look comprehensive. The corpus is ONE graph, so there is no header
// claiming graphs and no partial set to disclose.
//
// The property itself did not retire: a zero-coverage graph is still LOUD, and
// TestPracticeSearch_LoudWhenSegmentsAbsent below is where that now lives.

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
		return practiceBrowse(opCtx(), f.exec, queryArgs{Graph: "practice"})
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

// serverRowCeilingSentence is the fragment of the server truncation notice
// engine.WithTruncationNotice carries that the disclosure assertions anchor on. It is deliberately the SERVER-ROW-
// CEILING wording rather than the shorter "may be incomplete": the client-side
// limit-clamp notices (recallLimitClampNotice, searchLimitClampNotice) and
// plan_tree's subtree variant all carry that shorter fragment, so an assertion
// anchored on it would pass on a different notice entirely.
const serverRowCeilingSentence = "the server row ceiling engaged, so this result may be incomplete"
