// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// corpusRow is one seed row in the fake CorpusDelta server.
type corpusRow struct {
	id   string
	ua   int64
	tomb bool
}

// fakeCorpusScanner is a mini CorpusDelta server over a sorted single-layer corpus:
// it serves keyset pages (cursor, H] with a limit, a snapshot-wide probe, and the
// pinned/fresh horizon — enough to exercise the client drain loop (cold, dirty,
// burst-with-pinned-H, and forced-resync replay). rows MUST be sorted by (ua, id).
type fakeCorpusScanner struct {
	rows           []corpusRow
	freshH         int64
	probeLiveDelta int64 // inject a probe divergence (0 = faithful)
	calls          int
	pinnedSeen     []int64
}

func (f *fakeCorpusScanner) CorpusDelta(_ context.Context, req *knowledgev1.CorpusDeltaRequest) (*knowledgev1.CorpusDeltaResponse, error) {
	f.calls++
	f.pinnedSeen = append(f.pinnedSeen, req.GetPinnedHorizon())
	h := req.GetPinnedHorizon()
	if h == 0 {
		h = f.freshH
	}
	var afterUA int64
	var afterID string
	for _, c := range req.GetCursors() {
		if c.GetLayerKey() == "default" {
			afterUA, afterID = c.GetAfterUpdatedAt(), c.GetAfterId()
		}
	}
	limit := int(req.GetLimit())

	var items []*knowledgev1.Node
	var probeLive, probeMax int64
	for _, r := range f.rows {
		if r.ua > h {
			continue
		}
		if r.ua > probeMax {
			probeMax = r.ua
		}
		if !r.tomb {
			probeLive++
		}
		afterCursor := r.ua > afterUA || (r.ua == afterUA && r.id > afterID)
		if afterCursor && (limit == 0 || len(items) < limit) {
			n := &knowledgev1.Node{Id: r.id, Type: "thought", UpdatedAt: r.ua}
			if r.tomb {
				n.TombstonedAt = r.ua
			}
			items = append(items, n)
		}
	}
	next := &knowledgev1.LayerCursor{LayerKey: "default", AfterUpdatedAt: afterUA, AfterId: afterID}
	if len(items) > 0 {
		last := items[len(items)-1]
		next.AfterUpdatedAt, next.AfterId = last.GetUpdatedAt(), last.GetId()
	}
	return &knowledgev1.CorpusDeltaResponse{
		Items:       items,
		SafeHorizon: h,
		LayerProbes: []*knowledgev1.LayerProbe{{LayerKey: "default", LiveCount: probeLive + f.probeLiveDelta, MaxUpdatedAt: probeMax}},
		NextCursors: []*knowledgev1.LayerCursor{next},
	}, nil
}

func loopWithCorpus(fake *fakeCorpusScanner) *PropagationLoop {
	return (&PropagationLoop{}).WithCorpusScanner(fake)
}

// TestRefreshCorpusCache_ColdFullDrain: an empty cache cold-drains the whole
// corpus in one short page.
func TestRefreshCorpusCache_ColdFullDrain(t *testing.T) {
	fake := &fakeCorpusScanner{
		rows:   []corpusRow{{"t1", 1000, false}, {"t2", 2000, false}, {"t3", 3000, false}},
		freshH: 10_000_000,
	}
	p := loopWithCorpus(fake)
	p.refreshCorpusCache(context.Background())
	assert.Equal(t, 1, fake.calls, "cold drain of a sub-page corpus is one call")
	require.Len(t, p.corpus.Snapshot(), 3, "cold drain loads the whole corpus")
}

// TestRefreshCorpusCache_DirtyTickOneChange (measured criterion b): after a warm
// cache, a single new change drains in exactly ONE CorpusDelta call carrying ONE
// item — NOT a full re-drain.
func TestRefreshCorpusCache_DirtyTickOneChange(t *testing.T) {
	fake := &fakeCorpusScanner{
		rows:   []corpusRow{{"t1", 1000, false}, {"t2", 2000, false}},
		freshH: 10_000_000,
	}
	p := loopWithCorpus(fake)
	p.refreshCorpusCache(context.Background()) // cold warm-up
	fake.calls = 0
	fake.pinnedSeen = nil

	// One new thought arrives.
	fake.rows = append(fake.rows, corpusRow{"t3", 3000, false})
	p.refreshCorpusCache(context.Background())
	assert.Equal(t, 1, fake.calls, "a 1-change dirty tick is exactly ONE CorpusDelta call")
	require.Len(t, p.corpus.Snapshot(), 3, "the one new thought merged into the resident cache")
}

