// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// Fixtures + assertions for the excluded-citation clause: the rejection names the
// code citations the comparison set did NOT admit, and the mechanical reason each
// was excluded.
//
// WHY THIS CLAUSE EXISTS. Before it, a negator whose citations were ALL excluded —
// every edge method-less, or every resolved node content-less — received a
// rejection byte-identical to the one a negator citing no code at all receives.
// They were told to quote "the current source of <thought-id>" with no way to
// learn that the code they believed they had cited was never compared. That
// byte-identity was a deliberate, test-pinned property; it is now retired.

// linksParamSymbolProxyID / linksParamSymbolCodeID are the SMOKE REPRODUCTION
// shape: a symbol-level code node cited through the plain links param, so the
// edge carries no code-ref method and the symbol never reaches resolution even
// though it carries real source. The symbol id is what the caller believes they
// cited, so it is what the rejection must name.
const (
	linksParamSymbolProxyID = "proxy:knowledge:links-symbol"
	linksParamSymbolCodeID  = "pkg/writer.go:Sync"
)

// linksParamSymbolFake serves that shape: ONE method-less relates-to edge to a
// code proxy standing for a content-bearing symbol, plus the thought's own
// Summary+Content for the require-own-content path the gate falls back to.
func linksParamSymbolFake(id, summary, content string) *citedSourceFake {
	return &citedSourceFake{
		edges: []*knowledgev1.Edge{
			// Method deliberately unset — this is what the links param mints.
			{FromId: id, ToId: linksParamSymbolProxyID, Type: string(kgtypes.EdgeRelatesTo)},
		},
		proxyNodes: []*knowledgev1.Node{mkSourceCodeProxy(linksParamSymbolProxyID, linksParamSymbolCodeID)},
		codeNodes: []*knowledgev1.Node{
			{Id: linksParamSymbolCodeID, Type: "function", Content: "func Sync() error { return nil }"},
		},
		thoughtNode: &knowledgev1.Node{
			Id: id, Type: string(kgtypes.NodeThought),
			Summary: summary, Content: content,
		},
	}
}

// contentLessSymbolFake serves a thought whose only code-ref born-link resolves to
// a SYMBOL node carrying blank content — an indexing gap rather than the file
// node's designed emptiness. resolveThoughtCurrentSource excludes both
// identically; the rejection must NOT describe this one as a file node.
func contentLessSymbolFake(id, summary, content string) *citedSourceFake {
	const (
		proxyID = "proxy:knowledge:blank-symbol"
		codeID  = "pkg/writer.go:Drain"
	)
	return &citedSourceFake{
		edges: []*knowledgev1.Edge{
			{FromId: id, ToId: proxyID, Type: string(kgtypes.EdgeRelatesTo), Method: "code-ref"},
		},
		proxyNodes: []*knowledgev1.Node{mkSourceCodeProxy(proxyID, codeID)},
		// Content deliberately unset on a SYMBOL-shaped id (path-then-symbol).
		codeNodes: []*knowledgev1.Node{{Id: codeID, Type: "function"}},
		thoughtNode: &knowledgev1.Node{
			Id: id, Type: string(kgtypes.NodeThought),
			Summary: summary, Content: content,
		},
	}
}

// mixedLinksAndSymbolFake cites the SAME content-bearing symbol twice — once by a
// real code-ref born-link and once by a method-less links-param edge. The symbol
// IS in the comparison set, so naming it as excluded would be false; this is the
// fixture that proves the excluded list subtracts what was actually compared.
func mixedLinksAndSymbolFake(id, summary, content string) *citedSourceFake {
	const bornProxyID = "proxy:knowledge:born-symbol"
	return &citedSourceFake{
		edges: []*knowledgev1.Edge{
			{FromId: id, ToId: bornProxyID, Type: string(kgtypes.EdgeRelatesTo), Method: "code-ref"},
			{FromId: id, ToId: linksParamSymbolProxyID, Type: string(kgtypes.EdgeRelatesTo)},
		},
		proxyNodes: []*knowledgev1.Node{
			mkSourceCodeProxy(bornProxyID, linksParamSymbolCodeID),
			mkSourceCodeProxy(linksParamSymbolProxyID, linksParamSymbolCodeID),
		},
		codeNodes: []*knowledgev1.Node{
			{Id: linksParamSymbolCodeID, Type: "function", Content: "func Sync() error { return nil }"},
		},
		thoughtNode: &knowledgev1.Node{
			Id: id, Type: string(kgtypes.NodeThought),
			Summary: summary, Content: content,
		},
	}
}

