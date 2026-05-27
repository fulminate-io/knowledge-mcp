// SPDX-License-Identifier: Apache-2.0

package content

import (
	"context"
	"fmt"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildKeywordDensityFixture seeds a fakeCaller corpus mirroring the original
// store fixture:
//   - 5 page nodes, 2 of which have "Pattern" in the title SymbolName.
//   - 10 section nodes (headings), 4 of which contain "Problem" or "Solution".
//   - 5 paragraph nodes, 3 of which carry "adapter" in Content.
//   - 2 section nodes carrying class="pattern-card" metadata.
func buildKeywordDensityFixture() *fakeCaller {
	var nodes []*knowledgev1.Node

	// 5 pages: 2 have "Pattern" in title, 3 don't.
	titles := []string{
		"Observer Pattern",
		"Adapter Pattern",
		"Introduction",
		"Conclusion",
		"About the catalog",
	}
	for i, title := range titles {
		nodes = append(nodes, mkNode(fmt.Sprintf("page-%d", i), "page", fmt.Sprintf("page-%d-%s", i, title)))
	}

	// 10 section nodes (heading-like): 4 carry Problem/Solution in SymbolName.
	headings := []string{
		"Problem",
		"Solution",
		"Problem statement",
		"Solution overview",
		"Context",
		"Forces",
		"Examples",
		"Related",
		"Notes",
		"See also",
	}
	for i, h := range headings {
		nodes = append(nodes, mkContent(fmt.Sprintf("sec-%d", i), "section", h, fmt.Sprintf("section-%d body text", i)))
	}

	// 5 paragraph nodes with "adapter" in Content (3) vs plain prose (2).
	for i, c := range []string{"uses an adapter to", "needs the adapter pattern", "composes adapter+bridge", "plain prose one", "plain prose two"} {
		nodes = append(nodes, mkContent(fmt.Sprintf("p-%d", i), "paragraph", fmt.Sprintf("p-%d", i), c))
	}

	// 2 extra section nodes with class metadata for the attribute target.
	for i := range 2 {
		nodes = append(nodes, mkMeta(fmt.Sprintf("class-carrier-%d", i), "section", fmt.Sprintf("class-carrier-%d", i), "class", "pattern-card"))
	}

	return &fakeCaller{nodes: nodes}
}

// TestKeywordDensity_Heading verifies the heading target counts section /
// heading nodes whose SymbolName matches the regex.
func TestKeywordDensity_Heading(t *testing.T) {
	f := buildKeywordDensityFixture()

	a := KeywordDensityAnalyzer{}
	findings, err := a.Run(context.Background(), req(f, map[string]string{
		"keyword_regex": "Problem|Solution",
		"target":        "heading",
	}))
	require.NoError(t, err)
	require.Len(t, findings, 1, "keyword-density emits exactly one aggregate finding")

	fnd := findings[0]
	assert.Equal(t, "keyword-density", fnd.Algorithm)
	// 10 + 2 section nodes = 12 scanned; 4 of the 10 headings match
	// Problem|Solution. The 2 class-carrier sections also scan (by section
	// type) — but their SymbolName "class-carrier-N" doesn't match. So
	// matched=4, total=12.
	assert.InDelta(t, 4.0, fnd.Metrics["matched_count"], 1e-9)
	assert.InDelta(t, 12.0, fnd.Metrics["total_scanned"], 1e-9)
	assert.InDelta(t, 4.0/12.0, fnd.Metrics["density"], 1e-9)
}

// TestKeywordDensity_Attribute verifies the attribute target inspects
// class/id/role metadata keys.
func TestKeywordDensity_Attribute(t *testing.T) {
	f := buildKeywordDensityFixture()

	a := KeywordDensityAnalyzer{}
	findings, err := a.Run(context.Background(), req(f, map[string]string{
		"keyword_regex": "pattern",
		"target":        "attribute",
	}))
	require.NoError(t, err)
	require.Len(t, findings, 1)

	fnd := findings[0]
	// Only the two "class-carrier" nodes carry class="pattern-card" → match.
	// total_scanned counts only nodes that have at least one class/id/role.
	assert.InDelta(t, 2.0, fnd.Metrics["matched_count"], 1e-9)
	assert.InDelta(t, 2.0, fnd.Metrics["total_scanned"], 1e-9)
	assert.InDelta(t, 1.0, fnd.Metrics["density"], 1e-9)
}

// TestKeywordDensity_Title verifies the title target inspects page-node
// SymbolName / Metadata["title"].
func TestKeywordDensity_Title(t *testing.T) {
	f := buildKeywordDensityFixture()

	a := KeywordDensityAnalyzer{}
	findings, err := a.Run(context.Background(), req(f, map[string]string{
		"keyword_regex": "Pattern$",
		"target":        "title",
	}))
	require.NoError(t, err)
	require.Len(t, findings, 1)

	fnd := findings[0]
	// Only page titles scan. 2 of 5 end in "Pattern".
	assert.InDelta(t, 2.0, fnd.Metrics["matched_count"], 1e-9)
	assert.InDelta(t, 5.0, fnd.Metrics["total_scanned"], 1e-9)
}

// TestKeywordDensity_Content verifies the content target inspects every node
// with non-empty Content.
func TestKeywordDensity_Content(t *testing.T) {
	f := buildKeywordDensityFixture()

	a := KeywordDensityAnalyzer{}
	findings, err := a.Run(context.Background(), req(f, map[string]string{
		"keyword_regex": "adapter",
		"target":        "content",
	}))
	require.NoError(t, err)
	require.Len(t, findings, 1)

	fnd := findings[0]
	// 5 paragraph nodes + 10 section nodes have Content. 3 paragraphs carry
	// "adapter". Matched = 3, total = 15.
	assert.InDelta(t, 3.0, fnd.Metrics["matched_count"], 1e-9)
	assert.InDelta(t, 15.0, fnd.Metrics["total_scanned"], 1e-9)
}

// TestKeywordDensity_InvalidTarget verifies the unknown-target error path.
func TestKeywordDensity_InvalidTarget(t *testing.T) {
	f := buildKeywordDensityFixture()

	a := KeywordDensityAnalyzer{}
	_, err := a.Run(context.Background(), req(f, map[string]string{
		"keyword_regex": "x",
		"target":        "unknown-mode",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown target")
}

// TestKeywordDensity_EmptyKeywords verifies the missing-regex error path.
func TestKeywordDensity_EmptyKeywords(t *testing.T) {
	f := buildKeywordDensityFixture()

	a := KeywordDensityAnalyzer{}
	_, err := a.Run(context.Background(), req(f, map[string]string{
		"target": "heading",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keyword_regex")
}

// TestKeywordDensity_InvalidRegex verifies regex-syntax errors surface.
func TestKeywordDensity_InvalidRegex(t *testing.T) {
	f := buildKeywordDensityFixture()

	a := KeywordDensityAnalyzer{}
	_, err := a.Run(context.Background(), req(f, map[string]string{
		"keyword_regex": "(",
		"target":        "heading",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid regex")
}

// TestKeywordDensity_Registered verifies init() self-registration.
func TestKeywordDensity_Registered(t *testing.T) {
	a, ok := foundation.Get("keyword-density")
	require.True(t, ok, "keyword-density analyzer must be registered at package init")
	assert.Equal(t, "keyword-density", a.Name())
}
