// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// recordingFetchCaller is a segmentCaller that serves a synthetic blob per
// requested id (bytes = id) and records the id count of every Fetch call. An
// optional reject closure lets a test fail a Fetch whose chunk is "too large"
// (a byte-ceiling stand-in) with a chosen connect error, exercising the adaptive
// halving / propagation paths in fetchMisses.
type recordingFetchCaller struct {
	mu      sync.Mutex
	calls   [][]string // id sets per Fetch, in call order
	reject  func(ids []string) error
	listErr error
}

func (c *recordingFetchCaller) Ship(_ context.Context, _ *knowledgev1.ShipRequest) (*knowledgev1.ShipResponse, error) {
	return &knowledgev1.ShipResponse{}, nil
}

func (c *recordingFetchCaller) ListDelta(_ context.Context, _ *knowledgev1.ListDeltaRequest) (*knowledgev1.ListDeltaResponse, error) {
	return &knowledgev1.ListDeltaResponse{}, c.listErr
}

func (c *recordingFetchCaller) Fetch(_ context.Context, req *knowledgev1.FetchRequest) (*knowledgev1.FetchResponse, error) {
	ids := req.GetIds()
	c.mu.Lock()
	idsCopy := append([]string(nil), ids...)
	c.calls = append(c.calls, idsCopy)
	c.mu.Unlock()

	if c.reject != nil {
		if err := c.reject(ids); err != nil {
			return nil, err
		}
	}
	blobs := make([]*knowledgev1.SegmentBlobProto, 0, len(ids))
	for _, id := range ids {
		blobs = append(blobs, &knowledgev1.SegmentBlobProto{Id: id, Format: "mock", Bytes: []byte(id)})
	}
	return &knowledgev1.FetchResponse{Blobs: blobs}, nil
}

func (c *recordingFetchCaller) Prune(_ context.Context, _ *knowledgev1.PruneRequest) (*knowledgev1.PruneResponse, error) {
	return &knowledgev1.PruneResponse{}, nil
}

func (c *recordingFetchCaller) Publish(_ context.Context, _ *knowledgev1.PublishRequest) (*knowledgev1.PublishResponse, error) {
	return &knowledgev1.PublishResponse{}, nil
}

func (c *recordingFetchCaller) callCounts() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	counts := make([]int, len(c.calls))
	for i, ids := range c.calls {
		counts[i] = len(ids)
	}
	return counts
}

// newFetchMissesManager wires a distManager around a recordingFetchCaller so
// m.source.Fetch routes to it. The engine/cache are unused by fetchMisses.
func newFetchMissesManager(t *testing.T, caller *recordingFetchCaller) *distManager[mockQuery, mockStats] {
	t.Helper()
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "fetchmisses"}
	src := newRPCSegmentSource(caller, target, "", context.Background())
	cache := newDiskSegmentCache(t.TempDir(), 0)
	return newDistManager(newMockEngine(), src, cache, target, "")
}

func segIDs(n int) []searchengine.SegmentID {
	ids := make([]searchengine.SegmentID, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("seg-%05d", i)
	}
	return ids
}

// TestFetchMissesSubBatchesByCount drives fetchMisses with N ids where N is a few
// times maxFetchSegmentIDs and asserts: exactly ceil(N/cap) Fetch invocations,
// each requesting at most maxFetchSegmentIDs ids, and the concatenated result
// holds every blob in input order with no loss or reordering.
func TestFetchMissesSubBatchesByCount(t *testing.T) {
	caller := &recordingFetchCaller{}
	mgr := newFetchMissesManager(t, caller)

	const n = 3*maxFetchSegmentIDs + 7 // not a clean multiple of the cap
	ids := segIDs(n)

	blobs, err := mgr.fetchMisses(ids)
	require.NoError(t, err)

	wantCalls := (n + maxFetchSegmentIDs - 1) / maxFetchSegmentIDs
	counts := caller.callCounts()
	require.Len(t, counts, wantCalls, "fetchMisses must issue ceil(N/cap) Fetch RPCs")
	for i, c := range counts {
		require.LessOrEqualf(t, c, maxFetchSegmentIDs, "chunk %d exceeds the id cap", i)
		require.Positive(t, c, "no empty chunk should be Fetched")
	}

	// Every blob returned, in input order.
	require.Len(t, blobs, n)
	for i, b := range blobs {
		require.Equal(t, ids[i], b.ID, "blob %d out of order or missing", i)
	}
}

