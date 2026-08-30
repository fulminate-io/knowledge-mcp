// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// manager_construction_gate_test.go — a constructor that races a branch seed
// WAITS for it, and a seed that fails still releases its waiters.
//
// THE RACE IS HELD OPEN, NOT APPROXIMATED BY A SLEEP. A test-only seed hook runs at
// the top of the branch seed — the one piece of work the constructor does OFF the
// Manager lock — so a hook that blocks on a channel the test controls IS the
// seed-completion gate: goroutine A is parked inside the seed for exactly as long as
// the test wants, with no timing assumption anywhere.
//
// IT PARKS THERE AND NOWHERE ELSE, deliberately. Every other seam the constructor
// touches runs UNDER the Manager lock, and a racing caller parked against the LOCK
// would observe the right answer even with the gate deleted — a test that cannot
// fail. Only the off-lock seed distinguishes the gate from the mutex.

// gateFixtureRepo is this file's single base graph, and gateFixtureBranch the
// branch derived from it.
const gateFixtureRepo = "gate-repo"
const gateFixtureBranch = gateFixtureRepo + "@feature"

// blockingSeedHook parks a branch constructor inside its seed until release is
// closed, and reports entry so the test can know A is inside the seed rather than
// guessing.
//
// IT REPLACES A BLOCKING SEGMENT-SOURCE DOUBLE. The seed used to reach the injected
// source's List, so parking that List parked the seed; the seed is now a pure L2
// copy and consults no source at all, so a source double is never called and the
// handshake below would block forever waiting for an entry that cannot happen.
// withSeedHook restores the same park at the same place — the one piece of work the
// constructor does OFF the Manager lock, which is the only place the construction
// gate is observable at all.
type blockingSeedHook struct {
	release chan struct{}
	entered chan struct{}
	once    sync.Once
	// hookPanic drives the seed off its NORMAL RETURN PATH entirely, which is the
	// only way to tell a deferred close from a straight-line one. See the panic arm
	// of seed_failure_does_not_strand_waiters for why an error alone cannot.
	hookPanic bool
}

func newBlockingSeedHook() *blockingSeedHook {
	return &blockingSeedHook{release: make(chan struct{}), entered: make(chan struct{})}
}

// hook parks only the CONSTRUCT phase of a BRANCH construction. Two filters, two
// reasons: the hook also fires from inside the seed at the record-captured phase,
// which is a different window entirely, and it fires for every construction
// including this fixture's own base warming.
func (h *blockingSeedHook) hook(phase seedPhase, _ kgtypes.GraphType, name, _ string) {
	if phase != seedPhaseConstruct || !isBranchGraphName(name) {
		return
	}
	h.once.Do(func() { close(h.entered) })
	<-h.release
	if h.hookPanic {
		panic("seed hook panicked")
	}
}

// gateFixtureBase lays down a REAL base corpus through the ordinary write path,
// so the partitions the seed copies are blobs an engine can actually decode and
// search — which is what lets the completeness subtest assert on a search rather
// than on a file count.
func gateFixtureBase(t *testing.T, ctx context.Context, cacheDir string) {
	t.Helper()
	producer := closeOnCleanup(t, NewManager(cacheDir, 0))
	warmSeedVisibilityBase(t, ctx, producer, gateFixtureRepo)

	ids := branchBucketIDs(cacheDir, gateFixtureRepo, bm25.New().Name())
	require.NotEmpty(t, ids, "fixture control: base must hold real partitions for the seed to copy")
}

