// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// --- recognizeNegationOp: the four negation shapes, and only those ---

func TestRecognizeNegationOp(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		a        mutateArgs
		t        thinkArgs
		wantOK   bool
		wantNode string
		wantQ    string
	}{
		{
			name:     "mutate link contradicts",
			tool:     "mutate",
			a:        mutateArgs{Operation: "link", Relationship: "contradicts", To: "n1", VerifiedQuote: "q"},
			wantOK:   true,
			wantNode: "n1",
			wantQ:    "q",
		},
		{
			name:     "mutate update invalidated",
			tool:     "mutate",
			a:        mutateArgs{Operation: "update", Status: "invalidated", ID: "n2", VerifiedQuote: "q2"},
			wantOK:   true,
			wantNode: "n2",
			wantQ:    "q2",
		},
		{
			name:     "thoughts think branches_from supersession",
			tool:     "thoughts",
			t:        thinkArgs{BranchesFrom: "n3", VerifiedQuote: "q3"},
			wantOK:   true,
			wantNode: "n3",
			wantQ:    "q3",
		},
		{
			name:     "thoughts think branches_from + status invalidated",
			tool:     "thoughts",
			t:        thinkArgs{BranchesFrom: "n4", Status: "invalidated", VerifiedQuote: "q4"},
			wantOK:   true,
			wantNode: "n4",
			wantQ:    "q4",
		},
		// --- non-negation shapes: must NOT be recognized ---
		{
			name:   "mutate link relates-to (not contradicts)",
			tool:   "mutate",
			a:      mutateArgs{Operation: "link", Relationship: "relates-to", To: "n1"},
			wantOK: false,
		},
		{
			name:   "mutate update status validated (not invalidated)",
			tool:   "mutate",
			a:      mutateArgs{Operation: "update", Status: "validated", ID: "n2"},
			wantOK: false,
		},
		{
			name:   "mutate create (not a negation)",
			tool:   "mutate",
			a:      mutateArgs{Operation: "create", Type: "finding"},
			wantOK: false,
		},
		{
			name:   "thoughts think with no branches_from (fresh thought)",
			tool:   "thoughts",
			t:      thinkArgs{Status: "invalidated"},
			wantOK: false,
		},
		{
			name:   "thoughts charge (not think)",
			tool:   "thoughts",
			t:      thinkArgs{},
			wantOK: false,
		},
		{
			name:   "unrelated tool",
			tool:   "search",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op, ok := recognizeNegationOp(tc.tool, tc.a, tc.t)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantNode, op.ContradictedID)
				assert.Equal(t, tc.wantQ, op.Quote)
			}
		})
	}
}

// --- validateNegationQuote: deterministic substring match, fail-closed ---

