// SPDX-License-Identifier: Apache-2.0

package content

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildTitleShapeFixture seeds a fakeCaller corpus with 10 page nodes, 7 of
// which have titles ending in "Pattern" — match_fraction 0.7. The SymbolName
// underscores the title exactly as the original store fixture did; the
// ".*Pattern$" regex still matches the trailing "Pattern".
func buildTitleShapeFixture() *fakeCaller {
	titles := []string{
		"Observer Pattern",
		"Adapter Pattern",
		"Strategy Pattern",
		"Command Pattern",
		"Decorator Pattern",
		"Visitor Pattern",
		"Composite Pattern",
		"About",
		"Glossary",
		"Introduction",
	}
	var nodes []*knowledgev1.Node
	for i, title := range titles {
		nodes = append(nodes, mkNode(
			fmt.Sprintf("page-%d", i),
			"page",
			fmt.Sprintf("page-%d-%s", i, strings.ReplaceAll(title, " ", "_")),
		))
	}
	return &fakeCaller{nodes: nodes}
}

// TestTitleShape verifies the canonical happy path: regex ".*Pattern$" matches
// 7/10 page-title SymbolName strings → match_fraction 0.7.
func TestTitleShape(t *testing.T) {
	f := buildTitleShapeFixture()

	a := TitleShapeAnalyzer{}
	findings, err := a.Run(context.Background(), req(f, map[string]string{"pattern": ".*Pattern$"}))
	require.NoError(t, err)
	require.Len(t, findings, 1, "title-shape emits a single aggregate finding")

	fnd := findings[0]
	assert.Equal(t, "title-shape-distribution", fnd.Algorithm)
	assert.InDelta(t, 7.0, fnd.Metrics["matched_count"], 1e-9)
	assert.InDelta(t, 10.0, fnd.Metrics["total_titles"], 1e-9)
	assert.InDelta(t, 0.7, fnd.Metrics["match_fraction"], 1e-9)

	// Evidence records only matching titles.
	for _, e := range fnd.Evidence {
		parts := strings.SplitN(e, "::", 2)
		require.Len(t, parts, 2, "evidence format is '<id>::<title>'")
		assert.Contains(t, parts[1], "Pattern",
			"only matching titles may appear in Evidence")
	}
}

// TestTitleShape_ZeroMatches verifies the no-match path: match_fraction=0 and
// empty Evidence are legitimate, non-error results.
func TestTitleShape_ZeroMatches(t *testing.T) {
	f := buildTitleShapeFixture()

	a := TitleShapeAnalyzer{}
	findings, err := a.Run(context.Background(), req(f, map[string]string{"pattern": "^NeverMatches$"}))
	require.NoError(t, err)
	require.Len(t, findings, 1)

	assert.InDelta(t, 0.0, findings[0].Metrics["matched_count"], 1e-9)
	assert.InDelta(t, 10.0, findings[0].Metrics["total_titles"], 1e-9)
	assert.InDelta(t, 0.0, findings[0].Metrics["match_fraction"], 1e-9)
	assert.Empty(t, findings[0].Evidence)
}

// TestTitleShape_MissingPattern verifies the empty-regex error path.
func TestTitleShape_MissingPattern(t *testing.T) {
	f := buildTitleShapeFixture()

	a := TitleShapeAnalyzer{}
	_, err := a.Run(context.Background(), req(f, nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pattern")
}

// TestTitleShape_InvalidRegex verifies the regex-syntax error path.
func TestTitleShape_InvalidRegex(t *testing.T) {
	f := buildTitleShapeFixture()

	a := TitleShapeAnalyzer{}
	_, err := a.Run(context.Background(), req(f, map[string]string{"pattern": "("}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid regex")
}

// TestTitleShape_Registered verifies init() self-registration.
func TestTitleShape_Registered(t *testing.T) {
	a, ok := foundation.Get("title-shape-distribution")
	require.True(t, ok, "title-shape-distribution analyzer must be registered at package init")
	assert.Equal(t, "title-shape-distribution", a.Name())
}
