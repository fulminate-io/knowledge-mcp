// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// fakeRepairScanner serves scripted segment_rebuild pages and records the
// AfterStampedAtNanos every page was requested with — the watermark assertion is
// about what the repair ASKED FOR, which a fake that only returns items cannot
// witness.
type fakeRepairScanner struct {
	mu           sync.Mutex
	pages        [][]*knowledgev1.PipelineScanItem
	pageIter     int
	afterStamped []int64
	// scanFroms records scan_from_stamped_at_nanos beside it, appended in the SAME
	// statement so the two slices stay index-aligned per request.
	scanFroms []int64
	// horizon is the safe horizon every page echoes, exactly as the server does. Zero
	// by default, which is what an unset fixture legitimately means: this scan was
	// served no horizon, and no caller may persist one from it.
	horizon int64
}

func (f *fakeRepairScanner) PipelineScan(_ context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.afterStamped, f.scanFroms = append(f.afterStamped, req.GetAfterStampedAtNanos()), append(f.scanFroms, req.GetScanFromStampedAtNanos())
	if f.pageIter >= len(f.pages) {
		// The horizon rides the terminating empty page too — that is what makes "the
		// last page observed bounds the drain" true rather than an accident of paging.
		return &knowledgev1.PipelineScanResponse{ServedHorizonNanos: f.horizon}, nil
	}
	page := f.pages[f.pageIter]
	f.pageIter++
	return &knowledgev1.PipelineScanResponse{Items: page, ServedHorizonNanos: f.horizon}, nil
}

func (f *fakeRepairScanner) Execute(context.Context, *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

// fakeRepairShipper answers the membership probe from a scripted per-format
// missing set and records every document handed to each producer call.
type fakeRepairShipper struct {
	missingHNSW, missingBM25 []searchengine.ExternalID
	probeErr                 error

	probedIDs   []searchengine.ExternalID
	hnswDocs    []searchengine.Document
	fieldDocs   []searchengine.Document
	hnswCalls   int
	fieldCalls  int
	reEmitCalls int
}

func (s *fakeRepairShipper) UncoveredMembers(
	_ context.Context, _ kgtypes.GraphType, _ string, ids []searchengine.ExternalID,
) ([]searchengine.ExternalID, []searchengine.ExternalID, error) {
	s.probedIDs = append(s.probedIDs, ids...)
	if s.probeErr != nil {
		return nil, nil, s.probeErr
	}
	return s.missingHNSW, s.missingBM25, nil
}

func (s *fakeRepairShipper) AddAndMarkDirty(_ context.Context, _ kgtypes.GraphType, _ string, docs []searchengine.Document) error {
	s.hnswCalls++
	s.hnswDocs = append(s.hnswDocs, docs...)
	return nil
}

func (s *fakeRepairShipper) AddAndMarkDirtyFields(_ context.Context, _ kgtypes.GraphType, _ string, docs []searchengine.Document) error {
	s.fieldCalls++
	s.fieldDocs = append(s.fieldDocs, docs...)
	return nil
}

func (s *fakeRepairShipper) ReEmitDirtyBuckets(context.Context, kgtypes.GraphType, string) error {
	s.reEmitCalls++
	return nil
}

// docIDs projects the ids out of a captured document slice.
func docIDs(docs []searchengine.Document) []string {
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.ID)
	}
	return out
}

// repairCorpusSize is the fixture's TRUE eligible corpus size, asserted against as
// a constant derived from the fixture rather than from anything the code under test
// computed. Two pages so the paging loop is genuinely exercised.
const repairCorpusSize = 5

// newRepairFixture returns a scanner serving repairCorpusSize items across two
// pages (ids "rp00000000".."rp00000004").
func newRepairFixture() *fakeRepairScanner {
	return &fakeRepairScanner{pages: [][]*knowledgev1.PipelineScanItem{
		makeScanPage("rp", 0, 3),
		makeScanPage("rp", 3, 2),
	}}
}

