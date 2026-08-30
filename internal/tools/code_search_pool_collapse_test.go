// SPDX-License-Identifier: Apache-2.0

// code_search_pool_collapse_test.go — the shipped-complete gate and the single-pool
// collapse it admits. Every test here drives codeSearchPoolHits directly, because
// the property under test is the POOL SHAPE one query resolves to and that is
// decided at exactly one place.
//
// THE ASSERTIONS ARE ON WHICH POOL, NEVER ON HOW MANY. A collapse that read the
// BASE pool would serve every branch query from the default branch's index while a
// count-only test stayed green, so each case pins the pool by name AND checks the
// hits came from that pool's own canned list.

package tools

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// poolCoverageFake is the SegmentCoverageReader half of the gate's two operands:
// the segment engine's shipped doc count for one bucket. It RECORDS the names it
// was asked for and how many times, which is what lets a test prove an injected
// fault actually reached the gate rather than being served from the memo.
type poolCoverageFake struct {
	mu sync.Mutex
	// covered is the SUMMING resident count the loading reader returns. The gate
	// calls that reader for its load and its error and DISCARDS this number.
	covered int
	// liveCovered is the DISTINCT LIVE-SEARCHABLE count — the operand the gate
	// actually compares against the bar. Holding both separately is what lets a
	// fixture express cross-segment duplication (liveCovered < covered), which is
	// the state the gate used to read as complete.
	liveCovered int
	err         error
	asked       []string
}

func (f *poolCoverageFake) ShippedSegmentDocCount(
	_ context.Context, _ kgtypes.GraphType, name string,
) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, name)
	return f.covered, f.err
}

func (f *poolCoverageFake) askedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// LiveResidentDocCount IS READ BY THE GATE — it is the operand compared against the
// bar. It is served from its own field rather than from `covered` so a fixture can
// express a bucket carrying cross-segment duplication.
func (f *poolCoverageFake) LiveResidentDocCount(kgtypes.GraphType, string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.liveCovered
}

// The remaining SegmentCoverageReader methods exist only to satisfy the seam —
// the gate reads none of them, and a fake that returned something interesting here
// would suggest otherwise.
func (f *poolCoverageFake) ResidentDocCount(kgtypes.GraphType, string) int { return 0 }
func (f *poolCoverageFake) RepairVerification(kgtypes.GraphType, string) (RepairVerification, bool) {
	return RepairVerification{}, false
}

func (f *poolCoverageFake) LoadRebuildState(kgtypes.GraphType, string) (int64, []searchengine.ExternalID, error) {
	return 0, nil, nil
}
func (f *poolCoverageFake) LoadMergeWatermark(kgtypes.GraphType, string) (int64, error) {
	return 0, nil
}

// poolBarFake is the OTHER operand: the branch graph's embedded population, served
// over the Stats seam the gate reads through GraphEmbeddedCount's selector form. It
// records every selector it was addressed with, so a test can assert the bar was
// taken at the BRANCH — a bar read off the base graph would be a different number
// answering a different question.
type poolBarFake struct {
	mu        sync.Mutex
	embedded  int
	err       error
	selectors []*knowledgev1.GraphSelector
}

func (f *poolBarFake) Stats(
	_ context.Context, req *knowledgev1.StatsRequest,
) (*knowledgev1.StatsResponse, error) {
	f.mu.Lock()
	f.selectors = append(f.selectors, req.GetTarget())
	embedded, err := f.embedded, f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &knowledgev1.StatsResponse{
		GraphStats: &knowledgev1.GraphStats{BinaryVectorCount: int32(embedded)},
	}, nil
}

func (f *poolBarFake) Execute(
	_ context.Context, _ *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

func (f *poolBarFake) askedSelectors() []*knowledgev1.GraphSelector {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*knowledgev1.GraphSelector(nil), f.selectors...)
}

// collapseFixture wires one gate scenario: an engine fake holding DISTINCT canned
// hits per pool, the coverage operand, and the bar operand.
//
// THE TWO POOLS CARRY DIFFERENT IDS ON PURPOSE. Identical canned lists would make
// "the branch pool was read" and "the base pool was read" produce identical hits,
// and the direction this phase is most able to invert would go undetected.
type collapseFixture struct {
	cdeps  codeSearchDeps
	engine *codeSearchEngineFake
	cov    *poolCoverageFake
	bar    *poolBarFake
}

// collapseFixtureBranch is the branch every collapseFixture is built for, and the
// branch its tests hand the code under test. It is a CONST rather than a
// newCollapseFixture parameter because those two strings must be the SAME string —
// the fixture keys its overlay hit list by it, so a test supplying a different
// branch to the code under test would resolve a pool the engine fake has no canned
// list for and assert against an empty read. A parameter let the two be spelled
// independently while every call site passed the same value anyway.
const collapseFixtureBranch = "feat"