// nonCodeLinksFake cites nothing: its method-less relates-to edges point at a
// FINDING and a THOUGHT, the ordinary shape of a born-linked thought. Neither is
// a code proxy, so neither is a citation and neither may be named.
func nonCodeLinksFake(id, summary, content string) *citedSourceFake {
	return &citedSourceFake{
		edges: []*knowledgev1.Edge{
			{FromId: id, ToId: "finding-abc", Type: string(kgtypes.EdgeRelatesTo)},
			{FromId: id, ToId: "th-neighbor", Type: string(kgtypes.EdgeRelatesTo)},
		},
		proxyNodes: []*knowledgev1.Node{
			{Id: "finding-abc", Type: string(kgtypes.NodeFinding)},
			{Id: "th-neighbor", Type: string(kgtypes.NodeThought)},
		},
		thoughtNode: &knowledgev1.Node{
			Id: id, Type: string(kgtypes.NodeThought),
			Summary: summary, Content: content,
		},
	}
}

// The expected message vocabulary, written here as LITERALS rather than as
// references to the production constants. The assertions are then an external
// expectation of what a caller reads, not a restatement of whatever the code
// happens to define — and the same test bytes run red against the unamended gate
// and green against the amended one.
const (
	wantExcludedLead        = "; code citations excluded from that comparison: "
	wantReasonMethodless    = "cited via links (method-less relates-to edge; cite born-linked code-refs to make a citation verifiable)"
	wantReasonFileNoContent = "file node carries no content (cite a symbol-level node)"
	wantReasonNoContent     = "cited node carries no content (the graph holds no source for it)"
)

// absentQuote appears in no fixture's own content and in no cited source, so every
// call below is a genuine rejection rather than a pass.
const excludedFixtureQuote = "this sentence was never in any source"

// TestNegationGate_RejectionNamesExcludedCitations pins the CEO-directed amendment
// (2026-08-28): a rejection whose negated node carries code citations the
// comparison set excluded names each one and states why it was excluded.
//
// THE FOUR SUBTESTS ARE A CONTROL SET, not four samples of one behaviour. The two
// positive legs cover the two exclusion mechanisms, which live in different
// packages and cannot regress together. The own-content leg is the NEGATIVE
// CONTROL that proves the clause is conditional on an exclusion having happened —
// without it, a change that appended the clause unconditionally would pass every
// positive leg. The compared-against leg re-pins the behaviour established in
// commit 46e0ffed, which the amendment must not cost.
func TestNegationGate_RejectionNamesExcludedCitations(t *testing.T) {
	t.Run("links-param citation is named with the method-less reason", func(t *testing.T) {
		const (
			nodeID  = "th-links-excluded"
			summary = "claim: the writer syncs on every append"
			content = "the reasoning body, which quotes no source line"
		)
		handled, rejected, msg := runGateMessage(t, linksParamSymbolFake(nodeID, summary, content), map[string]any{
			"operation": "update", "id": nodeID, "status": "invalidated",
			"verified_quote": excludedFixtureQuote,
		})
		require.True(t, handled, "precondition: the gate claimed the call")
		require.True(t, rejected, "precondition: the call was rejected")
		require.NotEmpty(t, msg, "precondition: the rejection carries a message")

		assert.Contains(t, msg, wantExcludedLead,
			"the rejection opens an excluded-citations clause")
		assert.Contains(t, msg, "code:"+linksParamSymbolCodeID,
			"and names the citation the caller believes they supplied")
		assert.Contains(t, msg, wantReasonMethodless,
			"and states the mechanical reason it was excluded, plus the path to a verifiable citation")
	})

	t.Run("content-less file citation is named with the no-content reason", func(t *testing.T) {
		const (
			nodeID  = "th-file-excluded"
			summary = "claim: the cache is write-through"
			content = "the reasoning body, which quotes no source line"
		)
		handled, rejected, msg := runGateMessage(t, fileOnlyFake(nodeID, summary, content), map[string]any{
			"operation": "update", "id": nodeID, "status": "invalidated",
			"verified_quote": excludedFixtureQuote,
		})
		require.True(t, handled, "precondition: the gate claimed the call")
		require.True(t, rejected, "precondition: the call was rejected")
		require.NotEmpty(t, msg, "precondition: the rejection carries a message")

		assert.Contains(t, msg, wantExcludedLead)
		assert.Contains(t, msg, "code:"+fileOnlyCodeID,
			"the content-less file node is named by its code:<nodeID> form")
		assert.Contains(t, msg, wantReasonFileNoContent,
			"and the reason names the file shape and points at the symbol-level node")
	})

	t.Run("own-content-only negation carries NO excluded clause", func(t *testing.T) {
		const (
			nodeID  = "th-own-no-citations"
			summary = "claim: the writer is synchronous"
			content = "the reasoning body of a thought that cites no code"
		)
		handled, rejected, msg := runGateMessage(t, ownContentFake(nodeID, summary, content), map[string]any{
			"operation": "update", "id": nodeID, "status": "invalidated",
			"verified_quote": excludedFixtureQuote,
		})
		require.True(t, handled, "precondition: the gate claimed the call")
		require.True(t, rejected, "precondition: the call was rejected")

		// Asserted as full equality, not as two NotContains legs: this states that
		// the message is EXACTLY the base rejection with no suffix of any kind, so a
		// third clause added later cannot slip past a pair of negative assertions.
		assert.Equal(t, fmt.Sprintf(errFirstPartyEvidenceMsg, nodeID), msg,
			"a negation citing no code at all keeps 46e0ffed's message verbatim — the clause "+
				"appears only when citations were actually excluded")
	})

	t.Run("resolvable citations still enumerate as compared-against, with no excluded clause", func(t *testing.T) {
		handled, rejected, msg := runGateMessage(t, codeRefFake(), map[string]any{
			"operation": "link", "relationship": "contradicts", "to": codeRefThoughtID,
			"verified_quote": excludedFixtureQuote,
		})
		require.True(t, handled, "precondition: the gate claimed the call")
		require.True(t, rejected, "precondition: the call was rejected")
		require.NotEmpty(t, msg, "precondition: the rejection carries a message")

		assert.Contains(t, msg, "; compared against the current source of: code:"+codeRefCodeID,
			"the 46e0ffed compared-against enumeration survives the amendment")
		assert.NotContains(t, msg, wantExcludedLead,
			"nothing was excluded, so no excluded clause is appended")
	})
}

