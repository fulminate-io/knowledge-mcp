// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestSearchFetchHonorsCallerCtx proves a hung Fetch on the cold-load path is
// unwound by the CALLER's ctx. A consumer with a cold L2 cache must List the
// server delta then Fetch the misses; the source hangs Fetch forever. When the
// caller cancels the load ctx, load() must return promptly with the ctx error.
//
// RED pre-C2: Fetch ran under the source's stored Background ctx, so canceling
// the load ctx did nothing — load blocked past the test deadline. GREEN after C2:
// the caller ctx threads through fetchMisses → fetchChunkAdaptive → Fetch, so the
// cancel unwinds the in-flight RPC and load returns context.Canceled.
func TestSearchFetchHonorsCallerCtx(t *testing.T) {
	t.Parallel()

	gt, name := kgtypes.GraphCode, "hangRepo"
	target := graphSelector(gt, name)
	svc := newSharedServerFake()

	// A producer ships a real corpus so the consumer's cold List returns misses to
	// Fetch (the path that must be cancellable).
	producer := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(svc.viewFor(target, ""))))
	seedShipped(t, context.Background(), producer, gt, name, hnswVecDocs(1024))

	// Consumer points at a view that hangs Fetch; its L2 is cold (fresh TempDir),
	// so load() falls through to loadFromServer → fetchMisses → the hung Fetch.
	hangView := svc.viewFor(target, "")
	hangView.hangFetch = true
	consumer := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(hangView)))
	dm := consumer.managerFor(gt, name)

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