// TestFetchMissesHalvesOnResourceExhausted drives fetchMisses against a caller
// that rejects any chunk larger than a byte-proxy threshold with
// CodeResourceExhausted (the server byte-ceiling backstop) and otherwise serves
// the blobs. fetchMisses must halve+retry the over-threshold chunk until every
// sub-chunk fits, returning every blob with no loss; the recorded id counts shrink
// across the retried calls as the chunk is halved.
func TestFetchMissesHalvesOnResourceExhausted(t *testing.T) {
	// Reject any chunk with more than `threshold` ids (a stand-in for the byte
	// ceiling: too many ids = too many bytes). With cap=256 and threshold=64, the
	// first 256-id chunk is rejected and must halve down to <=64-id sub-chunks.
	const threshold = 64
	caller := &recordingFetchCaller{
		reject: func(ids []string) error {
			if len(ids) > threshold {
				return connect.NewError(connect.CodeResourceExhausted,
					fmt.Errorf("byte ceiling: %d ids too large", len(ids)))
			}
			return nil
		},
	}
	mgr := newFetchMissesManager(t, caller)

	ids := segIDs(maxFetchSegmentIDs) // one full count-capped chunk that over-runs bytes
	blobs, err := mgr.fetchMisses(ids)
	require.NoError(t, err, "halving must eventually fit every sub-chunk under the ceiling")

	// No blob loss: every id returned, in order.
	require.Len(t, blobs, len(ids))
	for i, b := range blobs {
		require.Equal(t, ids[i], b.ID, "blob %d out of order or missing after halving", i)
	}

	// Every SUCCEEDING Fetch carried <= threshold ids; the rejected (over-threshold)
	// calls were halved away. At least one call was rejected (the initial full chunk).
	counts := caller.callCounts()
	require.Greater(t, len(counts), 1, "the over-threshold chunk must have been halved into multiple Fetches")
	sawRejected := false
	for _, c := range counts {
		if c > threshold {
			sawRejected = true
		}
	}
	require.True(t, sawRejected, "the initial full chunk must have been a rejected (halved) attempt")
}

// TestFetchMissesPropagatesNonResourceExhausted asserts a non-ResourceExhausted
// error (CodeInternal) propagates immediately from fetchMisses with NO retry — only
// the byte-ceiling code triggers halving.
func TestFetchMissesPropagatesNonResourceExhausted(t *testing.T) {
	caller := &recordingFetchCaller{
		reject: func(_ []string) error {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("boom"))
		},
	}
	mgr := newFetchMissesManager(t, caller)

	_, err := mgr.fetchMisses(segIDs(10))
	require.Error(t, err)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err), "non-ResourceExhausted error must propagate unchanged")

	// No retry: exactly ONE Fetch attempt was made for the single chunk.
	require.Len(t, caller.callCounts(), 1, "a non-ResourceExhausted error must not be retried")
}

// TestFetchMissesSingleBlobOverCeilingHardErrors asserts the termination floor: a
// 1-id chunk that still over-runs the ceiling cannot be halved further, so
// fetchMisses returns a hard error (no infinite loop) rather than silently
// dropping the id.
func TestFetchMissesSingleBlobOverCeilingHardErrors(t *testing.T) {
	caller := &recordingFetchCaller{
		reject: func(_ []string) error {
			// Reject EVERY chunk, including a 1-id chunk — simulates a single blob
			// whose bytes alone exceed the server ceiling.
			return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("one blob over ceiling"))
		},
	}
	mgr := newFetchMissesManager(t, caller)

	_, err := mgr.fetchMisses(segIDs(4))
	require.Error(t, err, "a single blob over the ceiling must hard-error, not loop forever")
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
}
