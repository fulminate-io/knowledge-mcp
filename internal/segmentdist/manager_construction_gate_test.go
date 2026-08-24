// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// manager_construction_gate_test.go — a constructor that races a branch seed
// WAITS for it, and a seed that fails still releases its waiters.
//
// THE RACE IS HELD OPEN, NOT APPROXIMATED BY A SLEEP. The seed's base List runs
// through the injected-source seam, so a double whose List blocks on a channel
// the test controls IS the seed-completion gate: goroutine A is parked inside the
// seed for exactly as long as the test wants, with no timing assumption anywhere.

// gateFixtureRepo is this file's single base graph, and gateFixtureBranch the
// branch derived from it.
const gateFixtureRepo = "gate-repo"
const gateFixtureBranch = gateFixtureRepo + "@feature"

// blockingSeedSource parks the seed inside its List until release is closed, and
// reports entry so the test can know A is inside the seed rather than guessing.
type blockingSeedSource struct {
	metas   []searchengine.SegmentMeta
	release chan struct{}
	entered chan struct{}
	once    sync.Once
	listErr error
	// listPanic drives the seed off its NORMAL RETURN PATH entirely, which is the
	// only way to tell a deferred close from a straight-line one. See the panic arm
	// of seed_failure_does_not_strand_waiters for why the error alone cannot.
	listPanic bool
}

func (s *blockingSeedSource) List(context.Context, uint64) ([]searchengine.SegmentMeta, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	if s.listPanic {
		panic("seed list panicked")
	}
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.metas, nil
}

func (s *blockingSeedSource) Fetch(context.Context, []searchengine.SegmentID) ([]searchengine.SegmentBlob, error) {
	return nil, nil
}

func (s *blockingSeedSource) Ship(context.Context, []*knowledgev1.SegmentBlobProto) ([]*knowledgev1.SegmentMetaProto, error) {
	return nil, nil
}

func (s *blockingSeedSource) Prune([]searchengine.SegmentID) (int, error)          { return 0, nil }
func (s *blockingSeedSource) PublishManifest(string, []segmentDigest) (int, error) { return 0, nil }
func (s *blockingSeedSource) verifiesCompletenessServerSide() bool                 { return false }

// gateFixtureBase lays down a REAL base corpus through the ordinary write path,
// so the partitions the seed copies are blobs an engine can actually decode and
// search — which is what lets the completeness subtest assert on a search rather
// than on a file count. It returns the published metas for the blocking double.
func gateFixtureBase(t *testing.T, ctx context.Context, cacheDir string) []searchengine.SegmentMeta {
	t.Helper()
	producer := closeOnCleanup(t, NewManager(loginStateStub{}, cacheDir, 0))
	warmSeedVisibilityBase(t, ctx, producer, gateFixtureRepo)

	format := bm25.New().Name()
	ids := branchBucketIDs(cacheDir, gateFixtureRepo, format)
	require.NotEmpty(t, ids, "fixture control: base must hold real partitions for the seed to copy")
	metas := make([]searchengine.SegmentMeta, 0, len(ids))
	for _, id := range ids {
		metas = append(metas, searchengine.SegmentMeta{ID: id, Format: format})
	}
	return metas
}

// TestManagerFor_ConcurrentConstructionWaitsForTheSeed drives the BM25
// constructor, whose seed's base List is the double's.
func TestManagerFor_ConcurrentConstructionWaitsForTheSeed(t *testing.T) {
	ctx := context.Background()

	t.Run("racing_constructor_blocks_until_seed_completes", func(t *testing.T) {
		cacheDir := t.TempDir()
		metas := gateFixtureBase(t, ctx, cacheDir)

		src := &blockingSeedSource{
			metas:   metas,
			release: make(chan struct{}),
			entered: make(chan struct{}),
		}
		mgr := closeOnCleanup(t, NewManager(loginStateStub{}, cacheDir, 0, withSegmentSource(src)))

		// A enters construction and parks INSIDE the seed.
		var aDone sync.WaitGroup
		aDone.Go(func() {
			mgr.bm25ManagerFor(kgtypes.GraphCode, gateFixtureBranch)
		})
		<-src.entered // A is inside the seed, holding it open

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
		close(src.release)
		bDone.Wait()
		aDone.Wait()

		require.True(t, bObserved.Load(),
			"a constructor racing a seed must not return until the seed completes — returning early hands the "+
				"caller an engine whose load() latches a PARTIALLY copied corpus, silently and for the life of "+
				"the process")
	})

	t.Run("racing_constructor_observes_complete_corpus", func(t *testing.T) {
		cacheDir := t.TempDir()
		metas := gateFixtureBase(t, ctx, cacheDir)

		src := &blockingSeedSource{
			metas:   metas,
			release: make(chan struct{}),
			entered: make(chan struct{}),
		}
		mgr := closeOnCleanup(t, NewManager(loginStateStub{}, cacheDir, 0, withSegmentSource(src)))

		var aDone sync.WaitGroup
		aDone.Go(func() {
			mgr.bm25ManagerFor(kgtypes.GraphCode, gateFixtureBranch)
		})
		<-src.entered

		var (
			bDone sync.WaitGroup
			bDM   *distManager[bm25.Query, *bm25.CorpusStats]
		)
		bDone.Go(func() {
			bDM = mgr.bm25ManagerFor(kgtypes.GraphCode, gateFixtureBranch)
		})

		close(src.release)
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
		metas := gateFixtureBase(t, ctx, cacheDir)

		// THE HAZARD THIS STEP WOULD INTRODUCE IF THE CLOSE WERE NOT DEFERRED. A gate
		// closed only on the success path turns a partial corpus into a HUNG DAEMON,
		// which is strictly worse than the defect it fixes. The seed already tolerates
		// failure by design — it logs at Error and the branch rebuilds from the server
		// — so a failed seed must still publish a usable manager.
		src := &blockingSeedSource{
			metas:   metas,
			release: make(chan struct{}),
			entered: make(chan struct{}),
			listErr: errors.New("seed list failed"),
		}
		mgr := closeOnCleanup(t, NewManager(loginStateStub{}, cacheDir, 0, withSegmentSource(src)))

		var aDone sync.WaitGroup
		aDone.Go(func() {
			mgr.bm25ManagerFor(kgtypes.GraphCode, gateFixtureBranch)
		})
		<-src.entered

		bReturned := make(chan *distManager[bm25.Query, *bm25.CorpusStats], 1)
		go func() { bReturned <- mgr.bm25ManagerFor(kgtypes.GraphCode, gateFixtureBranch) }()

		close(src.release)

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
		panicMetas := gateFixtureBase(t, ctx, panicDir)
		psrc := &blockingSeedSource{
			metas:     panicMetas,
			release:   make(chan struct{}),
			entered:   make(chan struct{}),
			listPanic: true,
		}
		pmgr := closeOnCleanup(t, NewManager(loginStateStub{}, panicDir, 0, withSegmentSource(psrc)))

		var paDone sync.WaitGroup
		paDone.Go(func() {
			// The panic is recovered HERE so the harness survives it; the point is not
			// that the constructor tolerates a panic, but that the waiters are released
			// while it unwinds.
			defer func() { _ = recover() }()
			pmgr.bm25ManagerFor(kgtypes.GraphCode, gateFixtureBranch)
		})
		<-psrc.entered

		pbReturned := make(chan *distManager[bm25.Query, *bm25.CorpusStats], 1)
		go func() { pbReturned <- pmgr.bm25ManagerFor(kgtypes.GraphCode, gateFixtureBranch) }()

		close(psrc.release)

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
