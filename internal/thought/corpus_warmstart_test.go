// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// warmLoop builds a persistence-enabled loop over a fake scanner and a data root.
// Each call is a fresh loop, standing in for a fresh process over the same root.
func warmLoop(fake *fakeCorpusScanner, root string) *PropagationLoop {
	return (&PropagationLoop{}).WithCorpusScanner(fake).WithCorpusPersistence(root)
}

// corpusPayloadOffset is where the framed record's payload begins, so a test can
// flip a byte INSIDE it and hit the checksum rather than the header.
func corpusPayloadOffset() int {
	return corpusFrameFixedHead + len(strings.Join(corpusNodeTypes, ",")) + 4 + corpusFrameChecksum
}

// TestWarmRestart_PersistedCacheDrainsOnePage is the ticket's headline claim: a
// second process over the same data root resumes from the persisted cursors and
// serves only what changed while it was down.
//
// The CURSOR and ITEM-COUNT assertions are the discriminating ones. A call count of
// 1 alone does not distinguish a warm restart from a cold drain of a tiny corpus —
// both are one page.
func TestWarmRestart_PersistedCacheDrainsOnePage(t *testing.T) {
	dir := t.TempDir()
	fake := &fakeCorpusScanner{
		rows: []corpusRow{
			{"t1", 1000, false}, {"t2", 2000, false}, {"t3", 3000, false}, {"t4", 4000, false},
		},
		freshH: 10_000_000,
	}

	// Process A: cold drain, reconcile, persist.
	a := warmLoop(fake, dir)
	a.refreshCorpusCache(context.Background())
	require.Len(t, a.corpus.Snapshot(), 4, "process A cold-drained the whole corpus")
	require.Equal(t, int64(0), fake.cursorsSeen[0], "process A had no cursor to send")
	_, err := os.Stat(CorpusCachePathFor(dir))
	require.NoError(t, err, "process A persisted a record")

	// One row changed while the daemon was down.
	fake.rows = append(fake.rows, corpusRow{"t5", 5000, false})
	fake.calls, fake.cursorsSeen, fake.itemsServed, fake.pinnedSeen = 0, nil, nil, nil

	// Process B: same root, same server.
	b := warmLoop(fake, dir)
	b.refreshCorpusCache(context.Background())

	assert.Equal(t, 1, fake.calls, "the warm restart drains ONE page")
	require.Len(t, fake.cursorsSeen, 1)
	assert.Equal(t, int64(4000), fake.cursorsSeen[0],
		"process B resumed from the PERSISTED high-water, not from zero")
	assert.NotZero(t, fake.cursorsSeen[0], "a zero cursor would be a cold drain wearing a warm restart's call count")
	require.Len(t, fake.itemsServed, 1)
	assert.Equal(t, 1, fake.itemsServed[0], "only the row that changed while down was served")

	snap := b.corpus.Snapshot()
	require.Len(t, snap, 5, "the four adopted rows plus the one delta row are resident")
	ids := map[string]bool{}
	for _, n := range snap {
		ids[n.GetId()] = true
	}
	assert.True(t, ids["t5"], "the row that changed while down landed in the snapshot")
	assert.True(t, ids["t1"], "the adopted rows survived the validating drain")
}

// TestWarmRestart_CorruptRecordForcesFullDrain: a record that does not decode is
// never adopted, and the process falls back to a genuine cold drain — proven by the
// ZERO cursor it sends, not merely by the resulting row count.
func TestWarmRestart_CorruptRecordForcesFullDrain(t *testing.T) {
	rows := []corpusRow{{"t1", 1000, false}, {"t2", 2000, false}, {"t3", 3000, false}}

	cases := []struct {
		name  string
		plant func(t *testing.T, path string)
	}{
		{
			name: "garbage file",
			plant: func(t *testing.T, path string) {
				t.Helper()
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
				require.NoError(t, os.WriteFile(path, []byte("not a corpus record"), 0o600))
			},
		},
		{
			name: "valid record with one flipped payload byte",
			plant: func(t *testing.T, path string) {
				t.Helper()
				items := []*knowledgev1.Node{{Id: "t1", Type: "thought", UpdatedAt: 1000}}
				cursors := []*knowledgev1.LayerCursor{{LayerKey: "default", AfterUpdatedAt: 1000, AfterId: "t1"}}
				require.NoError(t, saveCorpusRecord(path, corpusNodeTypes, items, cursors))
				raw, err := os.ReadFile(path) //nolint:gosec // test fixture under t.TempDir.
				require.NoError(t, err)
				off := corpusPayloadOffset()
				require.Less(t, off, len(raw), "the fixture record must carry a payload to corrupt")
				raw[off] ^= 0xFF
				require.NoError(t, os.WriteFile(path, raw, 0o600)) //nolint:gosec // path is CorpusCachePathFor(t.TempDir()).
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.plant(t, CorpusCachePathFor(dir))

			fake := &fakeCorpusScanner{rows: rows, freshH: 10_000_000}
			p := warmLoop(fake, dir)
			p.refreshCorpusCache(context.Background())

			require.NotEmpty(t, fake.cursorsSeen)
			assert.Equal(t, int64(0), fake.cursorsSeen[0],
				"a rejected record must leave the cache empty, so the drain starts from a ZERO cursor")
			assert.Len(t, p.corpus.Snapshot(), len(rows), "the full cold drain loaded the whole corpus")
		})
	}
}