// TestNegationGate_ExcludedCitationEdgeCases covers the three ways the clause could
// be WRONG rather than merely absent: describing a content-less symbol as a file,
// naming a citation that WAS compared, and naming non-code neighbors as citations.
func TestNegationGate_ExcludedCitationEdgeCases(t *testing.T) {
	t.Run("a content-less SYMBOL is not described as a file node", func(t *testing.T) {
		const (
			nodeID  = "th-blank-symbol"
			summary = "claim: the drain is bounded"
			content = "the reasoning body, which quotes no source line"
		)
		handled, rejected, msg := runGateMessage(t, contentLessSymbolFake(nodeID, summary, content), map[string]any{
			"operation": "update", "id": nodeID, "status": "invalidated",
			"verified_quote": excludedFixtureQuote,
		})
		require.True(t, handled, "precondition: the gate claimed the call")
		require.True(t, rejected, "precondition: the call was rejected")
		require.NotEmpty(t, msg, "precondition: the rejection carries a message")

		assert.Contains(t, msg, "code:pkg/writer.go:Drain", "the blank symbol is still named")
		assert.Contains(t, msg, wantReasonNoContent,
			"a symbol whose content the graph does not hold gets the shape-neutral reason")
		assert.NotContains(t, msg, wantReasonFileNoContent,
			"calling a symbol node a file node would be a confidently wrong explanation")
	})

	t.Run("a citation reached through BOTH edge shapes is compared, not reported excluded", func(t *testing.T) {
		const (
			nodeID  = "th-both-shapes"
			summary = "claim: the writer syncs on every append"
			content = "the reasoning body, which quotes no source line"
		)
		handled, rejected, msg := runGateMessage(t, mixedLinksAndSymbolFake(nodeID, summary, content), map[string]any{
			"operation": "link", "relationship": "contradicts", "to": nodeID,
			"verified_quote": excludedFixtureQuote,
		})
		require.True(t, handled, "precondition: the gate claimed the call")
		require.True(t, rejected, "precondition: the call was rejected")
		require.NotEmpty(t, msg, "precondition: the rejection carries a message")

		assert.Contains(t, msg, "; compared against the current source of: code:"+linksParamSymbolCodeID,
			"the born-linked edge put the symbol in the comparison set")
		assert.NotContains(t, msg, wantExcludedLead,
			"the symbol WAS compared, so the method-less duplicate edge must not report it excluded")
	})

	t.Run("a mixed citation set renders BOTH clauses", func(t *testing.T) {
		const (
			nodeID  = "th-mixed-clauses"
			summary = "claim: flushing happens on a timer"
			content = "the reasoning body, which quotes no source line"
		)
		// mixedFake cites a content-BEARING symbol and a content-LESS file node, so
		// one citation is compared and the other excluded. This is the state a caller
		// is least able to infer unaided — partial comparison — and it is the reason
		// the two clauses are independent rather than mutually exclusive.
		handled, rejected, msg := runGateMessage(t, mixedFake(nodeID, summary, content), map[string]any{
			"operation": "link", "relationship": "contradicts", "to": nodeID,
			"verified_quote": excludedFixtureQuote,
		})
		require.True(t, handled, "precondition: the gate claimed the call")
		require.True(t, rejected, "precondition: the call was rejected")
		require.NotEmpty(t, msg, "precondition: the rejection carries a message")

		assert.Contains(t, msg, "; compared against the current source of: code:"+codeRefCodeID,
			"the content-bearing symbol was compared and is named as such")
		assert.Contains(t, msg, wantExcludedLead+"code:"+fileOnlyCodeID+" — "+wantReasonFileNoContent,
			"and the content-less file node is named as excluded, in the same message")
		assert.Less(t, strings.Index(msg, "; compared against"), strings.Index(msg, wantExcludedLead),
			"read order: what WAS compared precedes what was not")
	})

	t.Run("method-less edges to non-code neighbors are not citations", func(t *testing.T) {
		const (
			nodeID  = "th-non-code-links"
			summary = "claim: born-linking is not citing"
			content = "the reasoning body of a thought linked to a finding and a sibling"
		)
		handled, rejected, msg := runGateMessage(t, nonCodeLinksFake(nodeID, summary, content), map[string]any{
			"operation": "update", "id": nodeID, "status": "invalidated",
			"verified_quote": excludedFixtureQuote,
		})
		require.True(t, handled, "precondition: the gate claimed the call")
		require.True(t, rejected, "precondition: the call was rejected")

		assert.Equal(t, fmt.Sprintf(errFirstPartyEvidenceMsg, nodeID), msg,
			"a relates-to edge to a finding or a sibling thought is not a code citation, so it "+
				"contributes nothing to the clause and the base message stands unchanged")
	})
}

