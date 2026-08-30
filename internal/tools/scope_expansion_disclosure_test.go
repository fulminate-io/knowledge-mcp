// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// scope_expansion_disclosure_test.go holds the behavioral gates for the arms
// wired to the shared disclosure helper beyond the two the plan proved by hand:
// the analyze call-graph walks and the explain endpoint hydrate, plus the two
// resourceBrowse return arms the browse test does not reach.
//
// WHY THEY EXIST, stated plainly because their absence was MEASURED rather than
// suspected. A review mutated each of these sites — pinning a threaded verdict to
// false, or deleting a WithTruncationNotice wrapper outright — and every one left
// both packages fully green. The census's handles_actually_disclose sub-test
// closes half of that: it fails when a row's body calls no disclosure helper at
// all. It cannot close the other half, and by construction never will — it works
// at FUNCTION granularity, so it cannot see a verdict pinned to a constant before
// the call, nor a wrapper removed from ONE of several return arms while its
// siblings still call it. Those need a behavioral assertion per arm, which is
// what this file is.
//
// EVERY TEST HERE IS TWO-POLARITY. The untruncated leg is what refuses a notice
// appended unconditionally, which a truncated-only assertion would accept.

// analyzeExecFake answers the reads composeAnalyzeNode issues: the subject ByID
// and the four RETURN_MODE_TRAVERSAL call-graph walks, whose Truncated flag is
// the fixture knob. The walks carry no Limit, so the server row ceiling engages
// at 10,000 traversal rows and this is a real verdict rather than a contrivance.
type analyzeExecFake struct{ walkTruncated bool }

func (f *analyzeExecFake) exec(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	switch {
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL:
		return &knowledgev1.ExecuteResponse{
			TraversalResults: []*knowledgev1.TraversalResult{
				{Node: &knowledgev1.Node{Id: "p/c.go:Callee", SymbolName: "Callee", Type: "function"}, Distance: 1},
			},
			Truncated: f.walkTruncated,
		}, nil
	case q.GetById() != "":
		return &knowledgev1.ExecuteResponse{
			Nodes: []*knowledgev1.Node{{Id: "p/a.go:Subject", SymbolName: "Subject", Type: "function"}},
		}, nil
	default:
		return &knowledgev1.ExecuteResponse{}, nil
	}
}

// TestAnalyzeNode_TruncationNotice pins that a clamped call-graph walk says so.
// composeAnalyzeNode bypasses engine.Render, and AnalyzeView.Incomplete answers a
// NARROWER question — whether GROUP reconstruction was partial — so it does not
// cover a walk the row ceiling cut short.
func TestAnalyzeNode_TruncationNotice(t *testing.T) {
	analyze := func(truncated bool) string {
		f := &analyzeExecFake{walkTruncated: truncated}
		res := composeAnalyzeNode(opCtx(), f.exec, analyzeNodeArgs{ID: "p/a.go:Subject", Repo: "knowledge"})
		require.False(t, res.IsError, textBodyTools(res))
		return textBodyTools(res)
	}

	assert.Contains(t, analyze(true), serverRowCeilingSentence,
		"a call-graph walk the ceiling clamped renders a complete-looking graph with callers missing; "+
			"the arm bypasses Render, so it must disclose for itself")
	assert.NotContains(t, analyze(false), serverRowCeilingSentence,
		"a whole walk must not claim to be partial")
}

