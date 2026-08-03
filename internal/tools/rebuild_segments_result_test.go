// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// rebuildResultText drives the manual op end to end over the supplied shipper and
// returns the OPERATOR-FACING text. It asserts on the returned result rather than on
// log output on purpose: a WARN in the daemon log is not a surface the operator who
// issued manage(rebuild_segments) ever sees, and it is exactly where the refusal
// already goes unnoticed.
func rebuildResultText(t *testing.T, shipper *fakeRebuildShipper) string {
	t.Helper()
	scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{
		makeScanPage("r-", 0, searchengine.DefaultMinSegmentDocs),
	}}
	res := handleClientRebuildSegments(context.Background(), rebuildClientDeps{scanner: scanner, shipper: shipper}, manageArgs{
		Operation: "rebuild_segments", Graph: "code", Name: "myrepo",
	})
	require.False(t, res.IsError, "neither outcome is a tool ERROR — the refusal is a reported state, not a failure: %v", res.Content)
	require.NotEmpty(t, res.Content)
	return res.Content[0].Text
}

// TestRebuildResultDistinguishesShippedFromPublished.
//
// WHAT IT CATCHES. A rebuild can report "N scanned, N hash buckets built +
// shipped, 0 superseded pruned" while its publish was refused by the coverage
// gate — a success report for work that never became the live set.
// An operator reading that has no way to know the corpus was not restored, which is
// how the failed clean restore was scored as having run. A refused publish returns a
// NIL ERROR (the coverage-gate skip and the agent's missing-blob skip both do), so
// SHIPPED and PUBLISHED have to be separate words in the report or the distinction
// does not exist for the person reading it.
//
// BOTH DIRECTIONS ARE REQUIRED. Without the success leg, a result that always reads
// as a failure satisfies the refusal leg perfectly — the trivially "safe" regression.
func TestRebuildResultDistinguishesShippedFromPublished(t *testing.T) {
	t.Run("a refused publish does not read as success and names the refusal", func(t *testing.T) {
		// noSwap is the refusal: FinalizeRebuild returns a nil error and reports that
		// no manifest swap landed — the exact shape the coverage gate produces.
		body := rebuildResultText(t, &fakeRebuildShipper{noSwap: true})

		assert.NotContains(t, body, "rebuild_segments complete",
			"a rebuild whose publish was refused must NOT report completion — that phrasing is what an operator reads as a restored corpus")
		assert.Contains(t, body, "REFUSED",
			"the refusal must be NAMED in the operator-facing result, not left to a daemon-log WARN")
		assert.Contains(t, body, "NOT the live set",
			"the report must say what the refusal cost: the rebuilt segments exist but are not what a search reads")

		// The counts still have to be there — the blobs really did ship, and suppressing
		// that would trade one misleading report for another.
		assert.Contains(t, body, "built + shipped",
			"the shipped work is still reported; only the claim that it LANDED is withdrawn")
		assert.Contains(t, strings.ToLower(body), "intact",
			"a skip leaves the prior manifest and blobs alone, and an operator deciding what to do next needs to know nothing was lost")
	})

	t.Run("a landed publish still reports success", func(t *testing.T) {
		// The default fake swaps the manifest — the healthy control.
		body := rebuildResultText(t, &fakeRebuildShipper{})

		assert.Contains(t, body, "rebuild_segments complete",
			"a rebuild that PUBLISHED must still report completion, or the gate is satisfiable by a result that always reports failure")
		assert.Contains(t, body, "PUBLISHED as the live set",
			"success names the publish explicitly, so the two outcomes are distinguishable by more than the absence of a warning")
		assert.NotContains(t, body, "REFUSED")
	})
}
