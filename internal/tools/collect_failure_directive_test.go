// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// directiveSuccessText is the success line a WEB collect carries. The package's
// existing detachSuccessText is "Collected code /repo — streamed to server.", a
// CODE-collect message that would misdescribe every case below.
const directiveSuccessText = "Collected web site-alpha — streamed to server."

// refusedHarvestVerdict is the verbatim message shape the composition gate emits
// for a harvest that captured nothing usable — the outcome reproduced live
// against a real backend, where it left a registered graph behind and named no
// way to remove it.
const refusedHarvestVerdict = "collect web site-alpha: harvest captured nothing usable — " +
	"the crawl emitted no nodes at all (nodes 0, edges 0)"

// harvestComposition is the rendered node-type composition of a run that DID
// produce something, used by the success control.
const harvestComposition = "nodes 12 (paragraph 8, page 4), edges 11"

// refusedHarvestWork returns the work closure a refused harvest reports:
// builtinCollectWork returns the rendered composition and the collector's own
// produced graph name ALONGSIDE the verdict error, so a failure arm has the name
// in hand exactly as the success arm does.
func refusedHarvestWork(producedGraph string) func() (string, string, error) {
	return func() (string, string, error) {
		return "nodes 0, edges 0", producedGraph, errors.New(refusedHarvestVerdict)
	}
}

// succeededHarvestWork is the same shape with no verdict error — the input the
// characterization guard drives through the success arms.
func succeededHarvestWork(producedGraph string) func() (string, string, error) {
	return func() (string, string, error) {
		return harvestComposition, producedGraph, nil
	}
}