func newCollapseFixture(repo string, covered, embedded int) *collapseFixture {
	overlay := overlayName(repo, collapseFixtureBranch)
	eng := &codeSearchEngineFake{
		hitsByRepo: map[string][]searchengine.Hit{
			repo:    {{ID: "base-doc", Score: 1}},
			overlay: {{ID: "branch-doc", Score: 1}},
		},
	}
	// The existing fixtures model a bucket with NO cross-segment duplication, which
	// is what their assertions have always meant: both readers agree. A fixture that
	// wants duplication sets liveCovered below covered explicitly.
	cov := &poolCoverageFake{covered: covered, liveCovered: covered}
	bar := &poolBarFake{embedded: embedded}
	return &collapseFixture{
		cdeps: codeSearchDeps{
			mgr: eng, ovl: eng, cov: cov, gc: bar,
			degrade: &searchDegrade{}, exec: eng.Execute,
		},
		engine: eng,
		cov:    cov,
		bar:    bar,
	}
}

// hitIDs renders a hit list as its ids, so a failure message names the pool the
// result actually came from instead of a struct dump.
func hitIDs(hits []searchengine.Hit) []searchengine.ExternalID {
	out := make([]searchengine.ExternalID, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.ID)
	}
	return out
}

// TestCodeSearchPoolHits_SinglePoolWhenBranchShippedComplete is step 4.2's
// criterion: a branch whose own bucket covers the branch graph's whole embedded
// population is served from that ONE pool — and the pool is the BRANCH's, not
// base's.
func TestCodeSearchPoolHits_SinglePoolWhenBranchShippedComplete(t *testing.T) {
	f := newCollapseFixture("collapse-repo", 12, 12)

	hits := codeSearchPoolHits(context.Background(), f.cdeps,
		"collapse-repo", collapseFixtureBranch, "q", nil, 5)

	pools := f.engine.requestedPools()
	// KNOWN-POSITIVE FIRST: a gate that returned no hits at all would satisfy
	// "did not read the base pool" by reading nothing.
	require.Equal(t, []searchengine.ExternalID{"branch-doc"}, hitIDs(hits),
		"the collapsed read must return the BRANCH pool's documents; pools requested were %+v", pools)

	require.Len(t, pools, 1,
		"a shipped-complete branch reads exactly one pool; pools requested were %+v", pools)
	assert.Equal(t, poolReq{base: "collapse-repo@feat"}, pools[0],
		"the surviving pool must be the BRANCH pool — a collapse onto base serves every "+
			"branch query from the default branch's index")

	// The bar is only a cross-authority check if it was taken at the branch.
	sels := f.bar.askedSelectors()
	require.Len(t, sels, 1, "the gate reads the bar exactly once per uncached verdict")
	assert.Equal(t, "collapse-repo", sels[0].GetRepo())
	assert.Equal(t, "feat", sels[0].GetBranch())
	assert.Equal(t, []string{"collapse-repo@feat"}, f.cov.askedNames(),
		"the covered count must be read for the BRANCH bucket")
}

// TestCodeSearchPoolHits_TwoPoolWhenBranchShippedIncomplete is step 4.1's
// criterion: every state in which completeness is not positively established keeps
// the two-pool union, which is the safe direction.
//
// EACH SUBTEST USES ITS OWN REPO NAME because the verdict is memoized per
// overlay-qualified graph name; a shared name would let one subtest's verdict serve
// the next and the injected fault would never reach the gate. Every case asserts
// the coverage operand was actually consulted, which is what proves that.
func TestCodeSearchPoolHits_TwoPoolWhenBranchShippedIncomplete(t *testing.T) {
	cases := []struct {
		name     string
		repo     string
		covered  int
		embedded int
		mutate   func(*collapseFixture)
		twoPool  bool
	}{{
		// The known-positive control, in the SAME run: without it every assertion
		// below is satisfied by a gate that can never fire at all.
		name: "control_complete_collapses", repo: "incomplete-control", covered: 8, embedded: 8,
		twoPool: false,
	}, {
		// THE "unknown_denominator" CASE WAS DELETED HERE, with its reason, because
		// this was the ONLY test exercising the unknown-disarm path and a silent
		// removal would look like coverage that was merely dropped.
		//
		// The honest reason is that the path became UNREACHABLE, not that the case was
		// inconvenient: ShippedSegmentDocCount lost its unknown-count return when the
		// cloud branch went, and the surviving branch never reports an unknown
		// denominator — it reads a resident count, which is always known. A fixture
		// setting an unknown flag now sets a field nobody reads, so the case would
		// have gone on passing while asserting nothing at all.
		name: "covered_read_errors", repo: "incomplete-err", covered: 8, embedded: 8,
		mutate:  func(f *collapseFixture) { f.cov.err = errors.New("segment read failed") },
		twoPool: true,
	}, {
		// The one durably-short bucket: SeedBranchBucketFromBase warns and continues
		// when it copied fewer partitions than base published, writing base's
		// watermark, so the delta scan never re-fetches them. That bucket stays short
		// indefinitely and its documents are served correctly ONLY through the union.
		name: "short_bucket_durable_partial_seed", repo: "incomplete-short", covered: 7, embedded: 8,
		twoPool: true,
	}, {
		name: "bar_read_errors", repo: "incomplete-bar-err", covered: 8, embedded: 8,
		mutate:  func(f *collapseFixture) { f.bar.err = errors.New("stats unavailable") },
		twoPool: true,
	}, {
		// A zero bar is the tautology arriving by a different door: covered >= 0 is
		// true for an empty bucket, so an unavailable bar must read as unknown.
		name: "bar_is_zero", repo: "incomplete-bar-zero", covered: 0, embedded: 0,
		twoPool: true,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newCollapseFixture(tc.repo, tc.covered, tc.embedded)
			if tc.mutate != nil {
				tc.mutate(f)
			}

			hits := codeSearchPoolHits(context.Background(), f.cdeps, tc.repo, collapseFixtureBranch, "q", nil, 5)
			pools := f.engine.requestedPools()

			require.NotEmpty(t, f.cov.askedNames(),
				"the gate must have consulted the coverage operand — a verdict served from "+
					"the memo would mean this case's injected state never reached the gate")
			require.Len(t, pools, 1, "one query resolves one pool shape; pools requested were %+v", pools)

			if tc.twoPool {
				assert.Equal(t, poolReq{base: tc.repo, overlay: tc.repo + "@feat"}, pools[0],
					"an incomplete branch bucket keeps the base pool alongside its overlay")
				assert.Equal(t, []searchengine.ExternalID{"base-doc"}, hitIDs(hits),
					"the two-pool arm serves through SearchOverlay, whose fake returns the base list")
				return
			}
			assert.Equal(t, poolReq{base: tc.repo + "@feat"}, pools[0],
				"the control case must COLLAPSE — if it does not, every two-pool assertion "+
					"in this test is vacuous")
			assert.Equal(t, []searchengine.ExternalID{"branch-doc"}, hitIDs(hits))
		})
	}
}