func TestValidateNegationQuote(t *testing.T) {
	const (
		thoughtID = "th-own"
		summary   = "The summary claim"
		content   = "func DoThing() {\n\treturn nil\n}"
	)
	// Own-content source: no code-ref edge → render.FetchNode fallback serves the
	// thought node's Summary+Content.
	ownFake := func() *citedSourceFake {
		return &citedSourceFake{
			thoughtNode: &knowledgev1.Node{
				Id: thoughtID, Type: string(kgtypes.NodeThought),
				Summary: summary, Content: content,
			},
		}
	}

	cases := []struct {
		name    string
		op      negationOp
		wantErr bool
	}{
		{
			name:    "exact substring of content passes",
			op:      negationOp{ContradictedID: thoughtID, Quote: "return nil"},
			wantErr: false,
		},
		{
			name:    "substring of summary passes",
			op:      negationOp{ContradictedID: thoughtID, Quote: "The summary claim"},
			wantErr: false,
		},
		{
			name:    "whitespace-only differences still pass (normalize collapses newlines+indentation)",
			op:      negationOp{ContradictedID: thoughtID, Quote: "func DoThing() {     return nil }"},
			wantErr: false,
		},
		{
			name:    "empty quote rejects (fail-closed)",
			op:      negationOp{ContradictedID: thoughtID, Quote: ""},
			wantErr: true,
		},
		{
			name:    "whitespace-only quote rejects",
			op:      negationOp{ContradictedID: thoughtID, Quote: "   \n\t "},
			wantErr: true,
		},
		{
			name:    "hallucinated quote (not in source) rejects",
			op:      negationOp{ContradictedID: thoughtID, Quote: "this text was never in the node"},
			wantErr: true,
		},
		{
			name:    "unresolvable node rejects (fail-closed)",
			op:      negationOp{ContradictedID: "does-not-exist", Quote: "anything"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNegationQuote(context.Background(), ownFake(), tc.op)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// --- quoteLocalToRange: coarse cited-path consistency over Origin ---

func TestQuoteLocalToRange(t *testing.T) {
	codeSrc := currentSource{Text: "x", Origin: "code:pkg/file.go:Sym"}
	thoughtSrc := currentSource{Text: "x", Origin: "thought:th-1"}

	// Cited path matches the code node's path → local.
	assert.True(t, quoteLocalToRange(codeSrc, "pkg/file.go:120-140"))
	// Cited path does NOT match → not local.
	assert.False(t, quoteLocalToRange(codeSrc, "other/path.go:1-2"))
	// Empty / malformed range constrains nothing → local.
	assert.True(t, quoteLocalToRange(codeSrc, ""))
	assert.True(t, quoteLocalToRange(codeSrc, "norange"))
	// Own-content (thought) source has no file path → a path-scoped range cannot
	// constrain it → treated as local.
	assert.True(t, quoteLocalToRange(thoughtSrc, "pkg/file.go:1-2"))
}

func TestNormalize(t *testing.T) {
	assert.Equal(t, "a b c", normalize("  a\t b\n\nc  "))
	assert.Equal(t, "func X() { return }", normalize("func X() {\n\treturn\n}"))
	assert.Empty(t, normalize("   \n\t "))
}

// --- InterceptNegationGate dispatch: handled/IsError vs fall-through ---

// negationParams builds CallToolParams for a tool with the given JSON arg map.
func negationParams(t *testing.T, name string, args map[string]any) kgtools.CallToolParams {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return kgtools.CallToolParams{Name: name, Arguments: raw}
}

// runGateMessage drives the gate like runGate and additionally returns the rendered
// rejection text, which is what the compared-against assertions read. toolResultText
// (backend_lookup.go:278) is the in-tree renderer. The tool is fixed to mutate: the
// clause is a property of the rejection, not of which negation shape produced it,
// and runGate already covers every shape.
func runGateMessage(t *testing.T, gc GraphCaller, args map[string]any) (handled, rejected bool, msg string) {
	t.Helper()
	handled, res := InterceptNegationGate(opCtx(), interceptTestDeps{gc: gc}, negationParams(t, "mutate", args))
	return handled, res.IsError, toolResultText(res)
}

// TestNegationGate_RejectionNamesComparedOrigins: when a quote fails against cited
// code that actually carries source, the rejection names the origins it compared
// against instead of implying the negator hallucinated. When the comparison ran
// against the thought's own content because it cites no code at all, the message
// carries no clause of either kind.
//
// THE TWO OWN-CONTENT CASES ARE NO LONGER THE SAME MESSAGE (CEO amendment,
// 2026-08-28). This test used to pin the opposite — that an all-content-less
// citation set produced a rejection byte-identical to the ordinary own-content one
// — and that identity was the defect: the caller whose citations were all excluded
// got no signal that they had been. The third subtest now pins the amendment, and
// the excluded-citations clause itself is covered in
// negation_gate_excluded_citations_test.go.
//
// EVERY SUBTEST OPENS WITH THE SAME THREE PRECONDITIONS: handled, rejected, and a
// NON-EMPTY message. They are load-bearing, not ceremony. Each subtest asserts
// something about a rejection message, and without the non-empty requirement the
// not-contains assertion below would pass on an empty string and the equality
// assertion would pass because two empty strings are equal — both green against a
// gate that produced no message at all.
func TestNegationGate_RejectionNamesComparedOrigins(t *testing.T) {
	// Present in no fixture's own content and in no cited source.
	const hallucinated = "this sentence was never in any source"

	t.Run("failed quote against real cited code names the code origins", func(t *testing.T) {
		handled, rejected, msg := runGateMessage(t, codeRefFake(), map[string]any{
			"operation": "link", "relationship": "contradicts", "to": codeRefThoughtID,
			"verified_quote": hallucinated,
		})
		require.True(t, handled, "precondition: the gate claimed the call")
		require.True(t, rejected, "precondition: the call was rejected")
		require.NotEmpty(t, msg, "precondition: the rejection carries a message")

		assert.Contains(t, msg, "; compared against the current source of: ",
			"the rejection names what the quote was compared against")
		assert.Contains(t, msg, "code:"+codeRefCodeID,
			"and names the cited code origin by its code:<nodeID> form")
	})

	t.Run("failed quote against own content carries no clause", func(t *testing.T) {
		const (
			nodeID  = "th-own-no-clause"
			summary = "claim: the writer is synchronous"
			content = "the reasoning body of a thought that cites no code"
		)
		handled, rejected, msg := runGateMessage(t, ownContentFake(nodeID, summary, content), map[string]any{
			"operation": "update", "id": nodeID, "status": "invalidated",
			"verified_quote": hallucinated,
		})
		require.True(t, handled, "precondition: the gate claimed the call")
		require.True(t, rejected, "precondition: the call was rejected")
		require.NotEmpty(t, msg, "precondition: the rejection carries a message")

		assert.NotContains(t, msg, "compared against",
			"nothing with a code origin was compared, so no clause is appended")
	})

	t.Run("an all-content-less citation set is DISTINGUISHABLE from the ordinary own-content case", func(t *testing.T) {
		const (
			nodeID  = "th-same-message"
			summary = "claim: the flush is batched"
			content = "the reasoning body shared by both fixtures"
		)
		args := map[string]any{
			"operation": "update", "id": nodeID, "status": "invalidated",
			"verified_quote": hallucinated,
		}

		handledOwn, rejectedOwn, msgOwn := runGateMessage(t, ownContentFake(nodeID, summary, content), args)
		require.True(t, handledOwn, "precondition: the own-content call was claimed")
		require.True(t, rejectedOwn, "precondition: the own-content call was rejected")
		require.NotEmpty(t, msgOwn, "precondition: the own-content rejection carries a message")

		handledFile, rejectedFile, msgFile := runGateMessage(t, fileOnlyFake(nodeID, summary, content), args)
		require.True(t, handledFile, "precondition: the file-only call was claimed")
		require.True(t, rejectedFile, "precondition: the file-only call was rejected")
		require.NotEmpty(t, msgFile, "precondition: the file-only rejection carries a message")

		// Both fixtures share a node id and body, so the two messages differ ONLY by
		// the excluded-citations clause — which is what makes the inequality a
		// statement about the clause rather than about the fixtures.
		assert.NotEqual(t, msgOwn, msgFile,
			"a caller whose only citation was excluded must be told so; before the 2026-08-28 "+
				"amendment these two rejections were byte-identical and that was the defect")
		assert.True(t, strings.HasPrefix(msgFile, msgOwn),
			"and the difference is purely an appended clause — the base rejection is unchanged; got %q", msgFile)
	})
}

// TestNegationGate_ComparedOriginsRenderCapEngages: with more content-bearing cited
// code nodes than the render cap, the clause names the first ten in sorted order,
// omits the rest, and reports how many it omitted. This is the known-positive that
// proves the ceiling engages rather than being a constant nobody exercises.
func TestNegationGate_ComparedOriginsRenderCapEngages(t *testing.T) {
	const (
		nodeID       = "th-render-cap"
		hallucinated = "this sentence was never in any source"
	)

	// Eleven content-bearing cited symbols, ids zero-padded so lexicographic order
	// is unambiguous: pkg/f00.go:S through pkg/f10.go:S.
	fake := &citedSourceFake{}
	for i := 0; i <= 10; i++ {
		codeID := fmt.Sprintf("pkg/f%02d.go:S", i)
		proxyID := fmt.Sprintf("proxy:knowledge:cap-%02d", i)
		fake.edges = append(fake.edges, &knowledgev1.Edge{
			FromId: nodeID, ToId: proxyID, Type: string(kgtypes.EdgeRelatesTo), Method: "code-ref",
		})
		fake.proxyNodes = append(fake.proxyNodes, mkSourceCodeProxy(proxyID, codeID))
		fake.codeNodes = append(fake.codeNodes, &knowledgev1.Node{
			Id: codeID, Type: "function", Content: fmt.Sprintf("func S%02d() { return }", i),
		})
	}

	handled, rejected, msg := runGateMessage(t, fake, map[string]any{
		"operation": "link", "relationship": "contradicts", "to": nodeID,
		"verified_quote": hallucinated,
	})
	require.True(t, handled, "precondition: the gate claimed the call")
	require.True(t, rejected, "precondition: the call was rejected")
	require.NotEmpty(t, msg, "precondition: the rejection carries a message")

	assert.Contains(t, msg, "code:pkg/f00.go:S", "the first origin in sorted order is named")
	assert.Contains(t, msg, "code:pkg/f09.go:S", "the tenth origin in sorted order is named")
	assert.NotContains(t, msg, "code:pkg/f10.go:S", "the eleventh is over the cap and omitted")
	assert.True(t, strings.HasSuffix(msg, ", +1 more"),
		"the clause ends by reporting the omitted count; got %q", msg)
}

// TestNegationGate_RejectionStatesTheMechanicalRule pins WHAT THE REJECTION SAYS
// ABOUT WHY THE QUOTE FAILED, on both comparison paths.
//
// The rejection is read by an agent that is about to retry, so its job is to make
// the retry mechanical: state the test that was applied, and name the source it was
// applied to. The message previously closed on "a hallucinated or stale quote will
// not match", which supplies neither — it attributes a cause to the CALLER rather
// than describing the CHECK, and a negator whose quote came from a real but
// superseded revision reads an accusation instead of an instruction.
//
// WHY BOTH PATHS ARE ASSERTED IN ONE TEST. The mechanical sentence lives in the
// BASE message and the origin clause is a suffix appended only on the code path
// (firstPartyEvidenceError), so the two paths render different strings and a test
// covering one would stay green while the other regressed. The code subtest also
// carries the origin-naming assertion, so this test fails if EITHER half is lost.
//
// THE NOT-CONTAINS LEG IS NOT VACUOUS, and that is by construction rather than by
// assertion. Each subtest asserts Contains against the SAME string in the same run,
// so a blind probe — an empty message, a result the renderer did not populate —
// fails the Contains legs before the NotContains leg can pass for the wrong reason.
// The require.NotEmpty precondition closes the same hole a second way.
func TestNegationGate_RejectionStatesTheMechanicalRule(t *testing.T) {
	// The mechanical fact the message must state: the check that ran, over the
	// source it ran against. Asserted as one span rather than as scattered keywords
	// so a reworded message that drops the substring-match statement fails here
	// instead of passing on an incidental word.
	const mechanicalRule = "the check is a whitespace-normalized substring match against that source " +
		"as it reads now, so text absent from the current revision does not match"

	// Present in no fixture's own content and in no cited source.
	const absentQuote = "this sentence was never in any source"

	// assertMechanical is the shared half: the message states the rule and carries
	// no bad-faith attribution. Matched case-insensitively so "hallucinated",
	// "Hallucinated" and "hallucination" are all caught by the one leg.
	assertMechanical := func(t *testing.T, msg string) {
		t.Helper()
		assert.Contains(t, msg, mechanicalRule,
			"the rejection must state the check it applied, so a caller whose quote came from a "+
				"superseded revision can tell a stale quote from a wrong one")
		assert.NotContains(t, strings.ToLower(msg), "hallucinat",
			"the rejection describes the CHECK, never the caller's good faith; got %q", msg)
	}

	t.Run("cited-code rejection names the compared node ids AND states the rule", func(t *testing.T) {
		handled, rejected, msg := runGateMessage(t, codeRefFake(), map[string]any{
			"operation": "link", "relationship": "contradicts", "to": codeRefThoughtID,
			"verified_quote": absentQuote,
		})
		require.True(t, handled, "precondition: the gate claimed the call")
		require.True(t, rejected, "precondition: the call was rejected")
		require.NotEmpty(t, msg, "precondition: the rejection carries a message")

		assert.Contains(t, msg, "code:"+codeRefCodeID,
			"a caller told to quote 'the current source' must be told WHICH source — the "+
				"comparison set is the cited code, not the thought's own content")
		assertMechanical(t, msg)
	})

	t.Run("thought-only rejection states the same rule", func(t *testing.T) {
		const (
			nodeID  = "th-mechanical-own"
			summary = "claim: the writer is synchronous"
			content = "the reasoning body of a thought that cites no code"
		)
		handled, rejected, msg := runGateMessage(t, ownContentFake(nodeID, summary, content), map[string]any{
			"operation": "update", "id": nodeID, "status": "invalidated",
			"verified_quote": absentQuote,
		})
		require.True(t, handled, "precondition: the gate claimed the call")
		require.True(t, rejected, "precondition: the call was rejected")
		require.NotEmpty(t, msg, "precondition: the rejection carries a message")

		// The comparison set IS the thought, and the base message already names it.
		assert.Contains(t, msg, nodeID,
			"the own-content path compares against the thought itself, which the base message names")
		assertMechanical(t, msg)
	})
}

func TestInterceptNegationGate_Dispatch(t *testing.T) {
	const (
		nodeID  = "th-target"
		summary = "the contradicted claim"
		content = "the live reasoning body of the node"
	)
	// deps whose GraphCaller resolves nodeID's own current source.
	mkDeps := func() interceptTestDeps {
		return interceptTestDeps{gc: &citedSourceFake{
			thoughtNode: &knowledgev1.Node{
				Id: nodeID, Type: string(kgtypes.NodeThought),
				Summary: summary, Content: content,
			},
		}}
	}

	t.Run("contradicts-link no quote → handled+IsError", func(t *testing.T) {
		handled, res := InterceptNegationGate(opCtx(), mkDeps(), negationParams(t, "mutate", map[string]any{
			"operation": "link", "relationship": "contradicts", "to": nodeID,
		}))
		assert.True(t, handled, "missing-quote negation is claimed by the gate")
		assert.True(t, res.IsError, "and rejected")
	})

	t.Run("contradicts-link matching quote → falls through", func(t *testing.T) {
		handled, _ := InterceptNegationGate(opCtx(), mkDeps(), negationParams(t, "mutate", map[string]any{
			"operation": "link", "relationship": "contradicts", "to": nodeID,
			"verified_quote": "live reasoning body",
		}))
		assert.False(t, handled, "a validated negation falls through to the real handler")
	})

	t.Run("non-negation mutate(update status:validated) → falls through", func(t *testing.T) {
		handled, _ := InterceptNegationGate(opCtx(), mkDeps(), negationParams(t, "mutate", map[string]any{
			"operation": "update", "id": nodeID, "status": "validated",
		}))
		assert.False(t, handled, "a non-negation update is never claimed by the gate")
	})

	t.Run("unquoted think branches_from supersession → handled+IsError", func(t *testing.T) {
		handled, res := InterceptNegationGate(opCtx(), mkDeps(), negationParams(t, "thoughts", map[string]any{
			"operation": "think", "content": "new", "summary": "new", "branches_from": nodeID,
		}))
		assert.True(t, handled, "unquoted supersession is claimed by the gate")
		assert.True(t, res.IsError, "and rejected")
	})

	t.Run("nil gc → fail-open fall-through", func(t *testing.T) {
		handled, _ := InterceptNegationGate(opCtx(), interceptTestDeps{gc: nil}, negationParams(t, "mutate", map[string]any{
			"operation": "link", "relationship": "contradicts", "to": nodeID,
		}))
		assert.False(t, handled, "no graph access → fail-open to the existing handler")
	})

	t.Run("unrelated tool → falls through", func(t *testing.T) {
		handled, _ := InterceptNegationGate(opCtx(), mkDeps(), negationParams(t, "search", map[string]any{"query": "x"}))
		assert.False(t, handled)
	})
}