// TestCollectWaitOrDetach_FailureCarriesTheRawDropDirective pins BOTH render
// sites of collectWaitOrDetach and the four things that must NOT change with
// them.
//
// It drives collectWaitOrDetach DIRECTLY rather than through a registered
// collector: the work unit is a parameter of that function, so no stub has to be
// registered under the reserved "web" or "pdf" type (collector.Register panics on
// a duplicate name), and both render sites are reachable from one test.
//
// Each subtest uses a DISTINCT single-flight key: collectWaitOrDetach coalesces
// on the key, and a repeated key would return the "already running" text instead
// of the outcome under test.
//
// THE NOTICE ARGUMENT IS EMPTY THROUGHOUT, and deliberately so. It carries the
// pre-walk report of older hash-suffixed graphs, which none of these inputs has;
// an empty notice degrades the answer to the text unchanged, so every assertion
// below reads the response this test is actually about. It is also the value
// that keeps the two NotContains legs honest: a notice carrying its own
// drop_graph call would satisfy the Contains legs while making the negative ones
// unmeasurable.
func TestCollectWaitOrDetach_FailureCarriesTheRawDropDirective(t *testing.T) {
	// detachedArm builds a standing runtime whose detach timer never fires, so
	// the completed-in-time arm of the select always wins the race.
	detachedArm := func() *CollectRuntime {
		rt := NewCollectRuntime()
		rt.detachAfter = time.Hour
		return rt
	}

	// bothArms runs one input through the rt==nil synchronous fallback and the
	// completed-in-time arm, asserting the same property on each.
	bothArms := func(t *testing.T, collectorType, keyBase string,
		work func() (string, string, error), check func(t *testing.T, arm string, res kgtools.ToolResult)) {
		t.Helper()
		check(t, "sync", collectWaitOrDetach(nil, collectorType,
			keyBase+"-sync", "web site-alpha", "", "", directiveSuccessText, "", work))
		check(t, "detached", collectWaitOrDetach(detachedArm(), collectorType,
			keyBase+"-detached", "web site-alpha", "", "", directiveSuccessText, "", work))
	}

	t.Run("sync_arm_failure_names_the_drop_call", func(t *testing.T) {
		res := collectWaitOrDetach(nil, string(kgtypes.GraphWebRaw),
			"sync-fail", "web site-alpha", "", "", directiveSuccessText, "",
			refusedHarvestWork("site-alpha"))

		body := resultText(res)
		assert.True(t, res.IsError, "a refused harvest stays an error result")
		assert.Contains(t, body, refusedHarvestVerdict, "the verdict text survives verbatim")
		assert.Contains(t, body, "drop_graph", "the failure response names the drop call")
		assert.Contains(t, body, "site-alpha", "the drop call names the produced graph")
	})

	t.Run("detached_arm_failure_names_the_drop_call", func(t *testing.T) {
		res := collectWaitOrDetach(detachedArm(), string(kgtypes.GraphWebRaw),
			"detached-fail", "web site-alpha", "", "", directiveSuccessText, "",
			refusedHarvestWork("site-alpha"))

		body := resultText(res)
		assert.True(t, res.IsError, "a refused harvest stays an error result")
		assert.Contains(t, body, refusedHarvestVerdict, "the verdict text survives verbatim")
		assert.Contains(t, body, "drop_graph", "the failure response names the drop call")
		assert.Contains(t, body, "site-alpha", "the drop call names the produced graph")
	})

	t.Run("a_failed_code_collect_is_told_nothing", func(t *testing.T) {
		// Rejects appending the directive unconditionally, which would tell an
		// operator whose CODE collect failed to drop the repo graph they built.
		bothArms(t, string(kgtypes.GraphCode), "code-fail", refusedHarvestWork("site-alpha"),
			func(t *testing.T, arm string, res kgtools.ToolResult) {
				body := resultText(res)
				assert.True(t, res.IsError, "%s arm: a failed code collect stays an error", arm)
				assert.NotContains(t, body, "drop_graph",
					"%s arm: a code graph is the durable artifact, never something to drop", arm)
			})
	})

	t.Run("a_failure_with_no_produced_graph_names_no_call", func(t *testing.T) {
		// Rejects rendering a call with an empty target: an instruction that
		// would drop nothing, or be refused, reads worse than silence.
		bothArms(t, string(kgtypes.GraphWebRaw), "noname-fail", refusedHarvestWork(""),
			func(t *testing.T, arm string, res kgtools.ToolResult) {
				body := resultText(res)
				assert.True(t, res.IsError, "%s arm: the failure stays an error", arm)
				assert.NotContains(t, body, "drop_graph",
					"%s arm: no produced graph means no call, never a call with an empty target", arm)
			})
	})

	t.Run("the_success_path_still_carries_it", func(t *testing.T) {
		// CHARACTERIZATION GUARD: this passes on the unfixed tree. It is what
		// stops a change that MOVES the directive from the success path onto the
		// failure path from satisfying everything above.
		bothArms(t, string(kgtypes.GraphWebRaw), "web-ok", succeededHarvestWork("site-alpha"),
			func(t *testing.T, arm string, res kgtools.ToolResult) {
				body := resultText(res)
				assert.False(t, res.IsError, "%s arm: a successful collect is not an error", arm)
				assert.Contains(t, body, "drop_graph",
					"%s arm: the success path keeps its drop directive", arm)
				assert.Contains(t, body, harvestComposition,
					"%s arm: the success path keeps its composition", arm)
			})
	})

	t.Run("control_the_reader_sees_real_bodies", func(t *testing.T) {
		// The NotContains assertions above read through resultText. A reader
		// returning "" would satisfy every one of them while measuring nothing,
		// so pin that the same reader on the same shape returns a real body.
		res := collectWaitOrDetach(nil, string(kgtypes.GraphCode),
			"control-reader", "code /repo", "", "", directiveSuccessText, "",
			refusedHarvestWork("site-alpha"))

		body := resultText(res)
		require.NotEmpty(t, body,
			"the reader used by the NotContains assertions must return a real body")
		assert.Contains(t, body, refusedHarvestVerdict,
			"and that body is the response under test, not some other string")
	})
}