// TestAnalyzeNode_ClampedCandidateHydrate pins the OTHER half of the
// candidate-hydrate contract: EnrichCandidateGroups reports a clamped hydrate
// through its error return, and analyzeGroupSide must turn that into a FLAGGED
// INCOMPLETENESS rather than an error result — while keeping the partial
// enrichment and the candidates that did resolve.
//
// Both halves are load-bearing and neither implies the other. Erroring the whole
// analyze would turn a mostly-good call graph into nothing; discarding the
// partials would throw away work EnrichCandidateGroups deliberately hands back
// alongside its error, which is precisely what the success-only branch used to do.
func TestAnalyzeNode_ClampedCandidateHydrate(t *testing.T) {
	const subject = "p/a.go:Subject"
	const src = "p/a.go:Caller"
	const key = "p/a.go:1042:CALLS:Run"

	// ambiguousEdge is a member of a 2-of-3 group, so the group is provably
	// incomplete (Confidence is 1/N) and enrichment actually runs.
	ambiguousEdge := func(to string) *knowledgev1.Edge {
		return &knowledgev1.Edge{
			FromId: src, ToId: to, Type: string(kgtypes.EdgeCalls),
			Method: kgtypes.EdgeMethodAmbiguousName, Evidence: key, Confidence: 1.0 / 3.0,
		}
	}

	exec := func(hydrateTruncated bool) func(context.Context, *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		return func(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
			q := req.GetQuery()
			switch {
			case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL:
				return &knowledgev1.ExecuteResponse{
					TraversalResults: []*knowledgev1.TraversalResult{
						{Node: &knowledgev1.Node{Id: src, SymbolName: "Caller", Type: "function"}, Distance: 1},
					},
					TraversalEdges: []*knowledgev1.Edge{ambiguousEdge("p/b.go:Run"), ambiguousEdge("p/c.go:Run")},
				}, nil
			case q.GetById() != "":
				return &knowledgev1.ExecuteResponse{
					Nodes: []*knowledgev1.Node{{Id: subject, SymbolName: "Subject", Type: "function"}},
				}, nil
			case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
				return &knowledgev1.ExecuteResponse{Edges: []*knowledgev1.Edge{ambiguousEdge("p/d.go:Run")}}, nil
			default: // the candidate hydrate
				return &knowledgev1.ExecuteResponse{
					Nodes:     []*knowledgev1.Node{{Id: "p/b.go:Run", SymbolName: "ResolvedCandidate", Type: "function"}},
					Truncated: hydrateTruncated,
				}, nil
			}
		}
	}

	// THE OBSERVABLE OF A SURVIVING HYDRATE is the ABSENCE of the "(unhydrated)"
	// marker beside a candidate: RenderAnalyzeNode prints the candidate id either
	// way and tags the ones it could not resolve. p/b.go:Run is the one the fixture
	// server returns; p/c.go:Run and p/d.go:Run are the ones it withholds, so they
	// are the same run's known-positive for the marker itself.
	const resolved = "`p/b.go:Run`"
	const withheld = "`p/c.go:Run` (unhydrated)"

	t.Run("a clamped hydrate renders on, flagged, and keeps what resolved", func(t *testing.T) {
		res := composeAnalyzeNode(opCtx(), exec(true), analyzeNodeArgs{ID: subject, Repo: "knowledge"})
		require.False(t, res.IsError,
			"a clamped enrichment must NOT become an error result — the analyze itself succeeded")
		body := textBodyTools(res)

		assert.Contains(t, body, "group reconstruction incomplete",
			"the clamp reaches the reader as a FLAGGED incompleteness, which is the channel "+
				"EnrichCandidateGroups' error return is documented to feed")
		assert.NotContains(t, body, resolved+" (unhydrated)",
			"THE PARTIALS SURVIVE: the candidate the server DID return keeps its resolution. Taking "+
				"the hydrate only on the success path discarded it on exactly the path that produces it")
		assert.Contains(t, body, withheld,
			"the known-positive for the marker: the candidates the clamp withheld ARE tagged, so the "+
				"assertion above is discriminating rather than vacuous")
	})

	t.Run("a whole hydrate is the known-negative", func(t *testing.T) {
		// Without this leg, "no (unhydrated) marker on p/b.go:Run" would be
		// satisfied by an implementation that never tags anything.
		res := composeAnalyzeNode(opCtx(), exec(false), analyzeNodeArgs{ID: subject, Repo: "knowledge"})
		require.False(t, res.IsError)
		body := textBodyTools(res)
		assert.NotContains(t, body, "group reconstruction incomplete",
			"a whole hydrate must not flag the reconstruction incomplete")
		assert.NotContains(t, body, resolved+" (unhydrated)")
	})
}