// TestRenderCappedList proves the shared render ceiling engages and reports what it
// omitted. It is asserted directly on the helper because BOTH clauses render
// through it, and a message-level test can only reach one of them at a time.
func TestRenderCappedList(t *testing.T) {
	under := []string{"a", "b", "c"}
	assert.Equal(t, "a, b, c", renderCappedList(under), "a list under the cap renders whole")

	over := make([]string, negationOriginRenderCap+3)
	for i := range over {
		over[i] = fmt.Sprintf("item%02d", i)
	}
	got := renderCappedList(over)
	assert.Contains(t, got, "item00", "the first item is named")
	assert.Contains(t, got, fmt.Sprintf("item%02d", negationOriginRenderCap-1), "the last item under the cap is named")
	assert.NotContains(t, got, fmt.Sprintf("item%02d", negationOriginRenderCap), "the first item over the cap is omitted")
	assert.True(t, strings.HasSuffix(got, ", +3 more"),
		"and the omission is reported rather than silent; got %q", got)
}

// TestNegationGate_ExcludedCitationsRenderCapEngages is the message-level
// known-positive for the excluded clause's ceiling: with more excluded citations
// than the cap, the clause names the first ten in sorted order and reports the
// rest as omitted.
func TestNegationGate_ExcludedCitationsRenderCapEngages(t *testing.T) {
	const (
		nodeID  = "th-excluded-cap"
		summary = "claim: every citation here is method-less"
		content = "the reasoning body, which quotes no source line"
	)

	fake := &citedSourceFake{
		thoughtNode: &knowledgev1.Node{
			Id: nodeID, Type: string(kgtypes.NodeThought),
			Summary: summary, Content: content,
		},
	}
	for i := 0; i <= negationOriginRenderCap; i++ {
		codeID := fmt.Sprintf("pkg/g%02d.go:S", i)
		proxyID := fmt.Sprintf("proxy:knowledge:excl-%02d", i)
		fake.edges = append(fake.edges, &knowledgev1.Edge{
			FromId: nodeID, ToId: proxyID, Type: string(kgtypes.EdgeRelatesTo), // method-less
		})
		fake.proxyNodes = append(fake.proxyNodes, mkSourceCodeProxy(proxyID, codeID))
	}

	handled, rejected, msg := runGateMessage(t, fake, map[string]any{
		"operation": "update", "id": nodeID, "status": "invalidated",
		"verified_quote": excludedFixtureQuote,
	})
	require.True(t, handled, "precondition: the gate claimed the call")
	require.True(t, rejected, "precondition: the call was rejected")
	require.NotEmpty(t, msg, "precondition: the rejection carries a message")

	assert.Contains(t, msg, "code:pkg/g00.go:S", "the first excluded citation in sorted order is named")
	assert.Contains(t, msg, fmt.Sprintf("code:pkg/g%02d.go:S", negationOriginRenderCap-1),
		"the last one under the cap is named")
	assert.NotContains(t, msg, fmt.Sprintf("code:pkg/g%02d.go:S", negationOriginRenderCap),
		"the first one over the cap is omitted")
	assert.True(t, strings.HasSuffix(msg, ", +1 more"),
		"and the clause closes by reporting the omitted count; got %q", msg)
}
