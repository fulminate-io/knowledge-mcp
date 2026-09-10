// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// summary_provenance_test.go pins the one thing the conversion site knows that
// nothing downstream can recover: whether a node's summary came from its
// collector.
//
// WHY THE CONTROL MATTERS MORE THAN THE POSITIVE HERE. The store DERIVES a
// summary from a node's description on the way in, so a mark applied to every
// node — or to every node that ends up with a summary — would read as "the
// collector wrote this" for text the collector never produced, and the server
// would stop summarizing whole families that opted in. The negative arm is what
// makes the positive mean anything.
func TestToCollectResult_StampsCollectorSummaryProvenance(t *testing.T) {
	env := &Result{Nodes: []Node{
		{ID: "with", Type: "widget", Summary: "the collector wrote this summary"},
		{ID: "without", Type: "widget", Description: "no summary, only a description"},
		{ID: "blank", Type: "widget", Summary: "   \n "},
	}}
	res, err := env.ToCollectResult("widgets", "default")
	require.NoError(t, err)
	require.Len(t, res.Nodes, 3)

	byID := map[string]map[string]string{}
	for _, n := range res.Nodes {
		byID[n.GetId()] = n.GetMetadata()
	}

	assert.Equal(t, "1", byID["with"][MetaKeyCollectorSummary],
		"a node whose collector supplied a summary must carry the provenance mark; without it the "+
			"server cannot tell that summary from one it derived itself")

	assert.NotContains(t, byID["without"], MetaKeyCollectorSummary,
		"CONTROL: a node with no summary must carry NO mark — the store will derive one from its "+
			"description, and a mark here would make that derived text look collector-written and "+
			"stop the summarizer ever replacing it")

	assert.NotContains(t, byID["blank"], MetaKeyCollectorSummary,
		"CONTROL: a whitespace-only summary is not a summary, so it earns no mark")
}

// TestToCollectResult_ProvenanceDoesNotMutateTheCallersMetadata: the envelope's
// map belongs to the provider's own output, which the conversion does not own and
// a later reader may still be holding.
func TestToCollectResult_ProvenanceDoesNotMutateTheCallersMetadata(t *testing.T) {
	callers := map[string]string{"team": "platform"}
	env := &Result{Nodes: []Node{
		{ID: "n1", Type: "widget", Summary: "provided", Metadata: callers},
	}}
	res, err := env.ToCollectResult("widgets", "default")
	require.NoError(t, err)

	assert.Equal(t, map[string]string{"team": "platform"}, callers,
		"the caller's metadata map must be untouched")
	assert.Equal(t, "platform", res.Nodes[0].GetMetadata()["team"],
		"and the caller's own keys must survive onto the converted node")
	assert.Equal(t, "1", res.Nodes[0].GetMetadata()[MetaKeyCollectorSummary])
}

// TestCollectorSummaryKey_Stable is the client half of the two-sided literal pin.
//
// THE PIN IS A LITERAL ON PURPOSE. Every other test in this file reads
// MetaKeyCollectorSummary through its identifier, which is correct for asserting
// behavior and useless for asserting the CONTRACT: a rename of this constant
// moves all of them together and they keep agreeing with themselves. The server
// declares the same key by value in its own module, because the two binaries
// cannot share a package, so the only thing that can catch a one-sided rename is
// a test on each side that spells the bytes.
//
// WHAT A ONE-SIDED RENAME COSTS: the server's RegisteredSummaryProvided returns
// false for every node, so an opted-in family's collector-written summaries are
// re-summarized by the LLM and the provider's text is overwritten — with no
// marker, no log line and no red anywhere.
func TestCollectorSummaryKey_Stable(t *testing.T) {
	if MetaKeyCollectorSummary != "collector_summary" {
		t.Errorf("MetaKeyCollectorSummary = %q, want %q — this key is duplicated by value in "+
			"cmd/knowledge-server/internal/store, which READS it. If you are renaming it, rename "+
			"BOTH copies and both pins in the same change", MetaKeyCollectorSummary, "collector_summary")
	}
}