// TestWarmRestart_DrainFailureDiscardsUnvalidatedCache: the adopting tick's drain is
// what validates the adopted rows, so a drain error destroys them rather than
// leaving unproven data resident where a consumer could read it.
//
// The third loop is the CONTROL: it proves the record was adoptable all along, so
// the emptiness in the second arm is the discard and not an unreadable file.
func TestWarmRestart_DrainFailureDiscardsUnvalidatedCache(t *testing.T) {
	dir := t.TempDir()
	rows := []corpusRow{{"t1", 1000, false}, {"t2", 2000, false}, {"t3", 3000, false}}

	// Loop A seeds a valid record.
	seed := &fakeCorpusScanner{rows: rows, freshH: 10_000_000}
	warmLoop(seed, dir).refreshCorpusCache(context.Background())
	require.FileExists(t, CorpusCachePathFor(dir))

	// Loop B adopts it and then cannot validate it.
	failing := &fakeCorpusScanner{rows: rows, freshH: 10_000_000, err: assertWireErr{}}
	b := warmLoop(failing, dir)
	b.refreshCorpusCache(context.Background())
	assert.Empty(t, b.corpus.Snapshot(),
		"an adopted cache the tick could not validate is discarded, not left resident")

	// CONTROL: the same record, a working scanner, a fresh loop.
	working := &fakeCorpusScanner{rows: rows, freshH: 10_000_000}
	c := warmLoop(working, dir)
	c.refreshCorpusCache(context.Background())
	assert.NotEmpty(t, c.corpus.Snapshot(),
		"control: the record was adoptable — the emptiness above was the discard, not a bad file")
	require.NotEmpty(t, working.cursorsSeen)
	assert.NotZero(t, working.cursorsSeen[0], "control: the record really was adopted (non-zero resume cursor)")
}

// assertWireErr is the injected CorpusDelta wire failure.
type assertWireErr struct{}

func (assertWireErr) Error() string { return "corpus delta wire failure (injected)" }

// TestWarmLoad_AdoptedCacheIsColdUntilReconciled drives the unvalidated gate
// deterministically, without concurrency, by splitting refreshCorpusCache's two
// halves: adopt alone, then reconcile.
//
// The record seeds one node of EACH corpusNodeTypes type, because a projection that
// is empty for its own reason would report cold in the second half and pass the
// assertion vacuously.
//
// The fake is seeded with the SAME rows and the record's cursor sits at their
// high-water, so the validating drain returns ZERO items. That is required against
// CORRECT code, not a convenience: fakeCorpusScanner stamps every row it serves as
// Type "thought", and MergeDelta upserts by id — so any row the drain DID return
// would overwrite the seeded charge/session node of that id and collapse those two
// projections to empty, failing the test for a fixture reason after a perfectly
// correct reconcile.
func TestWarmLoad_AdoptedCacheIsColdUntilReconciled(t *testing.T) {
	dir := t.TempDir()
	items := []*knowledgev1.Node{
		{Id: "t1", Type: string(kgtypes.NodeThought), UpdatedAt: 1000},
		{Id: "c1", Type: string(kgtypes.NodeCharge), UpdatedAt: 2000},
		{Id: "s1", Type: string(kgtypes.NodeThoughtSession), UpdatedAt: 3000},
	}
	cursors := []*knowledgev1.LayerCursor{{LayerKey: "default", AfterUpdatedAt: 3000, AfterId: "s1"}}
	require.NoError(t, saveCorpusRecord(CorpusCachePathFor(dir), corpusNodeTypes, items, cursors))

	fake := &fakeCorpusScanner{
		rows:   []corpusRow{{"t1", 1000, false}, {"c1", 2000, false}, {"s1", 3000, false}},
		freshH: 10_000_000,
	}
	p := warmLoop(fake, dir)

	require.True(t, p.warmLoadCorpusOnce(), "the record was adopted")
	require.Len(t, p.corpus.Snapshot(), 3, "the adopted rows really are resident in the cache")
	assert.Equal(t, 0, fake.calls, "adoption issues no wire calls of its own")

	// Resident but unproven: every consumer sees cold.
	_, warm := p.CorpusSnapshot()
	assert.False(t, warm, "CorpusSnapshot reports cold while the adopted rows are unvalidated")
	_, warm = p.ChargeSnapshot()
	assert.False(t, warm, "ChargeSnapshot reports cold while the adopted rows are unvalidated")
	_, warm = p.SessionSnapshot()
	assert.False(t, warm, "SessionSnapshot reports cold while the adopted rows are unvalidated")

	// The validating half.
	p.refreshCorpusCache(context.Background())
	require.Equal(t, 1, fake.calls, "the validating drain is one page")
	require.NotEmpty(t, fake.itemsServed)
	require.Equal(t, 0, fake.itemsServed[0],
		"fixture precondition: the validating drain must return no rows, or it would overwrite the seeded types")

	thoughts, warm := p.CorpusSnapshot()
	assert.True(t, warm, "CorpusSnapshot is warm once the tick reconciled the adopted rows")
	assert.Len(t, thoughts, 1)
	charges, warm := p.ChargeSnapshot()
	assert.True(t, warm, "ChargeSnapshot is warm once the tick reconciled the adopted rows")
	assert.Len(t, charges, 1)
	sessions, warm := p.SessionSnapshot()
	assert.True(t, warm, "SessionSnapshot is warm once the tick reconciled the adopted rows")
	assert.Len(t, sessions, 1)
}

