// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_query_practice_gap_notice_test.go covers practiceSegmentGapNotice's
// OPERAND, as distinct from intercept_query_practice_browse_test.go which covers the
// notice as rendered through the whole browse path.
//
// THE TWO FILES ARE NOT REDUNDANT. The browse test drives gatedRoutePractice and reads
// a response body, so it proves the notice reaches a caller; these tests call the
// qualifier directly and read the (message, loud) pair, so they can assert things a
// rendered body cannot express — that a probe never ran, that a pool was not
// resurrected, that a load error became a caveat rather than a zero. The LOCAL operand
// moved off a remote manifest read and onto a local loading decider when the cloud
// segment rail was deleted, and that move is what these assertions are about.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// gapNoticeDeps assembles the qualifier's three seams: a fake server carrying the
// graph stats, a coverage reader carrying the local count, and the residency knobs.
//
// THE SERVER AND LOCAL NUMBERS ARE PROGRAMMED SEPARATELY ON PURPOSE. They are the two
// operands the whole check rests on, and a helper that derived one from the other
// would make every "they disagree" fixture below unconstructible.
//
//nolint:unparam // nodes is the intentional named API: it is one of the two operands the doc above insists stay separately programmable, so hardcoding it would make the "they disagree" fixtures unconstructible
func gapNoticeDeps(
	t *testing.T, language string, nodes, embedded, localCovered int,
) *interceptDeps {
	t.Helper()
	gc, h := newFanOutHarnessWithHandler(t, []string{language})
	h.stats = &knowledgev1.GraphStats{
		NodeCount:         int32(nodes),
		BinaryVectorCount: int32(embedded),
	}
	return &interceptDeps{
		gc:          gc,
		segMgr:      &fakeSegmentSearcher{},
		segCoverage: &gapCoverageFake{covered: localCovered},
	}
}

// TestPracticeGapNoticeDeclinesEvictedPool is the catcher for the mechanical operand
// swap: an EVICTED pool must get the truthful-inability caveat, never the confident
// "the ranked index is missing" alarm.
//
// THE NAMED HAZARD is in the qualifier's own doc — a resident-pool read "specifically
// reads 0 for an EVICTED pool, which would raise a false alarm on a graph whose
// segments are intact but paged out". An evicted pool's presence genuinely is not
// determinable without re-materializing it, and re-materializing it from a zero-hit
// search path would undo the residency policy.
//
// TWO ASSERTIONS, AND THE SECOND IS THE ONE THAT DISCRIMINATES. Asserting the caveat
// alone is satisfied by an implementation whose fence sits one line too LATE — after
// the loading read has already materialized the pool, which produces the same caveat
// off a different and destructive path. Asserting the pool is STILL EVICTED afterwards
// is what separates them, and no test omitting it can tell the two apart. The in-tree
// precedent is bootstrap/client_segment_arm_verdict_test.go's "the gate must not have
// resurrected the pool".
func TestPracticeGapNoticeDeclinesEvictedPool(t *testing.T) {
	const language = "design-patterns"

	// An embedded corpus with an empty local read: the arrangement that PRODUCES the
	// loud alarm when nothing fences it. That is what makes the caveat below
	// attributable to the fence rather than to a fixture that could never be loud.
	deps := gapNoticeDeps(t, language, 3117, 2556, 0)
	deps.poolEvicted = true

	msg, loud := practiceSegmentGapNotice(opCtx(), deps, language)

	require.False(t, loud, "an undeterminable state is not an error verdict: %s", msg)
	require.Contains(t, msg, "could not be qualified",
		"the honest answer names its own inability rather than reporting a clean zero")
	require.Contains(t, msg, "evicted", "and names the reason, so an operator can act on it")
	require.NotContains(t, msg, "rebuild_segments",
		"an evicted pool must NOT be told to rebuild — its segments are intact, merely paged out")

	// THE DISCRIMINATING ASSERTION: the fence ran BEFORE the local read.
	require.True(t, deps.PoolEvicted(kgtypes.GraphPractice, language),
		"the qualifier must not have resurrected the pool — a fence placed after the "+
			"loading read would have materialized it and still produced a caveat")
	require.Zero(t, deps.loadCalls,
		"the loading decider must never have been called: the load IS the materialization")

	// KNOWN POSITIVE, SAME RUN. The identical fixture with residency flipped DOES go
	// loud. Without this the two assertions above are equally satisfied by a qualifier
	// hard-wired to decline, and by a fixture whose stats seam was never wired.
	live := gapNoticeDeps(t, language, 3117, 2556, 0)
	msg, loud = practiceSegmentGapNotice(opCtx(), live, language)
	require.True(t, loud,
		"CONTROL: the same counts on a RESIDENT pool must raise the alarm — otherwise this "+
			"fixture proves nothing about the fence: %s", msg)
	require.Contains(t, msg, "rebuild_segments", "and the alarm carries the remedy")
	require.Positive(t, live.loadCalls, "CONTROL: the resident path DOES reach the loading decider")
}

