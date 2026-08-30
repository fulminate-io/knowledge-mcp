// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

// collectComposition drives a real in-tree PDF through PDFCollector.Collect and
// returns its node-type census. Both silence controls below are REAL DOCUMENTS
// rather than invented compositions, so they exercise the emitter as well as the
// asserter.
func collectComposition(t *testing.T, fixture string) collector.CollectComposition {
	t.Helper()
	abs, err := filepath.Abs(fixture)
	require.NoError(t, err)

	c := &PDFCollector{}
	res, err := c.Collect(context.Background(), abs, collector.CollectOptions{})
	require.NoError(t, err)
	return collector.NewCollectComposition(res)
}

// TestPDFCollector_AssertComposition_FiresWhenNoSubstantiveContent drives the
// shape a pdf harvest takes when it yielded nothing substantive: a document node
// and an unclassified block, with both substantive terms at zero.
func TestPDFCollector_AssertComposition_FiresWhenNoSubstantiveContent(t *testing.T) {
	c := &PDFCollector{}

	empty := collector.CollectComposition{
		GraphName:   "empty-doc",
		TotalNodes:  2,
		NodesByType: map[string]int{"document": 1, "block": 1},
	}
	require.Equal(t, 0, empty.NodesByType["paragraph"])
	require.Equal(t, 0, empty.NodesByType["code_block"])

	err := c.AssertComposition(empty)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harvest captured nothing usable")
	assert.Contains(t, err.Error(), "empty-doc", "the error names the source")
	assert.Contains(t, err.Error(), "nodes 2", "the error embeds what WAS captured")

	// KNOWN NEGATIVE on the code term alone: adding code_block to the SAME map
	// silences it, so the fire above is the predicate rather than an asserter that
	// refuses everything. The real code-only document is the fixture-backed
	// control below.
	withCode := collector.CollectComposition{
		GraphName:   "empty-doc",
		TotalNodes:  3,
		NodesByType: map[string]int{"document": 1, "block": 1, "code_block": 1},
	}
	assert.NoError(t, c.AssertComposition(withCode), "code alone must not fire")
}

// TestPDFCollector_AssertComposition_SilentOnRealFixture is the ORDINARY PROSE
// control, drawn from a real document rather than a hand-built map.
//
// It is ALSO the pdf leg's paragraph-blindness guard: this fixture has paragraph
// with code_block 0, so an implementation that dropped the paragraph term turns
// this test red.
func TestPDFCollector_AssertComposition_SilentOnRealFixture(t *testing.T) {
	comp := collectComposition(t, "../testdata/t4_paragraph_simple.pdf")

	assert.Positive(t, comp.NodesByType["paragraph"],
		"the prose fixture must emit paragraphs — this is what makes it the paragraph-blindness guard")
	assert.Equal(t, 0, comp.NodesByType["code_block"],
		"the prose fixture emits no code_block, so only the paragraph term can silence the guard")

	assert.NoError(t, (&PDFCollector{}).AssertComposition(comp))
}

// TestPDFCollector_AssertComposition_SilentOnCodeOnlyRealFixture is the
// CODE-ONLY control. This is the fixture whose false failure forced the ticket's
// second amendment: a paragraph-only rule declares a genuine multi-hundred-node
// harvest of an RFC "captured nothing usable" inside an error message that itself
// names its code blocks. Keeping it as a permanent control is what stops the
// paragraph-only predicate from being reintroduced.
//
// THE SHAPE IS ASSERTED, NOT A PINNED COUNT. The chunker's absolute code_block
// count is an accuracy property of the chunker against this document, and it
// moves when the chunker improves; zero-paragraph-and-positive-code is what the
// predicate actually reads and what survives that movement.
func TestPDFCollector_AssertComposition_SilentOnCodeOnlyRealFixture(t *testing.T) {
	comp := collectComposition(t, "../testdata/corpus/rfc-7234-caching/source.pdf")

	assert.Equal(t, 0, comp.NodesByType["paragraph"],
		"this document is genuinely code-only — the collector emits no paragraph for it")
	assert.Positive(t, comp.NodesByType["code_block"],
		"a genuine harvest: hundreds of code blocks, which the guard must accept as substantive")

	assert.NoError(t, (&PDFCollector{}).AssertComposition(comp),
		"a document made entirely of code is a real harvest and must stay silent")
}
