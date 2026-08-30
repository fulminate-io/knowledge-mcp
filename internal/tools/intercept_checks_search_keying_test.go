// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_checks_search_keying_test.go reproduces the THIRD appearance of one
// conflation: the search rails addressed the checks segment engine under the
// EMPTY instance name while the collector sealed its segments under the
// canonical one, so a search reached a different, empty engine instance and
// returned a confident zero.
//
// THE OLD FAKE COULD NOT SEE IT, and that is the lesson worth keeping. The shared
// fakeSegmentSearcher returns its canned hits for ANY name it is asked for, so it
// agreed with the broken rail by construction — the cutover test it backed even
// asserted the empty name as correct. A double that mirrors the code under test
// is worth less than no double; the keyed fake below is what makes the mismatch
// observable at all.

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// keyedSegmentSearcher serves hits ONLY for the (graph type, name) its corpus was
// sealed under, exactly as the real per-graph manager map does. Asked for any
// other key it returns nothing — which is a silent zero, not an error, because
// that is precisely how the production mismatch presented.
type keyedSegmentSearcher struct {
	sealedGT   kgtypes.GraphType
	sealedName string
	hits       []searchengine.Hit

	calls     atomic.Int64
	askedName atomic.Value // string
}

func (f *keyedSegmentSearcher) Search(
	_ context.Context, gt kgtypes.GraphType, name, _ string, _ []byte, _ int,
) ([]searchengine.Hit, error) {
	f.calls.Add(1)
	f.askedName.Store(name)
	if gt != f.sealedGT || name != f.sealedName {
		return nil, nil
	}
	return f.hits, nil
}

func (f *keyedSegmentSearcher) asked() string {
	v, _ := f.askedName.Load().(string)
	return v
}

// checksSealedName is the instance the checks corpus is actually sealed under —
// derived from the normalizer rather than typed, so this fixture cannot disagree
// with the collector about where the segments live.
var checksSealedName = func() string {
	ref, ok := workingset.Normalize(kgtypes.GraphChecks, "")
	if !ok {
		panic("the checks graph must normalize — the working-set fix is missing")
	}
	return ref.Name
}()

// keyedChecksHarness wires a deps whose segment engine holds ONE checks document
// sealed under the canonical name, plus a hydrate response for it.
func keyedChecksHarness(t *testing.T) (*interceptDeps, *keyedSegmentSearcher) {
	t.Helper()
	const hitID = "the-bucket-count-check"
	var execHits atomic.Int64
	gc, _ := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(&knowledgev1.Node{
		Id:         hitID,
		Type:       string(kgtypes.NodeFinding),
		SymbolName: "bucket-count-provenance",
		Summary:    "a partition count computed from a bare identifier argument carries no provenance",
	}))
	mgr := &keyedSegmentSearcher{
		sealedGT:   kgtypes.GraphChecks,
		sealedName: checksSealedName,
		hits:       []searchengine.Hit{{ID: hitID, Score: 1}},
	}
	return &interceptDeps{gc: gc, segMgr: mgr}, mgr
}

// TestInterceptChecksSearch_ReachesTheSealedSegmentInstance is the reproduction
// and the fix's proof, on BOTH rails.
//
// The assertion is on a RETURNED HIT rather than on the name the rail passed,
// because the name is the mechanism and the hit is the property: a rail that
// asked for the right key and still returned nothing would be a different defect,
// and one that returns the check is correct however it spells the lookup.
func TestInterceptChecksSearch_ReachesTheSealedSegmentInstance(t *testing.T) {
	t.Run("the SEARCH rail finds a check sealed under the canonical instance", func(t *testing.T) {
		deps, mgr := keyedChecksHarness(t)
		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "checks", "query": "BucketCountFor", "mode": "text",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "the checks search must be served: %s", engine.FirstTextContent(out))
		require.Equal(t, int64(1), mgr.calls.Load(), "the rail must reach the engine")
		assert.Contains(t, engine.FirstTextContent(out), "bucket-count-provenance",
			"the search must return the check the engine holds — a zero here is a key mismatch, not an empty corpus")
	})

	t.Run("the QUERY rail finds it too", func(t *testing.T) {
		deps, mgr := keyedChecksHarness(t)
		handled, out := InterceptQueryPracticeLinkage(opCtx(), deps, queryParams(t, map[string]any{
			"graph": "checks", "text": "BucketCountFor", "mode": "text",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "the checks query search must be served: %s", engine.FirstTextContent(out))
		require.Equal(t, int64(1), mgr.calls.Load())
		assert.Contains(t, engine.FirstTextContent(out), "bucket-count-provenance",
			"both rails address one engine instance, so both must find what it holds")
	})

	// THE MECHANISM, pinned separately so a regression names its own cause rather
	// than only reporting a missing hit.
	t.Run("the rail addresses the canonical instance, not the empty one", func(t *testing.T) {
		deps, mgr := keyedChecksHarness(t)
		_, _ = InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "checks", "query": "BucketCountFor",
		}))
		assert.Equal(t, checksSealedName, mgr.asked(),
			"the engine is keyed by (graph type, name); asking under the empty name reaches a different instance")
		assert.NotEmpty(t, mgr.asked(),
			"the empty name is the defect this test exists for — the user-facing selector carries none, "+
				"but the internal engine key is the canonical instance")
	})

	// CONTROL on the keyed fake itself: it really does refuse a wrong key, so the
	// hits above are evidence the rail asked correctly rather than evidence of a
	// fake that answers anything.
	t.Run("the keyed fake refuses a wrong key", func(t *testing.T) {
		_, mgr := keyedChecksHarness(t)
		hits, err := mgr.Search(context.Background(), kgtypes.GraphChecks, "", "BucketCountFor", nil, 10)
		require.NoError(t, err)
		assert.Empty(t, hits, "the empty name must reach nothing, or this whole file proves nothing")

		hits, err = mgr.Search(context.Background(), kgtypes.GraphChecks, checksSealedName, "BucketCountFor", nil, 10)
		require.NoError(t, err)
		assert.Len(t, hits, 1, "and the sealed name must reach the corpus")
	})
}
