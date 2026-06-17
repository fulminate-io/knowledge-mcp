// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// countingExecCaller is a GraphCaller that counts Execute calls. The cache-serve
// blind-spots handler must issue ZERO Execute calls (O(1) cache read) — any
// non-zero count proves a recompute leaked back onto the call path.
type countingExecCaller struct {
	calls atomic.Int64
}

func (c *countingExecCaller) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	c.calls.Add(1)
	return &knowledgev1.ExecuteResponse{}, nil
}

// sampleBlindSpotReport builds a constructed faceted report with items across two
// facets, used by the render + json tests.
func sampleBlindSpotReport() clientthought.BlindSpotReport {
	return clientthought.BlindSpotReport{
		Computed:      true,
		TotalThoughts: 42,
		Facets: []clientthought.BlindSpotFacet{
			{
				Key:   "confident_untested",
				Title: "Confident but untested",
				Items: []clientthought.BlindSpotItem{{
					ThoughtID: "t-settled", Name: "settled belief",
					Magnitude: 1.95, Consistency: 1.0, ChargeCount: 1, Influence: 0.0,
					Reason: "high magnitude + high consistency on <=1 charge",
				}},
			},
			{
				Key:   "foundational_unexamined",
				Title: "Foundational but unexamined",
				Items: []clientthought.BlindSpotItem{{
					ThoughtID: "t-hub", Name: "load-bearing assumption",
					Magnitude: 0.0, Consistency: 0.0, ChargeCount: 0, Influence: 0.4,
					Reason: "high influence with 0 charges",
				}},
			},
			{
				Key:   "belief_reversal",
				Title: "Belief reversal",
				Items: []clientthought.BlindSpotItem{{
					ThoughtID: "t-flip", Name: "a flipped stance",
					Magnitude: 1.2, Consistency: 0.5, ChargeCount: 4, Influence: 0.1,
					Reason: "old charges net positive, recent charges net negative — belief reversal",
				}},
				Groups: []clientthought.BlindSpotGroup{{
					Key: "cA", Label: "Authentication topic",
					OldNet: -8, RecentNet: 9, MemberCount: 2, Members: []string{"a0", "b0"},
					Reason: "pooled charges flip old=negative → recent=positive — topic/cluster-level belief reversal",
				}},
			},
		},
	}
}

// TestHandleReflectBlindSpots (FAILS-WHEN-ABSENT): the text render leads with the
// blind-spots header and names each non-empty facet Title plus an item Name and
// Reason; the json format returns the faceted report.
func TestHandleReflectBlindSpots(t *testing.T) {
	deps := interceptTestDeps{blindSpots: sampleBlindSpotReport()}

	text := resultText(handleReflectBlindSpots(context.Background(), deps, queryReflectArgs{}))
	assert.True(t, strings.HasPrefix(text, "# Blind Spots"), "render leads with the blind-spots header; got: %s", text)
	assert.Contains(t, text, "Confident but untested", "names facet 1 title")
	assert.Contains(t, text, "Foundational but unexamined", "names facet 2 title")
	assert.Contains(t, text, "settled belief", "names a facet-1 item")
	assert.Contains(t, text, "load-bearing assumption", "names a facet-2 item")
	assert.Contains(t, text, "high influence with 0 charges", "shows an item reason")
	// Facet-5 topic/cluster-level group view renders alongside the per-thought items.
	assert.Contains(t, text, "Topic/cluster-level reversals", "renders the group sub-section header")
	assert.Contains(t, text, "Authentication topic", "names the topic/cluster group label")
	assert.Contains(t, text, "topic/cluster-level belief reversal", "shows the group reason")

	jsonText := resultText(handleReflectBlindSpots(context.Background(), deps, queryReflectArgs{Format: "json"}))
	assert.Contains(t, jsonText, "foundational_unexamined", "json carries the facet keys")
	assert.Contains(t, jsonText, "t-hub", "json carries the item thought IDs")
	assert.Contains(t, jsonText, "Authentication topic", "json carries the group label")
}

// TestHandleReflectBlindSpots_ColdStart (FAILS-WHEN-ABSENT): a zero-value report
// (Computed=false) renders the not-yet-computed message, NOT an empty report.
func TestHandleReflectBlindSpots_ColdStart(t *testing.T) {
	deps := interceptTestDeps{blindSpots: clientthought.BlindSpotReport{}} // Computed=false.

	text := resultText(handleReflectBlindSpots(context.Background(), deps, queryReflectArgs{}))
	assert.Equal(t, blindSpotColdMessage, text, "cold cache renders the not-yet-computed message")
}

// TestHandleReflectBlindSpots_NilProvider (FAILS-WHEN-ABSENT): a nil
// BlindSpotProvider (reflection loop not running) renders the loop-not-running
// message rather than panicking or recomputing.
func TestHandleReflectBlindSpots_NilProvider(t *testing.T) {
	deps := interceptTestDeps{blindSpotProviderNil: true}

	text := resultText(handleReflectBlindSpots(context.Background(), deps, queryReflectArgs{}))
	assert.Contains(t, text, "reflection loop is not running", "nil provider renders the loop-not-running message")
}

// TestHandleReflectBlindSpots_NoRecompute (FAILS-WHEN-ABSENT, ticket-mandated): the
// cache-serve handler issues ZERO Execute calls on the gc. A recompute path
// (fetchClusterContext / adjacency / BlindSpotInfluenceVector / charge+node
// hydrate) would issue many Execute calls; zero is the structural proof of the
// O(1) cache-serve contract. The provider returns a computed report so the handler
// takes the full render path (not the cold short-circuit).
func TestHandleReflectBlindSpots_NoRecompute(t *testing.T) {
	gc := &countingExecCaller{}
	deps := interceptTestDeps{gc: gc, blindSpots: sampleBlindSpotReport()}

	// Text and json both exercise the served path.
	_ = handleReflectBlindSpots(context.Background(), deps, queryReflectArgs{})
	_ = handleReflectBlindSpots(context.Background(), deps, queryReflectArgs{Format: "json"})

	assert.Equal(t, int64(0), gc.calls.Load(),
		"cache-serve handler must issue ZERO Execute calls — a non-zero count means a recompute leaked onto the call path")
}