// TestWarmLoad_SkippedWhenKnowledgeNotAdmitted is the admission fence for the new
// file read. The binding rule, quoted so it cannot drift: "Load on the FIRST
// ADMITTED TICK, never at boot. The operative admission rule is binding: no
// background graph interaction before admission. Loading a local file is not a graph
// read, but the load must still sit behind the admission gate so the cold/warm
// distinction never reintroduces boot-time work."
//
// HONEST LABEL: this is a SCOPE FENCE, not a red-first reproduction. It passes the
// moment it is written against correct code, because the load sits inside
// refreshCorpusCache which is already behind the gate. Its job is to fail LATER if
// anyone hoists the load to Start() or NewPropagationLoop for a faster warm start.
//
// Deliberately NOT parallel — like every sibling gate test, this claims the
// process-global reflection single-flight guard, and a coalesced pass would record
// nothing for the wrong reason.
func TestWarmLoad_SkippedWhenKnowledgeNotAdmitted(t *testing.T) {
	dir := t.TempDir()
	// Seeded through the PRODUCTION path helper, never a hand-built literal: a
	// fixture written where the loader does not read would make the unadmitted
	// assertion below pass for the wrong reason.
	//
	// The NON-ZERO UpdatedAt is load-bearing for the control, not cosmetic. Reconcile
	// filters the cached live set by updated_at <= horizon, and the horizon on the
	// empty response this fake returns is ZERO. Rows stamped 0 would count toward the
	// cached-live-at-H total while the probe total is 0, so the reconcile would fail,
	// the loop would Reset, and the control would see an empty cache.
	nodes := []*knowledgev1.Node{
		{Id: "t1", Type: string(kgtypes.NodeThought), UpdatedAt: 1_000_000_000},
		{Id: "t2", Type: string(kgtypes.NodeThought), UpdatedAt: 1_000_000_000},
	}
	cursors := []*knowledgev1.LayerCursor{{LayerKey: "default", AfterUpdatedAt: 1_000_000_000, AfterId: "t2"}}
	require.NoError(t, saveCorpusRecord(CorpusCachePathFor(dir), corpusNodeTypes, nodes, cursors))

	var admitted bool
	var mu sync.Mutex
	p, rec := gatedLoop(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return admitted
	})
	p = p.WithCorpusPersistence(dir)

	p.runBackgroundPropagation()
	p.runBootClusterDetection()

	assert.Equal(t, []string(nil), rec.recorded(),
		"an unadmitted process issues ZERO requests — the catch-all Execute arm would have "+
			"recorded any read this fake was never taught about")
	assert.Empty(t, p.corpus.Snapshot(),
		"an unadmitted process reads no record: the warm load sits behind the admission gate")

	// KNOWN-POSITIVE CONTROL. Without it the empty cache above would prove only that
	// the fixture never wired a path the loader reads.
	mu.Lock()
	admitted = true
	mu.Unlock()

	p.runBackgroundPropagation()
	assert.NotEmpty(t, p.corpus.Snapshot(),
		"control: the same record warms the cache once the graph is admitted")
}

// TestPersistCorpusCache_AtomicWriteNoTempLeftover asserts the record directory holds
// the record and nothing else after a reconciled tick — the temp file the rename
// consumed leaves no straggler. Same assertion shape as the transcript watermark
// store's atomic-write test, over a different record.
//
// It doubles as the presence check for the items > 0 persist trigger: a save that
// silently no-oped would leave the directory absent and ReadDir would error.
func TestPersistCorpusCache_AtomicWriteNoTempLeftover(t *testing.T) {
	dir := t.TempDir()
	fake := &fakeCorpusScanner{
		rows:   []corpusRow{{"t1", 1000, false}, {"t2", 2000, false}},
		freshH: 10_000_000,
	}
	p := warmLoop(fake, dir)
	p.refreshCorpusCache(context.Background())

	ents, err := os.ReadDir(filepath.Dir(CorpusCachePathFor(dir)))
	require.NoError(t, err, "the reconciled tick created the record directory")
	require.Len(t, ents, 1, "exactly one file in the record dir — the record, no temp leftovers")
	assert.Equal(t, corpusCacheFile, ents[0].Name())
	for _, e := range ents {
		assert.False(t, strings.HasSuffix(e.Name(), ".tmp"),
			"no *.tmp straggler remains after the rename")
	}
}
