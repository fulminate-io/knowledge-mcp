// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestNewSegmentSource_CapabilityGate asserts the per-graph source is selected by
// the caller's live login state (the TWO-way gate): a not-logged-in caller yields
// the OSS-local *localSegmentSource, while a logged-in caller with NO transport
// builder yields the fail-loud *errorSegmentSource sentinel.
func TestNewSegmentSource_CapabilityGate(t *testing.T) {
	t.Parallel()

	t.Run("not-logged-in caller selects the OSS-local source", func(t *testing.T) {
		mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: false}, t.TempDir(), 0))
		hnsw := mgr.managerFor(kgtypes.GraphCode, "repo")
		require.IsType(t, (*localSegmentSource)(nil), hnsw.source,
			"a not-logged-in caller gets the L2-only localSegmentSource for the HNSW engine")
		bm := mgr.bm25ManagerFor(kgtypes.GraphCode, "repo")
		require.IsType(t, (*localSegmentSource)(nil), bm.source,
			"a not-logged-in caller gets the L2-only localSegmentSource for the BM25 engine too")
	})

	t.Run("logged-in caller with no transport yields the fail-loud sentinel", func(t *testing.T) {
		mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0))
		hnsw := mgr.managerFor(kgtypes.GraphCode, "repo")
		require.IsType(t, (*errorSegmentSource)(nil), hnsw.source,
			"a logged-in caller with no segment transport gets the fail-loud errorSegmentSource sentinel")
	})
}

// TestNewSegmentSource_LoggedInWithTransportSelectsGCS proves the cloud arm of the
// two-way gate: a logged-in caller carrying a segment-transport builder constructs
// the *gcsSegmentSource for BOTH the HNSW and BM25 engines. A logged-in caller whose
// builder FAILS yields the fail-loud *errorSegmentSource sentinel.
func TestNewSegmentSource_LoggedInWithTransportSelectsGCS(t *testing.T) {
	t.Parallel()

	builder := func() (SegmentControlTransport, error) {
		return auth.NewSyncTransport("http://unused", auth.StaticTokenSource{AccessToken: "t"}), nil
	}

	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0,
		WithSegmentTransport(builder)))
	hnsw := mgr.managerFor(kgtypes.GraphCode, "repo")
	require.IsType(t, (*gcsSegmentSource)(nil), hnsw.source,
		"a logged-in caller with a transport builder gets the GCS source for the HNSW engine")
	bm := mgr.bm25ManagerFor(kgtypes.GraphCode, "repo")
	require.IsType(t, (*gcsSegmentSource)(nil), bm.source,
		"a logged-in caller with a transport builder gets the GCS source for the BM25 engine")

	// A logged-in caller whose builder FAILS yields the fail-loud sentinel (there is
	// no server SegmentService fallback anymore).
	failBuilder := func() (SegmentControlTransport, error) { return nil, context.DeadlineExceeded }
	mgrFail := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0,
		WithSegmentTransport(failBuilder)))
	require.IsType(t, (*errorSegmentSource)(nil), mgrFail.managerFor(kgtypes.GraphCode, "repo").source,
		"a logged-in caller whose transport build fails gets the fail-loud errorSegmentSource sentinel")
}

// TestNewDistManager_L2AuthoritativeFlag pins the derived flag: newDistManager sets
// l2Authoritative true exactly when the source is a *localSegmentSource, and false
// for a non-local (server/agent-backed) source.
func TestNewDistManager_L2AuthoritativeFlag(t *testing.T) {
	t.Parallel()

	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "l2auth"}
	cache := newDiskSegmentCache(t.TempDir(), 0, adviceRandom)

	local := newDistManager(newMockEngine(t), newLocalSegmentSource(cache, ""), cache, target, "")
	require.True(t, local.l2Authoritative, "a *localSegmentSource source ⟺ l2Authoritative")

	// A non-local source (here the in-memory server-model fake) → l2Authoritative false.
	nonLocal := newSharedServerFake().viewFor(target, "")
	remote := newDistManager(newMockEngine(t), nonLocal, cache, target, "")
	require.False(t, remote.l2Authoritative, "a non-local (server-backed) source → l2Authoritative false")
}