// TestManagerFor_ConcurrentConstructionWaitsForTheSeed drives the BM25
// constructor, whose branch seed is parked by the test's hook.
func TestManagerFor_ConcurrentConstructionWaitsForTheSeed(t *testing.T) {
	ctx := context.Background()

	t.Run("racing_constructor_blocks_until_seed_completes", func(t *testing.T) {
		cacheDir := t.TempDir()
		gateFixtureBase(t, ctx, cacheDir)

		h := newBlockingSeedHook()
		mgr := closeOnCleanup(t, NewManager(cacheDir, 0, withSeedHook(h.hook)))

		// A enters construction and parks INSIDE the seed.
		var aDone sync.WaitGroup
		aDone.Go(func() {
			mgr.bm25ManagerFor(kgtypes.GraphCode, gateFixtureBranch)
		})
		<-h.entered // A is inside the seed, holding it open

		// B races the same key. It records the released flag THE INSTANT its
		// constructor returns — no sleep, no deadline, no timing assumption. With the
		// gate absent B takes the memo hit and returns while the seed is still held,
		// so it observes false; with the gate present it cannot return until the
		// deferred close, which happens after the seed, which happens after release.
		var (
			released  atomic.Bool
			bObserved atomic.Bool
			bStarted  = make(chan struct{})
			bDone     sync.WaitGroup
		)
		bDone.Go(func() {
			close(bStarted)
			mgr.bm25ManagerFor(kgtypes.GraphCode, gateFixtureBranch)
			bObserved.Store(released.Load())
		})
		<-bStarted

		released.Store(true)
		close(h.release)
		bDone.Wait()
		aDone.Wait()

		require.True(t, bObserved.Load(),
			"a constructor racing a seed must not return until the seed completes — returning early hands the "+
				"caller an engine whose load() latches a PARTIALLY copied corpus, silently and for the life of "+
				"the process")
	})

	t.Run("racing_constructor_observes_complete_corpus", func(t *testing.T) {
		cacheDir := t.TempDir()
		gateFixtureBase(t, ctx, cacheDir)

		h := newBlockingSeedHook()
		mgr := closeOnCleanup(t, NewManager(cacheDir, 0, withSeedHook(h.hook)))

		// THE BASE ENGINE IS WARMED FIRST, and this is a property of the seed rather
		// than fixture ceremony. The seed's source set is base's ENGINE export — the
		// live, non-superseded layer — never a listing of base's cache directory,
		// because a directory can hold superseded blobs and copying one resurrects
		// documents base already retired. A COLD base engine exports nothing, so the
		// seed copies nothing and the corpus assertion below would fail against a
		// perfectly correct seed. Warming it is what makes this subtest about the gate.
		require.NoError(t, mgr.bm25ManagerFor(kgtypes.GraphCode, gateFixtureRepo).load(ctx))

		var aDone sync.WaitGroup
		aDone.Go(func() {
			mgr.bm25ManagerFor(kgtypes.GraphCode, gateFixtureBranch)
		})
		<-h.entered

		var (
			bDone sync.WaitGroup
			bDM   *distManager[bm25.Query, *bm25.CorpusStats]
		)
		bDone.Go(func() {
			bDM = mgr.bm25ManagerFor(kgtypes.GraphCode, gateFixtureBranch)
		})

		close(h.release)
		bDone.Wait()
		aDone.Wait()

		// BLOCKING IS ONLY HALF THE PROPERTY. A gate that released B against a
		// partially copied engine would satisfy the ordering subtest alone, so this
		// asserts the manager B actually received serves the seeded corpus.
		require.NoError(t, bDM.load(ctx))
		hits := bDM.engine.Search(bm25.NewQuery(seedVisibilityTerm), 10)
		require.Contains(t, seedVisibilityHitIDs(hits), searchengine.ExternalID(seedVisibilityDoc),
			"the manager handed to the racing caller must serve the FULL seeded corpus")
	})

	t.Run("seed_failure_does_not_strand_waiters", func(t *testing.T) {
		cacheDir := t.TempDir()
		gateFixtureBase(t, ctx, cacheDir)

		// THE HAZARD THIS STEP WOULD INTRODUCE IF THE CLOSE WERE NOT DEFERRED. A gate
		// closed only on the success path turns a partial corpus into a HUNG DAEMON,
		// which is strictly worse than the defect it fixes. The seed already tolerates
		// failure by design — it logs at Error and the branch rebuilds from its own
		// embedded nodes — so a failed seed must still publish a usable manager.
		//
		// THE FAILURE IS DRIVEN THROUGH THE PRODUCTION PATH rather than injected: the
		// seed reads the BASE graph's rebuild record before a single partition moves,
		// so an undecodable record makes the real seed return a real error. That is
		// strictly better than the fake-source error it replaces, which could only fail
		// a seam production no longer calls.
		require.NoError(t, os.MkdirAll(filepath.Dir(
			rebuildStatePathFor(cacheDir, kgtypes.GraphCode, gateFixtureRepo)), 0o750))
		require.NoError(t, os.WriteFile(
			rebuildStatePathFor(cacheDir, kgtypes.GraphCode, gateFixtureRepo),
			[]byte("{not json"), 0o600))

		h := newBlockingSeedHook()
		mgr := closeOnCleanup(t, NewManager(cacheDir, 0, withSeedHook(h.hook)))

		var aDone sync.WaitGroup
		aDone.Go(func() {
			mgr.bm25ManagerFor(kgtypes.GraphCode, gateFixtureBranch)
		})
		<-h.entered

		bReturned := make(chan *distManager[bm25.Query, *bm25.CorpusStats], 1)
		go func() { bReturned <- mgr.bm25ManagerFor(kgtypes.GraphCode, gateFixtureBranch) }()

		close(h.release)

		// ITS OWN BOUNDED DEADLINE, so a stranded waiter FAILS this test instead of
		// hanging the whole suite with no attribution.
		select {
		case dm := <-bReturned:
			require.NotNil(t, dm, "a failed seed must still publish a usable manager")
		case <-time.After(30 * time.Second):
			require.FailNow(t, "waiter stranded",
				"the construction gate was never closed after a FAILED seed — every waiter on this graph is "+
					"blocked forever, which is a hung daemon rather than a degraded one")
		}
		aDone.Wait()

		// THE PANIC ARM, AND IT IS WHAT ACTUALLY DISCRIMINATES. The error arm above
		// is necessary but NOT sufficient, proven by mutation: seedBranchAtConstruction
		// absorbs a seed error by design (it logs at Error and returns), so the
		// constructor still reaches its normal return and even a straight-line close
		// runs. Replacing `defer close(gate.done)` with a straight-line close leaves
		// the error arm GREEN. Only leaving the normal return path — a panic — tells
		// the two apart, and `defer` is precisely the construct that survives one.
		panicDir := t.TempDir()
		gateFixtureBase(t, ctx, panicDir)
		ph := newBlockingSeedHook()
		ph.hookPanic = true
		pmgr := closeOnCleanup(t, NewManager(panicDir, 0, withSeedHook(ph.hook)))

		var paDone sync.WaitGroup
		paDone.Go(func() {
			// The panic is recovered HERE so the harness survives it; the point is not
			// that the constructor tolerates a panic, but that the waiters are released
			// while it unwinds.
			defer func() { _ = recover() }()
			pmgr.bm25ManagerFor(kgtypes.GraphCode, gateFixtureBranch)
		})
		<-ph.entered

		pbReturned := make(chan *distManager[bm25.Query, *bm25.CorpusStats], 1)
		go func() { pbReturned <- pmgr.bm25ManagerFor(kgtypes.GraphCode, gateFixtureBranch) }()

		close(ph.release)

		select {
		case dm := <-pbReturned:
			require.NotNil(t, dm, "a PANICKING seed must still release its waiters with a usable manager")
		case <-time.After(30 * time.Second):
			require.FailNow(t, "waiter stranded after a panicking seed",
				"the construction gate was not closed while the seed's panic unwound — a straight-line close "+
					"skips it, and every waiter on this graph blocks forever")
		}
		paDone.Wait()
	})
}