// TestPracticeGapNoticeDiscriminates is the both-directions gate: the qualifier must
// still separate FOUR states, not merely produce one of them.
//
// IT IS DELIBERATELY SEPARATE from the eviction test above. A gate that can only fire
// in one direction measures nothing, and an eviction fixture on its own cannot show
// that the other three readings are still reachable.
func TestPracticeGapNoticeDiscriminates(t *testing.T) {
	const language = "go"

	t.Run("a_live_resident_pool_is_a_genuine_no_match", func(t *testing.T) {
		// The CORRECT realization: the ranked index exists and was searched, so the
		// zero is the truth and carries no notice at all.
		deps := gapNoticeDeps(t, language, 3117, 2556, 42)
		msg, loud := practiceSegmentGapNotice(opCtx(), deps, language)
		require.False(t, loud, "a searched index returning nothing is data, not an error")
		require.Empty(t, msg, "a genuine no-match adds no notice: %s", msg)
	})

	t.Run("an_empty_pool_against_an_embedded_corpus_is_loud", func(t *testing.T) {
		// THE DEFECT the check exists for. An implementation returning loud=false
		// unconditionally fails here, exactly as the leg above kills loud=true.
		deps := gapNoticeDeps(t, language, 3117, 2556, 0)
		msg, loud := practiceSegmentGapNotice(opCtx(), deps, language)
		require.True(t, loud, "embedded content with no ranked index is an error: %s", msg)
		require.Contains(t, msg, "rebuild_segments", "the alarm names the remedy")
		require.Contains(t, msg, "2556", "and the embedded count it was derived from")
	})

	t.Run("the_operands_come_from_two_authorities", func(t *testing.T) {
		// THE IDENTITY CHECK. If both operands were secretly derived from one source
		// they could never disagree, and every assertion in this file would be
		// measuring a number against a restatement of itself.
		//
		// The proof is that moving ONE knob flips the verdict while the OTHER's value
		// stays observably the same. The server says 2556 embedded in both runs; only
		// the local pool changes, and only the verdict follows it.
		dark := gapNoticeDeps(t, language, 3117, 2556, 0)
		darkMsg, darkLoud := practiceSegmentGapNotice(opCtx(), dark, language)
		require.True(t, darkLoud)
		require.Contains(t, darkMsg, "2556",
			"the SERVER operand is 2556 while the LOCAL operand is 0 — two numbers about "+
				"one graph, disagreeing, which a single-authority pair could not do")

		lit := gapNoticeDeps(t, language, 3117, 2556, 7)
		litMsg, litLoud := practiceSegmentGapNotice(opCtx(), lit, language)
		require.False(t, litLoud,
			"the SAME server operand with a populated local pool is a no-match: %s", litMsg)
	})

	t.Run("a_constructed_but_unloaded_pool_declines", func(t *testing.T) {
		// THE LEG THAT CATCHES A NON-LOADING OPERAND. A pool constructed but never
		// loaded holds its segments in L2 while a non-loading read of it "legitimately
		// reads 0" — so an implementation reading the non-loading count passes all
		// three legs above and emits the confident "the ranked index is missing" for a
		// cold-but-healthy graph. It fails ONLY here.
		//
		// The loading decider RETURNS its load error instead of swallowing it, and the
		// qualifier routes that error into a caveat rather than acting on an empty view.
		deps := gapNoticeDeps(t, language, 3117, 2556, 0)
		deps.loadLiveResidentErr = errors.New("segment pool not loaded")

		msg, loud := practiceSegmentGapNotice(opCtx(), deps, language)
		require.False(t, loud, "a pool that could not be loaded yields no verdict: %s", msg)
		require.Contains(t, msg, "could not be qualified")
		require.Contains(t, msg, "segment pool not loaded",
			"the load error travels into the caveat — a swallowed error would read as a clean zero")
		require.NotContains(t, msg, "rebuild_segments",
			"the wrong answer here is the confident rebuild instruction for a healthy cold pool")
	})
}