// poolWiringDeps is interceptDeps with a coverage seam attached, for the one
// assertion the direct-cdeps tests structurally cannot make.
type poolWiringDeps struct {
	*interceptDeps
	cov SegmentCoverageReader
}

func (d *poolWiringDeps) SegmentCoverage() SegmentCoverageReader { return d.cov }

// TestComposeCodeSearch_WiresTheCoverageSeam covers the seam every other test in
// this file assumes: composeCodeSearch is where cdeps.cov is populated, and a
// codeSearchDeps built without it leaves the gate reading nil and the collapse
// silently disabled forever. Every direct-cdeps test here builds cov by hand, so
// none of them would notice — this is the only case that fails when the wiring
// line goes missing.
//
// It asserts the seam was CONSULTED, not the verdict: the harness serves the bar
// from a canned engine that has no Stats answer, so the gate correctly lands on
// not-complete. Reaching the coverage read at all is the property under test.
func TestComposeCodeSearch_WiresTheCoverageSeam(t *testing.T) {
	var execHits atomic.Int64
	gc := newInterceptHarness(t, &execHits, cannedNodesResp())
	eng := &codeSearchEngineFake{
		hitsByRepo: map[string][]searchengine.Hit{"wire-repo": {{ID: "base-doc", Score: 1}}},
	}
	cov := &poolCoverageFake{covered: 3, liveCovered: 3}
	deps := &poolWiringDeps{interceptDeps: &interceptDeps{gc: gc, segMgr: eng}, cov: cov}

	composeCodeSearch(context.Background(), deps, gc.Execute,
		codeSearchArgs{Graph: "code", Repo: "wire-repo", Branch: "feat", Text: "q"},
		[]string{"q"}, nil)

	assert.Equal(t, []string{"wire-repo@feat"}, cov.askedNames(),
		"composeCodeSearch must hand the coverage seam down to the pool decision — "+
			"without it the gate reads nil and no branch ever collapses")
}

// TestShippedCompleteForUnifiedSearch_MemoizesWithinTTL pins the perf shape the
// step requires: the gate runs once per code query on the hot read path, and
// neither operand may become a per-query read.
func TestShippedCompleteForUnifiedSearch_MemoizesWithinTTL(t *testing.T) {
	f := newCollapseFixture("memo-repo", 4, 4)
	ctx := context.Background()

	require.True(t, shippedCompleteForUnifiedSearch(ctx, f.cdeps, "memo-repo", collapseFixtureBranch))
	require.True(t, shippedCompleteForUnifiedSearch(ctx, f.cdeps, "memo-repo", collapseFixtureBranch))

	assert.Len(t, f.cov.askedNames(), 1, "the covered count is read once per TTL window, not per query")
	assert.Len(t, f.bar.askedSelectors(), 1, "the bar is read once per TTL window, not per query")

	// KNOWN-POSITIVE: a memo that served EVERY branch from one entry would satisfy
	// the counts above while answering the wrong branch's question.
	require.True(t, shippedCompleteForUnifiedSearch(ctx, f.cdeps, "memo-repo", "other"))
	assert.Equal(t, []string{"memo-repo@feat", "memo-repo@other"}, f.cov.askedNames(),
		"a second branch is a separate verdict and re-reads both operands")
}