// TestRenderExplainWithNames_TruncationNotice pins the endpoint hydrate's
// verdict. The hydrate is a bulk ids[] read over both endpoints of every incident
// edge — a set the 50,000-row edge drain can push past the 10,000-id bound, which
// the server flags on the request alone. A clamped hydrate renders every
// unresolved endpoint under its fallback name.
func TestRenderExplainWithNames_TruncationNotice(t *testing.T) {
	edges := []knowledgev1.Edge{{FromId: "a", ToId: "b", Type: "relates-to"}}

	explain := func(truncated bool) string {
		exec := func(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
			return &knowledgev1.ExecuteResponse{
				Nodes:     []*knowledgev1.Node{{Id: "a", SymbolName: "Alpha"}, {Id: "b", SymbolName: "Bravo"}},
				Truncated: truncated,
			}, nil
		}
		res := renderExplainWithNames(opCtx(), exec, nil, "knowledge", edges)
		require.False(t, res.IsError, textBodyTools(res))
		return textBodyTools(res)
	}

	assert.Contains(t, explain(true), serverRowCeilingSentence,
		"a clamped endpoint hydrate leaves endpoints unnamed, indistinguishable from endpoints that "+
			"genuinely have no symbol name")
	assert.NotContains(t, explain(false), serverRowCeilingSentence,
		"a whole hydrate must not claim to be partial")
}

// TestResourceBrowse_TruncationNoticePerArm covers the two resourceBrowse return
// arms TestResourceBrowse_TruncationNotice does not reach: the format:"json"
// branch and the empty-account branch. Both were wired deliberately so the
// disclosure is unconditional on the arm's EXITS rather than conditional on which
// branch ran — and a wrapper removed from either one left every package green,
// because its siblings still called the helper and the census works at function
// granularity.
func TestResourceBrowse_TruncationNoticePerArm(t *testing.T) {
	t.Run("json arm", func(t *testing.T) {
		browse := func(truncated bool) string {
			rec := &recordingStatsRPC{
				nodes:     []*knowledgev1.Node{{Id: "r1", SymbolName: "res"}},
				total:     7,
				truncated: truncated,
			}
			res := resourceBrowse(context.Background(), rec.Execute, cloudGraphKind,
				queryArgs{Graph: "cloud", Account: "acme", Format: "json"})
			require.False(t, res.IsError, textBodyTools(res))
			return textBodyTools(res)
		}
		assert.Contains(t, browse(true), serverRowCeilingSentence,
			"the json arm returns before the markdown ones, so it needs its own wrapper")
		assert.NotContains(t, browse(false), serverRowCeilingSentence)
	})

	t.Run("empty-account arm", func(t *testing.T) {
		browse := func(truncated bool) string {
			rec := &recordingStatsRPC{nodes: nil, total: 0, truncated: truncated}
			res := resourceBrowse(context.Background(), rec.Execute, cicdGraphKind,
				queryArgs{Graph: "cicd", Account: "acme"})
			require.False(t, res.IsError, textBodyTools(res))
			return textBodyTools(res)
		}
		// A no-op on today's server — ceilingEngaged needs rowCount >= effective and
		// zero rows never reach it — but the wrap is what keeps the disclosure
		// unconditional on the arm's exits, so it gets a gate that fails when it is
		// removed rather than resting on the claim that it cannot matter.
		assert.Contains(t, browse(true), serverRowCeilingSentence,
			"the empty-case early return is an EXIT of this arm and must disclose like the others")
		assert.NotContains(t, browse(false), serverRowCeilingSentence)
	})
}