// TestRepairShipsOnlyTheDifference pins the arm's whole contract: it ships the
// uncovered ids and NOTHING else. Shipping more than the difference is how a repair
// becomes a rebuild storm; shipping less leaves the hole it was called to close.
func TestRepairShipsOnlyTheDifference(t *testing.T) {
	ctx := context.Background()

	t.Run("hnsw_only", func(t *testing.T) {
		sc := newRepairFixture()
		sh := &fakeRepairShipper{missingHNSW: []searchengine.ExternalID{"rp00000001"}}

		out, err := RepairUncoveredSegments(ctx, sc, sh, kgtypes.GraphCode, "myrepo")
		require.NoError(t, err)
		require.True(t, out.Ran)

		require.Equal(t, 1, out.MissingHNSW)
		require.Equal(t, 1, out.ShippedHNSW)
		require.Equal(t, []string{"rp00000001"}, docIDs(sh.hnswDocs),
			"only the uncovered id is shipped — not the corpus it was found in")
		require.Equal(t, 0, out.ShippedBM25)
		require.Zero(t, sh.fieldCalls, "a format with nothing missing is not called at all")
		require.Equal(t, 1, sh.reEmitCalls, "the repair lands within the pass that found the gap")
	})

	t.Run("bm25_only", func(t *testing.T) {
		sc := newRepairFixture()
		sh := &fakeRepairShipper{missingBM25: []searchengine.ExternalID{"rp00000004"}}

		out, err := RepairUncoveredSegments(ctx, sc, sh, kgtypes.GraphCode, "myrepo")
		require.NoError(t, err)
		require.True(t, out.Ran)

		require.Equal(t, 1, out.MissingBM25)
		require.Equal(t, 1, out.ShippedBM25)
		require.Equal(t, []string{"rp00000004"}, docIDs(sh.fieldDocs))
		require.Equal(t, 0, out.ShippedHNSW)
		require.Zero(t, sh.hnswCalls, "a format with nothing missing is not called at all")
		require.Equal(t, 1, sh.reEmitCalls)
	})

	// converged_none is the identical-re-add fence. Re-adding documents a graph
	// already holds is not free — it has previously wiped the searchable corpus in
	// memory — so a converged graph must ship NOTHING, not "ship harmlessly".
	t.Run("converged_none", func(t *testing.T) {
		sc := newRepairFixture()
		sh := &fakeRepairShipper{} // nothing missing in either format

		out, err := RepairUncoveredSegments(ctx, sc, sh, kgtypes.GraphCode, "myrepo")
		require.NoError(t, err)

		require.True(t, out.Ran, "finding nothing missing is a SUCCESSFUL pass, not a no-op")
		// The known positive that keeps these zeros honest: the scan really walked a
		// corpus, so the zeros are a diff result rather than an empty walk.
		require.Equal(t, repairCorpusSize, out.ScannedEligible)
		require.Len(t, sh.probedIDs, repairCorpusSize, "the whole scanned corpus was probed")

		require.Zero(t, out.ShippedHNSW)
		require.Zero(t, out.ShippedBM25)
		require.Zero(t, sh.hnswCalls)
		require.Zero(t, sh.fieldCalls)
		require.Zero(t, sh.reEmitCalls, "a converged graph triggers no ship and no re-emit")
	})

	// reports_eligible pins ScannedEligible as the TRUE eligible corpus size the pass
	// measured — the diagnostic the INFO line carries and the operator reads. The
	// fixture deliberately makes it differ from every other count in the outcome, so
	// a field wired to the missing or shipped count cannot pass.
	t.Run("reports_eligible", func(t *testing.T) {
		sc := newRepairFixture()
		sh := &fakeRepairShipper{
			missingHNSW: []searchengine.ExternalID{"rp00000000", "rp00000002"},
			missingBM25: []searchengine.ExternalID{"rp00000002"},
		}

		out, err := RepairUncoveredSegments(ctx, sc, sh, kgtypes.GraphCode, "myrepo")
		require.NoError(t, err)

		require.Equal(t, repairCorpusSize, out.ScannedEligible,
			"ScannedEligible is the corpus the pass measured, not what it shipped")
		require.Equal(t, 2, out.MissingHNSW)
		require.Equal(t, 1, out.MissingBM25)
		require.NotEqual(t, out.ScannedEligible, out.MissingHNSW)
		require.NotEqual(t, out.ScannedEligible, out.ShippedHNSW)
	})
}