// TestRefreshCorpusCache_BurstPinsHorizon: a burst larger than the page size drains
// in ceil(M/pageSize) pages that ALL carry page 1's pinned horizon, and reconciles
// clean (no forced resync).
func TestRefreshCorpusCache_BurstPinsHorizon(t *testing.T) {
	const m = corpusDeltaPageSize + 100 // 2 pages
	rows := make([]corpusRow, 0, m)
	for i := 1; i <= m; i++ {
		rows = append(rows, corpusRow{id: idForBurst(i), ua: int64(i), tomb: false})
	}
	fake := &fakeCorpusScanner{rows: rows, freshH: 10_000_000}
	p := loopWithCorpus(fake)
	p.refreshCorpusCache(context.Background())

	require.Equal(t, 2, fake.calls, "ceil(M/pageSize) = 2 pages, no forced-resync re-drain")
	require.Len(t, fake.pinnedSeen, 2)
	assert.Equal(t, int64(0), fake.pinnedSeen[0], "page 1 requests a fresh horizon")
	assert.Equal(t, fake.freshH, fake.pinnedSeen[1], "page 2 is PINNED to page 1's horizon (defeats non-monotonic-H strand)")
	require.Len(t, p.corpus.Snapshot(), m, "the whole burst merged")
}

// idForBurst gives lexicographically-monotonic ids aligned with ascending ua so the
// keyset (ua, id) order is stable across the two burst pages.
func idForBurst(i int) string {
	const width = 6
	s := []byte("t000000")
	n := i
	for pos := width; pos >= 1 && n > 0; pos-- {
		s[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(s)
}

// TestRefreshCorpusCache_QuietTickZeroCalls: the quiet gate is UNCHANGED —
// a quiet tick returns before refreshCorpusCache, so it issues ZERO CorpusDelta
// calls (the cache is left resident and untouched).
func TestRefreshCorpusCache_QuietTickZeroCalls(t *testing.T) {
	corpusFake := &fakeCorpusScanner{
		rows:   []corpusRow{{"t1", 1000, false}},
		freshH: 10_000_000,
	}
	gate := &gateFake{probeGen: 5}
	require.NoError(t, writeLastReflectedGen(context.Background(), gate, 5)) // matching watermark → quiet
	loop := newGateLoop(gate).WithCorpusScanner(corpusFake)

	loop.runBackgroundPropagation()

	assert.False(t, gate.didPassRun(), "quiet tick must skip the pass body")
	assert.Equal(t, 0, corpusFake.calls, "a quiet tick issues ZERO CorpusDelta calls (gate unchanged)")
}

// bootOrderFake records the ORDER of the two read classes the boot path issues —
// the CorpusDelta drain and the detection pass's Execute reads — so the test can
// assert the resident cache is warmed BEFORE detection reads anything. It doubles as
// the loop's Caller, serving every query empty so the detection body quiesces.
type bootOrderFake struct {
	inner *fakeCorpusScanner
	mu    sync.Mutex
	seq   []string
}

func (f *bootOrderFake) CorpusDelta(ctx context.Context, req *knowledgev1.CorpusDeltaRequest) (*knowledgev1.CorpusDeltaResponse, error) {
	f.mu.Lock()
	f.seq = append(f.seq, "corpus_delta")
	f.mu.Unlock()
	return f.inner.CorpusDelta(ctx, req)
}

func (f *bootOrderFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.GetQuery() != nil {
		f.seq = append(f.seq, "execute")
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

func (f *bootOrderFake) order() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seq...)
}

// TestPropagationLoop_BootWarmsCorpusCache: a daemon restart no longer runs a cold
// pass — runBootClusterDetection refreshes the resident corpus cache BEFORE the
// detection body issues its first read, so the consumers inside detection find a
// warm cache instead of each re-draining the corpus.
func TestPropagationLoop_BootWarmsCorpusCache(t *testing.T) {
	fake := &bootOrderFake{inner: &fakeCorpusScanner{
		rows:   []corpusRow{{"t1", 1000, false}, {"t2", 2000, false}},
		freshH: 10_000_000,
	}}
	p := (&PropagationLoop{gc: fake, stopCh: make(chan struct{})}).WithCorpusScanner(fake)

	p.runBootClusterDetection()

	seq := fake.order()
	require.NotEmpty(t, seq, "boot must issue reads")
	assert.Equal(t, "corpus_delta", seq[0],
		"boot warms the resident corpus cache BEFORE the detection body's first Execute")
	require.Len(t, p.corpus.Snapshot(), 2, "the boot drain filled the resident cache")
}

// TestRefreshCorpusCache_ProbeMismatchForcesResync: a probe that reports more live
// rows than the cache holds forces a Reset + full re-drain.
func TestRefreshCorpusCache_ProbeMismatchForcesResync(t *testing.T) {
	fake := &fakeCorpusScanner{
		rows:           []corpusRow{{"t1", 1000, false}, {"t2", 2000, false}},
		freshH:         10_000_000,
		probeLiveDelta: 5, // server claims 5 extra live rows the cache never saw.
	}
	p := loopWithCorpus(fake)
	p.refreshCorpusCache(context.Background())
	// Initial drain = 1 call; the mismatch forces a Reset + one more full drain.
	assert.GreaterOrEqual(t, fake.calls, 2, "a genuine probe mismatch forces a resync re-drain")
}
