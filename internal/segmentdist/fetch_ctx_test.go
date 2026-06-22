// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// hangOnFetchCaller wraps a real segmentCaller and serves ListDelta normally (so a
// cold load reaches the Fetch leg) but BLOCKS Fetch until the caller's ctx is
// cancelled — a hung/unresponsive server. It then returns the ctx error. With the
// caller ctx threaded through Fetch (C2), a cancelled load ctx unwinds the hang
// promptly; without it (pre-fix, Fetch ran under a stored Background ctx) the hang
// outlives the cancel and the test deadlines.
type hangOnFetchCaller struct {
	inner segmentCaller
}

func (c *hangOnFetchCaller) Ship(ctx context.Context, req *knowledgev1.ShipRequest) (*knowledgev1.ShipResponse, error) {
	return c.inner.Ship(ctx, req)
}

func (c *hangOnFetchCaller) ListDelta(ctx context.Context, req *knowledgev1.ListDeltaRequest) (*knowledgev1.ListDeltaResponse, error) {
	return c.inner.ListDelta(ctx, req)
}

func (c *hangOnFetchCaller) Fetch(ctx context.Context, _ *knowledgev1.FetchRequest) (*knowledgev1.FetchResponse, error) {
	// Block until the CALLER's ctx is cancelled, then surface the ctx error. If
	// Fetch is invoked under a non-cancellable Background ctx (the pre-C2 bug), this
	// never returns until the test's outer deadline fires.
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *hangOnFetchCaller) Prune(ctx context.Context, req *knowledgev1.PruneRequest) (*knowledgev1.PruneResponse, error) {
	return c.inner.Prune(ctx, req)
}

func (c *hangOnFetchCaller) Publish(ctx context.Context, req *knowledgev1.PublishRequest) (*knowledgev1.PublishResponse, error) {
	return c.inner.Publish(ctx, req)
}

var _ segmentCaller = (*hangOnFetchCaller)(nil)

// TestSearchFetchHonorsCallerCtx proves a hung Fetch on the cold-load path is
// unwound by the CALLER's ctx. A consumer with a cold L2 cache must List the
// server delta then Fetch the misses; the server hangs Fetch forever. When the
// caller cancels the load ctx, load() must return promptly with the ctx error.
//
// RED pre-C2: Fetch ran under the source's stored Background ctx, so canceling
// the load ctx did nothing — load blocked past the test deadline. GREEN after C2:
// the caller ctx threads through fetchMisses → fetchChunkAdaptive → Fetch, so the
// cancel unwinds the in-flight RPC and load returns context.Canceled.
func TestSearchFetchHonorsCallerCtx(t *testing.T) {
	// A producer ships a real corpus so the consumer's cold List returns misses to
	// Fetch (the path that must be cancellable).
	_, gc := newSegmentHarness(t)
	require.NoError(t, NewManager(gc, t.TempDir(), 0).AddAndShip(
		context.Background(), kgtypes.GraphCode, "hangRepo", hnswVecDocs(1024)))

	// Consumer points at a caller that hangs Fetch; its L2 is cold (fresh TempDir),
	// so load() falls through to loadFromServer → fetchMisses → the hung Fetch.
	hang := &hangOnFetchCaller{inner: gc}
	consumer := NewManager(hang, t.TempDir(), 0)
	dm := consumer.managerFor(kgtypes.GraphCode, "hangRepo")

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after load() enters the hung Fetch.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() { done <- dm.load(ctx) }()

	select {
	case err := <-done:
		require.Error(t, err, "a canceled load ctx must abort the hung Fetch")
		require.ErrorIs(t, err, context.Canceled,
			"load must return the caller ctx's cancellation error, got: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("load() did not return after the caller ctx was cancelled — the hung Fetch ignored the cancel (ctx not threaded)")
	}
}