// TestRepairNamesUnshippableMissingIDs is the diagnostic gate for the one case the
// attribution line structurally cannot cover: ids reported missing that build no
// document, so nothing ships and the shipped line never runs. Such an id is missing
// again on every subsequent pass, and this DEBUG line is the only thing that ever
// names it — an operator asked to decide whether those nodes are legitimately
// text-free has no other way to obtain them.
func TestRepairNamesUnshippableMissingIDs(t *testing.T) {
	ctx := context.Background()

	// textlessFixture appends a node with a vector but NO BM25 fields to the ordinary
	// corpus: eligible for the scan, reportable as missing by the probe, and skipped
	// by BuildBM25Documents — so it can never be shipped, however many passes run.
	const textless = "rp-textless"
	textlessFixture := func() *fakeRepairScanner {
		page := append(makeScanPage("rp", 0, 3), &knowledgev1.PipelineScanItem{
			NodeId:       textless,
			GraphName:    "myrepo",
			BinaryVector: make([]byte, 32),
		})
		return &fakeRepairScanner{pages: [][]*knowledgev1.PipelineScanItem{page}}
	}

	t.Run("unbuildable_missing_id_is_named", func(t *testing.T) {
		logs := captureRebuildLogs(t)
		sc := textlessFixture()
		sh := &fakeRepairShipper{missingBM25: []searchengine.ExternalID{textless}}

		out, err := RepairUncoveredSegments(ctx, sc, sh, kgtypes.GraphCode, "myrepo")
		require.NoError(t, err)

		// Fixture preconditions. Without them the assertion below could pass on a pass
		// that shipped normally, which is the case already covered elsewhere.
		require.Equal(t, 1, out.MissingBM25, "fixture: the textless node must be reported missing")
		require.Zero(t, out.ShippedBM25, "fixture: a node with no fields builds no BM25 document")
		require.Zero(t, sh.reEmitCalls, "fixture: nothing shipped, so the pass takes the early return")
		require.Empty(t, logs.linesContaining(slog.LevelInfo, "shipped the uncovered difference"),
			"fixture: the shipped attribution line must NOT fire here — covering for its absence is why this line exists")

		lines := logs.linesContaining(slog.LevelDebug, "nothing was buildable to ship")
		require.Len(t, lines, 1, "an unshipped pass with missing ids logs exactly one diagnostic line")
		require.Contains(t, lines[0], "missing_sample")
		require.Contains(t, lines[0], textless,
			"the line must NAME the unshippable id — counts alone are what the operator already had")
	})

	// The known negative that keeps the arm above honest: a converged graph reaches
	// the SAME early return, and must stay quiet rather than log an empty sample for
	// every graph on every boot.
	t.Run("converged_graph_logs_no_sample", func(t *testing.T) {
		logs := captureRebuildLogs(t)
		sc := newRepairFixture()
		sh := &fakeRepairShipper{}

		out, err := RepairUncoveredSegments(ctx, sc, sh, kgtypes.GraphCode, "myrepo")
		require.NoError(t, err)
		require.Equal(t, repairCorpusSize, out.ScannedEligible,
			"fixture: the pass really walked a corpus, so the silence below is a decision and not an empty walk")
		require.Zero(t, out.MissingHNSW+out.MissingBM25)

		require.Empty(t, logs.linesContaining(slog.LevelDebug, "nothing was buildable to ship"),
			"a converged pass has no ids to name and must not log an empty sample")
	})
}

// TestRepairUncoveredSegmentsScansFromZeroWatermark is the watermark trap. A node
// whose ship was dropped is by definition UNCHANGED since the last landed rebuild,
// so a change-scoped scan pages straight past exactly the nodes this arm exists to
// find and the repair converges on nothing while reporting success.
func TestRepairUncoveredSegmentsScansFromZeroWatermark(t *testing.T) {
	sc := newRepairFixture()
	sh := &fakeRepairShipper{missingHNSW: []searchengine.ExternalID{"rp00000001"}}

	_, err := RepairUncoveredSegments(context.Background(), sc, sh, kgtypes.GraphCode, "myrepo")
	require.NoError(t, err)

	require.NotEmpty(t, sc.afterStamped, "the scan must have been paged at all")
	for i, got := range sc.afterStamped {
		require.Zero(t, got, "page %d must be requested from watermark ZERO (full corpus)", i)
	}
}

// TestSegmentRepairShipperSeamExcludesRebuild is the storm/collision fence. The
// repair's inability to trigger a manifest swap rests on the SEAM not carrying the
// rebuild methods — so that property is asserted on the type itself, not left to a
// comment that a later edit can quietly falsify.
func TestSegmentRepairShipperSeamExcludesRebuild(t *testing.T) {
	iface := reflect.TypeFor[SegmentRepairShipper]()

	got := make([]string, 0, iface.NumMethod())
	for m := range iface.Methods() {
		name := m.Name
		got = append(got, name)
		for _, forbidden := range []string{"Rebuild", "Finalize", "Stage"} {
			require.NotContains(t, name, forbidden,
				"SegmentRepairShipper must not carry %s-shaped methods: %s would let the repair reach the manifest swap",
				forbidden, name)
		}
	}

	// The forbidden-substring walk alone is satisfied by an EMPTY interface, so the
	// four expected methods must also be present.
	require.ElementsMatch(t,
		[]string{"UncoveredMembers", "AddAndMarkDirty", "AddAndMarkDirtyFields", "ReEmitDirtyBuckets"},
		got, "the seam carries exactly the four producer methods the repair needs")
	require.NotContains(t, strings.Join(got, ","), "Rebuild")
}
