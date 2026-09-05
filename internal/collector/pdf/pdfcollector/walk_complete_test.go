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

// TestPDFCollect_SuccessfulCollectAssertsACompleteWalk pins the WALK ASSERTION
// this collector makes, which is the second of the two gates that arm a raw
// graph's collect-driven deletion. The first is the server's full-replace family
// switch; without this one the server reaches its deletion phase, reads an
// incomplete walk off the result, returns an empty decision and destroys nothing.
// The runtime consequence of shipping that variant is the defect this changeset
// exists to close: a re-collected document leaves its previous generation in
// place and the graph holds the union of every parse.
//
// THERE IS NO FALSE LEG HERE, AND THAT IS BY CONSTRUCTION RATHER THAN AN
// OMISSION. Every per-page failure in the pdf path ERRORS rather than skipping:
// chunk.Build returns nil plus an error at its PageBlocks, PageTaggedBlocks,
// PageRuns and cluster call sites, its two `continue` statements are reached only
// after a nil error, Chunks returns on that error, and Collect returns on Chunks.
// So a partial-walk result is unconstructible without faking the PARSER rather
// than the document, and the correct implementation is the constant true. The
// discriminating direction that does exist is the OMISSION: the zero value is
// false, whose documented meaning is an incomplete walk that disables the
// deletion phase, and this test is red against exactly that. The WEB collector,
// whose crawl really can fail to read a unit it set out to read, carries the full
// property pair.
func TestPDFCollect_SuccessfulCollectAssertsACompleteWalk(t *testing.T) {
	t.Parallel()

	abs, err := filepath.Abs(integrationFixture)
	require.NoError(t, err)

	res, err := (&PDFCollector{}).Collect(context.Background(), abs, collector.CollectOptions{})
	require.NoError(t, err)
	require.NotNil(t, res)

	// THE CONTROL. A collect that emitted nothing would make the flag assertion
	// meaningless — a walk over an empty document is trivially complete.
	require.GreaterOrEqual(t, len(res.Nodes), 2,
		"the fixture must yield a document node plus at least one chunk-derived node, "+
			"or the walk assertion below is asserted over an empty emission")

	assert.True(t, res.WalkComplete,
		"a successful pdf collect read the whole document, so it must ASSERT a complete walk — "+
			"the zero value means an incomplete walk, which disables the server's deletion phase "+
			"and leaves every prior generation resident on re-collect")
}
